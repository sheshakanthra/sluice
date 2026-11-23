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
	return f.run(redisx.QueueClaim, keys.QueueClaimKeys(f.eid), ticket, uid.String(), now, 600_000)
}

func reply(r redisx.Reply) string {
	return fmt.Sprint(append([]string{r.Status}, r.Fields...))
}

func TestQueueJoinAndResolve(t *testing.T) {
	f := newFixture(t)
	a, b, c := uuid.New(), uuid.New(), uuid.New()
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
	if got := reply(f.claim(1, users[0], opensMS+10)); got != "[ADMITTED "+exp+"]" {
		t.Errorf("claim 1 = %s", got)
	}
	if got := reply(f.claim(1, users[0], opensMS+20)); got != "[ADMITTED "+exp+"]" {
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
