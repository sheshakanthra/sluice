package orders_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/mockpay"
	"github.com/sheshakanthra/sluice/internal/orders"
	"github.com/sheshakanthra/sluice/internal/payments"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

func intentOf(t *testing.T, v orders.View) string {
	t.Helper()
	if v.Payment == nil || v.Payment.IntentID == "" {
		t.Fatalf("order %s has no intent: %+v", v.OrderID, v.Payment)
	}
	return v.Payment.IntentID
}

// Happy path: hold -> order -> pay pm_ok -> CONFIRMED, tickets issued, the
// capture job captures (real River worker), webhooks are delivered by the
// MockPay dispatcher, ledger has exactly one capture.
func TestHappyPath(t *testing.T) {
	h := newHarness(t, withWorker(), withDispatcher())
	u := h.newUser()
	v := h.createOrder(u, 3, 4)
	if v.Status != "PENDING_PAYMENT" || v.AmountPaise != 100_000 || v.Payment.Attempt != 1 || v.Payment.CheckoutURL == "" {
		t.Fatalf("created order: %+v payment %+v", v, v.Payment)
	}
	hd, err := h.q.GetHold(h.ctx, h.order(v.OrderID).HoldID)
	mustNil(t, err)
	if hd.Status != gen.HoldStatusPAYMENTPENDING {
		t.Fatalf("hold is %s, want PAYMENT_PENDING", hd.Status)
	}
	intent := intentOf(t, v)

	r := h.pay(u, v.OrderID, uuid.NewString(), payments.MethodOK)
	if r.code != http.StatusOK {
		t.Fatalf("pay: HTTP %d %s", r.code, r.body)
	}
	pv := r.view(t)
	if pv.Status != "CONFIRMED" || len(pv.Tickets) != 2 || !pv.Charged {
		t.Fatalf("pay result: %+v", pv)
	}
	h.eventually(5*time.Second, "capture", func() bool {
		return h.payment(v.OrderID, 1).Status == gen.PaymentStatusCAPTURED
	})
	h.eventually(5*time.Second, "both webhooks processed", func() bool {
		return h.counter("webhooks_total", "result", orders.WebhookProcessed) >= 2
	})
	if l := h.ledger(intent); l["AUTH"] != 1 || l["CAPTURE"] != 1 || l["VOID"] != 0 || l["REFUND"] != 0 {
		t.Fatalf("ledger %v", l)
	}
	if n := h.intentsFor(v.OrderID); n != 1 {
		t.Fatalf("%d intents for the order, want 1", n)
	}
	g := h.getOrder(u, v.OrderID)
	if g.Status != "CONFIRMED" || !g.Charged || g.Payment.Status != "CAPTURED" || len(g.Tickets) != 2 || g.Tickets[0].Code == "" {
		t.Fatalf("GET order: %+v payment %+v", g, g.Payment)
	}
	for _, sn := range []string{"3", "4"} {
		ok, err := h.rdb.SIsMember(h.ctx, keys.Sold(h.eventID), sn).Result()
		mustNil(t, err)
		if !ok {
			t.Fatalf("seat %s not sold in Redis (hold_consume)", sn)
		}
	}
	q, err := h.q.GetQuota(h.ctx, gen.GetQuotaParams{EventID: h.eventID, UserID: u.id})
	mustNil(t, err)
	if q.SeatsPurchased != 2 {
		t.Fatalf("quota %d, want 2", q.SeatsPurchased)
	}
	// Another user cannot read it.
	if r := h.do(http.MethodGet, "/api/v1/orders/"+v.OrderID.String(), h.newUser().session, "", nil); r.code != http.StatusNotFound {
		t.Fatalf("foreign GET: HTTP %d", r.code)
	}
}

