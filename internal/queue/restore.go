package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// ErrNotRestored means the event's queue counters are not in Redis yet and
// no restorer is configured (or the restore did not take). The API answers
// 503 DEPENDENCY_UNAVAILABLE with Retry-After.
var ErrNotRestored = errors.New("queue: state not restored")

// tickNotInit is queue_join / queue_tick's "restore first" reply.
const tickNotInit = "NOT_INIT"

// restoreTimeout bounds one restore (a PG read plus one script).
const restoreTimeout = 3 * time.Second

// Restorer initialises an event's queue counters in Redis from its PG
// snapshot (R1, DATA-MODEL §6, ADR 0009). A fresh event has no snapshot and
// starts from zero. Restoring is idempotent: queue_restore.lua does nothing
// once q:init exists, so API replicas and workers may all call it.
type Restorer struct {
	q        *gen.Queries
	sc       *redisx.Scripts
	offerTTL time.Duration
	now      func() time.Time
	log      *slog.Logger
}

// NewRestorer builds a restorer. offerTTL is OFFER_CLAIM_TTL: the fresh
// deadline restored offers get. now and log may be nil.
func NewRestorer(q *gen.Queries, sc *redisx.Scripts, offerTTL time.Duration, now func() time.Time,
	log *slog.Logger,
) (*Restorer, error) {
	if q == nil || sc == nil || offerTTL <= 0 {
		return nil, errors.New("queue: restorer needs queries, scripts and an offer TTL")
	}
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Restorer{q: q, sc: sc, offerTTL: offerTTL, now: now, log: log}, nil
}

// Restore initialises eid's queue counters unless they already are. It
// reports whether this call did the restore (false: someone else had).
func (r *Restorer) Restore(ctx context.Context, eid uuid.UUID) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, restoreTimeout)
	defer cancel()
	snap, err := r.q.GetQueueSnapshot(ctx, eid)
	fresh := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !fresh {
		return false, fmt.Errorf("queue: read snapshot: %w", err)
	}
	if len(snap.ActiveUids) != len(snap.ActiveExpMs) { // also a CHECK constraint
		return false, fmt.Errorf("queue: snapshot of %s has %d active uids but %d expiries",
			eid, len(snap.ActiveUids), len(snap.ActiveExpMs))
	}
	now := r.now()
	presize := int64(-1)
	if snap.Presize != nil {
		presize = *snap.Presize
	}
	soldOut := 0
	if snap.SoldOut {
		soldOut = 1
	}
	args := make([]any, 0, 10+len(snap.Offers)+2*len(snap.ActiveUids))
	args = append(args, now.UnixMilli(), r.offerTTL.Milliseconds(), presize, snap.Tail, snap.Cursor, soldOut,
		strconv.FormatFloat(snap.AdmitRate, 'f', -1, 64), len(snap.Offers))
	for _, t := range snap.Offers {
		args = append(args, t)
	}
	args = append(args, len(snap.ActiveUids))
	for i, u := range snap.ActiveUids {
		args = append(args, u.String(), snap.ActiveExpMs[i])
	}
	res, err := r.sc.Run(ctx, redisx.QueueRestore, keys.QueueRestoreKeys(eid), args...)
	if err != nil {
		return false, err
	}
	switch res.Status {
	case "OK":
		r.log.InfoContext(ctx, "queue restored", slog.String("event_id", eid.String()), slog.Bool("fresh", fresh),
			slog.Int64("presize", presize), slog.Int64("tail", snap.Tail), slog.Int64("cursor", snap.Cursor),
			slog.Int("offers", len(snap.Offers)), slog.Int("active", len(snap.ActiveUids)))
		return true, nil
	case "ALREADY":
		return false, nil
	default:
		return false, fmt.Errorf("queue: restore: unexpected reply %q", res.Status)
	}
}

// Dump is the queue state queue_dump.lua reads for one event.
type Dump struct {
	Init     bool
	PreSize  int64 // -1 until frozen
	Tail     int64
	Cursor   int64
	SoldOut  bool
	Rate     float64
	PreCount int64 // lottery entries
	Offers   []int64
	Active   []uuid.UUID
	ActiveMS []int64
}

// Joined is how many tickets exist (or, before the freeze, lottery entries).
func (d Dump) Joined() int64 {
	if d.PreSize < 0 {
		return d.PreCount
	}
	return d.PreSize + d.Tail
}

// Waiting approximates the users still waiting: tickets not yet offered.
// Users who left are not subtracted (they are not tracked per ticket).
func (d Dump) Waiting() int64 {
	if d.PreSize < 0 {
		return d.PreCount
	}
	return max(0, d.Joined()-d.Cursor)
}

// ReadDump runs queue_dump.lua for eid.
func ReadDump(ctx context.Context, sc *redisx.Scripts, eid uuid.UUID) (Dump, error) {
	r, err := sc.Run(ctx, redisx.QueueDump, keys.QueueDumpKeys(eid))
	if err != nil {
		return Dump{}, err
	}
	return parseDump(append([]string{r.Status}, r.Fields...))
}

