package db_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
)

// Constraint names as Postgres derives them from DATA-MODEL section 1.
const (
	conTicketsSeat     = "tickets_event_id_seat_no_key"
	conPaymentsOneLive = "payments_one_live_per_order"
	conQuotaCap        = "quota_cap"
	conSeatStateShape  = "seat_state_shape"
	conHoldsOneActive  = "holds_one_active_per_user"
)

const workers = 64

// I1: a seat has at most one ticket.
func TestTicketsRejectSecondTicketForSeat(t *testing.T) {
	f := newFixture(t, 10)
	ctx := context.Background()
	o1, o2 := f.order(ctx, 3), f.order(ctx, 3)

	insert := func(o gen.Order, code string) error {
		_, err := f.q.InsertTickets(ctx, gen.InsertTicketsParams{
			EventID: f.eventID, SeatNos: []int32{3}, OrderID: o.ID, UserID: o.UserID, Codes: []string{code},
		})
		return err
	}
	if err := insert(o1, "SLC-AAAA-0001"); err != nil {
		t.Fatalf("first ticket: %v", err)
	}
	err := insert(o2, "SLC-AAAA-0002")
	if !db.IsUniqueViolation(err, conTicketsSeat) {
		t.Fatalf("second ticket for the same seat: got %v, want unique violation of %s", err, conTicketsSeat)
	}
	if n, _ := f.q.CountTicketsByEvent(ctx, f.eventID); n != 1 {
		t.Fatalf("tickets = %d, want 1", n)
	}
}

