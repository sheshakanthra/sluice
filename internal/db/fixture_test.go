package db_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/testutil/pgtest"
)

var pg *pgtest.Server

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m, &pg)) }

// fixture is one migrated database with one event and its seats.
type fixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *gen.Queries
	eventID uuid.UUID
}

func newFixture(t *testing.T, capacity int) *fixture {
	t.Helper()
	pool, _ := pg.NewMigratedDB(t)
	f := &fixture{t: t, pool: pool, q: gen.New(pool)}
	ctx := context.Background()
	now := time.Now().UTC()
	n, err := f.q.InsertEventIfAbsent(ctx, gen.InsertEventIfAbsentParams{
		Slug: "fixture", Name: "Fixture", Venue: "Test", StartsAt: now.Add(24 * time.Hour),
		PrequeueOpensAt: now, SaleOpensAt: now, Capacity: int32(capacity), PricePaise: 125000, //nolint:gosec // G115: small test capacity
		MaxPerUser: 4, Status: gen.EventStatusONSALE, Layout: []byte(`{}`),
	})
	if err != nil || n != 1 {
		t.Fatalf("insert event: n=%d err=%v", n, err)
	}
	ev, err := f.q.GetEventBySlug(ctx, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	f.eventID = ev.ID
	p := gen.InsertSeatsParams{EventID: ev.ID}
	for i := range capacity {
		p.SeatNos = append(p.SeatNos, int32(i)) //nolint:gosec // G115: small test capacity
		p.Labels = append(p.Labels, fmt.Sprintf("S-%d", i))
		p.Sections = append(p.Sections, "S")
		p.RowNos = append(p.RowNos, 1)
		p.ColNos = append(p.ColNos, int32(i+1)) //nolint:gosec // G115: small test capacity
	}
	if _, err := f.q.InsertSeats(ctx, p); err != nil {
		t.Fatal(err)
	}
	return f
}

// hold inserts an ACTIVE hold (Postgres row only; seats untouched).
func (f *fixture) hold(ctx context.Context, user uuid.UUID, seats ...int32) gen.Hold {
	f.t.Helper()
	h, err := f.q.InsertHold(ctx, gen.InsertHoldParams{
		ID: uuid.New(), EventID: f.eventID, UserID: user, SeatNos: seats,
		Fence: 1, ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		f.t.Fatalf("insert hold: %v", err)
	}
	return h
}

// order inserts a PENDING_PAYMENT order for a fresh hold of a fresh user.
func (f *fixture) order(ctx context.Context, seats ...int32) gen.Order {
	f.t.Helper()
	user := uuid.New()
	h := f.hold(ctx, user, seats...)
	o, err := f.q.InsertOrder(ctx, gen.InsertOrderParams{
		EventID: f.eventID, UserID: user, HoldID: h.ID, SeatNos: seats, AmountPaise: 125000,
	})
	if err != nil {
		f.t.Fatalf("insert order: %v", err)
	}
	return o
}

func (f *fixture) seat(ctx context.Context, no int32) gen.Seat {
	f.t.Helper()
	s, err := f.q.GetSeatsByNos(ctx, gen.GetSeatsByNosParams{EventID: f.eventID, SeatNos: []int32{no}})
	if err != nil || len(s) != 1 {
		f.t.Fatalf("get seat %d: %v (%d rows)", no, err, len(s))
	}
	return s[0]
}
