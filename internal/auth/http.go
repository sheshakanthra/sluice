package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/obs"
)

// ErrNoToken means the request carried no bearer token.
var ErrNoToken = errors.New("no bearer token")

// Bearer returns the bearer token of r.
func Bearer(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", ErrNoToken
	}
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || tok == "" {
		return "", ErrTokenInvalid
	}
	return strings.TrimSpace(tok), nil
}

type claimsKey struct{}

// WithClaims stores verified claims in ctx (and the user id for logging).
func WithClaims(ctx context.Context, c Claims) context.Context {
	ctx = obs.WithUserID(ctx, c.Subject.String())
	return context.WithValue(ctx, claimsKey{}, c)
}

// ClaimsFrom returns the verified claims stored by WithClaims.
func ClaimsFrom(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsKey{}).(Claims)
	return c, ok
}

// UserID returns the authenticated user id, or uuid.Nil. It satisfies the
// idempotency middleware's user lookup.
func UserID(ctx context.Context) uuid.UUID {
	c, _ := ClaimsFrom(ctx)
	return c.Subject
}

// WriteError maps a verification error to its problem response.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrTokenExpired) {
		httpx.WriteError(w, r, httpx.CodeTokenExpired, "")
		return
	}
	httpx.WriteError(w, r, httpx.CodeTokenInvalid, "")
}

// Require wraps next so it only runs with a valid token of one of auds. A
// missing token gives missingCode (TOKEN_INVALID, or NOT_ADMITTED on
// inventory routes when the queue is enforced). An admission token renews
// its lease (best effort; the lease state does not change the answer). An
// admission token past its cap but inside the spend grace is accepted
// without a renewal: these routes give back (DELETE /holds, DELETE /queue).
func (k *Keyring) Require(missingCode httpx.Code, next http.Handler, auds ...Audience) http.Handler {
	return k.require(missingCode, next, gateAny, auds)
}

// RequireLive is Require for POST /holds, which spends an admission: an
// admission token must also have a live server-side lease. A lapsed lease,
// or a token past its cap (inside the spend grace), gives 403
// ADMISSION_EXPIRED (the client may rejoin); a failed check gives 503
// DEPENDENCY_UNAVAILABLE, so the gate never fails open. Session tokens
// (direct mode) are not checked.
func (k *Keyring) RequireLive(missingCode httpx.Code, next http.Handler, auds ...Audience) http.Handler {
	return k.require(missingCode, next, gateLive, auds)
}

// RequireHoldBacked is RequireLive for POST /orders, which spends a hold
// the admission already paid for (ADR 0018). Before the cap it is
// RequireLive. Past the cap (token inside the spend grace) the request goes
// on marked AdmissionPastCap, and the handler must accept it only for the
// user's own live hold, else answer 403 ADMISSION_EXPIRED.
func (k *Keyring) RequireHoldBacked(missingCode httpx.Code, next http.Handler, auds ...Audience) http.Handler {
	return k.require(missingCode, next, gateHoldBacked, auds)
}

type gate int

const (
	gateAny gate = iota
	gateLive
	gateHoldBacked
)

type pastCapKey struct{}

// AdmissionPastCap reports whether RequireHoldBacked let the request
// through on an admission token past its cap.
func AdmissionPastCap(ctx context.Context) bool {
	v, _ := ctx.Value(pastCapKey{}).(bool)
	return v
}

func (k *Keyring) require(missingCode httpx.Code, next http.Handler, g gate, auds []Audience) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, err := Bearer(r)
		if errors.Is(err, ErrNoToken) {
			httpx.WriteError(w, r, missingCode, "an Authorization: Bearer token is required")
			return
		}
		if err != nil {
			WriteError(w, r, err)
			return
		}
		var (
			c       Claims
			pastCap bool
		)
		for _, aud := range auds {
			if c, err = k.Verify(tok, aud); err == nil {
				break
			}
			if errors.Is(err, ErrTokenExpired) {
				// The JWT lifetime check runs before the audience check, so an
				// expired admission token reports "expired" for any audience:
				// try the spend grace whenever the route accepts admission.
				if slices.Contains(auds, AudAdmission) {
					if gc, ok := k.verifyPastExp(tok); ok {
						c, err, pastCap = gc, nil, true
					}
				}
				break
			}
		}
		if err != nil {
			WriteError(w, r, err)
			return
		}
		ctx := r.Context()
		switch {
		case pastCap && g == gateLive:
			httpx.WriteError(w, r, httpx.CodeAdmissionExpired,
				"your admission reached its time limit; rejoin the queue (POST /queue?rejoin=true)")
			return
		case pastCap && g == gateHoldBacked:
			ctx = context.WithValue(ctx, pastCapKey{}, true)
		case pastCap: // give-back route: no renewal past the cap
		case c.Audience == AudAdmission && k.touch != nil:
			st := k.touch(ctx, c)
			if g != gateAny && !gatePasses(w, r, st) {
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(WithClaims(ctx, c)))
	})
}

// gatePasses writes the refusal for a non-live admission and reports
// whether the request may go on.
func gatePasses(w http.ResponseWriter, r *http.Request, st AdmissionState) bool {
	switch st {
	case AdmissionLapsed:
		httpx.WriteError(w, r, httpx.CodeAdmissionExpired,
			"your admission lease has ended; rejoin the queue (POST /queue?rejoin=true)")
		return false
	case AdmissionError:
		httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeDependencyUnavailable, "").WithRetryAfter(time.Second))
		return false
	default: // AdmissionLive, AdmissionUnknown (Redis lost the queue state: the token decides, R1)
		return true
	}
}

