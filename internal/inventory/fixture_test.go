package inventory_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/events"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
	"github.com/sheshakanthra/sluice/internal/testutil/pgtest"
	"github.com/sheshakanthra/sluice/internal/testutil/redistest"
)

var (
	pgs *pgtest.Server
	rs  *redistest.Server
)

// TestMain starts one Postgres 18 and one Redis 8 container for the package.
// Tests here never call t.Parallel: each flushes the shared Redis.
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

// modes are the two hold paths every race test runs against.
var modes = []struct {
	name   string
	pgOnly bool
}{{"redis_pg", false}, {"pg_only", true}}

type env struct {
	t       *testing.T
	ctx     context.Context
	pool    *pgxpool.Pool
	q       *gen.Queries
	rdb     *redis.Client
	sc      *redisx.Scripts
	svc     *inventory.Service
	reg     *prometheus.Registry
	pgOnly  bool
	ttl     time.Duration
	eventID uuid.UUID
	slug    string
}

type envOpt func(*envConfig)

type envConfig struct {
	capacity  int
	ttl       time.Duration
	pgOnly    bool
	onReclaim inventory.ReclaimHook
}

func capacity(n int) envOpt      { return func(c *envConfig) { c.capacity = n } }
func ttl(d time.Duration) envOpt { return func(c *envConfig) { c.ttl = d } }
func pgOnly(b bool) envOpt       { return func(c *envConfig) { c.pgOnly = b } }

func onReclaim(h inventory.ReclaimHook) envOpt { return func(c *envConfig) { c.onReclaim = h } }

