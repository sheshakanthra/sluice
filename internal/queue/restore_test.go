package queue_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/queue"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// TestRestoreAfterFlushAll is R1 for the queue: FLUSHALL mid-queue, then
// restore from queue_snapshots. Waiting users keep their exact positions
// (the snapshot was the last thing written before the flush), new joins
// continue after the old tail, admitted users are not admitted again, and
// outstanding offers can still be claimed exactly once.
func TestRestoreAfterFlushAll(t *testing.T) {
	e := newEnv(t, capacity(20), tickCfg(queue.TickConfig{
		MaxActive: 10, Overadmit: 1.5, MinAdmit: 1, MaxPerTick: 5, OfferTTL: time.Minute,
	}))
	eid := e.ev.ID
	pre := e.joinAll(30, 8) // lottery entries
	e.toT0(time.Millisecond)
	e.tick() // freezes presize = 30 and offers 1..5
	e.refresh()
	// Each pre user's first status resolves their lottery ticket; tickets
	// 1..5 are already offered, so those users also claim (admitted).
	admittedExp := map[int64]time.Time{}
	for _, u := range pre {
		r := e.status(u)
		if u.ticket < 1 || u.ticket > 30 {
			t.Fatalf("pre user resolved to %d (%s)", u.ticket, r.State)
		}
		if r.State == queue.StateAdmitted {
			admittedExp[u.ticket] = *r.AdmissionExpiresAt
		}
	}
	if len(admittedExp) != 5 {
		t.Fatalf("%d admitted after the first tick, want 5", len(admittedExp))
	}
	post := e.joinAll(20, 4) // tickets 31..50
	e.tick()                 // offers 6..10
	snap := e.refresh()
	if snap.Cursor != 10 {
		t.Fatalf("cursor before flush = %d, want 10", snap.Cursor)
	}
	all := append(append([]*user{}, pre...), post...)
	byTicket := map[int64]*user{}
	for _, u := range all {
		byTicket[u.ticket] = u
	}
	if len(byTicket) != 50 {
		t.Fatalf("%d distinct tickets for 50 users", len(byTicket))
	}
	// Tickets 6..7 claim too; 8..10 stay outstanding offers.
	for tk := int64(6); tk <= 7; tk++ {
		r := e.status(byTicket[tk])
		if r.State != queue.StateAdmitted {
			t.Fatalf("ticket %d: %s", tk, r.State)
		}
		admittedExp[tk] = *r.AdmissionExpiresAt
	}
	before := e.metric("queue_admitted_total")
	positions := map[int64]int64{}
	for tk := int64(11); tk <= 50; tk++ {
		r := e.status(byTicket[tk])
		if r.State != queue.StateWaiting {
			t.Fatalf("ticket %d before flush: %s", tk, r.State)
		}
		positions[tk] = *r.Position
	}
	d, written, err := e.snapper.SnapshotOnce(e.ctx, eid)
	mustNil(t, err)
	if !written || d.Cursor != 10 || d.Tail != 20 || d.PreSize != 30 || len(d.Offers) != 3 || len(d.Active) != 7 {
		t.Fatalf("snapshot written=%v %+v", written, d)
	}

	mustNil(t, e.rdb.FlushAll(e.ctx).Err())
	// While nothing is restored, the writer must not overwrite the snapshot.
	if _, written, err := e.snapper.SnapshotOnce(e.ctx, eid); err != nil || written {
		t.Fatalf("snapshot of a flushed queue: written=%v err=%v", written, err)
	}
	_, err = e.inv.Rebuild(e.ctx, eid) // inventory side of R1 (P2)
	mustNil(t, err)

	// The first join after the flush restores the counters, then continues
	// after the old tail: no ticket number is reused.
	late := &user{id: uuid.New()}
	if r := e.mustJoin(late); r.TicketNo == nil || *r.TicketNo != 51 {
		t.Fatalf("join after flush got %+v, want ticket 51", r.TicketNo)
	}
	snap = e.refresh()
	if snap.Cursor != 10 || snap.PreSize != 30 || snap.Tail != 21 || snap.Offers != 3 || snap.Active != 7 {
		t.Fatalf("restored snapshot %+v", snap)
	}
	for tk, pos := range positions {
		r := e.status(byTicket[tk])
		if r.State != queue.StateWaiting || *r.Position != pos {
			t.Errorf("ticket %d after restore: %s position %v, want %d", tk, r.State, r.Position, pos)
		}
	}
	// Admitted users keep their admission (same expiry); nobody new counted.
	for tk, exp := range admittedExp {
		r := e.status(byTicket[tk])
		if r.State != queue.StateAdmitted || !r.AdmissionExpiresAt.Equal(exp) {
			t.Errorf("ticket %d after restore: %s exp %v, want %v", tk, r.State, r.AdmissionExpiresAt, exp)
		}
	}
	if got := e.metric("queue_admitted_total"); got != before {
		t.Errorf("re-claims by admitted users counted %v new admissions", got-before)
	}
	// Outstanding offers 8..10 are claimable once each.
	for tk := int64(8); tk <= 10; tk++ {
		if r := e.status(byTicket[tk]); r.State != queue.StateAdmitted {
			t.Errorf("offer %d after restore: %s", tk, r.State)
		}
	}
	if got := e.metric("queue_admitted_total"); got != before+3 {
		t.Errorf("admissions after claiming 3 restored offers = +%v, want +3", got-before)
	}
	if n := e.rdb.ZCard(e.ctx, keys.QActive(eid)).Val(); n != 10 {
		t.Errorf("active = %d, want 10 distinct admitted users", n)
	}
	// The controller picks up where it left off: next offers start at 11.
	e.clk.Advance(time.Millisecond)
	res := e.tick()
	if res.State != queue.TickAdmitting {
		t.Fatalf("tick after restore: %+v", res)
	}
	if c := e.refresh().Cursor; c != 10+res.Admitted {
		t.Errorf("cursor after tick = %d, want %d", c, 10+res.Admitted)
	}
	// A slower writer holding an older dump can never replace a newer row.
	n, err := e.q.UpsertQueueSnapshot(e.ctx, gen.UpsertQueueSnapshotParams{
		EventID: eid, Offers: []int64{}, ActiveUids: []uuid.UUID{}, ActiveExpMs: []int64{},
		TakenAt: e.clk.Now().Add(-time.Hour),
	})
	mustNil(t, err)
	if n != 0 {
		t.Error("an older snapshot overwrote a newer one")
	}
	t.Logf("restore: 50 tickets, cursor 10, 7 admitted + 3 offers; after FLUSHALL: next ticket 51, "+
		"40 waiting positions unchanged, 7 admissions kept (+0 new), 3 offers claimed (+3), active %d",
		e.rdb.ZCard(e.ctx, keys.QActive(eid)).Val())
}

