package inventory_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

type outcome struct {
	h   inventory.Hold
	err error
}

// TestRaceSingleSeat: 2,000 distinct users hold seat 7 at once. Exactly one
// wins; PG has one HELD seat and one hold row (I1 at the hold stage).
func TestRaceSingleSeat(t *testing.T) {
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			e := newEnv(t, pgOnly(m.pgOnly))
			const n = 2000
			res := race(n, func(int) outcome {
				h, err := e.hold(uuid.New(), newKey(), seatsReq(7))
				return outcome{h, err}
			})
			var ok, taken int
			var winner inventory.Hold
			for _, r := range res {
				var te *inventory.SeatsTakenError
				switch {
				case r.err == nil:
					ok++
					winner = r.h
				case errors.As(r.err, &te) && slices.Equal(te.Taken, []int32{7}):
					taken++
				default:
					t.Fatalf("unexpected outcome: %v", r.err)
				}
			}
			if ok != 1 || taken != n-1 {
				t.Fatalf("ok=%d taken=%d, want 1 and %d", ok, taken, n-1)
			}
			seats := e.pgSeats()
			if seats[7].State != gen.SeatStateHELD || *seats[7].HoldID != winner.ID {
				t.Errorf("seat 7 = %s held by %v, want HELD by %s", seats[7].State, seats[7].HoldID, winner.ID)
			}
			if got := e.countSeats(gen.SeatStateHELD); got != 1 {
				t.Errorf("HELD seats = %d, want 1", got)
			}
			if got := e.countHolds(gen.HoldStatusACTIVE); got != 1 {
				t.Errorf("hold rows = %d, want 1", got)
			}
			if !m.pgOnly {
				if e.inAvail(7) {
					t.Error("seat 7 still in redis avail")
				}
				if n, _ := e.rdb.ZCard(e.ctx, keys.Holds(e.eventID)).Result(); n != 1 {
					t.Errorf("redis holds = %d, want 1", n)
				}
				if c := metric(e, "holds_compensations_total"); c != 0 {
					t.Errorf("compensations = %v, want 0", c)
				}
			}
		})
	}
}

// TestRaceBestAvailable: 10,000 users ask for 1 seat each on a 500-seat event.
// Exactly 500 win, no seat is in two holds, and nothing is left.
func TestRaceBestAvailable(t *testing.T) {
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			const capacityN, users = 500, 10_000
			e := newEnv(t, pgOnly(m.pgOnly), capacity(capacityN))
			res := race(users, func(int) outcome {
				h, err := e.hold(uuid.New(), newKey(), qtyReq(1))
				return outcome{h, err}
			})
			owner := map[int32]uuid.UUID{}
			var ok, insufficient int
			for _, r := range res {
				switch {
				case r.err == nil:
					ok++
					if len(r.h.SeatNos) != 1 {
						t.Fatalf("hold has %d seats", len(r.h.SeatNos))
					}
					s := r.h.SeatNos[0]
					if prev, dup := owner[s]; dup {
						t.Fatalf("seat %d in two holds: %s and %s", s, prev, r.h.ID)
					}
					owner[s] = r.h.ID
				case errors.Is(r.err, inventory.ErrInsufficient):
					insufficient++
				default:
					t.Fatalf("unexpected outcome: %v", r.err)
				}
			}
			if ok != capacityN || insufficient != users-capacityN {
				t.Fatalf("ok=%d insufficient=%d, want %d and %d", ok, insufficient, capacityN, users-capacityN)
			}
			for s, r := range e.pgSeats() {
				if r.State != gen.SeatStateHELD || *r.HoldID != owner[s] {
					t.Fatalf("PG seat %d = %s/%v, want HELD by %s", s, r.State, r.HoldID, owner[s])
				}
			}
			if got := e.countHolds(gen.HoldStatusACTIVE); got != capacityN {
				t.Errorf("hold rows = %d, want %d", got, capacityN)
			}
			free, err := e.q.CountFreeSeats(e.ctx, gen.CountFreeSeatsParams{EventID: e.eventID, Now: time.Now()})
			mustNil(t, err)
			if free != 0 {
				t.Errorf("PG free seats = %d, want 0", free)
			}
			if !m.pgOnly {
				if n, _ := e.rdb.SCard(e.ctx, keys.Avail(e.eventID)).Result(); n != 0 {
					t.Errorf("redis avail = %d, want 0", n)
				}
			}
		})
	}
}

