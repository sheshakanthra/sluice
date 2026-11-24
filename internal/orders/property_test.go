package orders_test

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"pgregory.net/rapid"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/jobs"
	"github.com/sheshakanthra/sluice/internal/mockpay"
	"github.com/sheshakanthra/sluice/internal/payments"
)

// propCase is the state of one rapid case: a fresh 4-seat event, 1-3 users
// who all want seats from {0,1,2}, and the orders they managed to create.
type propCase struct {
	h       *harness
	rt      *rapid.T
	eventID uuid.UUID
	users   []uuid.UUID
	orders  map[uuid.UUID]uuid.UUID // order -> user
}

// bug fails the case on an error that is never a business outcome: a
// Postgres error with a SQLSTATE (constraint violation, deadlock,
// serialization failure) means an invariant was defended by the database,
// not by the code, or the code is wrong.
func (c *propCase) bug(what string, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		c.rt.Fatalf("%s: postgres error %s: %v", what, pgErr.Code, err)
	}
}

func (c *propCase) intents() []string {
	var out []string
	for o := range c.orders {
		ps, err := c.h.q.ListPaymentsByOrder(c.h.ctx, o)
		if err != nil {
			c.rt.Fatal(err)
		}
		for i := range ps {
			p := &ps[i]
			if p.ProviderIntentID != nil {
				out = append(out, *p.ProviderIntentID)
			}
		}
	}
	return out
}

func (c *propCase) pickOrder(name string) (uuid.UUID, uuid.UUID, bool) {
	if len(c.orders) == 0 {
		return uuid.Nil, uuid.Nil, false
	}
	ids := make([]uuid.UUID, 0, len(c.orders))
	for o := range c.orders {
		ids = append(ids, o)
	}
	// Map iteration order is random; sort for reproducible shrinking.
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	o := rapid.SampledFrom(ids).Draw(c.rt, name)
	return o, c.orders[o], true
}

