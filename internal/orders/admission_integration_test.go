package orders_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/orders"
	"github.com/sheshakanthra/sluice/internal/payments"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// admit puts u in the event's q:active, as queue_claim does.
func (h *harness) admit(u user) {
	h.t.Helper()
	exp := float64(h.clk.now().Add(10 * time.Minute).UnixMilli())
	mustNil(h.t, h.rdb.ZAdd(h.ctx, keys.QActive(h.eventID), redis.Z{Score: exp, Member: u.id.String()}).Err())
}

// admitted reports whether u is still in q:active.
func (h *harness) admitted(u user) bool {
	h.t.Helper()
	err := h.rdb.ZScore(h.ctx, keys.QActive(h.eventID), u.id.String()).Err()
	if err != nil && !errors.Is(err, redis.Nil) {
		h.t.Fatal(err)
	}
	return err == nil
}

// TestConfirmedBuyerFreesAdmission (ADR 0010 addendum): the request that
// confirms the order also releases the buyer's admission, so the controller
// can offer the slot on its next tick. Nobody waits for ADMISSION_TTL.
func TestConfirmedBuyerFreesAdmission(t *testing.T) {
	h := newHarness(t)
	u, bystander := h.newUser(), h.newUser()
	h.admit(u)
	h.admit(bystander)
	v := h.createOrder(u, 3, 4)
	if !h.admitted(u) {
		t.Fatal("admission released while the order is still PENDING_PAYMENT")
	}
	r := h.pay(u, v.OrderID, uuid.NewString(), payments.MethodOK)
	if r.code != http.StatusOK || r.view(t).Status != "CONFIRMED" {
		t.Fatalf("pay: HTTP %d %s", r.code, r.body)
	}
	// Checked straight after the /pay response: no sleep, no clock change.
	if h.admitted(u) {
		t.Fatal("confirmed buyer still holds an admission slot")
	}
	if !h.admitted(bystander) {
		t.Fatal("another user's admission was released")
	}
	if n := h.counter("queue_admissions_released_total"); n != 1 {
		t.Fatalf("queue_admissions_released_total = %v, want 1", n)
	}
	// Replays and the sweeper's Redis pass are no-ops.
	r = h.pay(u, v.OrderID, uuid.NewString(), payments.MethodOK)
	mustNil(t, h.svc.ConfirmOrder(h.ctx, v.OrderID))
	if r.code != http.StatusOK || h.counter("queue_admissions_released_total") != 1 {
		t.Fatalf("replay: HTTP %d, released %v", r.code, h.counter("queue_admissions_released_total"))
	}
}

// TestFailedPaymentFreesAdmission: a declined attempt keeps the admission
// (the order can still be paid), and the order reaching FAILED releases it
// in the same request.
func TestFailedPaymentFreesAdmission(t *testing.T) {
	h := newHarness(t)
	u := h.newUser()
	h.admit(u)
	v := h.createOrder(u, 5)
	for attempt := 1; attempt <= orders.MaxAttempts; attempt++ {
		if attempt > 1 {
			_, err := h.svc.NewAttempt(h.ctx, v.OrderID, u.id)
			mustNil(t, err)
		}
		r := h.pay(u, v.OrderID, uuid.NewString(), payments.MethodDecline)
		if r.code != http.StatusOK {
			t.Fatalf("attempt %d: HTTP %d %s", attempt, r.code, r.body)
		}
		if attempt < orders.MaxAttempts {
			if st := r.view(t).Status; st != "PENDING_PAYMENT" || !h.admitted(u) {
				t.Fatalf("after decline %d: order %s, admitted %v (want PENDING_PAYMENT, still admitted)", attempt, st, h.admitted(u))
			}
		}
	}
	o := h.order(v.OrderID)
	if o.Status != gen.OrderStatusFAILED || o.FailureCode == nil || *o.FailureCode != orders.FailurePayment {
		t.Fatalf("order %s %v, want FAILED %s", o.Status, o.FailureCode, orders.FailurePayment)
	}
	if h.admitted(u) {
		t.Fatal("admission kept after the order FAILED")
	}
}

// TestExpiredHoldFreesAdmission: a buyer who never pays loses the hold to
// the sweeper; the sweep releases the admission too, and the order then
// settles (EXPIRED, after the unused intent is voided).
func TestExpiredHoldFreesAdmission(t *testing.T) {
	h := newHarness(t)
	u := h.newUser()
	h.admit(u)
	v := h.createOrder(u, 7)
	h.clk.advance(3 * time.Minute) // past PAYMENT_HOLD_TTL (2 min)
	res, err := h.inv.SweepOnce(h.ctx)
	mustNil(t, err)
	if res.Reclaimed != 1 {
		t.Fatalf("reclaimed %d", res.Reclaimed)
	}
	if h.admitted(u) {
		t.Fatal("admission kept after the hold expired")
	}
	h.drain()
	if o := h.order(v.OrderID); o.Status != gen.OrderStatusEXPIRED {
		t.Fatalf("order %s, want EXPIRED", o.Status)
	}
	if n := h.counter("queue_admissions_released_total"); n != 1 {
		t.Fatalf("queue_admissions_released_total = %v, want 1 (settling again is a no-op)", n)
	}
}

// TestReleasedHoldFreesAdmission: DELETE /holds/{id} with no order releases
// the admission; a user who holds again keeps it until that hold ends.
func TestReleasedHoldFreesAdmission(t *testing.T) {
	h := newHarness(t)
	u := h.newUser()
	h.admit(u)
	hd := h.hold(u, 9)
	_, err := h.inv.Release(h.ctx, hd.ID, u.id)
	mustNil(t, err)
	if h.admitted(u) {
		t.Fatal("admission kept after the user released their only hold")
	}

	w := h.newUser()
	h.admit(w)
	old := h.hold(w, 10)
	_, err = h.inv.Release(h.ctx, old.ID, w.id)
	mustNil(t, err)
	h.admit(w) // admitted again, holds again
	cur := h.hold(w, 11)
	h.inv.MirrorRelease(h.ctx, h.eventID, old.ID, w.id) // a late repeat of the old hold's end
	if !h.admitted(w) {
		t.Fatal("a late repeat for an old hold released the admission of a user who holds again")
	}
	_, err = h.inv.Release(h.ctx, cur.ID, w.id)
	mustNil(t, err)
	if h.admitted(w) {
		t.Fatal("admission kept after the current hold was released")
	}
}
