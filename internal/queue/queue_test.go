package queue_test

import (
	"context"
	"encoding/json"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/queue"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// TestLottery: 10,000 pre-queue joins; after T0 every ticket equals the
// user's rank in ascending random-score order, and tickets are not
// correlated with join order (Spearman |rho| < 0.05).
func TestLottery(t *testing.T) {
	pgs.Require(t)
	const n = 10_000
	e := newEnv(t)
	users := e.joinAll(n, 32) // completion (join) order
	for _, u := range users {
		if u.ticket != 0 {
			t.Fatalf("pre-T0 join got ticket %d", u.ticket)
		}
	}
	e.toT0(time.Millisecond)
	if res := e.tick(); res.State != queue.TickAdmitting {
		t.Fatalf("first tick after T0 = %+v", res)
	}
	if got := e.get(keys.QPreSize(e.ev.ID)); got != strconv.Itoa(n) {
		t.Fatalf("presize = %s, want %d", got, n)
	}
	e.refresh()

	// Resolve everyone through the status path (one queue_resolve each).
	var wg sync.WaitGroup
	var next atomic.Int64
	for range 32 {
		wg.Go(func() {
			for i := next.Add(1) - 1; i < n; i = next.Add(1) - 1 {
				c, err := e.keys.Verify(users[i].token, auth.AudQueue)
				if err != nil {
					t.Error(err)
					return
				}
				r, err := e.svc.Status(e.ctx, e.ev, c)
				if err != nil || r.TicketNo == nil || r.QueueToken == nil {
					t.Errorf("resolve %d: %+v %v", i, r, err)
					return
				}
				users[i].ticket = *r.TicketNo
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}

	byScore := e.rdb.ZRange(e.ctx, keys.QPre(e.ev.ID), 0, -1).Val()
	ticketOf := make(map[string]int64, n)
	for _, u := range users {
		ticketOf[u.id.String()] = u.ticket
	}
	for rank, uid := range byScore {
		if ticketOf[uid] != int64(rank+1) {
			t.Fatalf("user at score rank %d has ticket %d", rank, ticketOf[uid])
		}
	}
	// Spearman between join order and ticket (both permutations of 1..n).
	var d2 float64
	for i, u := range users {
		d := float64(i+1) - float64(u.ticket)
		d2 += d * d
	}
	rho := 1 - 6*d2/(float64(n)*(float64(n)*float64(n)-1))
	t.Logf("lottery: n=%d spearman(join order, ticket) rho=%.4f", n, rho)
	if math.Abs(rho) >= 0.05 {
		t.Errorf("|rho| = %.4f, want < 0.05", math.Abs(rho))
	}
}

// TestFIFOAfterT0: joins after T0 get consecutive tickets after presize, in
// arrival order.
func TestFIFOAfterT0(t *testing.T) {
	pgs.Require(t)
	e := newEnv(t)
	e.joinAll(100, 8)
	e.toT0(0)
	for i := range 200 {
		u := &user{id: uuid.New()}
		r := e.mustJoin(u)
		if r.State != queue.StateWaiting || u.ticket != int64(101+i) {
			t.Fatalf("post-T0 join %d: state %s ticket %d, want WAITING %d", i, r.State, u.ticket, 101+i)
		}
		if i == 50 {
			e.tick() // ticks interleaved with joins change nothing
		}
	}
	if got := e.get(keys.QPreSize(e.ev.ID)); got != "100" {
		t.Errorf("presize = %s", got)
	}
}

// TestFreezeRace: joins racing with T0 (the clock flips mid-stream) and with
// three concurrent tick loops: every user ends with exactly one ticket and
// the tickets are exactly 1..n.
func TestFreezeRace(t *testing.T) {
	pgs.Require(t)
	const n = 3000
	e := newEnv(t)
	e.clk.Set(e.ev.SaleOpensAt.Add(-2 * time.Millisecond))

	ctx, stop := context.WithCancel(e.ctx)
	var ticks sync.WaitGroup
	for range 3 {
		ticks.Go(func() {
			for ctx.Err() == nil {
				if _, _, err := e.ctrl.TickOnce(ctx, e.row()); err != nil && ctx.Err() == nil {
					t.Error(err)
					return
				}
			}
		})
	}
	users := make([]*user, n)
	var started, done atomic.Int64
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for i := started.Add(1) - 1; i < n; i = started.Add(1) - 1 {
				u := &user{id: uuid.New()}
				if _, err := e.join(u, false); err != nil {
					t.Error(err)
					return
				}
				users[i] = u
				if done.Add(1) == n/2 {
					e.toT0(time.Millisecond) // T0 arrives mid-stream
				}
			}
		})
	}
	wg.Wait()
	stop()
	ticks.Wait()
	if t.Failed() {
		return
	}
	e.tick()
	e.refresh()
	pre := 0
	seen := make(map[int64]uuid.UUID, n)
	for _, u := range users {
		if u.ticket == 0 {
			pre++
			if r := e.status(u); r.TicketNo == nil {
				t.Fatalf("pre user not resolved: %+v", r)
			}
		}
		if other, dup := seen[u.ticket]; dup {
			t.Fatalf("ticket %d issued to %s and %s", u.ticket, other, u.id)
		}
		seen[u.ticket] = u.id
		// joining again returns the same ticket
		again := &user{id: u.id}
		e.mustJoin(again)
		if again.ticket != u.ticket {
			t.Fatalf("re-join of %s: ticket %d, then %d", u.id, u.ticket, again.ticket)
		}
	}
	for tk := int64(1); tk <= n; tk++ {
		if _, ok := seen[tk]; !ok {
			t.Fatalf("gap: ticket %d never issued", tk)
		}
	}
	t.Logf("freeze race: %d pre-queue + %d post-T0 joins, tickets 1..%d with no duplicates or gaps", pre, n-pre, n)
	if pre == 0 || pre == n {
		t.Errorf("the clock flip did not split the joins (pre=%d)", pre)
	}
}

// TestAdmissionOrder: offers go out strictly in ascending ticket order, and
// only offered tickets can be admitted.
func TestAdmissionOrder(t *testing.T) {
	pgs.Require(t)
	const n = 300
	e := newEnv(t, capacity(100), tickCfg(queue.TickConfig{MaxActive: 1000, Overadmit: 1.5, MinAdmit: 0, MaxPerTick: 7, OfferTTL: time.Minute}))
	e.toT0(0)
	users := e.joinAll(n, 8)
	byTicket := make(map[int64]*user, n)
	for _, u := range users {
		byTicket[u.ticket] = u
	}
	var offered []int64
	known := map[string]bool{}
	for cursor := int64(0); cursor < n; {
		if len(offered) > n {
			t.Fatal("offered more tickets than exist")
		}
		res := e.tick()
		if res.State != queue.TickAdmitting || res.Admitted == 0 {
			t.Fatalf("tick at cursor %d = %+v", cursor, res)
		}
		var fresh []int64
		for _, m := range e.rdb.ZRange(e.ctx, keys.QOffers(e.ev.ID), 0, -1).Val() {
			if !known[m] {
				known[m] = true
				v, _ := strconv.ParseInt(m, 10, 64)
				fresh = append(fresh, v)
			}
		}
		slices.Sort(fresh)
		for i, v := range fresh {
			if v != cursor+int64(i)+1 {
				t.Fatalf("tick after cursor %d offered %v", cursor, fresh)
			}
		}
		cursor += res.Admitted
		offered = append(offered, fresh...)
		e.refresh()
		if nxt, ok := byTicket[cursor+1]; ok {
			if r := e.status(nxt); r.State != queue.StateWaiting || *r.Position != 1 {
				t.Fatalf("next in line: %+v", r)
			}
		}
		for _, v := range fresh { // claim, then leave to free the slot
			u := byTicket[v]
			r := e.status(u)
			if r.State != queue.StateAdmitted {
				t.Fatalf("ticket %d: %s", v, r.State)
			}
			c, err := e.keys.Verify(*r.AdmissionToken, auth.AudAdmission)
			mustNil(t, err)
			mustNil(t, e.svc.Leave(e.ctx, e.ev, c))
		}
	}
	for i, v := range offered {
		if v != int64(i+1) {
			t.Fatalf("offer #%d went to ticket %d", i+1, v)
		}
	}
}

// TestBoundedAdmission: 500 seats, OVERADMIT 1.5. Users claim, hold,
// release, leave or let offers lapse at random; after every tick that
// admits, seeking + outstanding <= that tick's target, and a tick that
// admits nobody had no room (or nobody left to admit).
func TestBoundedAdmission(t *testing.T) {
	pgs.Require(t)
	const n, rounds = 2000, 150
	e := newEnv(t, capacity(500), tickCfg(queue.TickConfig{MaxActive: 1000, Overadmit: 1.5, MinAdmit: 10, MaxPerTick: 100, OfferTTL: 2 * time.Second}))
	e.toT0(0)
	users := e.joinAll(n, 16)
	byTicket := make(map[int64]*user, n)
	for _, u := range users {
		byTicket[u.ticket] = u
	}
	type holder struct {
		u   *user
		hid uuid.UUID
	}
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // deterministic test schedule
	var admitted []*user               // admitted, not yet holding
	var holders []holder
	var cursor int64
	maxRatio, admitting, paused, holds, releases := 0.0, 0, 0, 0, 0
	for round := range rounds {
		res := e.tick()
		switch res.State {
		case queue.TickPaused:
			paused++
		case queue.TickAdmitting:
			admitting++
			after := e.seekingOutstanding()
			if res.Admitted > 0 {
				if res.Seeking+res.Outstanding+res.Admitted > res.Target || after > res.Target {
					t.Fatalf("round %d over-admitted: %+v, after=%d", round, res, after)
				}
				maxRatio = max(maxRatio, float64(after)/float64(res.Target))
			} else if res.Seeking+res.Outstanding < res.Target && cursor < n {
				t.Fatalf("round %d admitted nobody with room: %+v", round, res)
			}
		default:
			t.Fatalf("round %d: %+v", round, res)
		}
		prev := cursor
		cursor += res.Admitted
		e.refresh()
		for tk := prev + 1; tk <= cursor; tk++ { // 70% claim, 30% let the offer lapse
			if rng.Float64() < 0.7 {
				u := byTicket[tk]
				if r := e.status(u); r.State == queue.StateAdmitted {
					admitted = append(admitted, u)
				}
			}
		}
		var still []*user
		for _, u := range admitted {
			switch p := rng.Float64(); {
			case p < 0.3:
				h, err := e.inv.Hold(e.ctx, e.ev.ID, u.id, uuid.NewString(), inventory.Request{Quantity: 1 + rng.IntN(4)})
				if err == nil {
					holds++
					holders = append(holders, holder{u, h.ID})
				} else {
					mustNil(t, e.svc.Leave(e.ctx, e.ev, e.claims(u)))
				}
			case p < 0.4:
				mustNil(t, e.svc.Leave(e.ctx, e.ev, e.claims(u)))
			default:
				still = append(still, u)
			}
		}
		admitted = still
		var kept []holder
		for _, h := range holders { // 15% of holders give their seats back and leave
			if rng.Float64() < 0.15 {
				_, err := e.inv.Release(e.ctx, h.hid, h.u.id)
				mustNil(t, err)
				mustNil(t, e.svc.Leave(e.ctx, e.ev, e.claims(h.u)))
				releases++
				continue
			}
			kept = append(kept, h)
		}
		holders = kept
		e.clk.Advance(100 * time.Millisecond)
	}
	t.Logf("bounded admission: %d rounds (%d admitting, %d paused), cursor %d, %d holds, %d releases, "+
		"max (seeking+outstanding)/target after an admitting tick = %.3f",
		rounds, admitting, paused, cursor, holds, releases, maxRatio)
	if admitting < 20 || holds == 0 || releases == 0 {
		t.Errorf("simulation too small: admitting=%d holds=%d releases=%d", admitting, holds, releases)
	}
}

// claims returns u's queue-token claims.
func (e *env) claims(u *user) auth.Claims {
	e.t.Helper()
	c, err := e.keys.Verify(u.token, auth.AudQueue)
	mustNil(e.t, err)
	return c
}

// TestConcurrentControllers: three controller processes ticking the same
// event never over-admit compared with one (same final cursor, within one
// tick's worth), and never offer a ticket twice.
func TestConcurrentControllers(t *testing.T) {
	pgs.Require(t)
	const target = 750 // 500 seats times OVERADMIT 1.5
	cfg := queue.TickConfig{MaxActive: 1000, Overadmit: 1.5, MinAdmit: 10, MaxPerTick: 50, OfferTTL: 10 * time.Minute}
	final := map[int]int64{}
	for _, nctl := range []int{1, 3} {
		e := newEnv(t, capacity(500), opensIn(-time.Hour), tickCfg(cfg), realClock())
		e.joinAll(3000, 16)
		ctx, stop := context.WithCancel(e.ctx)
		var wg sync.WaitGroup
		for range nctl {
			c := e.newController()
			wg.Go(func() { _ = c.Run(ctx) })
		}
		var worst atomic.Int64
		wg.Go(func() { // sample the bound while the controllers run
			for ctx.Err() == nil {
				worst.Store(max(worst.Load(), e.seekingOutstanding()))
				time.Sleep(2 * time.Millisecond)
			}
		})
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) && e.get(keys.QCursor(e.ev.ID)) != strconv.Itoa(target) {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(300 * time.Millisecond) // ~30 more ticks per controller
		stop()
		wg.Wait()
		cursor, _ := strconv.ParseInt(e.get(keys.QCursor(e.ev.ID)), 10, 64)
		offers := e.rdb.ZCard(e.ctx, keys.QOffers(e.ev.ID)).Val()
		t.Logf("%d controller(s): cursor %d, offers %d, worst sampled seeking+outstanding %d", nctl, cursor, offers, worst.Load())
		if offers != cursor {
			t.Errorf("%d controllers: %d offers for cursor %d (a ticket offered twice or lost)", nctl, offers, cursor)
		}
		if worst.Load() > target {
			t.Errorf("%d controllers: sampled %d > target %d", nctl, worst.Load(), target)
		}
		if st := e.pgStatus(); st != gen.EventStatusONSALE {
			t.Errorf("%d controllers: PG status %s, want ON_SALE", nctl, st)
		}
		final[nctl] = cursor
	}
	if final[1] != target {
		t.Errorf("1 controller: cursor %d, want %d", final[1], target)
	}
	if d := final[3] - final[1]; d < -int64(cfg.MaxPerTick) || d > int64(cfg.MaxPerTick) {
		t.Errorf("3 controllers: cursor %d vs %d with 1", final[3], final[1])
	}
}

// TestSoldOut: once every seat is sold the tick sets the sold-out flag and
// PG status, no claim or hold succeeds, every ticket's status is SOLD_OUT,
// and new joins are told SOLD_OUT.
func TestSoldOut(t *testing.T) {
	pgs.Require(t)
	e := newEnv(t, capacity(8))
	e.toT0(0)
	users := e.joinAll(30, 4)
	byTicket := map[int64]*user{}
	for _, u := range users {
		byTicket[u.ticket] = u
	}
	if res := e.tick(); res.Admitted != 12 { // 8 seats times 1.5, above MIN_ADMIT 10
		t.Fatalf("first tick = %+v", res)
	}
	if st := e.pgStatus(); st != gen.EventStatusONSALE {
		t.Fatalf("PG status after admitting = %s", st)
	}
	e.refresh()
	for tk := int64(1); tk <= 2; tk++ { // two buyers take 4 seats each and pay
		u := byTicket[tk]
		if r := e.status(u); r.State != queue.StateAdmitted {
			t.Fatalf("ticket %d: %s", tk, r.State)
		}
		h, err := e.inv.Hold(e.ctx, e.ev.ID, u.id, uuid.NewString(), inventory.Request{Quantity: 4})
		mustNil(t, err)
		mustNil(t, e.inv.Consume(e.ctx, e.ev.ID, h.ID, u.id))
	}
	if r := e.status(byTicket[3]); r.State != queue.StateAdmitted { // admitted, but too late
		t.Fatalf("ticket 3: %s", r.State)
	}
	if res := e.tick(); res.State != queue.TickSoldOut {
		t.Fatalf("tick after the last seat sold = %+v", res)
	}
	if e.get(keys.QSoldOut(e.ev.ID)) != "1" {
		t.Error("soldout flag not set")
	}
	if st := e.pgStatus(); st != gen.EventStatusSOLDOUT {
		t.Errorf("PG status = %s, want SOLD_OUT", st)
	}
	cursor := e.get(keys.QCursor(e.ev.ID))
	if res := e.tick(); res.State != queue.TickSoldOut || e.get(keys.QCursor(e.ev.ID)) != cursor {
		t.Errorf("tick while sold out = %+v, cursor %s -> %s", res, cursor, e.get(keys.QCursor(e.ev.ID)))
	}
	e.refresh()
	for _, u := range users {
		if r := e.status(u); r.State != queue.StateSoldOut {
			t.Errorf("ticket %d: status %s, want SOLD_OUT", u.ticket, r.State)
		}
		r, err := e.sc.Run(e.ctx, redisx.QueueClaim, keys.QueueClaimKeys(e.ev.ID), u.ticket, u.id.String(),
			e.clk.Now().UnixMilli(), 600_000)
		mustNil(t, err)
		if r.Status != "SOLD_OUT" {
			t.Errorf("claim ticket %d after sell-out: %s", u.ticket, r.Status)
		}
	}
	if r := e.mustJoin(&user{id: uuid.New()}); r.State != queue.StateSoldOut || r.QueueToken != nil {
		t.Errorf("join after sell-out: %+v", r)
	}
	if _, err := e.inv.Hold(e.ctx, e.ev.ID, byTicket[3].id, uuid.NewString(), inventory.Request{Quantity: 1}); err == nil {
		t.Error("hold after sell-out succeeded")
	}
}

// TestOfferExpiryAndRejoin: an unclaimed offer expires and its permit goes
// to the next ticket; rejoin puts the user at the back; the third rejoin is
// refused with 409 REJOIN_LIMIT.
func TestOfferExpiryAndRejoin(t *testing.T) {
	pgs.Require(t)
	const ttl = 30 * time.Second
	e := newEnv(t, capacity(10), tickCfg(queue.TickConfig{MaxActive: 1, Overadmit: 1.5, MinAdmit: 0, MaxPerTick: 10, OfferTTL: ttl}))
	e.toT0(0)
	a, b := &user{id: uuid.New()}, &user{id: uuid.New()}
	e.mustJoin(a)
	e.mustJoin(b)
	if a.ticket != 1 || b.ticket != 2 {
		t.Fatalf("tickets %d %d", a.ticket, b.ticket)
	}
	if res := e.tick(); res.Admitted != 1 {
		t.Fatalf("tick 1 = %+v", res)
	}
	if res := e.tick(); res.Admitted != 0 {
		t.Fatalf("one permit: tick 2 = %+v", res)
	}
	e.clk.Advance(ttl)
	if res := e.tick(); res.ExpiredOffers != 1 || res.Admitted != 1 {
		t.Fatalf("after expiry the permit returns to ticket 2: %+v", res)
	}
	e.refresh()
	expire := func(round int) {
		t.Helper()
		if r := e.status(a); r.State != queue.StateOfferExpired {
			t.Fatalf("round %d: status %s, want OFFER_EXPIRED", round, r.State)
		}
	}
	expire(0)
	session, _, err := e.keys.Issue(auth.AudSession, a.id, uuid.Nil, nil, time.Hour)
	mustNil(t, err)
	if r, _ := e.join(a, true); r.State != queue.StateWaiting || a.ticket != 3 {
		t.Fatalf("rejoin 1: %s ticket %d, want WAITING 3 (the back)", r.State, a.ticket)
	}
	if r := e.status(b); r.State != queue.StateAdmitted { // b claims, then leaves
		t.Fatalf("b: %s", r.State)
	} else {
		c, err := e.keys.Verify(*r.AdmissionToken, auth.AudAdmission)
		mustNil(t, err)
		mustNil(t, e.svc.Leave(e.ctx, e.ev, c))
	}
	for round, want := range []int64{4, 0} {
		if res := e.tick(); res.Admitted != 1 {
			t.Fatalf("round %d: offer to a = %+v", round+1, res)
		}
		e.clk.Advance(ttl)
		e.tick()
		e.refresh()
		expire(round + 1)
		if want == 0 {
			break
		}
		if r, _ := e.join(a, true); a.ticket != want {
			t.Fatalf("rejoin %d: %+v, ticket %d", round+2, r, a.ticket)
		}
	}
	rec := e.do(http.MethodPost, "/api/v1/events/"+e.ev.ID.String()+"/queue?rejoin=true", session)
	var p struct{ Code string }
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusConflict || p.Code != "REJOIN_LIMIT" {
		t.Errorf("third rejoin: %d %s", rec.Code, rec.Body.String())
	}
}

// TestZeroRedisStatus: 10,000 status calls from WAITING users through the
// HTTP handler send zero commands to Redis (counted by a client hook) and
// leave queue_status_redis_calls_total at 0.
func TestZeroRedisStatus(t *testing.T) {
	pgs.Require(t)
	const n, calls, admitted = 10_000, 10_000, 100
	e := newEnv(t, capacity(500), tickCfg(queue.TickConfig{MaxActive: admitted, Overadmit: 1.5, MinAdmit: 10, MaxPerTick: 200, OfferTTL: time.Minute}))
	e.toT0(0)
	users := e.joinAll(n, 32)
	if res := e.tick(); res.Admitted != admitted {
		t.Fatalf("tick = %+v", res)
	}
	e.refresh()
	var waiting []*user
	for _, u := range users {
		if u.ticket > admitted {
			waiting = append(waiting, u)
		}
	}
	path := "/api/v1/events/" + e.ev.ID.String() + "/queue/status"
	e.do(http.MethodGet, path, waiting[0].token) // warm the PG event cache

	hook := &countHook{}
	e.rdb.AddHook(hook)
	before := e.metric("queue_status_redis_calls_total")
	var next atomic.Int64
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for i := next.Add(1) - 1; i < calls; i = next.Add(1) - 1 {
				u := waiting[int(i)%len(waiting)]
				rec := e.do(http.MethodGet, path, u.token)
				var r queue.StatusResponse
				if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &r) != nil {
					t.Errorf("status: %d %s", rec.Code, rec.Body.String())
					return
				}
				if r.State != queue.StateWaiting || r.Position == nil || *r.Position != u.ticket-admitted {
					t.Errorf("ticket %d: %+v", u.ticket, r)
					return
				}
			}
		})
	}
	wg.Wait()
	got := hook.n.Load()
	delta := e.metric("queue_status_redis_calls_total") - before
	t.Logf("zero-redis status: %d WAITING status calls -> %d Redis commands, queue_status_redis_calls_total +%v", calls, got, delta)
	if got != 0 || delta != 0 {
		t.Errorf("WAITING status touched Redis: %d commands, counter +%v", got, delta)
	}
}