// Double-click /pay with the same Idempotency-Key, concurrently: one
// authorization and one capture; every response is either the confirmed
// order or 409 REQUEST_IN_PROGRESS, and the retry replays the same body.
func TestDoubleClickPaySameKey(t *testing.T) {
	h := newHarness(t)
	u := h.newUser()
	v := h.createOrder(u, 1)
	key := uuid.NewString()
	const clicks = 8
	res := make([]resp, clicks)
	var wg sync.WaitGroup
	for i := range clicks {
		wg.Go(func() { res[i] = h.pay(u, v.OrderID, key, payments.MethodOK) })
	}
	wg.Wait()
	ok, inProgress := 0, 0
	var tickets []orders.TicketView
	for _, r := range res {
		switch {
		case r.code == http.StatusOK:
			ok++
			pv := r.view(t)
			if pv.Status != "CONFIRMED" {
				t.Fatalf("200 with status %s", pv.Status)
			}
			if tickets == nil {
				tickets = pv.Tickets
			} else if tickets[0].Code != pv.Tickets[0].Code {
				t.Fatalf("inconsistent tickets across responses")
			}
		case r.code == http.StatusConflict && r.problemCode() == "REQUEST_IN_PROGRESS":
			inProgress++
		default:
			t.Fatalf("unexpected HTTP %d %s", r.code, r.body)
		}
	}
	if ok < 1 {
		t.Fatalf("no 200 among %d clicks (%d in progress)", clicks, inProgress)
	}
	rep := h.pay(u, v.OrderID, key, payments.MethodOK)
	if rep.code != http.StatusOK || rep.hdr.Get(httpx_Replayed) != "true" || rep.view(t).Tickets[0].Code != tickets[0].Code {
		t.Fatalf("replay: HTTP %d replayed=%q %s", rep.code, rep.hdr.Get(httpx_Replayed), rep.body)
	}
	h.drain()
	intent := intentOf(t, v)
	if l := h.ledger(intent); l["AUTH"] != 1 || l["CAPTURE"] != 1 {
		t.Fatalf("ledger %v", l)
	}
	if n := h.intentsFor(v.OrderID); n != 1 {
		t.Fatalf("%d intents, want 1", n)
	}
	t.Logf("clicks=%d ok=%d in_progress=%d", clicks, ok, inProgress)
}

const httpx_Replayed = "Idempotent-Replayed" //nolint:revive // mirrors httpx.ReplayedHeader

// A rogue retry of /pay with a NEW key never creates a second intent or a
// second charge, and returns the confirmed order.
func TestRetryPayNewKey(t *testing.T) {
	h := newHarness(t)
	u := h.newUser()
	v := h.createOrder(u, 5)
	first := h.pay(u, v.OrderID, uuid.NewString(), payments.MethodOK)
	second := h.pay(u, v.OrderID, uuid.NewString(), payments.MethodOK)
	if first.code != http.StatusOK || second.code != http.StatusOK {
		t.Fatalf("HTTP %d / %d", first.code, second.code)
	}
	a, b := first.view(t), second.view(t)
	if a.Status != "CONFIRMED" || b.Status != "CONFIRMED" || a.Tickets[0].Code != b.Tickets[0].Code {
		t.Fatalf("first %+v second %+v", a, b)
	}
	h.drain()
	if l := h.ledger(intentOf(t, v)); l["AUTH"] != 1 || l["CAPTURE"] != 1 {
		t.Fatalf("ledger %v", l)
	}
	if n := h.intentsFor(v.OrderID); n != 1 {
		t.Fatalf("%d intents, want 1", n)
	}
}

// pm_timeout: MockPay commits the authorization, then hangs past the PSP
// client's timeout. The client retries with the same key and gets the
// stored answer; the user's retry with the same Idempotency-Key replays.
// One intent, one charge.
func TestPayTimeoutAfterCommit(t *testing.T) {
	h := newHarness(t)
	u := h.newUser()
	v := h.createOrder(u, 6, 7)
	key := uuid.NewString()
	start := time.Now()
	r := h.pay(u, v.OrderID, key, payments.MethodTimeout)
	took := time.Since(start)
	if r.code != http.StatusOK || r.view(t).Status != "CONFIRMED" {
		t.Fatalf("pay pm_timeout: HTTP %d %s", r.code, r.body)
	}
	if took < pspTimeout {
		t.Fatalf("pay took %s; the first PSP attempt should have timed out at %s", took, pspTimeout)
	}
	if n := h.counter("psp_requests_total", "op", payments.OpConfirm, "result", "timeout"); n < 1 {
		t.Fatalf("psp confirm timeouts = %v, want >= 1", n)
	}
	rep := h.pay(u, v.OrderID, key, payments.MethodTimeout)
	if rep.code != http.StatusOK || rep.hdr.Get(httpx_Replayed) != "true" {
		t.Fatalf("retry same key: HTTP %d replayed=%q", rep.code, rep.hdr.Get(httpx_Replayed))
	}
	h.drain()
	if l := h.ledger(intentOf(t, v)); l["AUTH"] != 1 || l["CAPTURE"] != 1 {
		t.Fatalf("ledger %v", l)
	}
	if n := h.intentsFor(v.OrderID); n != 1 {
		t.Fatalf("%d intents, want 1", n)
	}
	t.Logf("pay with pm_timeout took %s (PSP timeout %s, hang %s)", took.Round(time.Millisecond), pspTimeout, pspHang)
}

