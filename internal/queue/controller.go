package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// tickTimeout bounds one queue_tick.lua call plus its PG transition.
const tickTimeout = time.Second

// Tick outcomes (queue_tick.lua status words).
const (
	TickNotLoaded = "NOT_LOADED"
	TickPrequeue  = "PREQUEUE"
	TickSoldOut   = "SOLD_OUT"
	TickPaused    = "PAUSED"
	TickAdmitting = "ADMITTING"
)

// TickConfig are the admission knobs (QUEUE_* env vars).
type TickConfig struct {
	MaxActive  int
	Overadmit  float64
	MinAdmit   int
	MaxPerTick int
	OfferTTL   time.Duration
}

// TickResult is one queue_tick outcome. The numbers are set on ADMITTING.
type TickResult struct {
	State         string
	Admitted      int64 // offers issued by this tick
	Target        int64 // the bound this tick enforced
	Seeking       int64 // admitted users without a hold, before this tick
	Outstanding   int64 // unclaimed offers, before this tick
	ExpiredOffers int64 // offers past their deadline that this tick dropped
}

// Ticker runs queue_tick.lua for one event. It has no state of its own, so
// any number of tickers (in any number of workers) may run concurrently.
type Ticker struct {
	sc  *redisx.Scripts
	cfg TickConfig
}

// NewTicker validates cfg and builds a ticker.
func NewTicker(sc *redisx.Scripts, cfg TickConfig) (*Ticker, error) {
	if sc == nil || cfg.MaxActive < 1 || cfg.Overadmit < 1 || cfg.MinAdmit < 0 || cfg.MaxPerTick < 1 || cfg.OfferTTL <= 0 {
		return nil, fmt.Errorf("queue: invalid tick config %+v", cfg)
	}
	return &Ticker{sc: sc, cfg: cfg}, nil
}

// Tick runs one admission step for eid at now.
func (t *Ticker) Tick(ctx context.Context, eid uuid.UUID, opens, now time.Time) (TickResult, error) {
	r, err := t.sc.Run(ctx, redisx.QueueTick, keys.QueueTickKeys(eid),
		now.UnixMilli(), opens.UnixMilli(), t.cfg.MaxActive, strconv.FormatFloat(t.cfg.Overadmit, 'f', -1, 64),
		t.cfg.MinAdmit, t.cfg.MaxPerTick, t.cfg.OfferTTL.Milliseconds(), rateTau.Milliseconds())
	if err != nil {
		return TickResult{}, err
	}
	res := TickResult{State: r.Status}
	switch r.Status {
	case TickNotLoaded, TickPrequeue, TickSoldOut, TickPaused:
		return res, nil
	case TickAdmitting:
		for i, dst := range []*int64{&res.Admitted, &res.Target, &res.Seeking, &res.Outstanding, &res.ExpiredOffers} {
			if *dst, err = r.Int(i); err != nil {
				return res, fmt.Errorf("queue: tick reply: %w", err)
			}
		}
		return res, nil
	default:
		return res, fmt.Errorf("queue: tick: unexpected reply %q", r.Status)
	}
}

// ControllerOptions configure a Controller.
type ControllerOptions struct {
	Tick    time.Duration // QUEUE_TICK
	Refresh time.Duration // how often the open-event list is re-read (default 1 s)
	Now     func() time.Time
	Log     *slog.Logger
}

// Controller is the worker's admission controller: one loop per open
// (non-CLOSED) event, each running Ticker.Tick every QUEUE_TICK, plus the
// PG status transitions SCHEDULED -> ON_SALE (first admitting tick) and
// -> SOLD_OUT (the tick that sets the sold-out flag).
type Controller struct {
	q      *gen.Queries
	ticker *Ticker
	opt    ControllerOptions
}

