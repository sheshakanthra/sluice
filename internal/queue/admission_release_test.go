package queue_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/queue"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// TestAdmissionRefillsAfterCheckout (ADR 0010 addendum): admitted users
// check out concurrently while the controller ticks. A buyer whose hold is
// consumed (order CONFIRMED) and a user whose hold is released give their
// admission back in the same Redis script, so the controller refills the
// target at once. The fake clock never moves, so nothing here relies on
// ADMISSION_TTL (10 min in this env) or on offer expiry.
//
// 200 seats, OVERADMIT 1.5: the first tick offers 300. 100 buy one seat,
// 100 hold one and release it, 100 keep shopping. Afterwards 100 seats are
// left, the target is 150, only the 100 shoppers are still admitted, and
// the controller must have offered at least 50 more tickets. Before the
// change the 300 stale admissions kept seeking at 300 >= 150 and the cursor
// stayed at 300 until ADMISSION_TTL.
func TestAdmissionRefillsAfterCheckout(t *testing.T) {
	pgs.Require(t)
	const (
		seats  = 200
		first  = 300 // 200 seats times OVERADMIT 1.5
		buyers = 100
		quits  = 100 // hold, then release
		stay   = first - buyers - quits
	)
	e := newEnv(t, capacity(seats), tickCfg(queue.TickConfig{MaxActive: 1000, Overadmit: 1.5, MinAdmit: 10,
		MaxPerTick: 1000, OfferTTL: time.Minute}))
	e.toT0(0)
	t0 := e.clk.Now()
	users := e.joinAll(2*first, 16)
	byTicket := make(map[int64]*user, len(users))
	for _, u := range users {
		byTicket[u.ticket] = u
	}
	if res := e.tick(); res.Admitted != first {
		t.Fatalf("first tick = %+v, want %d offers", res, first)
	}
	e.refresh()

	// All 300 claim concurrently.
	admitted := make([]*user, first)
	var wg sync.WaitGroup
	errs := make(chan error, first)
	for i := range first {
		u := byTicket[int64(i+1)]
		admitted[i] = u
		c := e.claims(u)
		wg.Go(func() {
			r, err := e.svc.Status(e.ctx, e.ev, c)
			if err == nil && r.State != queue.StateAdmitted {
				err = fmt.Errorf("ticket %d: %s", u.ticket, r.State)
			}
			if err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if res := e.tick(); res.Admitted != 0 || res.Seeking != first {
		t.Fatalf("tick with every admission seeking = %+v, want 0 offers", res)
	}
	freedBefore := e.metric("queue_admissions_released_total")

	// Check out concurrently while two controllers tick.
	ctx, stop := context.WithCancel(e.ctx)
	var ctl sync.WaitGroup
	for range 2 {
		c := e.newController()
		ctl.Go(func() { _ = c.Run(ctx) })
	}
	errs = make(chan error, buyers+quits)
	for i := range buyers + quits {
		u := admitted[i]
		wg.Go(func() {
			h, err := e.inv.Hold(e.ctx, e.ev.ID, u.id, uuid.NewString(), inventory.Request{Quantity: 1})
			if err != nil {
				errs <- fmt.Errorf("hold for ticket %d: %w", u.ticket, err)
				return
			}
			if i < buyers {
				err = e.inv.Consume(e.ctx, e.ev.ID, h.ID, u.id) // the confirm mirror: order CONFIRMED
			} else {
				_, err = e.inv.Release(e.ctx, h.ID, u.id)
			}
			if err != nil {
				errs <- fmt.Errorf("end hold for ticket %d: %w", u.ticket, err)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	time.Sleep(200 * time.Millisecond) // ~20 more ticks per controller
	stop()
	ctl.Wait()
	res := e.tick()

	if !e.clk.Now().Equal(t0) {
		t.Fatalf("clock moved: %s -> %s", t0, e.clk.Now())
	}
	if got := e.metric("queue_admissions_released_total") - freedBefore; got != buyers+quits {
		t.Errorf("admissions released = %.0f, want %d", got, buyers+quits)
	}
	active, err := e.rdb.ZRange(e.ctx, keys.QActive(e.ev.ID), 0, -1).Result()
	mustNil(t, err)
	want := map[string]bool{}
	for _, u := range admitted[buyers+quits:] {
		want[u.id.String()] = true
	}
	if len(active) != stay {
		t.Errorf("%d admissions left, want the %d shoppers", len(active), stay)
	}
	kept := 0
	for _, id := range active {
		if !want[id] {
			kept++
		}
	}
	if kept > 0 {
		t.Errorf("%d admissions kept after their hold ended", kept)
	}
	avail := e.rdb.SCard(e.ctx, keys.Avail(e.ev.ID)).Val()
	cursor := e.rdb.Get(e.ctx, keys.QCursor(e.ev.ID)).Val()
	target := int64(150) // 100 seats left times OVERADMIT 1.5
	so := e.seekingOutstanding()
	t.Logf("after checkout: avail %d, cursor %s, seeking+outstanding %d, final tick %+v", avail, cursor, so, res)
	if avail != seats-buyers {
		t.Fatalf("avail = %d, want %d", avail, seats-buyers)
	}
	if res.Target != target || so < target {
		t.Errorf("target %d, seeking+outstanding %d: the controller did not refill to %d", res.Target, so, target)
	}
	if cursor == fmt.Sprint(first) {
		t.Errorf("cursor still %s: no new offers after %d admissions ended", cursor, buyers+quits)
	}
}