// The same webhook delivered 3 times (concurrently) is processed once and
// the inbox has one row.
func TestWebhookDuplicated3x(t *testing.T) {
	h := newHarness(t)
	u := h.newUser()
	v := h.createOrder(u, 8)
	intent := intentOf(t, v)
	_ = h.checkout(intent, payments.MethodOK) // authorizes at the PSP; webhook queued at MockPay
	ds := h.deliveries(intent)
	if len(ds) != 1 || ds[0].Type != payments.EventAuthorized {
		t.Fatalf("deliveries %+v", ds)
	}
	var wg sync.WaitGroup
	codes := make([]int, 3)
	for i := range 3 {
		wg.Go(func() { codes[i] = h.postWebhook(ds[0].EventID, ds[0].Payload).code })
	}
	wg.Wait()
	for _, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("webhook codes %v", codes)
		}
	}
	var rows int
	// Test-only assertion query.
	mustNil(t, h.pool.QueryRow(h.ctx, `SELECT count(*) FROM webhook_events WHERE event_id = $1`, ds[0].EventID).Scan(&rows))
	if rows != 1 {
		t.Fatalf("inbox rows %d, want 1", rows)
	}
	if d := h.counter("webhooks_total", "result", orders.WebhookDuplicate); d != 2 {
		t.Fatalf("duplicates counted %v, want 2", d)
	}
	h.drain()
	if p := h.counter("webhooks_total", "result", orders.WebhookProcessed); p != 1 {
		t.Fatalf("processed %v, want 1", p)
	}
	if o := h.order(v.OrderID); o.Status != gen.OrderStatusCONFIRMED {
		t.Fatalf("order %s, want CONFIRMED (via the webhook)", o.Status)
	}
	// A bad signature is rejected and counted.
	if r := h.do(http.MethodPost, "/api/v1/webhooks/mockpay", "", "", map[string]string{"id": "evt_x"}); r.code != http.StatusBadRequest {
		t.Fatalf("unsigned webhook: HTTP %d", r.code)
	}
	if n := h.counter("webhooks_total", "result", orders.WebhookBadSignature); n != 1 {
		t.Fatalf("bad_signature %v", n)
	}
}

