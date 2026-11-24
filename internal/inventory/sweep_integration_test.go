package inventory_test

import (
	"context"
	"encoding/base64"
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// TestExpirySweeper (I5): with HOLD_TTL 2 s the running sweeper returns the
// seats within 2 intervals of expires_at, the hold is EXPIRED, and a second
// user can then hold the same seats.
func TestExpirySweeper(t *testing.T) {
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			const interval = time.Second
			e := newEnv(t, pgOnly(m.pgOnly), ttl(2*time.Second))
			ctx, cancel := context.WithCancel(e.ctx)
			done := make(chan error, 1)
			go func() { done <- e.svc.RunSweeper(ctx, interval) }()
			defer func() {
				cancel()
				mustNil(t, <-done)
			}()

			h, err := e.hold(uuid.New(), newKey(), seatsReq(5, 6))
			mustNil(t, err)
			var reclaimedAt time.Time
			for deadline := h.ExpiresAt.Add(3 * interval); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				if e.pgHold(h.ID).Status == gen.HoldStatusEXPIRED {
					reclaimedAt = time.Now()
					break
				}
			}
			if reclaimedAt.IsZero() {
				t.Fatalf("hold not reclaimed within 3 intervals of expiry")
			}
			lag := reclaimedAt.Sub(h.ExpiresAt)
			t.Logf("reclaimed %v after expires_at (interval %v)", lag.Round(time.Millisecond), interval)
			if lag > 2*interval {
				t.Errorf("reclaim lag %v > 2 intervals", lag)
			}
			seats := e.pgSeats()
			if seats[5].State != gen.SeatStateAVAILABLE || seats[6].State != gen.SeatStateAVAILABLE {
				t.Errorf("seats = %s/%s, want AVAILABLE", seats[5].State, seats[6].State)
			}
			if !m.pgOnly && (e.redisHoldExists(h.ID) || !e.inAvail(5) || !e.inAvail(6)) {
				t.Error("redis not released after PG reclaim")
			}
			if got := metric(e, "holds_expired_total"); got != 1 {
				t.Errorf("holds_expired_total = %v, want 1", got)
			}
			if _, err := e.hold(uuid.New(), newKey(), seatsReq(5, 6)); err != nil {
				t.Errorf("second hold on reclaimed seats: %v", err)
			}
		})
	}
}

// TestSweeperRedisPass: Redis entries whose exp passed are resolved with PG
// as the judge: an orphan grant (no PG row) is released, a hold extended in
// PG is re-extended in Redis, a consumed hold is consumed.
func TestSweeperRedisPass(t *testing.T) {
	e := newEnv(t)
	past := time.Now().Add(-time.Minute)

	orphan := uuid.New()
	r, err := e.sc.Run(e.ctx, redisx.HoldSeats,
		[]string{keys.Meta(e.eventID), keys.Avail(e.eventID), keys.Holds(e.eventID), keys.Holding(e.eventID),
			keys.Fence(e.eventID), keys.Ver(e.eventID), keys.QSoldOut(e.eventID), keys.Hold(e.eventID, orphan)},
		orphan.String(), uuid.NewString(), past.UnixMilli(), 1000, 1)
	mustNil(t, err)
	if r.Status != "OK" {
		t.Fatalf("setup orphan: %+v", r)
	}

	live, err := e.hold(uuid.New(), newKey(), seatsReq(2))
	mustNil(t, err)
	consumed, err := e.hold(uuid.New(), newKey(), seatsReq(3))
	mustNil(t, err)
	_, err = e.svc.Extend(e.ctx, consumed.ID, time.Hour)
	mustNil(t, err)
	_, err = e.q.SellSeats(e.ctx, gen.SellSeatsParams{OrderID: uuid.New(), EventID: e.eventID, SeatNos: []int32{3}, HoldID: consumed.ID})
	mustNil(t, err)
	_, err = e.q.ConsumeHold(e.ctx, consumed.ID)
	mustNil(t, err)
	// Make Redis think both are past their expiry.
	for _, id := range []uuid.UUID{live.ID, consumed.ID} {
		mustNil(t, e.rdb.ZAdd(e.ctx, keys.Holds(e.eventID), redisZ(past, id)).Err())
	}

	res, err := e.svc.SweepOnce(e.ctx)
	mustNil(t, err)
	if res.RedisReleased != 1 || res.RedisExtended != 1 || res.RedisConsumed != 1 || res.Reclaimed != 0 {
		t.Fatalf("sweep = %+v, want 1 released, 1 extended, 1 consumed", res)
	}
	if e.redisHoldExists(orphan) || !e.inAvail(1) {
		t.Error("orphan grant not released")
	}
	if score, _ := e.rdb.ZScore(e.ctx, keys.Holds(e.eventID), live.ID.String()).Result(); int64(score) != live.ExpiresAt.UnixMilli() {
		t.Errorf("live hold score %v, want PG expiry %d", score, live.ExpiresAt.UnixMilli())
	}
	if got := e.redisSet(keys.Sold(e.eventID)); !slices.Equal(got, []int32{3}) {
		t.Errorf("sold = %v, want [3]", got)
	}
}