// TestScaleJoins: 50,000 joins through Service.Join (25,000 before T0 into
// the lottery, 25,000 after it), 64 at a time, against Redis 8. Reports the
// time taken and Redis memory; the stated budget is 120 s (it also runs
// under -race).
func TestScaleJoins(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	const n, par, budget = 50_000, 64, 120 * time.Second
	e := newEnv(t)
	used := func() int64 {
		info := e.rdb.InfoMap(e.ctx, "memory").Val()
		v, err := strconv.ParseInt(info["Memory"]["used_memory"], 10, 64)
		mustNil(t, err)
		return v
	}
	mem0 := used()
	start := time.Now()
	e.joinAll(n/2, par)
	e.toT0(time.Millisecond)
	e.joinAll(n/2, par)
	elapsed := time.Since(start)
	mem1 := used()
	snap := e.refresh()
	if snap.PreSize != n/2 || snap.Tail != n/2 {
		t.Fatalf("presize %d tail %d, want %d each", snap.PreSize, snap.Tail, n/2)
	}
	t.Logf("scale: %d joins (par %d) in %v = %.0f joins/s; Redis used_memory %d -> %d bytes (+%.1f MiB, %.0f B/user)",
		n, par, elapsed.Round(time.Millisecond), float64(n)/elapsed.Seconds(), mem0, mem1,
		float64(mem1-mem0)/(1<<20), float64(mem1-mem0)/n)
	if elapsed > budget {
		t.Errorf("50k joins took %v, budget %v", elapsed, budget)
	}
}