// `captured` arrives before `authorized`: the final state is CAPTURED and
// the late `authorized` is ignored and counted as a regression.
func TestWebhookCapturedBeforeAuthorized(t *testing.T) {
	t.Run("manual order", func(t *testing.T) {
		h := newHarness(t)
		u := h.newUser()
		v := h.createOrder(u, 9)
		intent := intentOf(t, v)
		if r := h.pay(u, v.OrderID, uuid.NewString(), payments.MethodOK); r.code != http.StatusOK {
			t.Fatalf("pay: %d", r.code)
		}
		h.drain() // capture
		ds := h.deliveries(intent)
		byType := map[string]int{}
		for i, d := range ds {
			byType[d.Type] = i
		}
		auth, okA := byType[payments.EventAuthorized]
		capt, okC := byType[payments.EventCaptured]
		if !okA || !okC {
			t.Fatalf("deliveries %v", byType)
		}
		if r := h.postWebhook(ds[capt].EventID, ds[capt].Payload); r.code != http.StatusOK {
			t.Fatal(r.code)
		}
		h.drain()
		if r := h.postWebhook(ds[auth].EventID, ds[auth].Payload); r.code != http.StatusOK {
			t.Fatal(r.code)
		}
		h.drain()
		if p := h.payment(v.OrderID, 1); p.Status != gen.PaymentStatusCAPTURED {
			t.Fatalf("payment %s, want CAPTURED", p.Status)
		}
		if n := h.counter("webhooks_total", "type", payments.EventAuthorized, "result", orders.WebhookRegressionIgnored); n != 1 {
			t.Fatalf("regression_ignored %v, want 1", n)
		}
	})
	t.Run("dispatcher reorder fault", func(t *testing.T) {
		h := newHarness(t, withWorker(), withDispatcher(), withWebhookDelay(750*time.Millisecond))
		u := h.newUser()
		v := h.createOrder(u, 10)
		h.flt.add("/confirm", mockpay.FaultReorderWebhook, 1) // hold `authorized` back >= 1.5 s
		if r := h.pay(u, v.OrderID, uuid.NewString(), payments.MethodOK); r.code != http.StatusOK {
			t.Fatalf("pay: %d", r.code)
		}
		h.eventually(10*time.Second, "both webhooks handled", func() bool {
			return h.counter("webhooks_total", "type", payments.EventAuthorized) >= 1 &&
				h.counter("webhooks_total", "type", payments.EventCaptured) >= 1
		})
		if p := h.payment(v.OrderID, 1); p.Status != gen.PaymentStatusCAPTURED {
			t.Fatalf("payment %s, want CAPTURED", p.Status)
		}
		if n := h.counter("webhooks_total", "type", payments.EventAuthorized, "result", orders.WebhookRegressionIgnored); n != 1 {
			t.Fatalf("regression_ignored %v, want 1", n)
		}
	})
}

// The webhook never arrives (no dispatcher): the user paid on the hosted
// checkout and the client's /confirm pulls the PSP and confirms.
func TestWebhookNeverArrivesConfirmPull(t *testing.T) {
	h := newHarness(t)
	u := h.newUser()
	v := h.createOrder(u, 11)
	loc := h.checkout(intentOf(t, v), payments.MethodOK)
	lu, err := url.Parse(loc)
	mustNil(t, err)
	if lu.Query().Get("o") != v.OrderID.String() || lu.Query().Get("status") != payments.StatusAuthorized {
		t.Fatalf("redirect %s", loc)
	}
	if o := h.order(v.OrderID); o.Status != gen.OrderStatusPENDINGPAYMENT {
		t.Fatalf("order %s before confirm", o.Status)
	}
	r := h.do(http.MethodPost, "/api/v1/orders/"+v.OrderID.String()+"/confirm", u.session, uuid.NewString(), nil)
	if r.code != http.StatusOK || r.view(t).Status != "CONFIRMED" || !r.view(t).Charged {
		t.Fatalf("confirm: HTTP %d %s", r.code, r.body)
	}
}

// The hold expires and is reclaimed BEFORE the authorization is processed;
// the seats go to a new holder. Both orders of the two follow-ups are
// covered: the authorization (ConfirmOrder) lands before the sweeper's void
// job, or the void job runs first.
func TestHoldReclaimedBeforeAuthorization(t *testing.T) {
	for _, authFirst := range []bool{true, false} {
		name := "void job first"
		if authFirst {
			name = "authorization lands first"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			u := h.newUser()
			v := h.createOrder(u, 12, 13)
			intent := intentOf(t, v)
			_ = h.checkout(intent, payments.MethodOK) // authorized at the PSP; nobody tells SLUICE yet
			h.clk.advance(3 * time.Minute)            // past PAYMENT_HOLD_TTL (2 min)
			res, err := h.inv.SweepOnce(h.ctx)
			mustNil(t, err)
			if res.Reclaimed != 1 {
				t.Fatalf("reclaimed %d", res.Reclaimed)
			}
			other := h.newUser()
			newHold := h.hold(other, 12, 13) // the seats go to someone else
			if authFirst {
				mustNil(t, h.svc.ConfirmOrder(h.ctx, v.OrderID))
			}
			h.drain()
			o := h.order(v.OrderID)
			if o.Status != gen.OrderStatusFAILED || o.FailureCode == nil || *o.FailureCode != orders.FailureHoldLost {
				t.Fatalf("order %s %v, want FAILED HOLD_LOST", o.Status, o.FailureCode)
			}
			if p := h.payment(v.OrderID, 1); p.Status != gen.PaymentStatusVOIDED {
				t.Fatalf("payment %s, want VOIDED", p.Status)
			}
			if l := h.ledger(intent); l["AUTH"] != 1 || l["VOID"] != 1 || l["CAPTURE"] != 0 {
				t.Fatalf("ledger %v", l)
			}
			g := h.getOrder(u, v.OrderID)
			if g.Charged || len(g.Tickets) != 0 {
				t.Fatalf("view %+v", g)
			}
			seats, err := h.q.GetSeatsByNos(h.ctx, gen.GetSeatsByNosParams{EventID: h.eventID, SeatNos: []int32{12, 13}})
			mustNil(t, err)
			for _, s := range seats {
				if s.State != gen.SeatStateHELD || s.HoldID == nil || *s.HoldID != newHold.ID {
					t.Fatalf("seat %d %s hold %v, want HELD by the new holder", s.SeatNo, s.State, s.HoldID)
				}
			}
		})
	}
}