// TestMultiSeatAllOrNothing: [1,2,3] where 2 is taken -> TAKEN [2]; 1 and 3
// stay available in PG and in Redis.
func TestMultiSeatAllOrNothing(t *testing.T) {
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			e := newEnv(t, pgOnly(m.pgOnly))
			_, err := e.hold(uuid.New(), newKey(), seatsReq(2))
			mustNil(t, err)
			_, err = e.hold(uuid.New(), newKey(), seatsReq(3, 1, 2))
			var te *inventory.SeatsTakenError
			if !errors.As(err, &te) || !slices.Equal(te.Taken, []int32{2}) {
				t.Fatalf("err = %v, want SEATS_TAKEN [2]", err)
			}
			seats := e.pgSeats()
			for _, s := range []int32{1, 3} {
				if seats[s].State != gen.SeatStateAVAILABLE {
					t.Errorf("PG seat %d = %s, want AVAILABLE", s, seats[s].State)
				}
				if !m.pgOnly && !e.inAvail(s) {
					t.Errorf("redis seat %d not available", s)
				}
			}
			if got := e.countHolds(gen.HoldStatusACTIVE); got != 1 {
				t.Errorf("active holds = %d, want 1", got)
			}
		})
	}
}

// TestOneActiveHoldPerUser: a second hold by the same user is HOLD_LIMIT, in
// Redis and in PG. In redis_pg mode the PG unique index also catches a user
// that Redis forgot (drift): the grant is compensated.
func TestOneActiveHoldPerUser(t *testing.T) {
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			e := newEnv(t, pgOnly(m.pgOnly))
			uid := uuid.New()
			first, err := e.hold(uid, newKey(), seatsReq(1))
			mustNil(t, err)
			if _, err := e.hold(uid, newKey(), seatsReq(2)); !errors.Is(err, inventory.ErrHoldLimit) {
				t.Fatalf("second hold: err = %v, want HOLD_LIMIT", err)
			}
			if got := e.countHolds(gen.HoldStatusACTIVE); got != 1 {
				t.Errorf("active holds = %d, want 1", got)
			}
			if m.pgOnly {
				return
			}
			// Simulate Redis forgetting the user: only PG's index stops it now.
			mustNil(t, e.rdb.SRem(e.ctx, keys.Holding(e.eventID), uid.String()).Err())
			if _, err := e.hold(uid, newKey(), seatsReq(2)); !errors.Is(err, inventory.ErrHoldLimit) {
				t.Fatalf("drifted redis: err = %v, want HOLD_LIMIT from PG", err)
			}
			if !e.inAvail(2) {
				t.Error("compensation did not return seat 2 to redis avail")
			}
			if got := metric(e, "holds_compensations_total"); got != 1 {
				t.Errorf("compensations = %v, want 1", got)
			}
			if e.pgHold(first.ID).Status != gen.HoldStatusACTIVE {
				t.Error("first hold changed")
			}
		})
	}
}

// TestIdempotentHold: the same (user, key) gives the same hold, also when
// the two calls race; never two holds.
func TestIdempotentHold(t *testing.T) {
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			e := newEnv(t, pgOnly(m.pgOnly))
			uid, key := uuid.New(), newKey()
			a, err := e.hold(uid, key, qtyReq(2))
			mustNil(t, err)
			b, err := e.hold(uid, key, qtyReq(2))
			mustNil(t, err)
			if a.ID != b.ID || !slices.Equal(a.SeatNos, b.SeatNos) || a.Fence != b.Fence || !b.Replayed {
				t.Fatalf("replay differs: %+v vs %+v", a, b)
			}
			if a.ID != inventory.HoldID(uid, key) {
				t.Error("hold id is not UUIDv5(user:key)")
			}
			// 50 concurrent calls with one fresh key, on two seats the first
			// (random best-available) hold did not get: with fixed seats 10
			// and 11 this test failed ~8% of runs in redis_pg mode (SPOP).
			var free []int32
			for sn := int32(0); len(free) < 2; sn++ {
				if !slices.Contains(a.SeatNos, sn) {
					free = append(free, sn)
				}
			}
			uid2, key2 := uuid.New(), newKey()
			res := race(50, func(int) outcome {
				h, err := e.hold(uid2, key2, seatsReq(free...))
				return outcome{h, err}
			})
			for _, r := range res {
				if r.err != nil || r.h.ID != inventory.HoldID(uid2, key2) {
					t.Fatalf("concurrent same-key hold: %+v %v", r.h, r.err)
				}
			}
			if got := e.countHolds(gen.HoldStatusACTIVE); got != 2 {
				t.Errorf("active holds = %d, want 2", got)
			}
		})
	}
}

