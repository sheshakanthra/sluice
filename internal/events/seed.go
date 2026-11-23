package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
)

// Demo event slugs (DATA-MODEL section 7).
const (
	SlugTatkal = "tatkal-chn-sbc"
	SlugQuiet  = "quiet-tuesday"
)

// Seed defaults.
const (
	defaultCapacity   = 500
	defaultPricePaise = 125000 // Rs 1,250
	defaultMaxPerUser = 4
	tatkalOpensIn     = 10 * time.Minute
	eventDuration     = 24 * time.Hour // starts_at = sale_opens_at + 24h
	maxCapacity       = 1_000_000
)

var slugRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// ValidSlug reports whether s is a well-formed event slug (1-64 chars,
// lowercase letters, digits and single hyphens).
func ValidSlug(s string) bool { return len(s) <= 64 && slugRE.MatchString(s) }

// Spec is one event to seed.
type Spec struct {
	Slug       string
	Name       string
	Venue      string
	Capacity   int
	PricePaise int64
	MaxPerUser int
	OpensIn    time.Duration // sale_opens_at = now + OpensIn; <= 0 means on sale now
}

// DefaultSpecs returns the two demo events.
func DefaultSpecs() []Spec {
	return []Spec{
		{
			Slug: SlugTatkal, Name: "Demo Express, Chennai → Bengaluru (Tatkal drop)", Venue: "Chennai → Bengaluru",
			Capacity: defaultCapacity, PricePaise: defaultPricePaise, MaxPerUser: defaultMaxPerUser, OpensIn: tatkalOpensIn,
		},
		{
			Slug: SlugQuiet, Name: "Quiet Tuesday, Chennai → Bengaluru", Venue: "Chennai → Bengaluru",
			Capacity: defaultCapacity, PricePaise: defaultPricePaise, MaxPerUser: defaultMaxPerUser, OpensIn: 0,
		},
	}
}

// SeedOptions are the `sluice seed` flags.
type SeedOptions struct {
	Event    string         // --event: only this slug ("" = both demo events)
	Capacity int            // --capacity: rush/test only, requires Event (0 = default)
	OpensIn  *time.Duration // --opens-in: overrides every seeded event's default
	// AllowCapacity is true when APP_ENV is rush or test.
	AllowCapacity bool
}

// ErrCapacityNotAllowed is returned for --capacity outside rush/test.
var ErrCapacityNotAllowed = errors.New("--capacity is only allowed when APP_ENV is rush or test")

// Specs resolves the options into the events to seed.
func (o SeedOptions) Specs() ([]Spec, error) {
	if o.Capacity != 0 {
		if !o.AllowCapacity {
			return nil, ErrCapacityNotAllowed
		}
		if o.Event == "" {
			return nil, errors.New("--capacity needs --event")
		}
		if o.Capacity < 1 || o.Capacity > maxCapacity {
			return nil, fmt.Errorf("--capacity must be 1..%d, got %d", maxCapacity, o.Capacity)
		}
	}
	if o.OpensIn != nil && *o.OpensIn < 0 {
		return nil, fmt.Errorf("--opens-in must be >= 0, got %s", *o.OpensIn)
	}

	var specs []Spec
	if o.Event == "" {
		specs = DefaultSpecs()
	} else {
		if !ValidSlug(o.Event) {
			return nil, fmt.Errorf("--event %q is not a valid slug (lowercase letters, digits, hyphens)", o.Event)
		}
		for _, s := range DefaultSpecs() {
			if s.Slug == o.Event {
				specs = []Spec{s}
			}
		}
		if specs == nil { // a custom event, for example loadtest-big
			specs = []Spec{{
				Slug: o.Event, Name: "Load test event (" + o.Event + ")", Venue: "Chennai → Bengaluru",
				Capacity: defaultCapacity, PricePaise: defaultPricePaise, MaxPerUser: defaultMaxPerUser, OpensIn: tatkalOpensIn,
			}}
		}
	}
	for i := range specs {
		if o.Capacity != 0 {
			specs[i].Capacity = o.Capacity
		}
		if o.OpensIn != nil {
			specs[i].OpensIn = *o.OpensIn
		}
	}
	return specs, nil
}