// The authorization lands just before the sweeper: CONFIRMED, and the
// sweeper finds nothing to reclaim.
func TestAuthorizationJustBeforeSweeper(t *testing.T) {
	h := newHarness(t)
	u := h.newUser()
	v := h.createOrder(u, 14)
	_ = h.checkout(intentOf(t, v), payments.MethodOK)
	h.clk.advance(2*time.Minute + time.Second) // hold expired, not yet reclaimed
	mustNil(t, h.svc.ConfirmOrder(h.ctx, v.OrderID))
	res, err := h.inv.SweepOnce(h.ctx)
	mustNil(t, err)
	if res.Reclaimed != 0 {
		t.Fatalf("sweeper reclaimed %d, want 0", res.Reclaimed)
	}
	if o := h.order(v.OrderID); o.Status != gen.OrderStatusCONFIRMED {
		t.Fatalf("order %s", o.Status)
	}
	seats, err := h.q.GetSeatsByNos(h.ctx, gen.GetSeatsByNosParams{EventID: h.eventID, SeatNos: []int32{14}})
	mustNil(t, err)
	if seats[0].State != gen.SeatStateSOLD {
		t.Fatalf("seat %s", seats[0].State)
	}
}

// The §4.4 race, concurrently: authorizations and the sweeper hit expired
// PAYMENT_PENDING holds at the same time. Each order ends either CONFIRMED
// (seats sold, payment kept) or not confirmed with its money voided, never
// both, never neither.
func TestConfirmSweeperRace(t *testing.T) {
	const n = 16
	h := newHarness(t, withCapacity(2*n))
	type pair struct {
		u user
		v orders.View
	}
	ps := make([]pair, n)
	for i := range n {
		u := h.newUser()
		ps[i] = pair{u, h.createOrder(u, int32(2*i), int32(2*i+1))} //nolint:gosec // small
		_ = h.checkout(intentOf(t, ps[i].v), payments.MethodOK)
	}
	h.clk.advance(2*time.Minute + time.Second)
	var wg sync.WaitGroup
	half := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			time.Sleep(time.Duration(10*i) * time.Millisecond)
			if err := h.svc.ConfirmOrder(h.ctx, ps[i].v.OrderID); err != nil {
				t.Logf("confirm %d: %v", i, err)
			}
			if i == n/2 {
				close(half)
			}
		})
	}
	// A confirm starts with a PSP round trip and the first sweep reclaims
	// every expired hold in one batch, so fixed timings made one side win
	// every run. Confirms start 10 ms apart and the sweepers start the moment
	// confirm n/2 returns: earlier orders confirm, later ones lose, and the
	// ones in flight at that moment really contend for the order/hold locks.
	for range 3 {
		wg.Go(func() {
			<-half
			_, _ = h.inv.SweepOnce(h.ctx)
		})
	}
	wg.Wait()
	h.drain()
	// Settle the rest (orders whose confirm lost the race and were voided).
	for i := range n {
		mustNil(t, h.svc.ConfirmOrder(h.ctx, ps[i].v.OrderID))
	}
	h.drain()
	confirmed := 0
	for _, p := range ps {
		o := h.order(p.v.OrderID)
		pay := h.payment(p.v.OrderID, 1)
		l := h.ledger(intentOf(t, p.v))
		seats, err := h.q.GetSeatsByNos(h.ctx, gen.GetSeatsByNosParams{EventID: h.eventID, SeatNos: o.SeatNos})
		mustNil(t, err)
		switch o.Status {
		case gen.OrderStatusCONFIRMED:
			confirmed++
			if pay.Status != gen.PaymentStatusCAPTURED || l["CAPTURE"] != 1 || l["VOID"] != 0 {
				t.Fatalf("confirmed order %s: payment %s ledger %v", o.ID, pay.Status, l)
			}
			for _, s := range seats {
				if s.State != gen.SeatStateSOLD || s.OrderID == nil || *s.OrderID != o.ID {
					t.Fatalf("confirmed order seat %d %s", s.SeatNo, s.State)
				}
			}
		case gen.OrderStatusFAILED:
			if pay.Status != gen.PaymentStatusVOIDED || l["VOID"] != 1 || l["CAPTURE"] != 0 {
				t.Fatalf("failed order %s: payment %s ledger %v", o.ID, pay.Status, l)
			}
			for _, s := range seats {
				if s.State == gen.SeatStateSOLD {
					t.Fatalf("failed order kept seat %d", s.SeatNo)
				}
			}
		default:
			t.Fatalf("order %s ended %s", o.ID, o.Status)
		}
	}
	t.Logf("race: %d confirmed, %d voided (of %d)", confirmed, n-confirmed, n)
	if confirmed == 0 || confirmed == n {
		t.Logf("only one side of the race happened this run; invariants still checked")
	}
}

