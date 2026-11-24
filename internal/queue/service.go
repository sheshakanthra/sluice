package queue

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	mrand "math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// scriptTimeout bounds one queue script call on the request path.
const scriptTimeout = 2 * time.Second

// minQueueTokenTTL is the floor for queue tokens of events about to start.
const minQueueTokenTTL = time.Minute

// Options configure a Service.
type Options struct {
	AdmissionTTL time.Duration // admission hard cap (ADMISSION_TTL)
	// AdmissionIdleTTL is the sliding idle lease (ADMISSION_IDLE_TTL): an
	// admitted user whose last admission-token request is older than this
	// leaves q:active. 0 means AdmissionTTL (no idle expiry).
	AdmissionIdleTTL time.Duration
	Overadmit        float64 // QUEUE_OVERADMIT, used by the outlook
	Now              func() time.Time
	Rand             io.Reader      // lottery scores; nil = crypto/rand
	Jitter           func() float64 // returns U(0,1); nil = math/rand/v2
	Log              *slog.Logger
	Metrics          *Metrics
	// Restorer initialises the queue counters when a join finds them missing
	// (a fresh event, or Redis lost them). nil: such joins fail with
	// ErrNotRestored and the worker restores within one tick.
	Restorer *Restorer
}

// Service answers join, status and leave for one API replica.
type Service struct {
	sc    *redisx.Scripts
	keys  *auth.Keyring
	cache *Cache
	opt   Options
}

