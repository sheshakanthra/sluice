package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// Admin and stats routes (API.md §2, §3.9).
const (
	RouteStats    = "GET /api/v1/stats/{eventId}"
	RouteOpenSale = "POST /api/v1/admin/events/{eventId}/open-sale"
)

// statsMaxAge is how long one replica reuses a computed /stats body.
const statsMaxAge = time.Second

// maxOpensIn bounds open-sale's opens_in_s (one day).
const maxOpensIn = 24 * 60 * 60

// CountsSource gives live seat counts (inventory.AvailabilityCache).
type CountsSource interface {
	LiveCounts(ctx context.Context, eventID uuid.UUID) (inventory.Counts, error)
}

// Stats is the body of GET /stats/{id} (API.md §3.9).
type Stats struct {
	TS          time.Time        `json:"ts"`
	EventStatus gen.EventStatus  `json:"event_status"`
	SaleOpensAt time.Time        `json:"sale_opens_at"`
	SoldOutAt   *time.Time       `json:"sold_out_at"`
	Queue       StatsQueue       `json:"queue"`
	Inventory   inventory.Counts `json:"inventory"`
	Correctness StatsCorrectness `json:"correctness"`
	DemoRush    StatsDemo        `json:"demo_rush"`
}

// StatsQueue is the queue part of Stats.
type StatsQueue struct {
	Joined    int64   `json:"joined"`
	Waiting   int64   `json:"waiting"`
	Cursor    int64   `json:"cursor"`
	Offers    int64   `json:"offers"`
	Active    int64   `json:"active"`
	AdmitRate float64 `json:"admit_rate"`
}

// StatsCorrectness comes from the worker's audit cache (sl:{e}:audit,
// refreshed every 5 s for ON_SALE events). All nil when there is none.
type StatsCorrectness struct {
	Oversold         *int64     `json:"oversold"`
	DuplicateCharges *int64     `json:"duplicate_charges"`
	OrphanCharges    *int64     `json:"orphan_charges"`
	CheckedAt        *time.Time `json:"checked_at"`
}

// StatsDemo is the demo-rush block; demo mode arrives in P7/P8.
type StatsDemo struct {
	Running bool `json:"running"`
}

// StatsDeps are /stats and open-sale's dependencies.
type StatsDeps struct {
	Q          *gen.Queries
	Cache      *Cache
	Events     EventSource
	Counts     CountsSource
	Redis      *redis.Client
	Scripts    *redisx.Scripts
	Restorer   *Restorer
	AdminToken string
	Now        func() time.Time
	Log        *slog.Logger
}

// StatsService computes /stats bodies, at most once per statsMaxAge per
// event per replica.
type StatsService struct {
	d       StatsDeps
	entries sync.Map // uuid.UUID -> *statsEntry
}

type statsEntry struct {
	mu   sync.Mutex
	at   time.Time
	body []byte
}