// TestPGTakeover: seat 8 is HELD in PG by a hold whose held_until passed,
// and Redis never heard of that hold (it was rebuilt). A new hold takes the
// seat over; the old hold is EXPIRED and its other seat freed in the same tx.
func TestPGTakeover(t *testing.T) {
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			e := newEnv(t, pgOnly(m.pgOnly))
			old := uuid.New()
			past := time.Now().Add(-time.Minute)
			_, err := e.q.InsertHold(e.ctx, gen.InsertHoldParams{ID: old, EventID: e.eventID, UserID: uuid.New(),
				SeatNos: []int32{8, 9}, Fence: 1, ExpiresAt: past})
			mustNil(t, err)
			rows, err := e.q.TakeSeats(e.ctx, gen.TakeSeatsParams{HoldID: old, HeldUntil: past, EventID: e.eventID,
				Now: time.Now(), SeatNos: []int32{8, 9}})
			mustNil(t, err)
			if len(rows) != 2 {
				t.Fatal("setup: seats not held")
			}
			if !m.pgOnly {
				// Redis rebuilt from PG: 8 and 9 are reclaimable, so in avail,
				// and the expired hold is not loaded. Rebuild would reclaim it
				// first, so load the state directly to keep the PG hold live.
				mustNil(t, e.rdb.SAdd(e.ctx, keys.Avail(e.eventID), 8, 9).Err())
				if e.redisHoldExists(old) {
					t.Fatal("setup: old hold present in redis")
				}
			}
			h, err := e.hold(uuid.New(), newKey(), seatsReq(8))
			mustNil(t, err)
			if got := e.pgHold(old).Status; got != gen.HoldStatusEXPIRED {
				t.Errorf("old hold = %s, want EXPIRED", got)
			}
			seats := e.pgSeats()
			if seats[8].State != gen.SeatStateHELD || *seats[8].HoldID != h.ID {
				t.Errorf("seat 8 = %s/%v, want HELD by new hold", seats[8].State, seats[8].HoldID)
			}
			if seats[9].State != gen.SeatStateAVAILABLE {
				t.Errorf("seat 9 = %s, want AVAILABLE", seats[9].State)
			}
			if !m.pgOnly {
				if e.inAvail(8) || !e.inAvail(9) {
					t.Errorf("redis: 8 avail=%v (want false), 9 avail=%v (want true)", e.inAvail(8), e.inAvail(9))
				}
			}
		})
	}
}

// TestCompensation: PG conflicts after the Redis grant (seat 3 was held in
// PG behind Redis's back). The grant is undone in Redis, seat 3 is repaired
// out of avail, and the caller gets SEATS_TAKEN [3].
func TestCompensation(t *testing.T) {
	e := newEnv(t)
	other := uuid.New()
	_, err := e.q.InsertHold(e.ctx, gen.InsertHoldParams{ID: other, EventID: e.eventID, UserID: uuid.New(),
		SeatNos: []int32{3}, Fence: 999, ExpiresAt: time.Now().Add(time.Hour)})
	mustNil(t, err)
	_, err = e.q.TakeSeats(e.ctx, gen.TakeSeatsParams{HoldID: other, HeldUntil: time.Now().Add(time.Hour),
		EventID: e.eventID, Now: time.Now(), SeatNos: []int32{3}})
	mustNil(t, err)
	if !e.inAvail(3) {
		t.Fatal("setup: redis should still think seat 3 is free")
	}

	uid, key := uuid.New(), newKey()
	_, err = e.hold(uid, key, seatsReq(3, 4))
	var te *inventory.SeatsTakenError
	if !errors.As(err, &te) || !slices.Equal(te.Taken, []int32{3}) {
		t.Fatalf("err = %v, want SEATS_TAKEN [3]", err)
	}
	hid := inventory.HoldID(uid, key)
	if e.redisHoldExists(hid) {
		t.Error("redis hold survived compensation")
	}
	if ok, _ := e.rdb.SIsMember(e.ctx, keys.Holding(e.eventID), uid.String()).Result(); ok {
		t.Error("user still in redis holding")
	}
	if !e.inAvail(4) {
		t.Error("seat 4 not returned to redis avail")
	}
	if e.inAvail(3) {
		t.Error("seat 3 not repaired out of redis avail")
	}
	if got := metric(e, "holds_compensations_total"); got != 1 {
		t.Errorf("compensations = %v, want 1", got)
	}
	if got := e.countHolds(gen.HoldStatusACTIVE); got != 1 {
		t.Errorf("active holds = %d, want only the injected one", got)
	}
	// The user can still hold free seats.
	if _, err := e.hold(uid, newKey(), seatsReq(4)); err != nil {
		t.Errorf("hold after compensation: %v", err)
	}
}

