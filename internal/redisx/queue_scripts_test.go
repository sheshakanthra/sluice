package redisx_test

import (
	"fmt"
	"slices"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// Direct contract tests for the queue scripts (DATA-MODEL §5). Behavioural
// tests (lottery, freeze race, bounded admission, ...) live in internal/queue.

const opensMS = nowMS + 60_000 // T0 one minute after nowMS

func (f *fixture) join(uid uuid.UUID, now int64, score string, rejoin bool) redisx.Reply {
	f.t.Helper()
	rj := 0
	if rejoin {
		rj = 1
	}
	return f.run(redisx.QueueJoin, keys.QueueJoinKeys(f.eid, uid), uid.String(), now, opensMS, score, rj, 2)
}

func (f *fixture) tick(now int64, maxActive, minAdmit, maxPerTick int) redisx.Reply {
	f.t.Helper()
	return f.run(redisx.QueueTick, keys.QueueTickKeys(f.eid), now, opensMS, maxActive, "1.5", minAdmit, maxPerTick, 60_000, 5_000)
}

func (f *fixture) claim(ticket int64, uid uuid.UUID, now int64) redisx.Reply {
	f.t.Helper()
	return f.claimIdle(ticket, uid, now, 600_000) // idle = cap: the lease behaves like a fixed TTL
}

func (f *fixture) claimIdle(ticket int64, uid uuid.UUID, now, idle int64) redisx.Reply {
	f.t.Helper()
	return f.run(redisx.QueueClaim, keys.QueueClaimKeys(f.eid), ticket, uid.String(), now, 600_000, idle)
}

func (f *fixture) touch(uid uuid.UUID, now, idle, capMS int64) redisx.Reply {
	f.t.Helper()
	return f.run(redisx.QueueTouch, keys.QueueTouchKeys(f.eid), uid.String(), now, idle, capMS)
}

func (f *fixture) lease(uid uuid.UUID) (int64, bool) {
	f.t.Helper()
	v, err := f.rdb.ZScore(f.ctx, keys.QActive(f.eid), uid.String()).Result()
	if err != nil {
		return 0, false
	}
	return int64(v), true
}

// TestAdmissionLease (ADR 0010 addendum): a claim sets the idle lease
// min(now + idle, cap) in q:active and the hard cap in q:cap; touch renews
// with ZADD XX GT, never past the cap, never shortening, and never
// re-creating an admission that ended; the tick drops idle and capped
// leases and prunes q:cap.
func TestAdmissionLease(t *testing.T) {
	const idle, ttl = 60_000, 600_000
	f := newFixture(t)
	f.initQueue()
	f.load(10, nil, 0)
	users := make([]uuid.UUID, 3)
	for i := range users {
		users[i] = uuid.New()
		f.join(users[i], opensMS, "0", false)
	}
	f.tick(opensMS, 100, 1, 10)
	t0 := int64(opensMS + 10)
	capMS := t0 + ttl
	for i, u := range users {
		if got := reply(f.claimIdle(int64(i+1), u, t0, idle)); got != fmt.Sprintf("[ADMITTED %d 1]", capMS) {
			t.Fatalf("claim %d = %s (the token carries the hard cap)", i, got)
		}
		if l, _ := f.lease(u); l != t0+idle {
			t.Fatalf("lease %d = %d, want claim + idle %d", i, l, t0+idle)
		}
	}
	if got := reply(f.claimIdle(1, users[0], t0+5, idle)); got != fmt.Sprintf("[ADMITTED %d 0]", capMS) {
		t.Errorf("re-claim = %s, want the same cap", got)
	}

	tests := []struct {
		name    string
		uid     uuid.UUID
		now     int64
		want    string
		wantExp int64 // 0: not in q:active
	}{
		{"renew slides the lease", users[0], t0 + 30_000, "[OK 1]", t0 + 30_000 + idle},
		{"an older request never shortens it", users[0], t0 + 10_000, "[OK 0]", t0 + 30_000 + idle},
		{"near the cap the lease stops at the cap", users[0], capMS - 1_000, "[OK 1]", capMS},
		{"past the cap nothing renews", users[0], capMS + 1, "[OK 0]", capMS},
		{"a user who never claimed is not created", uuid.New(), t0, "[OK 0]", 0},
	}
	for _, tc := range tests {
		if got := reply(f.touch(tc.uid, tc.now, idle, capMS)); got != tc.want {
			t.Errorf("%s: touch = %s, want %s", tc.name, got, tc.want)
		}
		l, ok := f.lease(tc.uid)
		if (tc.wantExp == 0 && ok) || (tc.wantExp != 0 && l != tc.wantExp) {
			t.Errorf("%s: lease = %d (present %v), want %d", tc.name, l, ok, tc.wantExp)
		}
	}

	// An admission that ended (left the queue) is never brought back.
	f.run(redisx.QueueLeave, keys.QueueLeaveKeys(f.eid, users[1]), users[1].String(), 2)
	if got := reply(f.touch(users[1], t0+1_000, idle, capMS)); got != "[OK 0]" {
		t.Errorf("touch after leave = %s", got)
	}
	if _, ok := f.lease(users[1]); ok {
		t.Error("touch re-created an admission that had left")
	}

	// users[2] never renews: the tick at claim + idle drops it; users[0]
	// (renewed to the cap) stays until the cap, then goes, and q:cap is pruned.
	f.tick(t0+idle+1, 100, 1, 10)
	if _, ok := f.lease(users[2]); ok {
		t.Error("idle admission survived its lease")
	}
	if _, ok := f.lease(users[0]); !ok {
		t.Error("renewed admission dropped before its cap")
	}
	f.tick(capMS+1, 100, 1, 10)
	if _, ok := f.lease(users[0]); ok {
		t.Error("admission survived its hard cap")
	}
	if n := f.rdb.ZCard(f.ctx, keys.QCap(f.eid)).Val(); n != 0 {
		t.Errorf("q:cap has %d entries after every cap passed", n)
	}
}

// initQueue marks the queue counters restored (q:init), as the restorer
// does for a fresh event; join and tick answer NOT_INIT until then.
func (f *fixture) initQueue() {
	f.t.Helper()
	if r := reply(f.run(redisx.QueueRestore, keys.QueueRestoreKeys(f.eid), nowMS, 60_000, -1, 0, 0, 0, "0", 0, 0)); r != "[OK]" {
		f.t.Fatalf("restore = %s", r)
	}
}

func reply(r redisx.Reply) string {
	return fmt.Sprint(append([]string{r.Status}, r.Fields...))
}

func TestQueueJoinAndResolve(t *testing.T) {
	f := newFixture(t)
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	if r := f.join(a, nowMS, "0.9", false); r.Status != "NOT_INIT" {
		t.Fatalf("join before restore: %s", reply(r))
	}
	f.initQueue()
	// Pre-queue: b has the lower score, so b gets ticket 1.
	if r := f.join(a, nowMS, "0.9", false); r.Status != "PRE" {
		t.Fatalf("pre join a: %s", reply(r))
	}
	if r := f.join(b, nowMS, "0.1", false); r.Status != "PRE" {
		t.Fatalf("pre join b: %s", reply(r))
	}
	if r := f.join(a, nowMS, "0.0", false); r.Status != "PRE" {
		t.Fatalf("idempotent pre join: %s", reply(r))
	}
	if got := f.rdb.ZScore(f.ctx, keys.QPre(f.eid), a.String()).Val(); got != 0.9 {
		t.Errorf("re-join changed the lottery score to %v", got)
	}
	resolve := func(u uuid.UUID) string {
		return reply(f.run(redisx.QueueResolve, keys.QueueResolveKeys(f.eid, u), u.String()))
	}
	if got := resolve(a); got != "[PENDING]" {
		t.Errorf("resolve before freeze = %s", got)
	}
	// First call at/after T0 freezes presize = 2; c gets the first tail ticket.
	if got := reply(f.join(c, opensMS, "0.5", false)); got != "[TICKET 3]" {
		t.Errorf("post-T0 join = %s", got)
	}
	if got := f.get(keys.QPreSize(f.eid)); got != "2" {
		t.Errorf("presize = %s", got)
	}
	if got := resolve(b); got != "[TICKET 1]" {
		t.Errorf("resolve b = %s", got)
	}
	if got := resolve(b); got != "[TICKET 1]" {
		t.Errorf("resolve b again = %s", got)
	}
	// A pre user's join after the freeze returns their lottery ticket.
	if got := reply(f.join(a, opensMS, "0.5", false)); got != "[TICKET 2]" {
		t.Errorf("join a after freeze = %s", got)
	}
	if got := resolve(uuid.New()); got != "[NOT_FOUND]" {
		t.Errorf("resolve stranger = %s", got)
	}
	// Sold out: existing entries are still returned; new joins are refused.
	f.rdb.Set(f.ctx, keys.QSoldOut(f.eid), "1", 0)
	if got := reply(f.join(c, opensMS, "0.5", false)); got != "[TICKET 3]" {
		t.Errorf("existing entry while sold out = %s", got)
	}
	if got := reply(f.join(uuid.New(), opensMS, "0.5", false)); got != "[SOLD_OUT]" {
		t.Errorf("new join while sold out = %s", got)
	}
}

func TestQueueTickClaimRejoin(t *testing.T) {
	f := newFixture(t)
	if got := reply(f.tick(nowMS, 10, 1, 5)); got != "[NOT_LOADED]" {
		t.Errorf("tick without inventory = %s (must never read as sold out)", got)
	}
	if f.rdb.Exists(f.ctx, keys.QSoldOut(f.eid)).Val() != 0 {
		t.Fatal("tick without inventory set soldout")
	}
	f.load(2, nil, 0) // 2 seats -> target max(1, ceil(2*1.5)) = 3
	if got := reply(f.tick(nowMS, 10, 1, 5)); got != "[NOT_INIT]" {
		t.Errorf("tick before restore = %s", got)
	}
	f.initQueue()
	if got := reply(f.tick(nowMS, 10, 1, 5)); got != "[PREQUEUE]" {
		t.Errorf("tick before T0 = %s", got)
	}
	users := make([]uuid.UUID, 5)
	for i := range users {
		users[i] = uuid.New()
		if got := reply(f.join(users[i], opensMS, "0", false)); got != fmt.Sprintf("[TICKET %d]", i+1) {
			t.Fatalf("join %d = %s", i, got)
		}
	}
	// target 3, nobody seeking or outstanding -> offers 1..3
	if got := reply(f.tick(opensMS, 10, 1, 5)); got != "[ADMITTING 3 3 0 0 0]" {
		t.Errorf("tick 1 = %s", got)
	}
	offers := f.rdb.ZRange(f.ctx, keys.QOffers(f.eid), 0, -1).Val()
	if !slices.Equal(offers, []string{"1", "2", "3"}) {
		t.Errorf("offers = %v", offers)
	}
	if got := reply(f.tick(opensMS+1, 10, 1, 5)); got != "[ADMITTING 0 3 0 3 0]" {
		t.Errorf("tick 2 (full) = %s", got)
	}
	exp := strconv.FormatInt(opensMS+10+600_000, 10)
	if got := reply(f.claim(1, users[0], opensMS+10)); got != "[ADMITTED "+exp+" 1]" {
		t.Errorf("claim 1 = %s", got)
	}
	if got := reply(f.claim(1, users[0], opensMS+20)); got != "[ADMITTED "+exp+" 0]" {
		t.Errorf("claim 1 replay = %s", got)
	}
	if got := reply(f.claim(4, users[3], opensMS+20)); got != "[WAITING]" {
		t.Errorf("claim of an unoffered ticket = %s", got)
	}
	// rejoin while the offer is still live is a no-op
	if got := reply(f.join(users[1], opensMS+20, "0", true)); got != "[TICKET 2]" {
		t.Errorf("rejoin with a live offer = %s", got)
	}
	// Offers 2 and 3 expire at opensMS+60_000. The tick drops them (2 expired)
	// and re-offers their permits to tickets 4 and 5: 1 seeking, so k = 2.
	if got := reply(f.tick(opensMS+60_000, 10, 1, 5)); got != "[ADMITTING 2 3 1 0 2]" {
		t.Errorf("tick after expiry = %s", got)
	}
	if got := reply(f.claim(2, users[1], opensMS+60_001)); got != "[OFFER_EXPIRED]" {
		t.Errorf("claim of an expired offer = %s", got)
	}
	if got := reply(f.join(users[1], opensMS+60_001, "0", true)); got != "[TICKET 6]" {
		t.Errorf("rejoin 1 = %s", got)
	}
	// ticket 6 > cursor 5: not eligible to rejoin again yet
	if got := reply(f.join(users[1], opensMS+60_001, "0", true)); got != "[TICKET 6]" {
		t.Errorf("rejoin while waiting = %s", got)
	}
	if got := f.get(keys.QUser(f.eid, users[1])); got != "6:1" {
		t.Errorf("user entry = %s", got)
	}
	// Leave frees the admission and the offer.
	f.run(redisx.QueueLeave, keys.QueueLeaveKeys(f.eid, users[0]), users[0].String(), 1)
	if f.rdb.ZCard(f.ctx, keys.QActive(f.eid)).Val() != 0 {
		t.Error("leave kept the admission")
	}
	// Sell out: tick sets the flag; claims are refused.
	f.rdb.Del(f.ctx, keys.Avail(f.eid))
	if got := reply(f.tick(opensMS+60_002, 10, 1, 5)); got != "[SOLD_OUT]" {
		t.Errorf("tick with nothing left = %s", got)
	}
	if got := reply(f.claim(4, users[3], opensMS+60_003)); got != "[SOLD_OUT]" {
		t.Errorf("claim after sell-out = %s", got)
	}
	if got := f.get(keys.QRate(f.eid)); got == "0" || got == "" {
		t.Errorf("admit rate never updated: %q", got)
	}
}

func TestQueueRejoinLimit(t *testing.T) {
	f := newFixture(t)
	f.initQueue()
	f.load(1, nil, 0) // target = max(1, ceil(1.5)) = 2
	u := uuid.New()
	now := opensMS
	r := f.join(u, now, "0", false)
	for i := range 3 {
		ticket, _ := r.Int(0)
		f.tick(now, 1, 1, 5) // max_active 1: one offer at a time
		now += 60_000        // offer expires
		f.tick(now, 1, 1, 5)
		if got := reply(f.claim(ticket, u, now)); got != "[OFFER_EXPIRED]" {
			t.Fatalf("round %d claim = %s", i, got)
		}
		r = f.join(u, now, "0", true)
		if i < 2 && r.Status != "TICKET" {
			t.Fatalf("rejoin %d = %s", i+1, reply(r))
		}
	}
	if r.Status != "REJOIN_LIMIT" {
		t.Errorf("third rejoin = %s, want REJOIN_LIMIT", reply(r))
	}
}

// TestQueueRestoreDumpReopen covers the R1 scripts (ADR 0009): restore
// raises counters, re-creates the snapshot's offers with fresh deadlines and
// its admissions with their old expiries, runs only once, and dump reads it
// all back; reopen refuses a queue with entries and sets the q:opens override.
func TestQueueRestoreDumpReopen(t *testing.T) {
	f := newFixture(t)
	u1, u2 := uuid.New(), uuid.New()
	dump := func() string { return reply(f.run(redisx.QueueDump, keys.QueueDumpKeys(f.eid))) }
	if got := dump(); got != "[0 -1 0 0 0 0 0 0 0]" {
		t.Errorf("empty dump = %s", got)
	}
	// presize 40, tail 60, cursor 30, not sold out, rate 2.5, offers {29, 30},
	// active {u1 live, u2 already expired}.
	r := reply(f.run(redisx.QueueRestore, keys.QueueRestoreKeys(f.eid), nowMS, 60_000, 40, 60, 30, 0, "2.5",
		2, 29, 30, 2, u1.String(), nowMS+5_000, u2.String(), nowMS-1))
	if r != "[OK]" {
		t.Fatalf("restore = %s", r)
	}
	deadline := strconv.FormatInt(nowMS+60_000, 10)
	want := fmt.Sprintf("[1 40 60 30 0 2.5 0 2 29 30 1 %s %d]", u1, nowMS+5_000)
	if got := dump(); got != want {
		t.Errorf("dump after restore = %s, want %s", got, want)
	}
	if got := f.rdb.ZScore(f.ctx, keys.QOffers(f.eid), "29").Val(); strconv.FormatFloat(got, 'f', 0, 64) != deadline {
		t.Errorf("offer 29 deadline = %v, want %s", got, deadline)
	}
	// A second restore (another replica, or a stale snapshot) is a no-op.
	r = reply(f.run(redisx.QueueRestore, keys.QueueRestoreKeys(f.eid), nowMS, 60_000, 1, 1, 1, 1, "0", 0, 0))
	if r != "[ALREADY]" || dump() != want {
		t.Errorf("second restore = %s, dump %s", r, dump())
	}
	// The restored admission is idempotent: no second admission for u1.
	if got := reply(f.claim(29, u1, nowMS+10)); got != fmt.Sprintf("[ADMITTED %d 0]", nowMS+5_000) {
		t.Errorf("claim by restored admission = %s", got)
	}
	// Reopen refuses: tickets exist.
	if got := reply(f.run(redisx.QueueReopen, keys.QueueReopenKeys(f.eid), nowMS+1)); got != "[BUSY 100]" {
		t.Errorf("reopen with tickets = %s", got)
	}

	// A queue nobody joined: frozen at 0 by a tick, reopened to a later T0.
	g := newFixture(t)
	g.initQueue()
	g.load(2, nil, 0)
	if got := reply(g.tick(opensMS, 10, 1, 5)); got != "[ADMITTING 0 3 0 0 0]" {
		t.Fatalf("empty tick after T0 = %s", got)
	}
	later := opensMS + 600_000
	if got := reply(g.run(redisx.QueueReopen, keys.QueueReopenKeys(g.eid), later)); got != "[OK]" {
		t.Fatalf("reopen = %s", got)
	}
	// Callers still pass the old T0; q:opens wins: pre-queue again.
	if got := reply(g.tick(opensMS+1, 10, 1, 5)); got != "[PREQUEUE]" {
		t.Errorf("tick after reopen = %s", got)
	}
	if got := reply(g.join(u1, opensMS+2, "0.5", false)); got != "[PRE]" {
		t.Errorf("join after reopen = %s", got)
	}
	if got := reply(g.run(redisx.QueueReopen, keys.QueueReopenKeys(g.eid), later)); got != "[BUSY 1]" {
		t.Errorf("reopen with a lottery entry = %s", got)
	}
}
