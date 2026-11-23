package db_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
)

var errPartialHold = errors.New("not every seat was available")

// holdAllOrNothing is the Postgres half of a hold: every seat moves
// AVAILABLE -> HELD or the transaction rolls back.
func holdAllOrNothing(ctx context.Context, f *fixture, hold uuid.UUID, seats []int32) error {
	return db.WithTx(ctx, f.pool, func(tx pgx.Tx) error {
		got, err := gen.New(tx).HoldSeats(ctx, gen.HoldSeatsParams{
			HoldID: hold, HeldUntil: time.Now().Add(time.Minute), EventID: f.eventID, SeatNos: seats,
		})
		if err != nil {
			return err
		}
		if len(got) != len(seats) {
			return errPartialHold
		}
		return nil
	})
}

func TestSeatTransitions(t *testing.T) {
	f := newFixture(t, 10)
	ctx := context.Background()
	h1, h2 := uuid.New(), uuid.New()

	if err := holdAllOrNothing(ctx, f, h1, []int32{1, 2}); err != nil {
		t.Fatalf("h1 hold: %v", err)
	}
	s := f.seat(ctx, 1)
	if s.State != gen.SeatStateHELD || s.HoldID == nil || *s.HoldID != h1 || s.Version != 1 {
		t.Fatalf("seat 1 after hold: %+v", s)
	}

	// Overlapping request: seat 3 is free but 2 is not, so nothing moves.
	if err := holdAllOrNothing(ctx, f, h2, []int32{2, 3}); !errors.Is(err, errPartialHold) {
		t.Fatalf("h2 overlapping hold: %v, want partial", err)
	}
	if s := f.seat(ctx, 3); s.State != gen.SeatStateAVAILABLE || s.Version != 0 {
		t.Fatalf("seat 3 must be untouched after rollback: %+v", s)
	}

	// I6 fencing: a stale holder cannot sell seats it no longer holds.
	o := f.order(ctx, 1, 2)
	n, err := f.q.SellSeats(ctx, gen.SellSeatsParams{OrderID: o.ID, EventID: f.eventID, SeatNos: []int32{1, 2}, HoldID: h2})
	if err != nil || n != 0 {
		t.Fatalf("sell with wrong hold: n=%d err=%v, want 0", n, err)
	}

	// The right hold extends, then sells.
	if n, err := f.q.ExtendSeatHold(ctx, gen.ExtendSeatHoldParams{HeldUntil: time.Now().Add(5 * time.Minute), EventID: f.eventID, HoldID: h1}); err != nil || n != 2 {
		t.Fatalf("extend: n=%d err=%v", n, err)
	}
	if n, err := f.q.SellSeats(ctx, gen.SellSeatsParams{OrderID: o.ID, EventID: f.eventID, SeatNos: []int32{1, 2}, HoldID: h1}); err != nil || n != 2 {
		t.Fatalf("sell: n=%d err=%v", n, err)
	}
	if s := f.seat(ctx, 2); s.State != gen.SeatStateSOLD || s.OrderID == nil || *s.OrderID != o.ID || s.HoldID != nil || s.Version != 3 {
		t.Fatalf("seat 2 after sell: %+v", s)
	}

	// SOLD seats are never released or re-held.
	if n, _ := f.q.ReleaseSeats(ctx, gen.ReleaseSeatsParams{EventID: f.eventID, HoldID: h1}); n != 0 {
		t.Fatalf("release after sell moved %d seats", n)
	}
	if err := holdAllOrNothing(ctx, f, h2, []int32{1}); !errors.Is(err, errPartialHold) {
		t.Fatalf("re-hold a sold seat: %v", err)
	}

	// Release returns HELD seats to AVAILABLE.
	h3 := uuid.New()
	if err := holdAllOrNothing(ctx, f, h3, []int32{5, 6, 7}); err != nil {
		t.Fatal(err)
	}
	if n, err := f.q.ReleaseSeats(ctx, gen.ReleaseSeatsParams{EventID: f.eventID, HoldID: h3}); err != nil || n != 3 {
		t.Fatalf("release: n=%d err=%v", n, err)
	}
	if s := f.seat(ctx, 6); s.State != gen.SeatStateAVAILABLE || s.HoldID != nil || s.HeldUntil != nil {
		t.Fatalf("seat 6 after release: %+v", s)
	}

	c, err := f.q.EventSeatCounts(ctx, f.eventID)
	if err != nil || c.Available != 8 || c.Held != 0 || c.Sold != 2 || c.Total != 10 {
		t.Fatalf("counts %+v err=%v", c, err)
	}
}

// Many goroutines race for overlapping multi-seat holds. Every seat ends up
// with at most one hold, every winner holds all of its seats, and every loser
// holds none (all-or-nothing under contention).
func TestHoldSeatsConcurrentAllOrNothing(t *testing.T) {
	const capacity = 20
	f := newFixture(t, capacity)
	ctx := context.Background()

	type attempt struct {
		hold  uuid.UUID
		seats []int32
		won   bool
	}
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // G404: deterministic test data
	attempts := make([]*attempt, 200)
	for i := range attempts {
		k := 1 + rng.IntN(4)
		seats := make([]int32, 0, k)
		for len(seats) < k {
			s := int32(rng.IntN(capacity)) //nolint:gosec // G115: < capacity
			if !slices.Contains(seats, s) {
				seats = append(seats, s)
			}
		}
		attempts[i] = &attempt{hold: uuid.New(), seats: seats}
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, a := range attempts {
		wg.Go(func() {
			<-start
			err := holdAllOrNothing(ctx, f, a.hold, a.seats)
			switch {
			case err == nil:
				a.won = true
			case errors.Is(err, errPartialHold):
			default:
				t.Errorf("hold: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()

	seats, err := f.q.ListSeatsByEvent(ctx, f.eventID)
	if err != nil {
		t.Fatal(err)
	}
	owner := map[int32]uuid.UUID{}
	for _, s := range seats {
		if s.State == gen.SeatStateHELD {
			owner[s.SeatNo] = *s.HoldID
		}
	}
	claimed := map[int32]uuid.UUID{}
	winners := 0
	for _, a := range attempts {
		for _, s := range a.seats {
			got, held := owner[s]
			switch {
			case a.won && (!held || got != a.hold):
				t.Fatalf("winner %s does not hold seat %d", a.hold, s)
			case !a.won && held && got == a.hold:
				t.Fatalf("loser %s holds seat %d", a.hold, s)
			}
			if a.won {
				if prev, dup := claimed[s]; dup {
					t.Fatalf("seat %d won by both %s and %s", s, prev, a.hold)
				}
				claimed[s] = a.hold
			}
		}
		if a.won {
			winners++
		}
	}
	if len(claimed) != len(owner) {
		t.Fatalf("%d seats held, but winners claimed %d", len(owner), len(claimed))
	}
	t.Logf("%d/%d holds won, %d/%d seats held", winners, len(attempts), len(owner), capacity)
}
