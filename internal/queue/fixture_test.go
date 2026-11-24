package queue_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/events"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/queue"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
	"github.com/sheshakanthra/sluice/internal/testutil/pgtest"
	"github.com/sheshakanthra/sluice/internal/testutil/redistest"
)

var (
	pgs *pgtest.Server
	rs  *redistest.Server
)

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	var (
		wg           sync.WaitGroup
		pgErr, rdErr error
	)
	wg.Go(func() { pgs, pgErr = pgtest.Start(ctx) })
	wg.Go(func() { rs, rdErr = redistest.Start(ctx) })
	wg.Wait()
	cancel()
	if pgErr != nil || rdErr != nil {
		_, _ = fmt.Fprintln(os.Stderr, "containers:", pgErr, rdErr)
		os.Exit(1)
	}
	code := m.Run()
	pgs.Close()
	rs.Close()
	os.Exit(code)
}

// clock is a settable clock shared by the queue service and controller.
type clock struct{ ms atomic.Int64 }

func (c *clock) Now() time.Time          { return time.UnixMilli(c.ms.Load()).UTC() }
func (c *clock) Set(t time.Time)         { c.ms.Store(t.UnixMilli()) }
func (c *clock) Advance(d time.Duration) { c.ms.Add(d.Milliseconds()) }

type envConfig struct {
	capacity  int
	opensIn   time.Duration // sale_opens_at relative to the env's start
	tick      queue.TickConfig
	realClock bool          // controller uses time.Now (concurrent-controller test)
	idle      time.Duration // ADMISSION_IDLE_TTL (0: same as the 10 min ADMISSION_TTL)
}

type envOpt func(*envConfig)

func capacity(n int) envOpt             { return func(c *envConfig) { c.capacity = n } }
func opensIn(d time.Duration) envOpt    { return func(c *envConfig) { c.opensIn = d } }
func tickCfg(t queue.TickConfig) envOpt { return func(c *envConfig) { c.tick = t } }
func realClock() envOpt                 { return func(c *envConfig) { c.realClock = true } }
func idleTTL(d time.Duration) envOpt    { return func(c *envConfig) { c.idle = d } }

// admissionTTL is the env's ADMISSION_TTL (the hard cap).
const admissionTTL = 10 * time.Minute

func defaultTick() queue.TickConfig {
	return queue.TickConfig{MaxActive: 1000, Overadmit: 1.5, MinAdmit: 10, MaxPerTick: 200, OfferTTL: time.Minute}
}

type env struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	q      *gen.Queries
	rdb    *redis.Client
	sc     *redisx.Scripts
	inv    *inventory.Service
	keys   *auth.Keyring
	svc    *queue.Service
	ctrl   *queue.Controller
	clk    *clock
	reg    *prometheus.Registry
	router http.Handler
	ev     queue.Event
	cfg    envConfig

	restorer *queue.Restorer
	snapper  *queue.Snapshotter
	wm       *queue.WorkerMetrics
	lease    *queue.Lease // renews admission leases on the fake clock
}