// NewController builds a controller.
func NewController(q *gen.Queries, ticker *Ticker, opt ControllerOptions) (*Controller, error) {
	if q == nil || ticker == nil || opt.Tick <= 0 {
		return nil, errors.New("queue: controller needs queries, a ticker and Tick > 0")
	}
	if opt.Refresh <= 0 {
		opt.Refresh = time.Second
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	return &Controller{q: q, ticker: ticker, opt: opt}, nil
}

// TickOnce runs one admission step for ev and applies the PG transition it
// implies. It returns ev's status after the step.
func (c *Controller) TickOnce(ctx context.Context, ev gen.ListOpenEventsRow) (TickResult, gen.EventStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()
	res, err := c.ticker.Tick(ctx, ev.ID, ev.SaleOpensAt, c.opt.Now())
	if err != nil {
		return res, ev.Status, err
	}
	var to gen.EventStatus
	var from []string
	switch res.State {
	case TickAdmitting, TickPaused:
		to, from = gen.EventStatusONSALE, []string{string(gen.EventStatusSCHEDULED)}
	case TickSoldOut:
		to, from = gen.EventStatusSOLDOUT, []string{string(gen.EventStatusSCHEDULED), string(gen.EventStatusONSALE)}
	default:
		return res, ev.Status, nil
	}
	if ev.Status == to {
		return res, ev.Status, nil
	}
	n, err := c.q.TransitionEventStatus(ctx, gen.TransitionEventStatusParams{ToStatus: to, ID: ev.ID, FromStatuses: from})
	if err != nil {
		return res, ev.Status, fmt.Errorf("queue: transition %s: %w", to, err)
	}
	if n == 1 {
		c.opt.Log.InfoContext(ctx, "event status", slog.String("event_id", ev.ID.String()),
			slog.String("from", string(ev.Status)), slog.String("to", string(to)))
	}
	return res, to, nil
}

// loop is one event's tick loop; the supervisor swaps in fresh event rows.
type loop struct {
	row    atomic.Pointer[gen.ListOpenEventsRow]
	cancel context.CancelFunc
}

// Run supervises the per-event loops until ctx is done: it re-reads the
// open events every Refresh, starts a loop for each new one and stops the
// loops of events that closed.
func (c *Controller) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	loops := map[uuid.UUID]*loop{}
	defer func() {
		for _, l := range loops {
			l.cancel()
		}
		wg.Wait()
	}()
	refresh := func() {
		rctx, cancel := context.WithTimeout(ctx, tickTimeout)
		rows, err := c.q.ListOpenEvents(rctx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				c.opt.Log.WarnContext(ctx, "queue controller: list events", slog.String("error", err.Error()))
			}
			return
		}
		seen := make(map[uuid.UUID]bool, len(rows))
		for i := range rows {
			row := rows[i]
			seen[row.ID] = true
			if l, ok := loops[row.ID]; ok {
				l.row.Store(&row)
				continue
			}
			lctx, cancel := context.WithCancel(ctx)
			l := &loop{cancel: cancel}
			l.row.Store(&row)
			loops[row.ID] = l
			wg.Go(func() { c.runEvent(lctx, l) })
		}
		for id, l := range loops {
			if !seen[id] {
				l.cancel()
				delete(loops, id)
			}
		}
	}
	refresh()
	t := time.NewTicker(c.opt.Refresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			refresh()
		}
	}
}

func (c *Controller) runEvent(ctx context.Context, l *loop) {
	t := time.NewTicker(c.opt.Tick)
	defer t.Stop()
	var lastErr time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		row := l.row.Load()
		_, status, err := c.TickOnce(ctx, *row)
		if err != nil {
			if ctx.Err() == nil && time.Since(lastErr) > 5*time.Second {
				lastErr = time.Now()
				c.opt.Log.WarnContext(ctx, "queue tick", slog.String("event_id", row.ID.String()),
					slog.String("error", err.Error()))
			}
			continue
		}
		if status != row.Status {
			next := *row
			next.Status = status
			l.row.CompareAndSwap(row, &next)
		}
	}
}