// The per-user quota is enforced at confirm (I4): the user's quota row is
// bumped to 3 after the hold of 2 was placed, so confirm would make 5 > 4.
func TestQuotaExceededAtConfirm(t *testing.T) {
	h := newHarness(t)
	u := h.newUser()
	v := h.createOrder(u, 15, 16)
	_, err := h.q.AddQuota(h.ctx, gen.AddQuotaParams{EventID: h.eventID, UserID: u.id, Seats: 3, MaxAllowed: 4})
	mustNil(t, err)
	r := h.pay(u, v.OrderID, uuid.NewString(), payments.MethodOK)
	if r.code != http.StatusOK {
		t.Fatalf("pay: HTTP %d %s", r.code, r.body)
	}
	pv := r.view(t)
	if pv.Status != "FAILED" || pv.FailureCode == nil || *pv.FailureCode != orders.FailureQuotaExceeded {
		t.Fatalf("pay result %+v", pv)
	}
	h.drain()
	g := h.getOrder(u, v.OrderID)
	if g.Charged || g.Payment.Status != "VOIDED" {
		t.Fatalf("after void: charged=%v payment %+v", g.Charged, g.Payment)
	}
	if l := h.ledger(intentOf(t, v)); l["AUTH"] != 1 || l["VOID"] != 1 || l["CAPTURE"] != 0 {
		t.Fatalf("ledger %v", l)
	}
	q, err := h.q.GetQuota(h.ctx, gen.GetQuotaParams{EventID: h.eventID, UserID: u.id})
	mustNil(t, err)
	if q.SeatsPurchased != 3 {
		t.Fatalf("quota %d, want 3 (unchanged)", q.SeatsPurchased)
	}
	hd, err := h.q.GetHold(h.ctx, h.order(v.OrderID).HoldID)
	mustNil(t, err)
	if hd.Status != gen.HoldStatusRELEASED {
		t.Fatalf("hold %s, want RELEASED", hd.Status)
	}
	seats, err := h.q.GetSeatsByNos(h.ctx, gen.GetSeatsByNosParams{EventID: h.eventID, SeatNos: []int32{15, 16}})
	mustNil(t, err)
	for _, s := range seats {
		if s.State != gen.SeatStateAVAILABLE {
			t.Fatalf("seat %d %s", s.SeatNo, s.State)
		}
	}
}

