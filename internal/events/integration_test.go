package events_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/events"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/testutil/pgtest"
)

var pg *pgtest.Server

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m, &pg)) }

type totals struct{ events, seats int }

func count(t *testing.T, q *gen.Queries) totals {
	t.Helper()
	evs, err := q.ListEventsWithCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var tot totals
	for i := range evs {
		tot.events++
		tot.seats += int(evs[i].Available + evs[i].Held + evs[i].Sold)
	}
	return tot
}

func TestSeedIsIdempotent(t *testing.T) {
	pool, _ := pg.NewMigratedDB(t)
	ctx := context.Background()
	q := gen.New(pool)
	now := time.Date(2026, 11, 23, 4, 30, 0, 0, time.UTC)

	res, err := events.Seed(ctx, pool, events.DefaultSpecs(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || !res[0].Created || !res[1].Created || res[0].SeatsInserted != 500 || res[1].SeatsInserted != 500 {
		t.Fatalf("first seed: %+v", res)
	}
	if res[0].Status != gen.EventStatusSCHEDULED || !res[0].SaleOpensAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("tatkal should be SCHEDULED at now+10m: %+v", res[0])
	}
	if res[1].Status != gen.EventStatusONSALE || !res[1].SaleOpensAt.Equal(now) {
		t.Fatalf("quiet-tuesday should be ON_SALE now: %+v", res[1])
	}
	first := count(t, q)
	if first != (totals{2, 1000}) {
		t.Fatalf("after first seed: %+v", first)
	}

	// Second run, later and with a different --opens-in: nothing changes.
	res, err = events.Seed(ctx, pool, events.DefaultSpecs(), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Created || r.SeatsInserted != 0 || r.Seats != 500 {
			t.Fatalf("second seed changed something: %+v", r)
		}
	}
	if !res[0].SaleOpensAt.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("second seed re-timed the event: %v", res[0].SaleOpensAt)
	}
	if second := count(t, q); second != first {
		t.Fatalf("after second seed: %+v, want %+v", second, first)
	}

	// Seeds racing each other still end with one copy of everything.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := events.Seed(ctx, pool, events.DefaultSpecs(), now); err != nil {
				t.Errorf("concurrent seed: %v", err)
			}
		})
	}
	wg.Wait()
	if third := count(t, q); third != first {
		t.Fatalf("after concurrent seeds: %+v, want %+v", third, first)
	}

	// A missing seat is filled back in by a re-run.
	ev, err := q.GetEventBySlug(ctx, events.SlugTatkal)
	if err != nil {
		t.Fatal(err)
	}
	// Hand-written SQL: simulate a partially seeded event.
	if _, err := pool.Exec(ctx, `DELETE FROM seats WHERE event_id = $1 AND seat_no = 499`, ev.ID); err != nil {
		t.Fatal(err)
	}
	res, err = events.Seed(ctx, pool, events.DefaultSpecs()[:1], now)
	if err != nil || res[0].SeatsInserted != 1 || res[0].Seats != 500 {
		t.Fatalf("gap fill: %+v %v", res, err)
	}

	// A capacity mismatch on an existing event is refused, not applied.
	sp := events.DefaultSpecs()[:1]
	sp[0].Capacity = 600
	if _, err := events.Seed(ctx, pool, sp, now); err == nil {
		t.Fatal("capacity change on an existing event should fail")
	}
	if c := count(t, q); c != first {
		t.Fatalf("after refused capacity change: %+v", c)
	}
}

func TestSeedLargeCustomEvent(t *testing.T) {
	pool, _ := pg.NewMigratedDB(t)
	ctx := context.Background()
	specs, err := events.SeedOptions{Event: "loadtest-big", Capacity: 50000, AllowCapacity: true}.Specs()
	if err != nil {
		t.Fatal(err)
	}
	res, err := events.Seed(ctx, pool, specs, time.Now())
	if err != nil || res[0].Seats != 50000 || res[0].SeatsInserted != 50000 {
		t.Fatalf("%+v %v", res, err)
	}
	d, err := events.NewStore(gen.New(pool)).Get(ctx, "loadtest-big")
	if err != nil {
		t.Fatal(err)
	}
	var l events.Layout
	if err := json.Unmarshal(d.Layout, &l); err != nil || len(l.Coaches) != 1000 || d.Counts.Available != 50000 {
		t.Fatalf("layout coaches=%d counts=%+v err=%v", len(l.Coaches), d.Counts, err)
	}
}

func TestEventEndpoints(t *testing.T) {
	pool, _ := pg.NewMigratedDB(t)
	ctx := context.Background()
	if _, err := events.Seed(ctx, pool, events.DefaultSpecs(), time.Now()); err != nil {
		t.Fatal(err)
	}
	rt := httpx.NewRouter()
	events.RegisterRoutes(rt, events.NewStore(gen.New(pool)), slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(rt)
	defer srv.Close()

	get := func(path string, into any) int {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, http.NoBody)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if into != nil {
			if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
		}
		return resp.StatusCode
	}

	var list events.ListResponse
	if code := get("/api/v1/events", &list); code != http.StatusOK || len(list.Events) != 2 {
		t.Fatalf("list: %d %+v", code, list)
	}
	slugs := map[string]events.Event{}
	for _, e := range list.Events {
		slugs[e.Slug] = e
		if e.Counts != (events.Counts{Available: 500, Capacity: 500}) || e.Currency != "INR" || e.PricePaise != 125000 {
			t.Errorf("list entry %+v", e)
		}
	}
	tatkal, ok := slugs[events.SlugTatkal]
	if !ok || slugs[events.SlugQuiet].Status != "ON_SALE" || tatkal.Status != "SCHEDULED" {
		t.Fatalf("list slugs/status: %+v", slugs)
	}

	for _, key := range []string{events.SlugTatkal, tatkal.ID.String()} {
		var d events.EventDetail
		if code := get("/api/v1/events/"+key, &d); code != http.StatusOK {
			t.Fatalf("get %s: %d", key, code)
		}
		var l events.Layout
		if err := json.Unmarshal(d.Layout, &l); err != nil {
			t.Fatal(err)
		}
		if d.ID != tatkal.ID || len(l.Coaches) != 10 || l.Capacity() != 500 {
			t.Fatalf("get %s: id=%s coaches=%d capacity=%d", key, d.ID, len(l.Coaches), l.Capacity())
		}
		for _, c := range l.Coaches {
			if c.Seats != 50 || c.Rows != 10 || l.Cols != 5 || l.AisleAfterCol != 3 {
				t.Fatalf("coach %+v", c)
			}
		}
	}

	for _, missing := range []string{"no-such-event", "0193a3a0-0000-7000-8000-000000000000", "Bad%20Slug"} {
		var p map[string]any
		if code := get("/api/v1/events/"+missing, &p); code != http.StatusNotFound || p["code"] != "NOT_FOUND" {
			t.Fatalf("get %s: %d %v", missing, code, p)
		}
	}
}