// NewStatsService builds the stats service.
func NewStatsService(d StatsDeps) (*StatsService, error) {
	if d.Q == nil || d.Cache == nil || d.Events == nil || d.Counts == nil || d.Redis == nil {
		return nil, errors.New("queue: stats needs queries, cache, events, counts and redis")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &StatsService{d: d}, nil
}

// RegisterStatsRoutes mounts /stats (public) and open-sale (admin).
func RegisterStatsRoutes(rt *httpx.Router, s *StatsService) {
	rt.HandleFunc(RouteStats, s.handleStats)
	rt.Handle(RouteOpenSale, auth.RequireAdmin(s.d.AdminToken, http.HandlerFunc(s.handleOpenSale)))
}

func (s *StatsService) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	info, err := s.d.Events.Event(ctx, r.PathValue("eventId"))
	if errors.Is(err, inventory.ErrEventNotFound) {
		httpx.WriteError(w, r, httpx.CodeNotFound, "no event with that id or slug")
		return
	}
	if err != nil {
		s.unavailable(ctx, w, r, "stats: resolve event", err)
		return
	}
	body, err := s.Body(ctx, info.ID)
	if err != nil {
		s.unavailable(ctx, w, r, "stats", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=1")
	_, _ = w.Write(body) //nolint:gosec // G705: json.Marshal output served as application/json, not HTML
}

// Body returns eid's stats JSON, recomputed when the cached copy is older
// than statsMaxAge. Concurrent callers for one event wait for one compute.
func (s *StatsService) Body(ctx context.Context, eid uuid.UUID) ([]byte, error) {
	v, _ := s.entries.LoadOrStore(eid, &statsEntry{})
	e, _ := v.(*statsEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	now := s.d.Now()
	if e.body != nil && now.Sub(e.at) < statsMaxAge {
		return e.body, nil
	}
	st, err := s.Compute(ctx, eid)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return nil, fmt.Errorf("queue: stats json: %w", err)
	}
	e.at, e.body = now, b
	return b, nil
}

// Compute builds eid's stats from PG (event status), the queue snapshot
// cache, the live seat counts and the audit hash.
func (s *StatsService) Compute(ctx context.Context, eid uuid.UUID) (Stats, error) {
	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()
	ev, err := s.d.Q.GetEventByID(ctx, eid) // fresh status, not the 5 s event cache
	if err != nil {
		return Stats{}, fmt.Errorf("queue: stats event: %w", err)
	}
	snap := s.d.Cache.Peek(eid)
	if snap == nil || s.d.Now().Sub(snap.At) >= statsMaxAge {
		if snap, err = s.d.Cache.Refresh(ctx, eid); err != nil {
			return Stats{}, err
		}
	}
	counts, err := s.d.Counts.LiveCounts(ctx, eid)
	if err != nil {
		return Stats{}, err
	}
	st := Stats{
		TS: s.d.Now().UTC(), EventStatus: ev.Status, SaleOpensAt: ev.SaleOpensAt.UTC(),
		Queue: queueStats(snap), Inventory: counts,
	}
	audit, err := s.d.Redis.HGetAll(ctx, keys.Audit(eid)).Result()
	if err != nil {
		return Stats{}, fmt.Errorf("queue: stats audit: %w", err)
	}
	st.Correctness = correctness(audit)
	return st, nil
}

func queueStats(snap *Snapshot) StatsQueue {
	joined := snap.Pre // before the freeze
	if snap.PreSize > 0 || snap.Tail > 0 {
		joined = snap.PreSize + snap.Tail
	}
	return StatsQueue{
		Joined: joined, Waiting: max(0, joined-snap.Cursor), Cursor: snap.Cursor, Offers: snap.Offers,
		Active: snap.Active, AdmitRate: math.Round(snap.Rate*10) / 10,
	}
}

func correctness(h map[string]string) StatsCorrectness {
	num := func(k string) *int64 {
		n, err := strconv.ParseInt(h[k], 10, 64)
		if err != nil {
			return nil
		}
		return &n
	}
	c := StatsCorrectness{Oversold: num("oversold"), DuplicateCharges: num("duplicates"), OrphanCharges: num("orphans")}
	if ms := num("at_ms"); ms != nil {
		t := time.UnixMilli(*ms).UTC()
		c.CheckedAt = &t
	}
	return c
}

// OpenSaleRequest is open-sale's body.
type OpenSaleRequest struct {
	OpensInS *int `json:"opens_in_s"`
}

// OpenSaleResponse is open-sale's reply.
type OpenSaleResponse struct {
	EventID     uuid.UUID `json:"event_id"`
	SaleOpensAt time.Time `json:"sale_opens_at"`
	Status      string    `json:"status"`
}

// handleOpenSale re-times a sale: T0 = now + opens_in_s (demo and tests).
// Only a queue nobody has joined can be re-timed; otherwise 409 EVENT_STATE.
func (s *StatsService) handleOpenSale(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req OpenSaleRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.OpensInS == nil || *req.OpensInS < 0 || *req.OpensInS > maxOpensIn {
		httpx.WriteError(w, r, httpx.CodeValidation, fmt.Sprintf("body must be {\"opens_in_s\": 0..%d}", maxOpensIn))
		return
	}
	info, err := s.d.Events.Event(ctx, r.PathValue("eventId"))
	if errors.Is(err, inventory.ErrEventNotFound) {
		httpx.WriteError(w, r, httpx.CodeNotFound, "no event with that id or slug")
		return
	}
	if err != nil {
		s.unavailable(ctx, w, r, "open-sale: resolve event", err)
		return
	}
	opens := s.d.Now().Add(time.Duration(*req.OpensInS) * time.Second).Truncate(time.Millisecond).UTC()
	err = s.OpenSale(ctx, info.ID, opens)
	var busy *busyError
	switch {
	case errors.As(err, &busy):
		httpx.WriteError(w, r, httpx.CodeEventState, busy.Error())
	case err != nil:
		s.unavailable(ctx, w, r, "open-sale", err)
	default:
		s.d.Log.InfoContext(ctx, "sale re-timed", slog.String("event_id", info.ID.String()),
			slog.Time("sale_opens_at", opens))
		httpx.WriteJSON(w, http.StatusOK, OpenSaleResponse{EventID: info.ID, SaleOpensAt: opens,
			Status: string(gen.EventStatusSCHEDULED)})
	}
}

type busyError struct{ msg string }

func (e *busyError) Error() string { return e.msg }

// OpenSale sets eid's T0 to opens and puts it back to SCHEDULED. It fails
// with a *busyError when the event sold out or closed, or anybody already
// holds a ticket or a lottery entry.
func (s *StatsService) OpenSale(ctx context.Context, eid uuid.UUID, opens time.Time) error {
	if s.d.Scripts == nil || s.d.Restorer == nil {
		return errors.New("queue: open-sale needs scripts and a restorer")
	}
	ev, err := s.d.Q.GetEventByID(ctx, eid)
	if err != nil {
		return fmt.Errorf("queue: open-sale event: %w", err)
	}
	if ev.Status != gen.EventStatusSCHEDULED && ev.Status != gen.EventStatusONSALE {
		return &busyError{msg: "the event is " + string(ev.Status)}
	}
	// Make Redis reflect every ticket PG knows about before checking.
	if _, err := s.d.Restorer.Restore(ctx, eid); err != nil {
		return err
	}
	res, err := s.d.Scripts.Run(ctx, redisx.QueueReopen, keys.QueueReopenKeys(eid), opens.UnixMilli())
	if err != nil {
		return err
	}
	if res.Status == "BUSY" {
		return &busyError{msg: "the queue already has entries; reset the event to re-time it"}
	}
	starts := ev.StartsAt
	if !starts.After(opens) {
		starts = opens.Add(2 * time.Hour)
	}
	pre := ev.PrequeueOpensAt
	if opens.Before(pre) {
		pre = opens
	}
	if _, err := s.d.Q.SetEventSchedule(ctx, gen.SetEventScheduleParams{
		ID: eid, PrequeueOpensAt: pre, SaleOpensAt: opens, StartsAt: starts, Status: gen.EventStatusSCHEDULED,
	}); err != nil {
		return fmt.Errorf("queue: open-sale schedule: %w", err)
	}
	// The counters are zero again: write that down so a later restore (and
	// any slower snapshot writer, by taken_at) cannot bring back the old ones.
	if _, err := s.d.Q.UpsertQueueSnapshot(ctx, gen.UpsertQueueSnapshotParams{
		EventID: eid, Offers: []int64{}, ActiveUids: []uuid.UUID{}, ActiveExpMs: []int64{}, TakenAt: s.d.Now(),
	}); err != nil {
		return fmt.Errorf("queue: open-sale snapshot: %w", err)
	}
	return nil
}

func (s *StatsService) unavailable(ctx context.Context, w http.ResponseWriter, r *http.Request, what string, err error) {
	s.d.Log.ErrorContext(ctx, what, slog.String("error", err.Error()))
	httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeDependencyUnavailable, "").WithRetryAfter(time.Second))
}
