package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// touchTimeout bounds one queue_touch.lua call on the request path. A
// renewal is best effort: the request goes on even if it fails.
const touchTimeout = 500 * time.Millisecond

// Lease renews admission leases (ADR 0010 addendum). An admission sits in
// q:active with expiry min(last request + idle, hard cap); every request
// that presents the admission token renews it with one atomic
// ZADD XX GT (queue_touch.lua). An admitted user who stops making requests
// leaves q:active after idle, without client cooperation, and the
// controller offers the slot to the next ticket. The hard cap
// (ADMISSION_TTL, the token's exp) is never extended.
type Lease struct {
	sc      *redisx.Scripts
	idle    time.Duration
	now     func() time.Time
	log     *slog.Logger
	touches *prometheus.CounterVec
}

// Touch results (queue_admission_touches_total{result}).
const (
	TouchRenewed = "renewed" // the lease moved forward
	TouchNoop    = "noop"    // no live admission (released, expired, left) or already at the cap
	TouchError   = "error"
)

// NewLease builds the lease renewer. reg may be nil; now may be nil.
func NewLease(sc *redisx.Scripts, idle time.Duration, now func() time.Time, log *slog.Logger,
	reg prometheus.Registerer,
) (*Lease, error) {
	if sc == nil || idle <= 0 {
		return nil, errors.New("queue: lease needs scripts and idle > 0")
	}
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	l := &Lease{sc: sc, idle: idle, now: now, log: log, touches: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "queue_admission_touches_total",
		Help: "Admission lease renewals by result (renewed, noop, error).",
	}, []string{"result"})}
	if reg != nil {
		if err := reg.Register(l.touches); err != nil {
			return nil, fmt.Errorf("queue: register lease metrics: %w", err)
		}
	}
	return l, nil
}

// Touch renews c's admission lease and reports its state (auth's
// AdmissionCheck). It ignores every token that is not an admission token
// (AdmissionLive). Errors are logged and reported as AdmissionError; only
// RequireLive routes refuse on them.
func (l *Lease) Touch(ctx context.Context, c auth.Claims) auth.AdmissionState {
	if c.Audience != auth.AudAdmission || c.EventID == uuid.Nil {
		return auth.AdmissionLive
	}
	res, st, err := l.Renew(ctx, c)
	if err != nil {
		l.log.WarnContext(ctx, "admission lease renewal", slog.String("event_id", c.EventID.String()),
			slog.String("error", err.Error()))
	}
	l.touches.WithLabelValues(res).Inc()
	return st
}

// Renew runs queue_touch.lua for c and reports the renewal result and the
// lease state.
func (l *Lease) Renew(ctx context.Context, c auth.Claims) (string, auth.AdmissionState, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), touchTimeout)
	defer cancel()
	r, err := l.sc.Run(ctx, redisx.QueueTouch, keys.QueueTouchKeys(c.EventID), c.Subject.String(),
		l.now().UnixMilli(), l.idle.Milliseconds(), c.ExpiresAt().UnixMilli())
	if err != nil {
		return TouchError, auth.AdmissionError, err
	}
	n, err := r.Int(0)
	if err != nil {
		return TouchError, auth.AdmissionError, fmt.Errorf("queue: touch reply: %w", err)
	}
	if len(r.Fields) < 2 {
		return TouchError, auth.AdmissionError, errors.New("queue: touch reply has no state")
	}
	var st auth.AdmissionState
	switch state := r.Fields[1]; state {
	case "LIVE":
		st = auth.AdmissionLive
	case "LAPSED":
		st = auth.AdmissionLapsed
	case "UNKNOWN":
		st = auth.AdmissionUnknown
	default:
		return TouchError, auth.AdmissionError, fmt.Errorf("queue: touch state %q", state)
	}
	if n == 1 {
		return TouchRenewed, st, nil
	}
	return TouchNoop, st, nil
}

// Grant writes a fresh lease for an admission issued outside the queue
// (POST /test/admit, rush/test only): q:active = min(now + idle, cap) and
// q:cap = cap, the same entries queue_claim.lua writes, so RequireLive
// treats the user like a claimed admission. Two ZADDs in one MULTI; no
// script is needed because nothing is read.
func (l *Lease) Grant(ctx context.Context, rdb redis.Cmdable, eid, uid uuid.UUID, capAt time.Time) error {
	lease := min(l.now().Add(l.idle).UnixMilli(), capAt.UnixMilli())
	_, err := rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.ZAdd(ctx, keys.QActive(eid), redis.Z{Score: float64(lease), Member: uid.String()})
		p.ZAdd(ctx, keys.QCap(eid), redis.Z{Score: float64(capAt.UnixMilli()), Member: uid.String()})
		return nil
	})
	if err != nil {
		return fmt.Errorf("queue: grant lease: %w", err)
	}
	return nil
}