// NewService builds the queue service.
func NewService(sc *redisx.Scripts, keyring *auth.Keyring, cache *Cache, opt Options) (*Service, error) {
	if sc == nil || keyring == nil || cache == nil {
		return nil, errors.New("queue: scripts, keyring and cache are required")
	}
	if opt.AdmissionTTL <= 0 || opt.Overadmit < 1 || opt.AdmissionIdleTTL < 0 || opt.AdmissionIdleTTL > opt.AdmissionTTL {
		return nil, errors.New("queue: AdmissionTTL > 0, 0 <= AdmissionIdleTTL <= AdmissionTTL and Overadmit >= 1 are required")
	}
	if opt.AdmissionIdleTTL == 0 {
		opt.AdmissionIdleTTL = opt.AdmissionTTL
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Rand == nil {
		opt.Rand = rand.Reader
	}
	if opt.Jitter == nil {
		opt.Jitter = mrand.Float64 //nolint:gosec // G404: poll jitter, not a secret
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{sc: sc, keys: keyring, cache: cache, opt: opt}, nil
}

// Cache returns the snapshot cache (the API runs its refresh loop).
func (s *Service) Cache() *Cache { return s.cache }

// JoinResponse is the body of POST /events/{id}/queue (API.md §3.2).
type JoinResponse struct {
	State        State     `json:"state"`
	QueueToken   *string   `json:"queue_token"`
	TicketNo     *int64    `json:"ticket_no"`
	OpensAt      time.Time `json:"opens_at"`
	RetryAfterMS int64     `json:"retry_after_ms"`
}

// Counts are the seat counts shown while waiting.
type Counts struct {
	Available int64 `json:"available"`
	Held      int64 `json:"held"`
	Sold      int64 `json:"sold"`
	Capacity  int64 `json:"capacity"`
}

// StatusResponse is the body of GET /events/{id}/queue/status (API.md §3.3).
type StatusResponse struct {
	State              State      `json:"state"`
	TicketNo           *int64     `json:"ticket_no"`
	Position           *int64     `json:"position,omitempty"`
	Ahead              *int64     `json:"ahead,omitempty"`
	EtaS               *int64     `json:"eta_s,omitempty"`
	Outlook            Outlook    `json:"outlook,omitempty"`
	Counts             Counts     `json:"counts"`
	RetryAfterMS       int64      `json:"retry_after_ms"`
	QueueToken         *string    `json:"queue_token"`
	AdmissionToken     *string    `json:"admission_token"`
	AdmissionExpiresAt *time.Time `json:"admission_expires_at,omitempty"`
	OpensAt            time.Time  `json:"opens_at"`
}

// Join puts uid in ev's queue (idempotent per user). rejoin asks for a new
// ticket at the back after OFFER_EXPIRED; it fails with ErrRejoinLimit after
// MaxRejoins.
func (s *Service) Join(ctx context.Context, ev Event, uid uuid.UUID, rejoin bool) (JoinResponse, error) {
	resp, err := s.doJoin(ctx, ev, uid, rejoin)
	s.opt.Metrics.join(resp.State, err)
	return resp, err
}

func (s *Service) doJoin(ctx context.Context, ev Event, uid uuid.UUID, rejoin bool) (JoinResponse, error) {
	now := s.opt.Now()
	resp := JoinResponse{OpensAt: ev.SaleOpensAt}
	switch ev.Status {
	case gen.EventStatusCLOSED:
		resp.State = StateClosed
		return resp, nil
	case gen.EventStatusSOLDOUT:
		resp.State = StateSoldOut
		return resp, nil
	}
	r, err := s.join(ctx, ev, uid, rejoin, now)
	if err != nil {
		return resp, err
	}
	switch r.Status {
	case "PRE":
		tok, err := s.queueToken(ev, uid, 0, now)
		if err != nil {
			return resp, err
		}
		resp.State, resp.QueueToken = StatePrequeue, &tok
		resp.RetryAfterMS = prequeueRetry(ev.SaleOpensAt.Sub(now)).Milliseconds()
	case "TICKET":
		t, err := r.Int(0)
		if err != nil {
			return resp, fmt.Errorf("queue: join reply: %w", err)
		}
		tok, err := s.queueToken(ev, uid, t, now)
		if err != nil {
			return resp, err
		}
		resp.State, resp.QueueToken, resp.TicketNo = StateWaiting, &tok, &t
		retry := time.Second
		if snap := s.cache.Peek(ev.ID); snap != nil {
			retry = s.waitingRetry(t-snap.Cursor, snap.Rate)
		}
		resp.RetryAfterMS = retry.Milliseconds()
	case "SOLD_OUT":
		resp.State = StateSoldOut
	case "REJOIN_LIMIT":
		return resp, ErrRejoinLimit
	default:
		return resp, fmt.Errorf("queue: join: unexpected reply %q", r.Status)
	}
	return resp, nil
}

func (s *Service) join(ctx context.Context, ev Event, uid uuid.UUID, rejoin bool, now time.Time) (redisx.Reply, error) {
	score, err := randScore(s.opt.Rand)
	if err != nil {
		return redisx.Reply{}, err
	}
	rj := 0
	if rejoin {
		rj = 1
	}
	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()
	run := func() (redisx.Reply, error) {
		return s.sc.Run(ctx, redisx.QueueJoin, keys.QueueJoinKeys(ev.ID, uid),
			uid.String(), now.UnixMilli(), ev.SaleOpensAt.UnixMilli(), score, rj, MaxRejoins)
	}
	r, err := run()
	if err != nil || r.Status != tickNotInit {
		return r, err
	}
	// Fresh event, or Redis lost the queue keys: restore the counters from
	// the PG snapshot first (R1), then join.
	if s.opt.Restorer == nil {
		return r, ErrNotRestored
	}
	if _, err := s.opt.Restorer.Restore(ctx, ev.ID); err != nil {
		return r, err
	}
	if r, err = run(); err == nil && r.Status == tickNotInit {
		err = ErrNotRestored
	}
	return r, err
}

// Status answers a queue-token holder. WAITING users (ticket above the
// cursor) are answered from the snapshot and the token alone: zero Redis
// calls. Redis is called only to resolve a pre-queue ticket once after T0,
// to claim an offer, or the first time this replica sees the event.
func (s *Service) Status(ctx context.Context, ev Event, c auth.Claims) (StatusResponse, error) {
	resp, err := s.doStatus(ctx, ev, c)
	if err == nil {
		s.opt.Metrics.status(resp.State)
	}
	return resp, err
}

func (s *Service) doStatus(ctx context.Context, ev Event, c auth.Claims) (StatusResponse, error) {
	now := s.opt.Now()
	resp := StatusResponse{OpensAt: ev.SaleOpensAt}
	snap := s.cache.Peek(ev.ID)
	if snap == nil {
		s.opt.Metrics.statusRedisCall()
		var err error
		if snap, err = s.cache.Refresh(ctx, ev.ID); err != nil {
			return resp, err
		}
	}
	resp.Counts = counts(snap, ev.Capacity)
	if ev.Status == gen.EventStatusCLOSED {
		resp.State = StateClosed
		return resp, nil
	}
	if snap.SoldOut {
		resp.State = StateSoldOut
		return resp, nil
	}

	var ticket int64
	if c.Ticket != nil {
		ticket = *c.Ticket
	}
	if ticket == 0 {
		// Pre-queue token: wait for T0 and the freeze, then resolve once.
		if now.Before(ev.SaleOpensAt) || snap.PreSize == 0 {
			resp.State = StatePrequeue
			resp.RetryAfterMS = prequeueRetry(ev.SaleOpensAt.Sub(now)).Milliseconds()
			return resp, nil
		}
		t, state, err := s.resolve(ctx, ev, c.Subject, now)
		if err != nil || state != "" {
			resp.State = state
			if state == StatePrequeue {
				resp.RetryAfterMS = prequeueRetry(0).Milliseconds()
			}
			return resp, err
		}
		tok, err := s.queueToken(ev, c.Subject, t, now)
		if err != nil {
			return resp, err
		}
		ticket, resp.QueueToken = t, &tok
	}
	resp.TicketNo = &ticket

	if ticket > snap.Cursor {
		s.waiting(&resp, snap, ticket-snap.Cursor)
		return resp, nil
	}
	return s.claim(ctx, ev, c.Subject, ticket, snap, resp, now)
}

// resolve turns a pre-queue entry into its lottery ticket. A non-empty state
// means "answer with this state" (not resolved yet, or sold out).
func (s *Service) resolve(ctx context.Context, ev Event, uid uuid.UUID, now time.Time) (int64, State, error) {
	s.opt.Metrics.statusRedisCall()
	rctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	r, err := s.sc.Run(rctx, redisx.QueueResolve, keys.QueueResolveKeys(ev.ID, uid), uid.String())
	cancel()
	if err != nil {
		return 0, "", err
	}
	if r.Status == "NOT_FOUND" {
		// The entry was lost (Redis restore, or the user left): join again,
		// which lands at the back after T0.
		s.opt.Metrics.statusRedisCall()
		if r, err = s.join(ctx, ev, uid, false, now); err != nil {
			return 0, "", err
		}
	}
	switch r.Status {
	case "TICKET":
		t, err := r.Int(0)
		if err != nil {
			return 0, "", fmt.Errorf("queue: resolve reply: %w", err)
		}
		return t, "", nil
	case "PENDING", "PRE":
		return 0, StatePrequeue, nil
	case "SOLD_OUT":
		return 0, StateSoldOut, nil
	default:
		return 0, "", fmt.Errorf("queue: resolve: unexpected reply %q", r.Status)
	}
}

// claim turns the offer for ticket into an admission token.
func (s *Service) claim(ctx context.Context, ev Event, uid uuid.UUID, ticket int64, snap *Snapshot,
	resp StatusResponse, now time.Time,
) (StatusResponse, error) {
	s.opt.Metrics.statusRedisCall()
	cctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()
	r, err := s.sc.Run(cctx, redisx.QueueClaim, keys.QueueClaimKeys(ev.ID),
		ticket, uid.String(), now.UnixMilli(), s.opt.AdmissionTTL.Milliseconds(), s.opt.AdmissionIdleTTL.Milliseconds())
	if err != nil {
		return resp, err
	}
	switch r.Status {
	case "ADMITTED":
		expMS, err := r.Int(0)
		if err != nil {
			return resp, fmt.Errorf("queue: claim reply: %w", err)
		}
		// The token expires at the admission's hard cap (q:cap, floored to
		// the second), so every claim of the same admission reports the
		// same expiry; the idle lease in q:active is renewed by Lease.Touch
		// and never passes that cap (ADR 0010 addendum).
		exp := time.UnixMilli(expMS).UTC()
		if !exp.After(now) {
			exp = now.Add(s.opt.AdmissionTTL)
		}
		tok, c, err := s.keys.IssueUntil(auth.AudAdmission, uid, ev.ID, nil, exp)
		if err != nil {
			return resp, err
		}
		if isNew, err := r.Int(1); err == nil && isNew == 1 {
			s.opt.Metrics.admitted()
		}
		exp = c.ExpiresAt()
		resp.State, resp.AdmissionToken, resp.AdmissionExpiresAt = StateAdmitted, &tok, &exp
	case "OFFER_EXPIRED":
		resp.State = StateOfferExpired
	case "SOLD_OUT":
		resp.State = StateSoldOut
	case "WAITING": // this replica's snapshot is ahead of Redis (restore)
		s.waiting(&resp, snap, 1)
	default:
		return resp, fmt.Errorf("queue: claim: unexpected reply %q", r.Status)
	}
	return resp, nil
}

// waiting fills a WAITING (or PAUSED) answer from the snapshot alone.
func (s *Service) waiting(resp *StatusResponse, snap *Snapshot, position int64) {
	if snap.Avail == 0 { // everything is held: wait for expiries
		resp.State = StatePaused
		resp.RetryAfterMS = (5*time.Second + time.Duration(s.opt.Jitter()*float64(5*time.Second))).Milliseconds()
		return
	}
	ahead := position - 1
	eta := int64(math.Ceil(float64(position) / math.Max(snap.Rate, 1)))
	resp.State = StateWaiting
	resp.Position, resp.Ahead, resp.EtaS = &position, &ahead, &eta
	resp.Outlook = outlook(position, resp.Counts, s.opt.Overadmit)
	resp.RetryAfterMS = s.waitingRetry(position, snap.Rate).Milliseconds()
}

// waitingRetry is clamp(eta/4, 1 s, 15 s) x U(0.8, 1.2).
func (s *Service) waitingRetry(position int64, rate float64) time.Duration {
	eta := time.Duration(float64(max(position, 0)) / math.Max(rate, 1) * float64(time.Second))
	base := min(max(eta/4, time.Second), 15*time.Second)
	return time.Duration(float64(base) * (0.8 + 0.4*s.opt.Jitter()))
}

// prequeueRetry is clamp(time_to_T0 / 2, 0.5 s, 5 s).
func prequeueRetry(untilT0 time.Duration) time.Duration {
	return min(max(untilT0/2, 500*time.Millisecond), 5*time.Second)
}

// outlook: remaining = avail + 0.25 x held; LIKELY if position <=
// remaining x overadmit, POSSIBLE if <= 3x that, else UNLIKELY.
func outlook(position int64, c Counts, overadmit float64) Outlook {
	reach := (float64(c.Available) + 0.25*float64(c.Held)) * overadmit
	switch p := float64(position); {
	case p <= reach:
		return OutlookLikely
	case p <= 3*reach:
		return OutlookPossible
	default:
		return OutlookUnlikely
	}
}

func counts(snap *Snapshot, capacity int32) Counts {
	c := Counts{Available: snap.Avail, Sold: snap.Sold, Capacity: int64(capacity)}
	c.Held = max(0, c.Capacity-c.Available-c.Sold)
	return c
}

// Leave removes the user's admission, offer and queue entry.
func (s *Service) Leave(ctx context.Context, ev Event, c auth.Claims) error {
	var ticket int64
	if c.Audience == auth.AudQueue && c.Ticket != nil {
		ticket = *c.Ticket
	}
	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()
	_, err := s.sc.Run(ctx, redisx.QueueLeave, keys.QueueLeaveKeys(ev.ID, c.Subject), c.Subject.String(), ticket)
	return err
}

// queueToken issues a queue token (tkt 0 = pre-queue) valid until the event
// starts.
func (s *Service) queueToken(ev Event, uid uuid.UUID, ticket int64, now time.Time) (string, error) {
	ttl := max(ev.StartsAt.Sub(now), minQueueTokenTTL)
	tok, _, err := s.keys.Issue(auth.AudQueue, uid, ev.ID, &ticket, ttl)
	if err != nil {
		return "", fmt.Errorf("queue: issue token: %w", err)
	}
	return tok, nil
}
