package inventory_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// hookRecorder is a ReclaimHook that records what it saw inside the
// reclaim transaction, and can be told to fail.
type hookRecorder struct {
	mu   sync.Mutex
	seen map[uuid.UUID]gen.HoldStatus
	fail error
}

func (r *hookRecorder) hook(ctx context.Context, tx pgx.Tx, holdID uuid.UUID) error {
	h, err := gen.New(tx).GetHold(ctx, holdID)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	if r.seen == nil {
		r.seen = map[uuid.UUID]gen.HoldStatus{}
	}
	r.seen[holdID] = h.Status
	return nil
}

// The sweeper calls the hook inside the reclaim transaction (the hold is
// already EXPIRED there), and a failing hook rolls the reclaim back: the
// hold and its seats stay as they were until the next sweep succeeds.
func TestReclaimHookSweeper(t *testing.T) {
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			rec := &hookRecorder{fail: errors.New("orders unavailable")}
			e := newEnv(t, pgOnly(m.pgOnly), ttl(200*time.Millisecond), capacity(10), onReclaim(rec.hook))
			h, err := e.hold(uuid.Must(uuid.NewV7()), newKey(), seatsReq(1, 2))
			mustNil(t, err)
			time.Sleep(300 * time.Millisecond)

			if _, err := e.svc.SweepOnce(e.ctx); err == nil {
				t.Fatal("sweep succeeded although the hook failed")
			}
			got, err := e.q.GetHold(e.ctx, h.ID)
			mustNil(t, err)
			if got.Status != gen.HoldStatusACTIVE {
				t.Fatalf("hold %s after a failed hook, want ACTIVE (rolled back)", got.Status)
			}
			if n := e.countSeats(gen.SeatStateHELD); n != 2 {
				t.Fatalf("%d seats HELD after a failed hook, want 2", n)
			}

			rec.mu.Lock()
			rec.fail = nil
			rec.mu.Unlock()
			res, err := e.svc.SweepOnce(e.ctx)
			mustNil(t, err)
			if res.Reclaimed != 1 {
				t.Fatalf("reclaimed %d", res.Reclaimed)
			}
			if st, ok := rec.seen[h.ID]; !ok || st != gen.HoldStatusEXPIRED {
				t.Fatalf("hook saw %v (called=%v), want EXPIRED inside the transaction", st, ok)
			}
			if n := e.countSeats(gen.SeatStateAVAILABLE); n != 10 {
				t.Fatalf("%d seats available", n)
			}
		})
	}
}

// Takeover of an expired, not yet swept hold calls the hook for the old
// hold in the new hold's transaction.
func TestReclaimHookTakeover(t *testing.T) {
	rec := &hookRecorder{}
	e := newEnv(t, pgOnly(true), ttl(200*time.Millisecond), capacity(10), onReclaim(rec.hook))
	old, err := e.hold(uuid.Must(uuid.NewV7()), newKey(), seatsReq(3))
	mustNil(t, err)
	time.Sleep(300 * time.Millisecond)
	nh, err := e.hold(uuid.Must(uuid.NewV7()), newKey(), seatsReq(3, 4))
	mustNil(t, err)
	if st, ok := rec.seen[old.ID]; !ok || st != gen.HoldStatusEXPIRED {
		t.Fatalf("hook for the taken-over hold: %v (called=%v)", st, ok)
	}
	if _, ok := rec.seen[nh.ID]; ok {
		t.Fatal("hook called for the new hold")
	}
}

// RepairDrift puts Redis back in line with Postgres: a seat missing from
// avail, a stray sold seat and a stray avail seat are repaired; a second
// run finds nothing.
func TestRepairDrift(t *testing.T) {
	e := newEnv(t, capacity(10))
	_, err := e.hold(uuid.Must(uuid.NewV7()), newKey(), seatsReq(5))
	mustNil(t, err)
	avail, sold := keys.Avail(e.eventID), keys.Sold(e.eventID)
	n, err := e.svc.RepairDrift(e.ctx, e.eventID)
	mustNil(t, err)
	if n != 0 {
		t.Fatalf("clean inventory: %d repairs", n)
	}
	mustNil(t, e.rdb.SRem(e.ctx, avail, "1").Err()) // lost an available seat
	mustNil(t, e.rdb.SAdd(e.ctx, sold, "2").Err())  // phantom sale
	mustNil(t, e.rdb.SAdd(e.ctx, avail, "5").Err()) // held seat shown as free
	n, err = e.svc.RepairDrift(e.ctx, e.eventID)
	mustNil(t, err)
	if n == 0 {
		t.Fatal("drift not detected")
	}
	for _, tc := range []struct {
		key  string
		seat int
		want bool
	}{{avail, 1, true}, {sold, 2, false}, {avail, 2, true}, {avail, 5, false}} {
		ok, err := e.rdb.SIsMember(e.ctx, tc.key, strconv.Itoa(tc.seat)).Result()
		mustNil(t, err)
		if ok != tc.want {
			t.Fatalf("seat %d in %s = %v, want %v", tc.seat, tc.key, ok, tc.want)
		}
	}
	n, err = e.svc.RepairDrift(e.ctx, e.eventID)
	mustNil(t, err)
	if n != 0 {
		t.Fatalf("second run: %d repairs", n)
	}
	mfs, err := e.reg.Gather()
	mustNil(t, err)
	counted := 0.0
	for _, mf := range mfs {
		if mf.GetName() == "inventory_drift_repairs_total" {
			for _, m := range mf.GetMetric() {
				counted += m.GetCounter().GetValue()
			}
		}
	}
	if counted == 0 {
		t.Fatal("inventory_drift_repairs_total not counted")
	}
}