func parseDump(v []string) (Dump, error) {
	p := dumpParser{v: v}
	d := Dump{Init: p.int() == 1, PreSize: p.int(), Tail: p.int(), Cursor: p.int(), SoldOut: p.int() == 1}
	d.Rate = p.float()
	d.PreCount = p.int()
	n := p.int()
	for i := int64(0); i < n && p.err == nil; i++ {
		d.Offers = append(d.Offers, p.int())
	}
	n = p.int()
	for i := int64(0); i < n && p.err == nil; i++ {
		d.Active = append(d.Active, p.uuid())
		d.ActiveMS = append(d.ActiveMS, p.int())
	}
	if p.err == nil && p.i != len(v) {
		p.err = fmt.Errorf("%d trailing fields", len(v)-p.i)
	}
	if p.err != nil {
		return Dump{}, fmt.Errorf("queue: dump: %w", p.err)
	}
	return d, nil
}

// dumpParser reads queue_dump's flat reply; the first error sticks.
type dumpParser struct {
	v   []string
	i   int
	err error
}

func (p *dumpParser) next() string {
	if p.err != nil {
		return ""
	}
	if p.i >= len(p.v) {
		p.err = fmt.Errorf("reply ends at field %d", p.i)
		return ""
	}
	p.i++
	return p.v[p.i-1]
}

func (p *dumpParser) int() int64 {
	s := p.next()
	if p.err != nil {
		return 0
	}
	// ZRANGE ... WITHSCORES returns scores like "1.7e+12" for large values.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		p.err = fmt.Errorf("field %d %q: %w", p.i-1, s, err)
		return 0
	}
	return int64(f)
}

func (p *dumpParser) float() float64 {
	s := p.next()
	if p.err != nil {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		p.err = fmt.Errorf("field %d %q: %w", p.i-1, s, err)
	}
	return f
}

func (p *dumpParser) uuid() uuid.UUID {
	s := p.next()
	if p.err != nil {
		return uuid.Nil
	}
	u, err := uuid.Parse(s)
	if err != nil {
		p.err = fmt.Errorf("field %d: %w", p.i-1, err)
	}
	return u
}

// Snapshotter is the worker loop that persists every open event's queue
// state to queue_snapshots (R1) and publishes the per-event queue gauges.
type Snapshotter struct {
	q       *gen.Queries
	sc      *redisx.Scripts
	now     func() time.Time
	log     *slog.Logger
	metrics *WorkerMetrics
}

// NewSnapshotter builds the snapshot writer. now, log and m may be nil.
func NewSnapshotter(q *gen.Queries, sc *redisx.Scripts, now func() time.Time, log *slog.Logger,
	m *WorkerMetrics,
) (*Snapshotter, error) {
	if q == nil || sc == nil {
		return nil, errors.New("queue: snapshotter needs queries and scripts")
	}
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Snapshotter{q: q, sc: sc, now: now, log: log, metrics: m}, nil
}

// SnapshotOnce dumps eid's queue state and writes it to PG. An event whose
// counters are not initialised (q:init missing: Redis lost them and nobody
// has restored yet) is skipped, so a flush can never overwrite a good
// snapshot with zeros. It returns the dump and whether it was written.
func (s *Snapshotter) SnapshotOnce(ctx context.Context, eid uuid.UUID) (Dump, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, restoreTimeout)
	defer cancel()
	takenAt := s.now() // before the read: a newer read always carries a newer stamp
	d, err := ReadDump(ctx, s.sc, eid)
	if err != nil || !d.Init {
		return d, false, err
	}
	var presize *int64
	if d.PreSize >= 0 {
		presize = &d.PreSize
	}
	_, err = s.q.UpsertQueueSnapshot(ctx, gen.UpsertQueueSnapshotParams{
		EventID: eid, Presize: presize, Tail: d.Tail, Cursor: d.Cursor, SoldOut: d.SoldOut, AdmitRate: d.Rate,
		Offers: nonNil(d.Offers), ActiveUids: nonNil(d.Active), ActiveExpMs: nonNil(d.ActiveMS), TakenAt: takenAt,
	})
	if err != nil {
		return d, false, fmt.Errorf("queue: write snapshot: %w", err)
	}
	return d, true, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Run snapshots every open event each interval until ctx is done.
func (s *Snapshotter) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	var lastErr time.Time
	warn := func(msg string, err error) {
		if ctx.Err() == nil && time.Since(lastErr) > 5*time.Second {
			lastErr = time.Now()
			s.log.WarnContext(ctx, msg, slog.String("error", err.Error()))
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		lctx, cancel := context.WithTimeout(ctx, restoreTimeout)
		evs, err := s.q.ListOpenEvents(lctx)
		cancel()
		if err != nil {
			warn("queue snapshot: list events", err)
			continue
		}
		for _, ev := range evs {
			d, _, err := s.SnapshotOnce(ctx, ev.ID)
			if err != nil {
				warn("queue snapshot "+ev.ID.String(), err)
				continue
			}
			s.metrics.observe(ev.ID, d)
		}
	}
}