// checkoutTolerant pays an intent through the hosted checkout and accepts
// any answer (the intent may already be canceled or authorized).
func (c *propCase) checkoutTolerant(intentID, method string) {
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(c.h.ctx, http.MethodPost, c.h.mpSrv.URL+"/checkout/"+intentID+"/pay",
		strings.NewReader("payment_method="+method))
	if err != nil {
		c.rt.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := cl.Do(req)
	if err != nil {
		c.rt.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode >= 500 {
		c.rt.Fatalf("checkout %s: HTTP %d", intentID, res.StatusCode)
	}
}

// deliverAll posts every MockPay delivery for this case's intents once
// (the inbox dedups) in a random or fixed order.
func (c *propCase) deliveries() []deliveryRef {
	var out []deliveryRef
	for _, in := range c.intents() {
		ds := c.h.deliveries(in)
		for i := range ds {
			out = append(out, deliveryRef{ds[i].EventID, ds[i].Payload})
		}
	}
	return out
}

type deliveryRef struct {
	id      string
	payload []byte
}

func (c *propCase) post(d deliveryRef) {
	if r := c.h.postWebhook(d.id, d.payload); r.code != http.StatusOK {
		c.rt.Fatalf("webhook %s: HTTP %d %s", d.id, r.code, r.body)
	}
}

// runJob runs one queued job (chosen by rapid) and deletes it if it
// succeeded; a failing job stays queued, as with River retries.
func (c *propCase) runJob() {
	res, err := c.h.river.JobList(c.h.ctx, river.NewJobListParams().First(100).States(
		rivertype.JobStateAvailable, rivertype.JobStateScheduled, rivertype.JobStateRetryable))
	if err != nil {
		c.rt.Fatal(err)
	}
	if len(res.Jobs) == 0 {
		return
	}
	j := rapid.SampledFrom(res.Jobs).Draw(c.rt, "job")
	if err := jobs.Dispatch(c.h.ctx, c.h.svc, j.Kind, j.EncodedArgs); err != nil {
		c.bug("job "+j.Kind, err)
		return
	}
	if _, err := c.h.river.JobDelete(c.h.ctx, j.ID); err != nil {
		c.rt.Fatal(err)
	}
}

// drainAll runs queued jobs until none is left.
func (c *propCase) drainAll() {
	for round := 0; round < 30; round++ {
		res, err := c.h.river.JobList(c.h.ctx, river.NewJobListParams().First(1000).States(
			rivertype.JobStateAvailable, rivertype.JobStateScheduled, rivertype.JobStateRetryable))
		if err != nil {
			c.rt.Fatal(err)
		}
		if len(res.Jobs) == 0 {
			return
		}
		for _, j := range res.Jobs {
			if err := jobs.Dispatch(c.h.ctx, c.h.svc, j.Kind, j.EncodedArgs); err != nil {
				c.bug("job "+j.Kind, err)
				continue
			}
			if _, err := c.h.river.JobDelete(c.h.ctx, j.ID); err != nil {
				c.rt.Fatal(err)
			}
		}
	}
	c.rt.Fatal("jobs still queued after 30 rounds")
}

// settle brings the case to rest the way production does: every webhook
// eventually arrives, the clock passes every hold, the sweeper and the
// reconciler run, and River drains (PSP faults have run out by then).
func (c *propCase) settle() {
	c.h.flt.reset()
	for range 3 {
		for _, d := range c.deliveries() {
			c.post(d)
		}
		c.drainAll()
		for o := range c.orders {
			c.bug("settle confirm", c.h.svc.ConfirmOrder(c.h.ctx, o))
		}
		c.h.clk.advance(3 * time.Minute)
		_, err := c.h.inv.SweepOnce(c.h.ctx)
		c.bug("settle sweep", err)
		c.drainAll()
		_, err = c.h.svc.Reconcile(c.h.ctx)
		c.bug("settle reconcile", err)
		c.drainAll()
	}
}

// TestPaymentsProperty: random interleavings of create / pay / hosted
// checkout / confirm / webhooks (duplicated, reordered) / jobs (any order)
// / expiry + sweeper / reconcile / capture failures / new attempts, for 1-3
// users contending for the same seats. After settling: I1-I4 hold, at most
// one ledger capture per order, and every captured intent belongs to a
// CONFIRMED order or was refunded.
func TestPaymentsProperty(t *testing.T) {
	// ~3-4 min under -race (100 rapid cases); 4 min was too tight.
	h := newHarness(t, withCapacity(4), withTimeout(12*time.Minute))
	tally := map[string]int{}
	defer func() { t.Logf("outcomes over all cases: %v", tally) }()
	rapid.Check(t, func(rt *rapid.T) {
		c := &propCase{h: h, rt: rt, eventID: h.newEvent(4), orders: map[uuid.UUID]uuid.UUID{}}
		if _, err := h.inv.Rebuild(h.ctx, c.eventID); err != nil {
			rt.Fatal(err)
		}
		nUsers := rapid.IntRange(1, 3).Draw(rt, "users")
		for range nUsers {
			c.users = append(c.users, uuid.Must(uuid.NewV7()))
		}
		rt.Repeat(map[string]func(*rapid.T){
			"create": func(rt *rapid.T) {
				u := rapid.SampledFrom(c.users).Draw(rt, "user")
				seats := rapid.SliceOfNDistinct(rapid.Int32Range(0, 2), 1, 2, rapid.ID[int32]).Draw(rt, "seats")
				hd, err := h.inv.Hold(h.ctx, c.eventID, u, uuid.NewString(), inventory.Request{SeatNos: seats})
				if err != nil {
					c.bug("hold", err)
					return
				}
				v, err := h.svc.Create(h.ctx, u, hd.ID)
				if err != nil {
					c.bug("create", err)
					return
				}
				c.orders[v.OrderID] = u
			},
			"pay": func(rt *rapid.T) {
				o, u, ok := c.pickOrder("order")
				if !ok {
					return
				}
				m := rapid.SampledFrom([]string{payments.MethodOK, payments.MethodDecline}).Draw(rt, "method")
				_, err := h.svc.Pay(h.ctx, o, u, m)
				c.bug("pay", err)
			},
			"checkout": func(rt *rapid.T) {
				o, _, ok := c.pickOrder("order")
				if !ok {
					return
				}
				ps, err := h.q.ListPaymentsByOrder(h.ctx, o)
				if err != nil {
					rt.Fatal(err)
				}
				p := ps[len(ps)-1]
				if p.ProviderIntentID == nil {
					return
				}
				m := rapid.SampledFrom([]string{payments.MethodOK, payments.MethodDecline}).Draw(rt, "method")
				c.checkoutTolerant(*p.ProviderIntentID, m)
			},
			"confirm": func(*rapid.T) {
				o, _, ok := c.pickOrder("order")
				if !ok {
					return
				}
				c.bug("confirm", h.svc.ConfirmOrder(h.ctx, o))
			},
			"webhook": func(rt *rapid.T) {
				ds := c.deliveries()
				if len(ds) == 0 {
					return
				}
				d := rapid.SampledFrom(ds).Draw(rt, "delivery") // any order: reorder for free
				for range rapid.IntRange(1, 3).Draw(rt, "copies") {
					c.post(d)
				}
			},
			"job": func(*rapid.T) { c.runJob() },
			"expire": func(rt *rapid.T) {
				h.clk.advance(time.Duration(rapid.IntRange(30, 180).Draw(rt, "seconds")) * time.Second)
				_, err := h.inv.SweepOnce(h.ctx)
				c.bug("sweep", err)
			},
			"reconcile": func(*rapid.T) {
				_, err := h.svc.Reconcile(h.ctx)
				c.bug("reconcile", err)
			},
			"captureFault": func(rt *rapid.T) {
				h.flt.add("/capture", mockpay.FaultFail, rapid.IntRange(1, 6).Draw(rt, "failures"))
			},
			"newAttempt": func(*rapid.T) {
				o, u, ok := c.pickOrder("order")
				if !ok {
					return
				}
				_, err := h.svc.NewAttempt(h.ctx, o, u)
				c.bug("new attempt", err)
			},
		})
		c.settle()
		c.check(tally)
	})
}

// check asserts the invariants on the settled case.
func (c *propCase) check(tally map[string]int) {
	h, rt := c.h, c.rt
	ctx := h.ctx
	seatOwner := map[int32]uuid.UUID{}
	quota := map[uuid.UUID]int32{}
	for oid, uid := range c.orders {
		o, err := h.q.GetOrder(ctx, oid)
		if err != nil {
			rt.Fatal(err)
		}
		ps, err := h.q.ListPaymentsByOrder(ctx, oid)
		if err != nil {
			rt.Fatal(err)
		}
		live, captures := 0, 0
		for i := range ps {
			p := &ps[i]
			if p.Status == gen.PaymentStatusAUTHORIZED || p.Status == gen.PaymentStatusCAPTURED {
				live++
			}
			if p.ProviderIntentID == nil {
				continue
			}
			l := h.ledger(*p.ProviderIntentID)
			captures += l["CAPTURE"]
			// I3 and no money left behind: authorized money is captured or
			// voided; captured money is this order's ticket or refunded.
			if l["AUTH"] > 0 && l["CAPTURE"] == 0 && l["VOID"] == 0 {
				rt.Fatalf("order %s (%s): intent %s authorized and never captured or voided: %v", oid, o.Status, *p.ProviderIntentID, l)
			}
			if l["CAPTURE"] > 0 && l["REFUND"] == 0 && (o.Status != gen.OrderStatusCONFIRMED || p.Status != gen.PaymentStatusCAPTURED) {
				rt.Fatalf("order %s (%s): intent %s captured without a confirmed order or refund (payment %s)", oid, o.Status, *p.ProviderIntentID, p.Status)
			}
		}
		tally["order_"+string(o.Status)]++
		for i := range ps {
			p := &ps[i]
			tally["payment_"+string(p.Status)]++
			if p.ProviderIntentID != nil {
				for k, n := range h.ledger(*p.ProviderIntentID) {
					tally["ledger_"+k] += n
				}
			}
		}
		if captures > 1 {
			rt.Fatalf("order %s: %d ledger captures", oid, captures)
		}
		// I2: at most one live payment.
		if live > 1 {
			rt.Fatalf("order %s: %d live payments", oid, live)
		}
		switch o.Status {
		case gen.OrderStatusCONFIRMED:
			if live != 1 || captures != 1 {
				rt.Fatalf("confirmed order %s: live=%d captures=%d", oid, live, captures)
			}
			for _, s := range o.SeatNos {
				if prev, dup := seatOwner[s]; dup {
					rt.Fatalf("I1: seat %d confirmed for orders %s and %s", s, prev, oid)
				}
				seatOwner[s] = oid
			}
			quota[uid] += int32(len(o.SeatNos)) //nolint:gosec // <= 4
		case gen.OrderStatusFAILED, gen.OrderStatusEXPIRED:
			if live != 0 {
				rt.Fatalf("%s order %s still has a live payment", o.Status, oid)
			}
			v, err := h.svc.Get(ctx, oid, uid)
			if err != nil {
				rt.Fatal(err)
			}
			if v.Charged {
				rt.Fatalf("%s order %s reports charged", o.Status, oid)
			}
		default:
			rt.Fatalf("order %s did not settle: %s", oid, o.Status)
		}
	}
	// I1 from the seat side: SOLD exactly for confirmed seats, one ticket each.
	seats, err := h.q.ListSeatStates(ctx, c.eventID)
	if err != nil {
		rt.Fatal(err)
	}
	for _, s := range seats {
		owner, sold := seatOwner[s.SeatNo]
		if sold != (s.State == gen.SeatStateSOLD) {
			rt.Fatalf("seat %d is %s; confirmed owner %v", s.SeatNo, s.State, owner)
		}
		var tickets int
		// Test-only assertion query.
		if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE event_id = $1 AND seat_no = $2`,
			c.eventID, s.SeatNo).Scan(&tickets); err != nil {
			rt.Fatal(err)
		}
		if (sold && tickets != 1) || (!sold && tickets != 0) {
			rt.Fatalf("seat %d: %d tickets (sold=%v)", s.SeatNo, tickets, sold)
		}
	}
	// I4: quota row equals confirmed seats and stays within max_per_user.
	for _, u := range c.users {
		q, err := h.q.GetQuota(ctx, gen.GetQuotaParams{EventID: c.eventID, UserID: u})
		got := int32(0)
		if err == nil {
			got = q.SeatsPurchased
		}
		if got != quota[u] || got > 4 {
			rt.Fatalf("user %s: quota row %d, confirmed seats %d", u, got, quota[u])
		}
	}
}