// SeedResult reports what one Spec did.
type SeedResult struct {
	Slug          string
	Created       bool  // false: the event already existed and was left as is
	SeatsInserted int64 // 0 on a clean re-run
	Seats         int   // seats the event has after the run
	Status        gen.EventStatus
	SaleOpensAt   time.Time
}

// Seed inserts every spec that is not there yet, with its seats, one
// transaction per event. It is idempotent: an existing event is never
// modified (re-time it with the admin open-sale endpoint), and missing seats
// of an existing event are filled in.
func Seed(ctx context.Context, pool *pgxpool.Pool, specs []Spec, now time.Time) ([]SeedResult, error) {
	now = now.UTC().Truncate(time.Second)
	out := make([]SeedResult, 0, len(specs))
	for _, sp := range specs {
		var res SeedResult
		// The closure only inserts-if-absent, so a retry after a
		// serialization failure is safe.
		err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			r, err := seedOne(ctx, gen.New(tx), sp, now)
			res = r
			return err
		})
		if err != nil {
			return out, fmt.Errorf("seed %s: %w", sp.Slug, err)
		}
		out = append(out, res)
	}
	return out, nil
}

func seedOne(ctx context.Context, q *gen.Queries, sp Spec, now time.Time) (SeedResult, error) {
	res := SeedResult{Slug: sp.Slug}
	layout, err := TrainLayout(sp.Capacity)
	if err != nil {
		return res, err
	}
	layoutJSON, err := json.Marshal(layout)
	if err != nil {
		return res, fmt.Errorf("marshal layout: %w", err)
	}
	sale := now.Add(max(sp.OpensIn, 0))
	status := gen.EventStatusSCHEDULED
	if !sale.After(now) {
		status = gen.EventStatusONSALE
	}
	n, err := q.InsertEventIfAbsent(ctx, gen.InsertEventIfAbsentParams{
		Slug: sp.Slug, Name: sp.Name, Venue: sp.Venue,
		StartsAt: sale.Add(eventDuration), PrequeueOpensAt: now, SaleOpensAt: sale,
		Capacity: int32(sp.Capacity), PricePaise: sp.PricePaise, MaxPerUser: int32(sp.MaxPerUser), //nolint:gosec // G115: capacity <= maxCapacity, max_per_user <= 10
		Status: status, Layout: layoutJSON,
	})
	if err != nil {
		return res, fmt.Errorf("insert event: %w", err)
	}
	res.Created = n == 1

	ev, err := q.GetEventBySlug(ctx, sp.Slug)
	if err != nil {
		return res, fmt.Errorf("load event: %w", err)
	}
	if int(ev.Capacity) != sp.Capacity {
		return res, fmt.Errorf("event %s already exists with capacity %d (asked for %d); seed never changes an existing event",
			sp.Slug, ev.Capacity, sp.Capacity)
	}
	res.Status, res.SaleOpensAt = ev.Status, ev.SaleOpensAt.UTC()

	// Seats always follow the stored layout, so a re-run fills gaps the same way.
	var stored Layout
	if err := json.Unmarshal(ev.Layout, &stored); err != nil {
		return res, fmt.Errorf("stored layout of %s: %w", sp.Slug, err)
	}
	p := gen.InsertSeatsParams{EventID: ev.ID}
	for _, s := range stored.Seats() {
		p.SeatNos = append(p.SeatNos, s.SeatNo)
		p.Labels = append(p.Labels, s.Label)
		p.Sections = append(p.Sections, s.Section)
		p.RowNos = append(p.RowNos, s.Row)
		p.ColNos = append(p.ColNos, s.Col)
	}
	if res.SeatsInserted, err = q.InsertSeats(ctx, p); err != nil {
		return res, fmt.Errorf("insert seats: %w", err)
	}
	c, err := q.EventSeatCounts(ctx, ev.ID)
	if err != nil {
		return res, fmt.Errorf("count seats: %w", err)
	}
	res.Seats = int(c.Total)
	if res.Seats != sp.Capacity {
		return res, fmt.Errorf("event %s has %d seats, want %d", sp.Slug, res.Seats, sp.Capacity)
	}
	return res, nil
}
