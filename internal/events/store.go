package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sheshakanthra/sluice/internal/db/gen"
)

// ErrEventNotFound is returned when no event matches an id or slug.
var ErrEventNotFound = errors.New("event not found")

// Counts are seat counts by state. In P1 they come straight from Postgres;
// P2 replaces them with live counts from Redis.
type Counts struct {
	Available int32 `json:"available"`
	Held      int32 `json:"held"`
	Sold      int32 `json:"sold"`
	Capacity  int32 `json:"capacity"`
}

// Event is the public view of an event.
type Event struct {
	ID              uuid.UUID `json:"id"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	Venue           string    `json:"venue"`
	Status          string    `json:"status"`
	StartsAt        time.Time `json:"starts_at"`
	PrequeueOpensAt time.Time `json:"prequeue_opens_at"`
	SaleOpensAt     time.Time `json:"sale_opens_at"`
	Capacity        int32     `json:"capacity"`
	PricePaise      int64     `json:"price_paise"`
	Currency        string    `json:"currency"`
	MaxPerUser      int32     `json:"max_per_user"`
	Counts          Counts    `json:"counts"`
}

// EventDetail adds the seat layout.
type EventDetail struct {
	Event
	Layout json.RawMessage `json:"layout"`
}

// Store reads events from Postgres.
type Store struct {
	q *gen.Queries
}

// NewStore wraps generated queries (a pool or a transaction).
func NewStore(q *gen.Queries) *Store { return &Store{q: q} }

// List returns every event with its seat counts, by sale time.
func (s *Store) List(ctx context.Context) ([]Event, error) {
	rows, err := s.q.ListEventsWithCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	out := make([]Event, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		out = append(out, Event{
			ID: r.ID, Slug: r.Slug, Name: r.Name, Venue: r.Venue, Status: string(r.Status),
			StartsAt: r.StartsAt.UTC(), PrequeueOpensAt: r.PrequeueOpensAt.UTC(), SaleOpensAt: r.SaleOpensAt.UTC(),
			Capacity: r.Capacity, PricePaise: r.PricePaise, Currency: "INR", MaxPerUser: r.MaxPerUser,
			Counts: Counts{Available: r.Available, Held: r.Held, Sold: r.Sold, Capacity: r.Capacity},
		})
	}
	return out, nil
}

// Get returns one event by UUID or slug, with its layout and counts.
func (s *Store) Get(ctx context.Context, idOrSlug string) (EventDetail, error) {
	var (
		ev  gen.Event
		err error
	)
	if id, perr := uuid.Parse(idOrSlug); perr == nil {
		ev, err = s.q.GetEventByID(ctx, id)
	} else if ValidSlug(idOrSlug) {
		ev, err = s.q.GetEventBySlug(ctx, idOrSlug)
	} else {
		return EventDetail{}, ErrEventNotFound
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return EventDetail{}, ErrEventNotFound
	}
	if err != nil {
		return EventDetail{}, fmt.Errorf("get event: %w", err)
	}
	c, err := s.q.EventSeatCounts(ctx, ev.ID)
	if err != nil {
		return EventDetail{}, fmt.Errorf("event counts: %w", err)
	}
	return EventDetail{
		Event: Event{
			ID: ev.ID, Slug: ev.Slug, Name: ev.Name, Venue: ev.Venue, Status: string(ev.Status),
			StartsAt: ev.StartsAt.UTC(), PrequeueOpensAt: ev.PrequeueOpensAt.UTC(), SaleOpensAt: ev.SaleOpensAt.UTC(),
			Capacity: ev.Capacity, PricePaise: ev.PricePaise, Currency: "INR", MaxPerUser: ev.MaxPerUser,
			Counts: Counts{Available: c.Available, Held: c.Held, Sold: c.Sold, Capacity: ev.Capacity},
		},
		Layout: json.RawMessage(ev.Layout),
	}, nil
}