func mustNil(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// newEnv creates a migrated database, an ON_SALE event with capacity seats,
// a flushed Redis and a service. In redis_pg mode the event is loaded.
func newEnv(t *testing.T, opts ...envOpt) *env {
	t.Helper()
	cfg := envConfig{capacity: 50, ttl: time.Minute}
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
	m, err := inventory.NewMetrics(reg)
	mustNil(t, err)
	svc, err := inventory.NewService(pool, rdb, sc, inventory.Options{
		HoldTTL: cfg.ttl, MaxSeats: 4, PGOnly: cfg.pgOnly, Metrics: m, OnReclaim: cfg.onReclaim,
	})
	mustNil(t, err)
	e := &env{t: t, ctx: ctx, pool: pool, q: gen.New(pool), rdb: rdb, sc: sc, svc: svc, reg: reg,
		pgOnly: cfg.pgOnly, ttl: cfg.ttl}
	e.eventID, e.slug = e.newEvent(cfg.capacity)
	if !cfg.pgOnly {
		_, err := svc.Rebuild(ctx, e.eventID)
		mustNil(t, err)
	}
	return e
}

func (e *env) newEvent(capacity int) (uuid.UUID, string) {
	e.t.Helper()
	now := time.Now().UTC()
	slug := "t-" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	l, err := events.TrainLayout(capacity)
	mustNil(e.t, err)
	lj, err := json.Marshal(l)
	mustNil(e.t, err)
	_, err = e.q.InsertEventIfAbsent(e.ctx, gen.InsertEventIfAbsentParams{
		Slug: slug, Name: "Test " + slug, Venue: "Test", StartsAt: now.Add(48 * time.Hour),
		PrequeueOpensAt: now.Add(-time.Hour), SaleOpensAt: now.Add(-time.Hour),
		Capacity: int32(capacity), PricePaise: 50_000, MaxPerUser: 4, Status: gen.EventStatusONSALE, Layout: lj, //nolint:gosec // test sizes
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
	return ev.ID, slug
}

func (e *env) hold(uid uuid.UUID, key string, req inventory.Request) (inventory.Hold, error) {
	return e.svc.Hold(e.ctx, e.eventID, uid, key, req)
}

func seatsReq(s ...int32) inventory.Request { return inventory.Request{SeatNos: s} }
func qtyReq(n int) inventory.Request        { return inventory.Request{Quantity: n} }

func newKey() string { return uuid.NewString() }

// pgSeats returns seat_no -> (state, hold_id) from Postgres.
func (e *env) pgSeats() map[int32]gen.ListSeatStatesRow {
	e.t.Helper()
	rows, err := e.q.ListSeatStates(e.ctx, e.eventID)
	mustNil(e.t, err)
	out := make(map[int32]gen.ListSeatStatesRow, len(rows))
	for _, r := range rows {
		out[r.SeatNo] = r
	}
	return out
}

func (e *env) countSeats(state gen.SeatState) int {
	n := 0
	for _, r := range e.pgSeats() {
		if r.State == state {
			n++
		}
	}
	return n
}

func (e *env) countHolds(status gen.HoldStatus) int {
	e.t.Helper()
	var n int
	// Test-only assertion query; not part of the application's SQL.
	err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM holds WHERE event_id = $1 AND status = $2`, e.eventID, status).Scan(&n)
	mustNil(e.t, err)
	return n
}

func (e *env) pgHold(id uuid.UUID) gen.Hold {
	e.t.Helper()
	h, err := e.q.GetHold(e.ctx, id)
	mustNil(e.t, err)
	return h
}

func (e *env) redisSet(key string) []int32 {
	e.t.Helper()
	ss, err := e.rdb.SMembers(e.ctx, key).Result()
	mustNil(e.t, err)
	out := make([]int32, 0, len(ss))
	for _, s := range ss {
		n, err := strconv.Atoi(s)
		mustNil(e.t, err)
		out = append(out, int32(n)) //nolint:gosec // test sizes
	}
	slices.Sort(out)
	return out
}

func (e *env) inAvail(seat int32) bool {
	e.t.Helper()
	ok, err := e.rdb.SIsMember(e.ctx, keys.Avail(e.eventID), seat).Result()
	mustNil(e.t, err)
	return ok
}

func (e *env) redisHoldExists(hid uuid.UUID) bool {
	e.t.Helper()
	n, err := e.rdb.Exists(e.ctx, keys.Hold(e.eventID, hid)).Result()
	mustNil(e.t, err)
	return n == 1
}

// redisSeatStates derives A/H/S per seat from Redis: avail, sold and the
// seats listed in hold hashes. A seat in two places is reported as "X".
func (e *env) redisSeatStates(capacity int) map[int32]string {
	e.t.Helper()
	out := make(map[int32]string, capacity)
	mark := func(s int32, st string) {
		if prev, ok := out[s]; ok && prev != st {
			out[s] = "X"
			return
		}
		out[s] = st
	}
	for _, s := range e.redisSet(keys.Avail(e.eventID)) {
		mark(s, "A")
	}
	for _, s := range e.redisSet(keys.Sold(e.eventID)) {
		mark(s, "S")
	}
	ids, err := e.rdb.ZRange(e.ctx, keys.Holds(e.eventID), 0, -1).Result()
	mustNil(e.t, err)
	for _, id := range ids {
		csv, err := e.rdb.HGet(e.ctx, keys.Prefix(e.eventID)+"hold:"+id, "seats").Result()
		mustNil(e.t, err)
		for _, f := range strings.Split(csv, ",") {
			n, err := strconv.Atoi(f)
			mustNil(e.t, err)
			mark(int32(n), "H") //nolint:gosec // test sizes
		}
	}
	return out
}

// pgSeatStates is the same view from Postgres (HELD past held_until counts
// as available, as in a rebuild).
func (e *env) pgSeatStates() map[int32]string {
	out := map[int32]string{}
	now := time.Now()
	for s, r := range e.pgSeats() {
		switch {
		case r.State == gen.SeatStateSOLD:
			out[s] = "S"
		case r.State == gen.SeatStateHELD && r.HeldUntil.After(now):
			out[s] = "H"
		default:
			out[s] = "A"
		}
	}
	return out
}

// race runs n goroutines that each call fn(i) and returns the results.
func race[T any](n int, fn func(i int) T) []T {
	out := make([]T, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			out[i] = fn(i)
		})
	}
	close(start)
	wg.Wait()
	return out
}

func redisZ(at time.Time, id uuid.UUID) redis.Z {
	return redis.Z{Score: float64(at.UnixMilli()), Member: id.String()}
}

func csv(seats []int32) string {
	s := make([]string, len(seats))
	for i, n := range seats {
		s[i] = strconv.Itoa(int(n))
	}
	return strings.Join(s, ",")
}

func errorsIsTaken(err error) bool { return errors.Is(err, inventory.ErrSeatsTaken) }

// diffStates lists up to 10 seats where two seat-state views disagree.
func diffStates(got, want map[int32]string) string {
	var out []string
	for s, w := range want {
		if got[s] != w {
			out = append(out, fmt.Sprintf("seat %d: got %q want %q", s, got[s], w))
		}
	}
	for s, g := range got {
		if _, ok := want[s]; !ok {
			out = append(out, fmt.Sprintf("seat %d: got %q, not in PG", s, g))
		}
	}
	slices.Sort(out)
	return strings.Join(out[:min(10, len(out))], "; ")
}
