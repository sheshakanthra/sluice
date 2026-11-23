package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sheshakanthra/sluice/internal/db/gen"
)

// Idempotency defaults (API.md §1.2).
const (
	IdempotencyHeader   = "Idempotency-Key"
	ReplayedHeader      = "Idempotent-Replayed"
	IdempotencyLock     = 30 * time.Second
	IdempotencyKeyTTL   = 24 * time.Hour
	maxIdempotentBody   = 64 << 10
	idempotencyStoreTTL = 2 * time.Second
)

// Idempotency is the Stripe-style, Postgres-backed idempotency middleware.
// The first request with a (user, key) inserts a STARTED row locked for 30 s
// and runs; its response (any status below 500) is stored and the row
// COMPLETED. A repeat with the same request hash replays the stored response
// with Idempotent-Replayed: true; a different hash is 422; a repeat while the
// first is still running is 409 REQUEST_IN_PROGRESS. A STARTED row whose lock
// lapsed (the process died) is re-executed, which is safe because handlers
// are idempotent (deterministic hold ids). 5xx responses are not stored: the
// row is deleted so a retry runs again.
type Idempotency struct {
	q      *gen.Queries
	userID func(context.Context) uuid.UUID
	log    *slog.Logger
	now    func() time.Time
}

// NewIdempotency builds the middleware. userID returns the authenticated
// user (uuid.Nil if none); now may be nil.
func NewIdempotency(q *gen.Queries, userID func(context.Context) uuid.UUID, log *slog.Logger, now func() time.Time) *Idempotency {
	if now == nil {
		now = time.Now
	}
	return &Idempotency{q: q, userID: userID, log: log, now: now}
}

// ValidIdempotencyKey: 8-128 visible ASCII characters.
func ValidIdempotencyKey(k string) bool {
	if len(k) < 8 || len(k) > 128 {
		return false
	}
	for i := range len(k) {
		if k[i] < 0x21 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

// RequestHash is sha256(method | route | canonical JSON body). Canonical
// JSON re-encodes the body with sorted object keys and no insignificant
// whitespace; a body that is not JSON is hashed as-is.
func RequestHash(method, route string, body []byte) []byte {
	canon := body
	if len(bytes.TrimSpace(body)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err == nil {
			if b, err := json.Marshal(v); err == nil {
				canon = b
			}
		}
	} else {
		canon = nil
	}
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{'|'})
	h.Write([]byte(route))
	h.Write([]byte{'|'})
	h.Write(canon)
	return h.Sum(nil)
}

// Wrap guards next, registered under route (the pattern, not the raw path).
func (m *Idempotency) Wrap(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(IdempotencyHeader)
		if key == "" {
			WriteError(w, r, CodeIdempotencyKeyRequired, "send an Idempotency-Key header (a UUID per user intent)")
			return
		}
		if !ValidIdempotencyKey(key) {
			WriteError(w, r, CodeValidation, "Idempotency-Key must be 8-128 visible ASCII characters")
			return
		}
		uid := m.userID(r.Context())
		if uid == uuid.Nil {
			WriteError(w, r, CodeTokenInvalid, "")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxIdempotentBody+1))
		if err != nil || len(body) > maxIdempotentBody {
			WriteError(w, r, CodeValidation, "request body is unreadable or too large")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		hash := RequestHash(r.Method, route, body)

		run, err := m.claim(r.Context(), uid, key, r.Method, route, hash, w, r)
		if err != nil {
			m.log.ErrorContext(r.Context(), "idempotency claim", slog.String("error", err.Error()))
			WriteProblem(w, r, NewProblem(CodeDependencyUnavailable, "").WithRetryAfter(time.Second))
			return
		}
		if !run {
			return // replayed, in progress or reused: already answered
		}

		rec := &recorder{header: http.Header{}, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		m.finish(r.Context(), uid, key, rec)
		for k, vs := range rec.header {
			w.Header()[k] = vs
		}
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body.Bytes()) // the client may have gone
	})
}