// TestStatsAndOpenSale covers GET /stats (live queue and inventory, the
// audit block, the 1 s cache) and the admin open-sale endpoint.
func TestStatsAndOpenSale(t *testing.T) {
	e := newEnv(t, capacity(10), opensIn(time.Hour))
	const admin = "test-admin-token"
	st, err := queue.NewStatsService(queue.StatsDeps{
		Q: e.q, Cache: e.svc.Cache(), Events: e.inv, Counts: e.inv.NewAvailabilityCache(0), Redis: e.rdb,
		Scripts: e.sc, Restorer: e.restorer, AdminToken: admin, Now: e.clk.Now,
	})
	mustNil(t, err)
	rt := httpx.NewRouter()
	queue.RegisterStatsRoutes(rt, st)
	stats := func() queue.Stats {
		t.Helper()
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequestWithContext(e.ctx, http.MethodGet, "/api/v1/stats/"+e.ev.ID.String(), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("stats: %d %s", rec.Code, rec.Body)
		}
		var s queue.Stats
		mustNil(t, json.Unmarshal(rec.Body.Bytes(), &s))
		return s
	}
	openSale := func(body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(e.ctx, http.MethodPost,
			"/api/v1/admin/events/"+e.ev.ID.String()+"/open-sale", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		return rec
	}

	s := stats()
	if s.EventStatus != gen.EventStatusSCHEDULED || s.Queue.Joined != 0 || s.Inventory.Available != 10 ||
		s.Inventory.Capacity != 10 || s.Correctness.Oversold != nil || s.DemoRush.Running {
		t.Fatalf("initial stats %+v", s)
	}
	if rec := openSale(`{"opens_in_s":30}`, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("open-sale without admin token: %d", rec.Code)
	}
	if rec := openSale(`{"opens_in_s":-1}`, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("open-sale with a bad body: %d", rec.Code)
	}
	rec := openSale(`{"opens_in_s":30}`, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("open-sale: %d %s", rec.Code, rec.Body)
	}
	t0 := e.clk.Now().Add(30 * time.Second)
	ev, err := e.q.GetEventByID(e.ctx, e.ev.ID)
	mustNil(t, err)
	if !ev.SaleOpensAt.Equal(t0) || ev.Status != gen.EventStatusSCHEDULED {
		t.Fatalf("after open-sale: opens %v status %s, want %v SCHEDULED", ev.SaleOpensAt, ev.Status, t0)
	}

	users := e.joinAll(5, 2) // still PREQUEUE (q:opens is the new T0)
	s = stats()
	if s.Queue.Joined != 0 {
		t.Errorf("stats changed within its 1 s cache: joined %d", s.Queue.Joined)
	}
	e.clk.Advance(time.Second)
	if s = stats(); s.Queue.Joined != 5 || s.Queue.Waiting != 5 || !s.SaleOpensAt.Equal(t0) {
		t.Errorf("stats after 5 pre-queue joins: %+v", s.Queue)
	}
	if rec := openSale(`{"opens_in_s":10}`, admin); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), string(httpx.CodeEventState)) {
		t.Errorf("open-sale with entries: %d %s", rec.Code, rec.Body)
	}

	e.clk.Set(t0.Add(time.Millisecond))
	e.ev.SaleOpensAt = t0
	res := e.tick()
	if res.State != queue.TickAdmitting || res.Admitted != 5 {
		t.Fatalf("tick at the new T0: %+v", res)
	}
	mustNil(t, e.rdb.HSet(e.ctx, keys.Audit(e.ev.ID), "oversold", 0, "duplicates", 0, "orphans", 0,
		"at_ms", e.clk.Now().UnixMilli()).Err())
	e.clk.Advance(time.Second)
	s = stats()
	if s.EventStatus != gen.EventStatusONSALE || s.Queue.Joined != 5 || s.Queue.Cursor != 5 ||
		s.Queue.Offers != 5 || s.Queue.Waiting != 0 || s.Queue.AdmitRate <= 0 {
		t.Errorf("stats after admission: %s %+v", s.EventStatus, s.Queue)
	}
	if s.Correctness.Oversold == nil || *s.Correctness.Oversold != 0 || s.Correctness.CheckedAt == nil {
		t.Errorf("correctness %+v", s.Correctness)
	}
	r := e.status(users[0])
	if r.State != queue.StateAdmitted {
		t.Fatalf("first user: %s", r.State)
	}
	e.clk.Advance(time.Second)
	if s = stats(); s.Queue.Active != 1 || s.Queue.Offers != 4 {
		t.Errorf("stats after one claim: %+v", s.Queue)
	}
	t.Logf("stats: %+v inventory %+v", s.Queue, s.Inventory)
}