// RequireAdmin checks `Authorization: Bearer <ADMIN_TOKEN>` in constant time.
func RequireAdmin(adminToken string, next http.Handler) http.Handler {
	want := []byte(adminToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, err := Bearer(r)
		if err != nil || subtle.ConstantTimeCompare([]byte(tok), want) != 1 {
			httpx.WriteError(w, r, httpx.CodeTokenInvalid, "admin token required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SessionResponse is the body of POST /api/v1/sessions (API.md §3.1).
type SessionResponse struct {
	UserID       uuid.UUID `json:"user_id"`
	SessionToken string    `json:"session_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// SessionsHandler creates an anonymous UUIDv7 user and a session token.
func (k *Keyring) SessionsHandler(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, err := uuid.NewV7()
		if err != nil {
			log.ErrorContext(r.Context(), "new user id", slog.String("error", err.Error()))
			httpx.WriteError(w, r, httpx.CodeInternal, "")
			return
		}
		tok, c, err := k.Issue(AudSession, uid, uuid.Nil, nil, SessionTTL)
		if err != nil {
			log.ErrorContext(r.Context(), "issue session", slog.String("error", err.Error()))
			httpx.WriteError(w, r, httpx.CodeInternal, "")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteJSON(w, http.StatusCreated, SessionResponse{UserID: uid, SessionToken: tok, ExpiresAt: c.ExpiresAt()})
	}
}

// AdmitRequest is the body of POST /api/v1/test/admit.
type AdmitRequest struct {
	EventID string    `json:"event_id"`
	UserID  uuid.UUID `json:"user_id"`
}

// AdmitResponse carries an admission token.
type AdmitResponse struct {
	EventID        uuid.UUID `json:"event_id"`
	UserID         uuid.UUID `json:"user_id"`
	AdmissionToken string    `json:"admission_token"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// EventResolver maps an event UUID or slug to its UUID (false if unknown).
type EventResolver func(ctx context.Context, idOrSlug string) (uuid.UUID, bool, error)

// AdmissionGrant writes the server-side lease for an admission issued
// outside the queue (ADR 0010 addendum part 3).
type AdmissionGrant func(ctx context.Context, eid, uid uuid.UUID, capAt time.Time) error

// TestAdmitHandler issues admission tokens directly (APP_ENV rush/test with
// TEST_ENDPOINTS_ENABLED only; the caller decides whether to mount it). It
// lets holds be exercised before the queue exists. grant, if set, also
// writes the admission's lease, so RequireLive routes accept the token.
func (k *Keyring) TestAdmitHandler(resolve EventResolver, ttl time.Duration, grant AdmissionGrant, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req AdmitRequest
		dec := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req.EventID == "" || req.UserID == uuid.Nil {
			httpx.WriteError(w, r, httpx.CodeValidation, "body must be {\"event_id\": <uuid or slug>, \"user_id\": <uuid>}")
			return
		}
		eid, ok, err := resolve(r.Context(), req.EventID)
		if err != nil {
			log.ErrorContext(r.Context(), "resolve event", slog.String("error", err.Error()))
			httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeDependencyUnavailable, "").WithRetryAfter(time.Second))
			return
		}
		if !ok {
			httpx.WriteError(w, r, httpx.CodeNotFound, "no event with that id or slug")
			return
		}
		tok, c, err := k.Issue(AudAdmission, req.UserID, eid, nil, ttl)
		if err != nil {
			log.ErrorContext(r.Context(), "issue admission", slog.String("error", err.Error()))
			httpx.WriteError(w, r, httpx.CodeInternal, "")
			return
		}
		if grant != nil {
			if err := grant(r.Context(), eid, req.UserID, c.ExpiresAt()); err != nil {
				log.ErrorContext(r.Context(), "grant admission lease", slog.String("error", err.Error()))
				httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeDependencyUnavailable, "").WithRetryAfter(time.Second))
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteJSON(w, http.StatusOK, AdmitResponse{EventID: eid, UserID: req.UserID, AdmissionToken: tok, ExpiresAt: c.ExpiresAt()})
	}
}

// RequestSubject is Subject applied to the request's bearer token.
func (k *Keyring) RequestSubject(r *http.Request) (uuid.UUID, bool) {
	tok, err := Bearer(r)
	if err != nil {
		return uuid.Nil, false
	}
	return k.Subject(tok)
}

// TouchAdmission renews the admission lease of the request's bearer token
// on a public route (GET /availability, ADR 0010 addendum part 4). It acts
// only on a valid, unexpired admission token for eventID and never refuses:
// a missing, invalid or foreign token is ignored and the route answers as it
// would without one. It reports whether a touch ran and the lease state.
// ADMISSION_TTL stays the hard cap: the lease is never renewed past it.
func (k *Keyring) TouchAdmission(r *http.Request, eventID uuid.UUID) (AdmissionState, bool) {
	if k == nil || k.touch == nil {
		return AdmissionUnknown, false
	}
	tok, err := Bearer(r)
	if err != nil {
		return AdmissionUnknown, false
	}
	c, err := k.Verify(tok, AudAdmission)
	if err != nil || c.EventID != eventID {
		return AdmissionUnknown, false
	}
	return k.touch(r.Context(), c), true
}