// I1 under contention: many orders race to ticket the same seat; exactly one wins.
func TestTicketsConcurrentSameSeatOneWinner(t *testing.T) {
	f := newFixture(t, 10)
	ctx := context.Background()
	orders := make([]gen.Order, workers)
	for i := range orders {
		orders[i] = f.order(ctx, 7)
	}
	var wins, dupes atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, o := range orders {
		wg.Go(func() {
			<-start
			err := db.WithTx(ctx, f.pool, func(tx pgx.Tx) error {
				_, err := gen.New(tx).InsertTickets(ctx, gen.InsertTicketsParams{
					EventID: f.eventID, SeatNos: []int32{7}, OrderID: o.ID, UserID: o.UserID,
					Codes: []string{fmt.Sprintf("SLC-RACE-%04d", i)},
				})
				return err
			})
			switch {
			case err == nil:
				wins.Add(1)
			case db.IsUniqueViolation(err, conTicketsSeat):
				dupes.Add(1)
			default:
				t.Errorf("worker %d: unexpected error %v", i, err)
			}
		})
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 || dupes.Load() != workers-1 {
		t.Fatalf("wins=%d dupes=%d, want 1 and %d", wins.Load(), dupes.Load(), workers-1)
	}
	if n, _ := f.q.CountTicketsByEvent(ctx, f.eventID); n != 1 {
		t.Fatalf("tickets = %d, want 1", n)
	}
}

// I2: at most one AUTHORIZED/CAPTURED payment per order.
func TestPaymentsOneLivePerOrder(t *testing.T) {
	tests := []struct {
		first, second string
		wantReject    bool
	}{
		{"AUTHORIZED", "AUTHORIZED", true},
		{"AUTHORIZED", "CAPTURED", true},
		{"CAPTURED", "AUTHORIZED", true},
		{"CAPTURED", "CAPTURED", true},
		{"VOIDED", "AUTHORIZED", false},
		{"FAILED", "CAPTURED", false},
		{"REFUNDED", "AUTHORIZED", false},
		{"CREATED", "AUTHORIZED", false},
		{"AUTHORIZED", "FAILED", false},
	}
	f := newFixture(t, 30)
	ctx := context.Background()
	for i, tt := range tests {
		t.Run(tt.first+"_then_"+tt.second, func(t *testing.T) {
			o := f.order(ctx, int32(i)) //nolint:gosec // G115: tiny index
			// Hand-written SQL: the generated InsertPayment always starts at
			// CREATED; this table needs arbitrary starting statuses.
			const ins = `INSERT INTO payments (order_id, attempt, psp_idempotency_key, amount_paise, status)
			             VALUES ($1, $2, $3, 125000, $4::payment_status)`
			if _, err := f.pool.Exec(ctx, ins, o.ID, 1, fmt.Sprintf("order:%s:attempt:1", o.ID), tt.first); err != nil {
				t.Fatalf("first payment: %v", err)
			}
			_, err := f.pool.Exec(ctx, ins, o.ID, 2, fmt.Sprintf("order:%s:attempt:2", o.ID), tt.second)
			if tt.wantReject && !db.IsUniqueViolation(err, conPaymentsOneLive) {
				t.Fatalf("got %v, want unique violation of %s", err, conPaymentsOneLive)
			}
			if !tt.wantReject && err != nil {
				t.Fatalf("got %v, want success", err)
			}
		})
	}

	// The generated transition query hits the same backstop.
	t.Run("AuthorizePayment_second_attempt", func(t *testing.T) {
		o := f.order(ctx, 29)
		p1 := insertPayment(ctx, t, f, o, 1)
		p2 := insertPayment(ctx, t, f, o, 2)
		if n, err := f.q.AuthorizePayment(ctx, p1.ID); err != nil || n != 1 {
			t.Fatalf("authorize p1: n=%d err=%v", n, err)
		}
		_, err := f.q.AuthorizePayment(ctx, p2.ID)
		if !db.IsUniqueViolation(err, conPaymentsOneLive) {
			t.Fatalf("authorize p2: got %v, want unique violation", err)
		}
	})
}

// I2 under contention: three attempts race to authorize; exactly one wins.
func TestPaymentsConcurrentAuthorizeOneWinner(t *testing.T) {
	f := newFixture(t, 40)
	ctx := context.Background()
	for round := range 20 {
		o := f.order(ctx, int32(round)) //nolint:gosec // G115: tiny index
		ps := []gen.Payment{insertPayment(ctx, t, f, o, 1), insertPayment(ctx, t, f, o, 2), insertPayment(ctx, t, f, o, 3)}
		var wins atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, p := range ps {
			wg.Go(func() {
				<-start
				n, err := f.q.AuthorizePayment(ctx, p.ID)
				switch {
				case err == nil && n == 1:
					wins.Add(1)
				case db.IsUniqueViolation(err, conPaymentsOneLive):
				default:
					t.Errorf("authorize: n=%d err=%v", n, err)
				}
			})
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("round %d: %d live payments, want 1", round, wins.Load())
		}
	}
}

func insertPayment(ctx context.Context, t *testing.T, f *fixture, o gen.Order, attempt int32) gen.Payment {
	t.Helper()
	p, err := f.q.InsertPayment(ctx, gen.InsertPaymentParams{
		OrderID: o.ID, Attempt: attempt, AmountPaise: o.AmountPaise,
		PspIdempotencyKey: fmt.Sprintf("order:%s:attempt:%d", o.ID, attempt),
	})
	if err != nil {
		t.Fatalf("insert payment: %v", err)
	}
	return p
}

// I4: seats_purchased never exceeds max_allowed.
func TestQuotaCap(t *testing.T) {
	tests := []struct {
		name       string
		adds       []int32
		maxAllowed int32
		wantReject bool // the last add is rejected
		wantFinal  int32
	}{
		{"single_add_within_cap", []int32{4}, 4, false, 4},
		{"single_add_over_cap", []int32{5}, 4, true, 0},
		{"accumulate_to_cap", []int32{2, 1, 1}, 4, false, 4},
		{"accumulate_past_cap", []int32{3, 2}, 4, true, 3},
		{"cap_of_one", []int32{1, 1}, 1, true, 1},
	}
	f := newFixture(t, 1)
	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := uuid.New()
			var err error
			for i, n := range tt.adds {
				_, err = f.q.AddQuota(ctx, gen.AddQuotaParams{EventID: f.eventID, UserID: user, Seats: n, MaxAllowed: tt.maxAllowed})
				if i < len(tt.adds)-1 && err != nil {
					t.Fatalf("add %d: %v", i, err)
				}
			}
			if tt.wantReject != db.IsCheckViolation(err, conQuotaCap) {
				t.Fatalf("last add: got %v, wantReject=%v", err, tt.wantReject)
			}
			if !tt.wantReject && err != nil {
				t.Fatalf("last add: %v", err)
			}
			q, err := f.q.GetQuota(ctx, gen.GetQuotaParams{EventID: f.eventID, UserID: user})
			got := int32(0)
			if err == nil {
				got = q.SeatsPurchased
			} else if err != pgx.ErrNoRows { //nolint:errorlint // sentinel from pgx, never wrapped here
				t.Fatal(err)
			}
			if got != tt.wantFinal {
				t.Fatalf("seats_purchased = %d, want %d", got, tt.wantFinal)
			}
		})
	}

	t.Run("release_below_zero_rejected", func(t *testing.T) {
		user := uuid.New()
		if _, err := f.q.AddQuota(ctx, gen.AddQuotaParams{EventID: f.eventID, UserID: user, Seats: 1, MaxAllowed: 4}); err != nil {
			t.Fatal(err)
		}
		_, err := f.q.ReleaseQuota(ctx, gen.ReleaseQuotaParams{EventID: f.eventID, UserID: user, Seats: 2})
		if !db.IsCheckViolation(err, "event_user_quota_seats_purchased_check") {
			t.Fatalf("got %v, want seats_purchased >= 0 violation", err)
		}
	})
}