// TestSweeperConcurrentWorkers: three sweepers over 100 expired holds reclaim
// each exactly once (SKIP LOCKED + conditional updates).
func TestSweeperConcurrentWorkers(t *testing.T) {
	e := newEnv(t, capacity(200))
	past := time.Now().Add(-time.Second)
	for i := range 100 {
		id := uuid.New()
		_, err := e.q.InsertHold(e.ctx, gen.InsertHoldParams{ID: id, EventID: e.eventID, UserID: uuid.New(),
			SeatNos: []int32{int32(i)}, Fence: int64(i + 1), ExpiresAt: past}) //nolint:gosec // test sizes
		mustNil(t, err)
		_, err = e.q.TakeSeats(e.ctx, gen.TakeSeatsParams{HoldID: id, HeldUntil: past, EventID: e.eventID,
			Now: time.Now(), SeatNos: []int32{int32(i)}}) //nolint:gosec // test sizes
		mustNil(t, err)
	}
	res := race(3, func(int) inventory.SweepResult {
		r, err := e.svc.SweepOnce(e.ctx)
		if err != nil {
			t.Error(err)
		}
		return r
	})
	total := 0
	for _, r := range res {
		total += r.Reclaimed
	}
	if total != 100 {
		t.Errorf("reclaimed %d in total, want exactly 100", total)
	}
	if got := e.countHolds(gen.HoldStatusEXPIRED); got != 100 {
		t.Errorf("EXPIRED holds = %d, want 100", got)
	}
	if got := e.countSeats(gen.SeatStateAVAILABLE); got != 200 {
		t.Errorf("AVAILABLE seats = %d, want 200", got)
	}
}

// TestRebuildAfterFlushAll (R1): 300 holds (some released, extended or
// sold), FLUSHALL, rebuild: Redis matches PG seat by seat, hold by hold,
// and the fence keeps growing.
func TestRebuildAfterFlushAll(t *testing.T) {
	const capacityN = 500
	e := newEnv(t, capacity(capacityN))
	res := race(300, func(i int) outcome {
		h, err := e.hold(uuid.New(), newKey(), qtyReq(1+i%2))
		return outcome{h, err}
	})
	var holds []inventory.Hold
	for _, r := range res {
		mustNil(t, r.err)
		holds = append(holds, r.h)
	}
	for _, h := range holds[:50] {
		_, err := e.svc.Release(e.ctx, h.ID, h.UserID)
		mustNil(t, err)
	}
	for _, h := range holds[50:75] {
		_, err := e.svc.Extend(e.ctx, h.ID, time.Hour)
		mustNil(t, err)
	}
	for _, h := range holds[50:60] { // sold through P3's confirm path, simulated
		_, err := e.q.SellSeats(e.ctx, gen.SellSeatsParams{OrderID: uuid.New(), EventID: e.eventID, SeatNos: h.SeatNos, HoldID: h.ID})
		mustNil(t, err)
		_, err = e.q.ConsumeHold(e.ctx, h.ID)
		mustNil(t, err)
		mustNil(t, e.svc.Consume(e.ctx, e.eventID, h.ID, h.UserID))
	}
	pgView := e.pgSeatStates()
	before := e.redisSeatStates(capacityN)
	if !maps.Equal(before, pgView) {
		t.Fatalf("redis and PG disagree before the flush: %s", diffStates(before, pgView))
	}
	maxFence := int64(0)
	for _, h := range holds {
		maxFence = max(maxFence, h.Fence)
	}

	mustNil(t, e.rdb.FlushAll(e.ctx).Err())
	if ok, _ := e.svc.Loaded(e.ctx, e.eventID); ok {
		t.Fatal("still loaded after FLUSHALL")
	}
	rr, err := e.svc.Rebuild(e.ctx, e.eventID)
	mustNil(t, err)
	t.Logf("rebuild: %+v", rr)

	after := e.redisSeatStates(capacityN)
	if !maps.Equal(after, pgView) {
		t.Fatalf("redis and PG disagree after rebuild: %s", diffStates(after, pgView))
	}
	counts := map[string]int{}
	for _, st := range after {
		counts[st]++
	}
	if counts["S"] == 0 || counts["H"] == 0 || counts["A"] == 0 {
		t.Errorf("want all three states represented, got %v", counts)
	}

	live, err := e.q.ListLiveHoldsByEvent(e.ctx, e.eventID)
	mustNil(t, err)
	ids, err := e.rdb.ZRange(e.ctx, keys.Holds(e.eventID), 0, -1).Result()
	mustNil(t, err)
	if len(ids) != len(live) || len(live) != 300-50-10 {
		t.Fatalf("redis holds %d, PG live holds %d, want %d", len(ids), len(live), 240)
	}
	for _, h := range live {
		got, err := e.rdb.HGetAll(e.ctx, keys.Hold(e.eventID, h.ID)).Result()
		mustNil(t, err)
		state := "ACTIVE"
		if h.Status == gen.HoldStatusPAYMENTPENDING {
			state = "PAYMENT"
		}
		if got["uid"] != h.UserID.String() || got["fence"] != strconv.FormatInt(h.Fence, 10) || got["state"] != state ||
			got["exp_ms"] != strconv.FormatInt(h.ExpiresAt.UnixMilli(), 10) || got["seats"] != csv(h.SeatNos) {
			t.Fatalf("hold %s: redis %v, PG %+v", h.ID, got, h)
		}
		if ok, _ := e.rdb.SIsMember(e.ctx, keys.Holding(e.eventID), h.UserID.String()).Result(); !ok {
			t.Fatalf("user %s missing from holding", h.UserID)
		}
	}
	if n, _ := e.rdb.SCard(e.ctx, keys.Holding(e.eventID)).Result(); int(n) != len(live) {
		t.Errorf("holding = %d, want %d", n, len(live))
	}
	h, err := e.hold(uuid.New(), newKey(), qtyReq(1))
	mustNil(t, err)
	if h.Fence <= maxFence {
		t.Errorf("fence after rebuild %d, want > %d", h.Fence, maxFence)
	}
}