// Capture fails twice with a PSP 5xx, then succeeds: CAPTURED after
// retries, exactly one ledger capture.
func TestCaptureRetries(t *testing.T) {
	t.Run("client retries (2 failures)", func(t *testing.T) {
		h := newHarness(t, withWorker())
		u := h.newUser()
		v := h.createOrder(u, 17)
		h.flt.add("/capture", mockpay.FaultFail, 2)
		if r := h.pay(u, v.OrderID, uuid.NewString(), payments.MethodOK); r.code != http.StatusOK {
			t.Fatalf("pay %d", r.code)
		}
		h.eventually(5*time.Second, "capture", func() bool {
			return h.payment(v.OrderID, 1).Status == gen.PaymentStatusCAPTURED
		})
		if l := h.ledger(intentOf(t, v)); l["CAPTURE"] != 1 {
			t.Fatalf("ledger %v", l)
		}
		if n := h.counter("psp_requests_total", "op", payments.OpCapture, "result", "server_error"); n != 2 {
			t.Fatalf("capture 5xx %v, want 2", n)
		}
	})
	t.Run("job retries (5 failures)", func(t *testing.T) {
		h := newHarness(t)
		u := h.newUser()
		v := h.createOrder(u, 18)
		h.flt.add("/capture", mockpay.FaultFail, 5) // more than one job attempt can absorb (1 + 3 retries)
		if r := h.pay(u, v.OrderID, uuid.NewString(), payments.MethodOK); r.code != http.StatusOK {
			t.Fatalf("pay %d", r.code)
		}
		h.drain() // attempt 1 fails after 4 tries, attempt 2 succeeds
		if p := h.payment(v.OrderID, 1); p.Status != gen.PaymentStatusCAPTURED {
			t.Fatalf("payment %s", p.Status)
		}
		if l := h.ledger(intentOf(t, v)); l["CAPTURE"] != 1 {
			t.Fatalf("ledger %v", l)
		}
	})
}

// An orphan created artificially (an authorized intent with no order) is
// voided by the reconciler within one cycle; a captured one is refunded.
func TestOrphanReconciled(t *testing.T) {
	h := newHarness(t)
	mk := func(capture bool) string {
		ref := uuid.NewString()
		in, err := h.psp.CreateIntent(h.ctx, "orphan:"+ref, payments.CreateIntentRequest{AmountPaise: 1000, Currency: "INR",
			MerchantRef: ref, Confirm: true, PaymentMethod: payments.MethodOK})
		mustNil(t, err)
		if capture {
			_, err = h.psp.Capture(h.ctx, in.ID)
			mustNil(t, err)
		}
		return in.ID
	}
	authID, captID := mk(false), mk(true)
	h.clk.advance(2 * time.Second) // older than OrphanAge (1 s in tests)
	res, err := h.svc.Reconcile(h.ctx)
	mustNil(t, err)
	if res.OrphansFixed != 2 {
		t.Fatalf("reconcile %+v", res)
	}
	if l := h.ledger(authID); l["VOID"] != 1 {
		t.Fatalf("authorized orphan ledger %v", l)
	}
	if l := h.ledger(captID); l["REFUND"] != 1 {
		t.Fatalf("captured orphan ledger %v", l)
	}
	if n := h.counter("orphans_fixed_total"); n != 2 {
		t.Fatalf("orphans_fixed_total %v", n)
	}
	// Through the admin route, the next cycle finds nothing.
	r := h.do(http.MethodPost, "/api/v1/admin/reconcile", adminToken, "", nil)
	var rr orders.ReconcileResult
	mustNil(t, json.Unmarshal(r.body, &rr))
	if r.code != http.StatusOK || rr.OrphansFixed != 0 {
		t.Fatalf("second reconcile: HTTP %d %s", r.code, r.body)
	}
	if r := h.do(http.MethodPost, "/api/v1/admin/reconcile", "wrong", "", nil); r.code != http.StatusUnauthorized {
		t.Fatalf("reconcile without admin token: HTTP %d", r.code)
	}
}