func mustNil(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// newEnv creates a migrated database with one SCHEDULED event whose sale
// opens at start+opensIn, loads its inventory into a flushed Redis, and
// builds the queue service (manual snapshot refresh) and a controller.
// The fake clock starts at the env's start.
func newEnv(t *testing.T, opts ...envOpt) *env {
	t.Helper()
	cfg := envConfig{capacity: 50, opensIn: time.Minute, tick: defaultTick()}
	for _, o := range opts {
		o(&cfg)
	}
	pool, _ := pgs.NewMigratedDB(t)
	rdb := rs.NewClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	reg := prometheus.NewRegistry()
	sc, err := redisx.NewScripts(rdb, reg)
	mustNil(t, err)
	im, err := inventory.NewMetrics(reg)
	mustNil(t, err)
	inv, err := inventory.NewService(pool, rdb, sc, inventory.Options{HoldTTL: time.Minute, MaxSeats: 4, Metrics: im})
	mustNil(t, err)
	kr, err := auth.NewKeyring([]config.TokenKey{{KID: "test", Secret: []byte(strings.Repeat("k", 32))}}, nil)
	mustNil(t, err)

	start := time.Now().UTC().Truncate(time.Millisecond)
	clk := &clock{}
	clk.Set(start)
	e := &env{t: t, ctx: ctx, pool: pool, q: gen.New(pool), rdb: rdb, sc: sc, inv: inv, keys: kr, clk: clk, reg: reg, cfg: cfg}
	eid := e.newEvent(cfg.capacity, start.Add(cfg.opensIn))
	_, err = inv.Rebuild(ctx, eid)
	mustNil(t, err)
	info, err := inv.Event(ctx, eid.String())
	mustNil(t, err)
	e.ev = queue.Event{ID: info.ID, Capacity: info.Capacity, SaleOpensAt: info.SaleOpensAt, StartsAt: info.StartsAt, Status: info.Status}

	qm, err := queue.NewMetrics(reg)
	mustNil(t, err)
	e.wm, err = queue.NewWorkerMetrics(reg)
	mustNil(t, err)
	e.restorer, err = queue.NewRestorer(e.q, sc, cfg.tick.OfferTTL, clk.Now, nil)
	mustNil(t, err)
	e.snapper, err = queue.NewSnapshotter(e.q, sc, clk.Now, nil, e.wm)
	mustNil(t, err)
	e.svc, err = queue.NewService(sc, kr, queue.NewCache(sc, clk.Now, nil), queue.Options{
		AdmissionTTL: admissionTTL, AdmissionIdleTTL: cfg.idle, Overadmit: cfg.tick.Overadmit, Now: clk.Now, Metrics: qm,
		Restorer: e.restorer,
	})
	mustNil(t, err)
	idle := cfg.idle
	if idle == 0 {
		idle = admissionTTL
	}
	e.lease, err = queue.NewLease(sc, idle, clk.Now, nil, reg)
	mustNil(t, err)
	ticker, err := queue.NewTicker(sc, cfg.tick)
	mustNil(t, err)
	ticker.WithRestorer(e.restorer, e.wm)
	copt := queue.ControllerOptions{Tick: 10 * time.Millisecond, Refresh: 50 * time.Millisecond, Now: clk.Now}
	if cfg.realClock {
		copt.Now = nil
	}
	e.ctrl, err = queue.NewController(e.q, ticker, copt)
	mustNil(t, err)
	rt := httpx.NewRouter()
	queue.RegisterRoutes(rt, queue.HandlerDeps{Svc: e.svc, Events: inv, Keys: kr})
	e.router = rt
	return e
}

func (e *env) newEvent(capacity int, opens time.Time) uuid.UUID {
	e.t.Helper()
	slug := "q-" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	l, err := events.TrainLayout(capacity)
	mustNil(e.t, err)
	lj, err := json.Marshal(l)
	mustNil(e.t, err)
	_, err = e.q.InsertEventIfAbsent(e.ctx, gen.InsertEventIfAbsentParams{
		Slug: slug, Name: "Queue " + slug, Venue: "Test", StartsAt: opens.Add(48 * time.Hour),
		PrequeueOpensAt: opens.Add(-time.Hour), SaleOpensAt: opens,
		Capacity: int32(capacity), PricePaise: 50_000, MaxPerUser: 4, Status: gen.EventStatusSCHEDULED, Layout: lj, //nolint:gosec // test sizes
	})
	mustNil(e.t, err)
	ev, err := e.q.GetEventBySlug(e.ctx, slug)
	mustNil(e.t, err)
	p := gen.InsertSeatsParams{EventID: ev.ID}
	for _, s := range l.Seats() {
		p.SeatNos = append(p.SeatNos, s.SeatNo)
		p.Labels = append(p.Labels, s.Label)
		p.Sections = append(p.Sections, s.Section)
		p.RowNos = append(p.RowNos, s.Row)
		p.ColNos = append(p.ColNos, s.Col)
	}
	_, err = e.q.InsertSeats(e.ctx, p)
	mustNil(e.t, err)
	return ev.ID
}

// toT0 moves the fake clock to the sale opening (plus d).
func (e *env) toT0(d time.Duration) { e.clk.Set(e.ev.SaleOpensAt.Add(d)) }

// user is a queue participant: id plus the latest queue token.
type user struct {
	id     uuid.UUID
	token  string
	ticket int64 // 0 until known
}

func (e *env) join(u *user, rejoin bool) (queue.JoinResponse, error) {
	r, err := e.svc.Join(e.ctx, e.ev, u.id, rejoin)
	if err == nil && r.QueueToken != nil {
		u.token = *r.QueueToken
	}
	if err == nil && r.TicketNo != nil {
		u.ticket = *r.TicketNo
	}
	return r, err
}

func (e *env) mustJoin(u *user) queue.JoinResponse {
	e.t.Helper()
	r, err := e.join(u, false)
	mustNil(e.t, err)
	return r
}

// joinAll joins n new users with par workers; users[i] is the i-th to
// finish joining.
func (e *env) joinAll(n, par int) []*user {
	e.t.Helper()
	out := make([]*user, n)
	var started, done atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, par)
	for range par {
		wg.Go(func() {
			for started.Add(1) <= int64(n) {
				u := &user{id: uuid.New()}
				if _, err := e.join(u, false); err != nil {
					errs <- err
					return
				}
				out[done.Add(1)-1] = u
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		e.t.Fatal(err)
	}
	return out
}

// status calls Service.Status with u's token and adopts a re-issued token.
func (e *env) status(u *user) queue.StatusResponse {
	e.t.Helper()
	c, err := e.keys.Verify(u.token, auth.AudQueue)
	mustNil(e.t, err)
	r, err := e.svc.Status(e.ctx, e.ev, c)
	mustNil(e.t, err)
	if r.QueueToken != nil {
		u.token = *r.QueueToken
	}
	if r.TicketNo != nil {
		u.ticket = *r.TicketNo
	}
	return r
}

// newController builds an extra controller sharing the env's tick config.
func (e *env) newController() *queue.Controller {
	e.t.Helper()
	ticker, err := queue.NewTicker(e.sc, e.cfg.tick)
	mustNil(e.t, err)
	ticker.WithRestorer(e.restorer, e.wm)
	opt := queue.ControllerOptions{Tick: 10 * time.Millisecond, Refresh: 50 * time.Millisecond, Now: e.clk.Now}
	if e.cfg.realClock {
		opt.Now = nil
	}
	c, err := queue.NewController(e.q, ticker, opt)
	mustNil(e.t, err)
	return c
}

// metric returns the value of a counter or gauge registered on the env.
func (e *env) metric(name string) float64 {
	e.t.Helper()
	mfs, err := e.reg.Gather()
	mustNil(e.t, err)
	for _, mf := range mfs {
		if mf.GetName() == name && len(mf.GetMetric()) > 0 {
			m := mf.GetMetric()[0]
			if c := m.GetCounter(); c != nil {
				return c.GetValue()
			}
			return m.GetGauge().GetValue()
		}
	}
	e.t.Fatalf("metric %s not found", name)
	return 0
}

func (e *env) refresh() *queue.Snapshot {
	e.t.Helper()
	s, err := e.svc.Cache().Refresh(e.ctx, e.ev.ID)
	mustNil(e.t, err)
	return s
}

// row is the event as the controller sees it.
func (e *env) row() gen.ListOpenEventsRow {
	e.t.Helper()
	rows, err := e.q.ListOpenEvents(e.ctx)
	mustNil(e.t, err)
	for _, r := range rows {
		if r.ID == e.ev.ID {
			return r
		}
	}
	e.t.Fatal("event not open")
	return gen.ListOpenEventsRow{}
}

func (e *env) tick() queue.TickResult {
	e.t.Helper()
	res, _, err := e.ctrl.TickOnce(e.ctx, e.row())
	mustNil(e.t, err)
	return res
}

func (e *env) pgStatus() gen.EventStatus {
	e.t.Helper()
	ev, err := e.q.GetEventByID(e.ctx, e.ev.ID)
	mustNil(e.t, err)
	return ev.Status
}

func (e *env) get(key string) string {
	e.t.Helper()
	v, err := e.rdb.Get(e.ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return ""
	}
	mustNil(e.t, err)
	return v
}

// seekingOutstanding reads admitted-without-hold plus unclaimed offers.
func (e *env) seekingOutstanding() int64 {
	e.t.Helper()
	eid := e.ev.ID
	active := e.rdb.ZCard(e.ctx, keys.QActive(eid)).Val()
	holding := e.rdb.ZInterCard(e.ctx, 0, keys.QActive(eid), keys.Holding(eid)).Val()
	return max(0, active-holding) + e.rdb.ZCard(e.ctx, keys.QOffers(eid)).Val()
}

// do sends an HTTP request through the queue routes.
func (e *env) do(method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(e.ctx, method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// countHook counts every command sent on a client.
type countHook struct{ n atomic.Int64 }

func (h *countHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *countHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.n.Add(1)
		return next(ctx, cmd)
	}
}
func (h *countHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		h.n.Add(int64(len(cmds)))
		return next(ctx, cmds)
	}
}