// TestQuotaPreCheck: a user who already bought max_per_user seats gets
// QUOTA_EXCEEDED, and a Redis grant is undone.
func TestQuotaPreCheck(t *testing.T) {
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			e := newEnv(t, pgOnly(m.pgOnly))
			uid := uuid.New()
			_, err := e.q.AddQuota(e.ctx, gen.AddQuotaParams{EventID: e.eventID, UserID: uid, Seats: 3, MaxAllowed: 4})
			mustNil(t, err)
			if _, err := e.hold(uid, newKey(), seatsReq(1, 2)); !errors.Is(err, inventory.ErrQuotaExceeded) {
				t.Fatalf("err = %v, want QUOTA_EXCEEDED", err)
			}
			if !m.pgOnly && (!e.inAvail(1) || !e.inAvail(2)) {
				t.Error("redis grant not undone")
			}
			if _, err := e.hold(uid, newKey(), seatsReq(1)); err != nil {
				t.Errorf("within quota: %v", err)
			}
		})
	}
}

func TestHoldValidation(t *testing.T) {
	e := newEnv(t, pgOnly(true), capacity(10))
	tests := []struct {
		name string
		req  inventory.Request
	}{
		{"empty", inventory.Request{}},
		{"both", inventory.Request{SeatNos: []int32{1}, Quantity: 1}},
		{"too many", qtyReq(5)},
		{"out of range", seatsReq(10)},
		{"negative", seatsReq(-1)},
		{"duplicate", seatsReq(1, 1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.hold(uuid.New(), newKey(), tc.req); !errors.Is(err, inventory.ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
	if _, err := e.svc.Hold(e.ctx, uuid.New(), uuid.New(), newKey(), qtyReq(1)); !errors.Is(err, inventory.ErrEventNotFound) {
		t.Errorf("unknown event: %v", err)
	}
}

// TestReleaseExtendConsume covers the hold lifecycle calls P3 builds on.
func TestReleaseExtendConsume(t *testing.T) {
	e := newEnv(t)
	uid := uuid.New()
	h, err := e.hold(uid, newKey(), seatsReq(5, 6))
	mustNil(t, err)

	if _, err := e.svc.Release(e.ctx, h.ID, uuid.New()); !errors.Is(err, inventory.ErrHoldNotFound) {
		t.Errorf("release by another user: %v, want not found", err)
	}
	r, err := e.svc.Release(e.ctx, h.ID, uid)
	mustNil(t, err)
	if r.Status != inventory.HoldReleased || e.countSeats(gen.SeatStateHELD) != 0 || !e.inAvail(5) || !e.inAvail(6) || e.redisHoldExists(h.ID) {
		t.Fatalf("after release: %+v, held=%d", r, e.countSeats(gen.SeatStateHELD))
	}
	if _, err := e.svc.Release(e.ctx, h.ID, uid); err != nil {
		t.Errorf("second release: %v, want no-op", err)
	}

	h2, err := e.hold(uid, newKey(), seatsReq(5))
	mustNil(t, err)
	x, err := e.svc.Extend(e.ctx, h2.ID, 10*time.Minute)
	mustNil(t, err)
	if x.Status != inventory.HoldPaymentPending || !x.ExpiresAt.After(h2.ExpiresAt) {
		t.Errorf("extend: %+v", x)
	}
	if st, _ := e.rdb.HGet(e.ctx, keys.Hold(e.eventID, h2.ID), "state").Result(); st != "PAYMENT" {
		t.Errorf("redis state = %q, want PAYMENT", st)
	}
	if s := e.pgSeats()[5]; !s.HeldUntil.Equal(x.ExpiresAt) {
		t.Errorf("seat held_until %v, want %v", s.HeldUntil, x.ExpiresAt)
	}
	if _, err := e.svc.Release(e.ctx, h2.ID, uid); !errors.Is(err, inventory.ErrHoldState) {
		t.Errorf("release PAYMENT_PENDING: %v, want ErrHoldState", err)
	}
	if _, err := e.svc.Extend(e.ctx, h2.ID, time.Minute); !errors.Is(err, inventory.ErrHoldState) {
		t.Errorf("second extend: %v, want ErrHoldState", err)
	}

	// P3's confirm tx consumes the hold in PG; Consume mirrors it in Redis.
	_, err = e.q.ConsumeHold(e.ctx, h2.ID)
	mustNil(t, err)
	mustNil(t, e.svc.Consume(e.ctx, e.eventID, h2.ID))
	mustNil(t, e.svc.Consume(e.ctx, e.eventID, h2.ID)) // idempotent
	if got := e.redisSet(keys.Sold(e.eventID)); !slices.Equal(got, []int32{5}) {
		t.Errorf("redis sold = %v, want [5]", got)
	}
}

// metric sums a counter or gauge family from the env's registry.
func metric(e *env, name string) float64 {
	e.t.Helper()
	mfs, err := e.reg.Gather()
	mustNil(e.t, err)
	var v float64
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			v += m.GetCounter().GetValue() + m.GetGauge().GetValue()
		}
	}
	return v
}