// I4 under contention: many single-seat purchases race; exactly max_allowed commit.
func TestQuotaConcurrentAddsStopAtCap(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	user := uuid.New()
	const maxAllowed = 4
	var ok, capped atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range workers {
		wg.Go(func() {
			<-start
			err := db.WithTx(ctx, f.pool, func(tx pgx.Tx) error {
				_, err := gen.New(tx).AddQuota(ctx, gen.AddQuotaParams{EventID: f.eventID, UserID: user, Seats: 1, MaxAllowed: maxAllowed})
				return err
			})
			switch {
			case err == nil:
				ok.Add(1)
			case db.IsCheckViolation(err, conQuotaCap):
				capped.Add(1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()
	q, err := f.q.GetQuota(ctx, gen.GetQuotaParams{EventID: f.eventID, UserID: user})
	if err != nil {
		t.Fatal(err)
	}
	if ok.Load() != maxAllowed || capped.Load() != workers-maxAllowed || q.SeatsPurchased != maxAllowed {
		t.Fatalf("ok=%d capped=%d final=%d, want %d/%d/%d", ok.Load(), capped.Load(), q.SeatsPurchased, maxAllowed, workers-maxAllowed, maxAllowed)
	}
}

// seat_state_shape: every combination outside the three legal shapes is rejected.
func TestSeatStateShape(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	o := f.order(ctx, 0)
	hold := uuid.New()
	until := time.Now().Add(time.Minute)

	tests := []struct {
		name       string
		state      string
		holdID     *uuid.UUID
		heldUntil  *time.Time
		orderID    *uuid.UUID
		wantReject bool
	}{
		{"available_ok", "AVAILABLE", nil, nil, nil, false},
		{"held_ok", "HELD", &hold, &until, nil, false},
		{"sold_ok", "SOLD", nil, nil, &o.ID, false},

		{"available_with_hold_id", "AVAILABLE", &hold, nil, nil, true},
		{"available_with_held_until", "AVAILABLE", nil, &until, nil, true},
		{"available_with_order_id", "AVAILABLE", nil, nil, &o.ID, true},
		{"held_without_hold_id", "HELD", nil, &until, nil, true},
		{"held_without_held_until", "HELD", &hold, nil, nil, true},
		{"held_with_order_id", "HELD", &hold, &until, &o.ID, true},
		{"sold_without_order_id", "SOLD", nil, nil, nil, true},
		{"sold_with_hold_id", "SOLD", &hold, nil, &o.ID, true},
		{"sold_with_held_until", "SOLD", nil, &until, &o.ID, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Hand-written SQL: the generated queries only ever write legal
			// shapes; this test writes illegal ones on purpose.
			_, err := f.pool.Exec(ctx,
				`UPDATE seats SET state = $1::seat_state, hold_id = $2, held_until = $3, order_id = $4
				 WHERE event_id = $5 AND seat_no = 0`,
				tt.state, tt.holdID, tt.heldUntil, tt.orderID, f.eventID)
			if tt.wantReject && !db.IsCheckViolation(err, conSeatStateShape) {
				t.Fatalf("got %v, want %s violation", err, conSeatStateShape)
			}
			if !tt.wantReject && err != nil {
				t.Fatalf("legal shape rejected: %v", err)
			}
		})
	}
}

// One live (ACTIVE or PAYMENT_PENDING) hold per user per event.
func TestHoldsOneActivePerUser(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()

	tests := []struct {
		name        string
		firstStatus string // status the first hold is moved to before the second insert
		wantReject  bool
	}{
		{"second_while_active", "ACTIVE", true},
		{"second_while_payment_pending", "PAYMENT_PENDING", true},
		{"second_after_released", "RELEASED", false},
		{"second_after_expired", "EXPIRED", false},
		{"second_after_consumed", "CONSUMED", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := uuid.New()
			first := f.hold(ctx, user, 0)
			// Hand-written SQL: put the first hold straight into the state under test.
			if _, err := f.pool.Exec(ctx, `UPDATE holds SET status = $1::hold_status WHERE id = $2`, tt.firstStatus, first.ID); err != nil {
				t.Fatal(err)
			}
			_, err := f.q.InsertHold(ctx, gen.InsertHoldParams{
				ID: uuid.New(), EventID: f.eventID, UserID: user, SeatNos: []int32{0}, Fence: 2, ExpiresAt: time.Now().Add(time.Minute),
			})
			if tt.wantReject && !db.IsUniqueViolation(err, conHoldsOneActive) {
				t.Fatalf("got %v, want %s violation", err, conHoldsOneActive)
			}
			if !tt.wantReject && err != nil {
				t.Fatalf("got %v, want success", err)
			}
		})
	}

	t.Run("other_user_and_other_event_unaffected", func(t *testing.T) {
		user := uuid.New()
		f.hold(ctx, user, 0)
		f.hold(ctx, uuid.New(), 0) // another user, same event

		now := time.Now().UTC()
		if _, err := f.q.InsertEventIfAbsent(ctx, gen.InsertEventIfAbsentParams{
			Slug: "fixture-2", Name: "Fixture 2", Venue: "Test", StartsAt: now.Add(time.Hour),
			PrequeueOpensAt: now, SaleOpensAt: now, Capacity: 1, PricePaise: 1, MaxPerUser: 4,
			Status: gen.EventStatusONSALE, Layout: []byte(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
		other, err := f.q.GetEventBySlug(ctx, "fixture-2")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.q.InsertHold(ctx, gen.InsertHoldParams{ // same user, another event
			ID: uuid.New(), EventID: other.ID, UserID: user, SeatNos: []int32{0}, Fence: 4, ExpiresAt: now.Add(time.Minute),
		}); err != nil {
			t.Fatalf("same user, other event: %v", err)
		}
	})

	t.Run("concurrent_inserts_one_winner", func(t *testing.T) {
		user := uuid.New()
		var wins atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range workers {
			wg.Go(func() {
				<-start
				_, err := f.q.InsertHold(ctx, gen.InsertHoldParams{
					ID: uuid.New(), EventID: f.eventID, UserID: user, SeatNos: []int32{0}, Fence: 3, ExpiresAt: time.Now().Add(time.Minute),
				})
				switch {
				case err == nil:
					wins.Add(1)
				case db.IsUniqueViolation(err, conHoldsOneActive):
				default:
					t.Errorf("unexpected: %v", err)
				}
			})
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("%d live holds for one user, want 1", wins.Load())
		}
	})
}