// Two authorized attempts for the same order (forced past the API's
// one-attempt-at-a-time rule): one confirms, the second is voided and
// counted in duplicate_charges_prevented_total.
func TestTwoAuthorizedAttempts(t *testing.T) {
	h := newHarness(t)
	u := h.newUser()
	v := h.createOrder(u, 19)
	o := h.order(v.OrderID)
	p2, err := h.q.InsertPayment(h.ctx, gen.InsertPaymentParams{OrderID: o.ID, Attempt: 2,
		PspIdempotencyKey: payments.CreateKey(o.ID, 2), AmountPaise: o.AmountPaise})
	mustNil(t, err)
	in2, err := h.psp.CreateIntent(h.ctx, p2.PspIdempotencyKey, payments.CreateIntentRequest{AmountPaise: o.AmountPaise,
		Currency: "INR", MerchantRef: o.ID.String(), Metadata: map[string]string{"attempt": "2"}})
	mustNil(t, err)
	_, err = h.q.SetPaymentIntent(h.ctx, gen.SetPaymentIntentParams{ProviderIntentID: &in2.ID, ID: p2.ID})
	mustNil(t, err)
	_, err = h.psp.Confirm(h.ctx, intentOf(t, v), payments.MethodOK)
	mustNil(t, err)
	_, err = h.psp.Confirm(h.ctx, in2.ID, payments.MethodOK)
	mustNil(t, err)

	mustNil(t, h.svc.ConfirmOrder(h.ctx, o.ID))
	h.drain()
	if got := h.order(o.ID); got.Status != gen.OrderStatusCONFIRMED {
		t.Fatalf("order %s", got.Status)
	}
	if p := h.payment(o.ID, 1); p.Status != gen.PaymentStatusCAPTURED {
		t.Fatalf("attempt 1 %s", p.Status)
	}
	if p := h.payment(o.ID, 2); p.Status != gen.PaymentStatusVOIDED {
		t.Fatalf("attempt 2 %s", p.Status)
	}
	if l := h.ledger(in2.ID); l["AUTH"] != 1 || l["VOID"] != 1 || l["CAPTURE"] != 0 {
		t.Fatalf("attempt 2 ledger %v", l)
	}
	if n := h.counter("duplicate_charges_prevented_total"); n != 1 {
		t.Fatalf("duplicate_charges_prevented_total %v, want 1", n)
	}
}

// POST /orders while the PSP is down: 503 with Retry-After, the
// Idempotency-Key is released, and the retry re-runs the create with the
// same PSP key (order:<id>:attempt:1): one order, one intent.
func TestCreateOrderPSPDownThenRetry(t *testing.T) {
	h := newHarness(t)
	u := h.newUser()
	hd := h.hold(u, 2, 3)
	h.flt.add("/v1/intents", mockpay.FaultFail, 4) // 1 try + 3 client retries
	key := uuid.NewString()
	r := h.do(http.MethodPost, "/api/v1/orders", u.admission, key, map[string]any{"hold_id": hd.ID})
	if r.code != http.StatusServiceUnavailable || r.hdr.Get("Retry-After") == "" || r.problemCode() != "DEPENDENCY_UNAVAILABLE" {
		t.Fatalf("PSP down: HTTP %d Retry-After=%q %s", r.code, r.hdr.Get("Retry-After"), r.body)
	}
	r = h.do(http.MethodPost, "/api/v1/orders", u.admission, key, map[string]any{"hold_id": hd.ID})
	if r.code != http.StatusCreated {
		t.Fatalf("retry: HTTP %d %s", r.code, r.body)
	}
	v := r.view(t)
	if n := h.intentsFor(v.OrderID); n != 1 {
		t.Fatalf("%d intents, want 1", n)
	}
	if p := h.payment(v.OrderID, 1); p.PspIdempotencyKey != payments.CreateKey(v.OrderID, 1) || p.ProviderIntentID == nil {
		t.Fatalf("payment key %q intent %v", p.PspIdempotencyKey, p.ProviderIntentID)
	}
	rep := h.do(http.MethodPost, "/api/v1/orders", u.admission, key, map[string]any{"hold_id": hd.ID})
	if rep.code != http.StatusCreated || rep.hdr.Get(httpx_Replayed) != "true" {
		t.Fatalf("replay: HTTP %d replayed=%q", rep.code, rep.hdr.Get(httpx_Replayed))
	}
	if n := h.counter("idempotency_replays_total"); n != 1 {
		t.Fatalf("idempotency_replays_total %v, want 1", n)
	}
}
