package db_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/orders"
)

// Every object 0001_init.sql creates, checked present after Up and gone after Down.
var schemaObjects = []string{
	"events", "seats", "holds", "orders", "payments", "tickets", "event_user_quota",
	"idempotency_keys", "webhook_events", "queue_snapshots", "hold_fence_seq",
	"seats_hold_idx", "holds_expiry_idx", "holds_one_active_per_user", "orders_user_idx",
	"payments_one_live_per_order", "idempotency_gc_idx",
}

var schemaTypes = []string{"event_status", "seat_state", "hold_status", "order_status", "payment_status"}

func TestMigrationsUpDownUp(t *testing.T) {
	pool, _ := pg.NewDB(t)
	ctx := context.Background()
	m, err := db.NewMigrator(pool, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	if err := m.Check(ctx); err == nil {
		t.Fatal("Check on an empty database should fail")
	}

	res, err := m.Up(ctx)
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !slices.Equal(res.GooseApplied, []int64{1, 2}) || res.GooseVersion != 2 || len(res.RiverApplied) == 0 {
		t.Fatalf("first up: %+v", res)
	}
	assertSchema(t, pool, true)
	if err := m.Check(ctx); err != nil {
		t.Fatalf("Check after up: %v", err)
	}

	// Running migrate twice is safe and does nothing.
	res, err = m.Up(ctx)
	if err != nil || len(res.GooseApplied) != 0 || len(res.RiverApplied) != 0 || res.GooseVersion != 2 {
		t.Fatalf("second up: %+v, %v", res, err)
	}

	if err := m.DownAll(ctx); err != nil {
		t.Fatalf("down: %v", err)
	}
	assertSchema(t, pool, false)
	if err := m.Check(ctx); !errors.Is(err, db.ErrMigrationsPending) {
		t.Fatalf("Check after down: %v, want ErrMigrationsPending", err)
	}

	res, err = m.Up(ctx)
	if err != nil || !slices.Equal(res.GooseApplied, []int64{1, 2}) {
		t.Fatalf("up again: %+v, %v", res, err)
	}
	assertSchema(t, pool, true)
	if err := m.Check(ctx); err != nil {
		t.Fatalf("Check after up again: %v", err)
	}
}

func assertSchema(t *testing.T, pool *pgxpool.Pool, present bool) {
	t.Helper()
	ctx := context.Background()
	for _, obj := range schemaObjects {
		var exists bool
		// Hand-written SQL: catalogue lookups are test-only and not in sqlc.
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, obj).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != present {
			t.Errorf("relation %s exists=%v, want %v", obj, exists, present)
		}
	}
	for _, typ := range schemaTypes {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regtype('public.' || $1) IS NOT NULL`, typ).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != present {
			t.Errorf("type %s exists=%v, want %v", typ, exists, present)
		}
	}
}

// The domain state machines must declare exactly the Postgres enum values.
func TestDomainStatesMatchPostgresEnums(t *testing.T) {
	check := func(name string, domain, pg []string) {
		t.Helper()
		if !slices.Equal(domain, pg) {
			t.Errorf("%s: domain %v, postgres enum %v", name, domain, pg)
		}
	}
	check("seat_state", strs(inventory.SeatMachine.States()), strs(gen.AllSeatStateValues()))
	check("hold_status", strs(inventory.HoldMachine.States()), strs(gen.AllHoldStatusValues()))
	check("order_status", strs(orders.OrderMachine.States()), strs(gen.AllOrderStatusValues()))
	check("payment_status", strs(orders.PaymentMachine.States()), strs(gen.AllPaymentStatusValues()))
}

func strs[S ~string](in []S) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}