// claim inserts or inspects the key row. It returns true when this request
// should execute; otherwise it has written the response.
func (m *Idempotency) claim(ctx context.Context, uid uuid.UUID, key, method, route string, hash []byte, w http.ResponseWriter, r *http.Request) (bool, error) {
	now := m.now()
	n, err := m.q.InsertIdempotencyKey(ctx, gen.InsertIdempotencyKeyParams{
		UserID: uid, Key: key, Method: method, Route: route, RequestHash: hash, LockedUntil: now.Add(IdempotencyLock),
	})
	if err != nil {
		return false, fmt.Errorf("insert key: %w", err)
	}
	if n == 1 {
		return true, nil
	}
	row, err := m.q.GetIdempotencyKey(ctx, gen.GetIdempotencyKeyParams{UserID: uid, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		// Deleted between our insert and read (a 5xx first attempt): retry.
		WriteProblem(w, r, NewProblem(CodeRequestInProgress, "retry the request").WithRetryAfter(time.Second))
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get key: %w", err)
	}
	if !bytes.Equal(row.RequestHash, hash) {
		WriteError(w, r, CodeIdempotencyKeyReused, "this Idempotency-Key was used with a different request")
		return false, nil
	}
	if row.Status == "COMPLETED" && row.ResponseCode != nil {
		h := w.Header()
		h.Set(ReplayedHeader, "true")
		h.Set("Cache-Control", "no-store")
		ct := "application/json"
		if *row.ResponseCode >= 400 {
			ct = ProblemContentType
		}
		if len(row.ResponseBody) > 0 {
			h.Set("Content-Type", ct)
		}
		w.WriteHeader(int(*row.ResponseCode))
		if len(row.ResponseBody) > 0 {
			_, _ = w.Write(row.ResponseBody)
			_, _ = w.Write([]byte{'\n'})
		}
		return false, nil
	}
	if row.LockedUntil.Before(now) {
		n, err := m.q.ReclaimIdempotencyKey(ctx, gen.ReclaimIdempotencyKeyParams{
			LockedUntil: now.Add(IdempotencyLock), UserID: uid, Key: key, Now: now,
		})
		if err != nil {
			return false, fmt.Errorf("reclaim key: %w", err)
		}
		if n == 1 {
			return true, nil
		}
	}
	WriteProblem(w, r, NewProblem(CodeRequestInProgress, "").WithRetryAfter(time.Second))
	return false, nil
}

// finish stores the response (status < 500) or drops the key (5xx). It runs
// detached from the client's cancellation.
func (m *Idempotency) finish(ctx context.Context, uid uuid.UUID, key string, rec *recorder) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), idempotencyStoreTTL)
	defer cancel()
	if rec.status >= 500 {
		if _, err := m.q.DeleteIdempotencyKey(ctx, gen.DeleteIdempotencyKeyParams{UserID: uid, Key: key}); err != nil {
			m.log.ErrorContext(ctx, "idempotency delete", slog.String("error", err.Error()))
		}
		return
	}
	var body []byte
	if b := bytes.TrimSpace(rec.body.Bytes()); len(b) > 0 && json.Valid(b) {
		body = b
	}
	code := int32(rec.status) //nolint:gosec // G115: HTTP status codes fit in int32
	if _, err := m.q.CompleteIdempotencyKey(ctx, gen.CompleteIdempotencyKeyParams{
		ResponseCode: &code, ResponseBody: body, UserID: uid, Key: key,
	}); err != nil {
		// The row stays STARTED; after its lock lapses a retry re-executes,
		// which the handlers make safe.
		m.log.ErrorContext(ctx, "idempotency complete", slog.String("error", err.Error()))
	}
}

// GCIdempotencyKeys deletes keys older than 24 h (worker job).
func GCIdempotencyKeys(ctx context.Context, q *gen.Queries, now time.Time) (int64, error) {
	n, err := q.GCIdempotencyKeys(ctx, now.Add(-IdempotencyKeyTTL))
	if err != nil {
		return 0, fmt.Errorf("idempotency gc: %w", err)
	}
	return n, nil
}

// recorder buffers a handler's response so it can be stored before it is
// sent.
type recorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return r.body.Write(b)
}