// TestEnsureLoadedOnDemand: after a flush the next hold rebuilds the event
// itself (NOT_LOADED -> rebuild -> retry) instead of saying "taken".
func TestEnsureLoadedOnDemand(t *testing.T) {
	e := newEnv(t)
	first, err := e.hold(uuid.New(), newKey(), seatsReq(1))
	mustNil(t, err)
	mustNil(t, e.rdb.FlushAll(e.ctx).Err())
	if _, err := e.hold(uuid.New(), newKey(), seatsReq(1)); !errorsIsTaken(err) {
		t.Fatalf("seat 1 after flush: %v, want SEATS_TAKEN (PG still holds it)", err)
	}
	if _, err := e.hold(uuid.New(), newKey(), seatsReq(2)); err != nil {
		t.Fatalf("seat 2 after flush: %v", err)
	}
	if !e.redisHoldExists(first.ID) {
		t.Error("rebuild did not restore the first hold")
	}
}

func TestAvailability(t *testing.T) {
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			e := newEnv(t, pgOnly(m.pgOnly), capacity(10))
			ev, err := e.svc.Event(e.ctx, e.slug)
			mustNil(t, err)
			cache := e.svc.NewAvailabilityCache(0)
			a1, err := cache.Get(e.ctx, ev)
			mustNil(t, err)
			if a1.Counts != (inventory.Counts{Available: 10, Capacity: 10}) {
				t.Fatalf("counts = %+v", a1.Counts)
			}
			h, err := e.hold(uuid.New(), newKey(), seatsReq(1, 6))
			mustNil(t, err)
			_, err = e.svc.Extend(e.ctx, h.ID, time.Hour)
			mustNil(t, err)
			_, err = e.q.SellSeats(e.ctx, gen.SellSeatsParams{OrderID: uuid.New(), EventID: e.eventID, SeatNos: []int32{6}, HoldID: h.ID})
			mustNil(t, err)
			mustNil(t, e.svc.Consume(e.ctx, e.eventID, h.ID, h.UserID))
			if !m.pgOnly {
				// Redis has 1 and 6 sold after Consume; keep 1 held to
				// mirror PG (6 sold, 1 still HELD).
				mustNil(t, e.rdb.SRem(e.ctx, keys.Sold(e.eventID), 1).Err())
			}
			a2, err := cache.Get(e.ctx, ev)
			mustNil(t, err)
			if a2.Counts != (inventory.Counts{Available: 8, Held: 1, Sold: 1, Capacity: 10}) || a2.ETag == a1.ETag {
				t.Fatalf("after hold: %+v etag %s (before %s)", a2.Counts, a2.ETag, a1.ETag)
			}
			bits, err := base64.StdEncoding.DecodeString(a2.States)
			mustNil(t, err)
			if len(bits) != 3 {
				t.Fatalf("bitmap %d bytes, want 3", len(bits))
			}
			for i := range 10 {
				want := byte(0)
				switch i {
				case 1:
					want = 1
				case 6:
					want = 2
				}
				if got := inventory.SeatCode(bits, i); got != want {
					t.Errorf("seat %d code %02b, want %02b", i, got, want)
				}
			}
			a3, err := cache.Get(e.ctx, ev)
			mustNil(t, err)
			if a3.ETag != a2.ETag {
				t.Errorf("etag changed without a seat change: %s -> %s", a2.ETag, a3.ETag)
			}
			// A long minAge serves the cached snapshot without re-reading.
			slow := e.svc.NewAvailabilityCache(time.Hour)
			s1, err := slow.Get(e.ctx, ev)
			mustNil(t, err)
			_, err = e.hold(uuid.New(), newKey(), seatsReq(2))
			mustNil(t, err)
			s2, err := slow.Get(e.ctx, ev)
			mustNil(t, err)
			if s1 != s2 {
				t.Error("cache refreshed before minAge")
			}
			if got := metric(e, "inventory_seats"); got != 10 {
				t.Errorf("inventory_seats sums to %v, want 10", got)
			}
		})
	}
}
