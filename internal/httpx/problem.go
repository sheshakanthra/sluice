package httpx

import (
	"encoding/json"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sheshakanthra/sluice/internal/obs"
)

// Code is a stable machine-readable error code (API.md §1.3).
type Code string

// Error code catalogue. Keep in sync with docs/API.md §1.3.
const (
	CodeValidation               Code = "VALIDATION"
	CodeIdempotencyKeyRequired   Code = "IDEMPOTENCY_KEY_REQUIRED"
	CodeTokenInvalid             Code = "TOKEN_INVALID"
	CodeTokenExpired             Code = "TOKEN_EXPIRED"
	CodeNotAdmitted              Code = "NOT_ADMITTED"
	CodeAdmissionExpired         Code = "ADMISSION_EXPIRED"
	CodeNotFound                 Code = "NOT_FOUND"
	CodeSeatsTaken               Code = "SEATS_TAKEN"
	CodeInsufficientSeats        Code = "INSUFFICIENT_SEATS"
	CodeSoldOut                  Code = "SOLD_OUT"
	CodeHoldLimit                Code = "HOLD_LIMIT"
	CodeHoldExpired              Code = "HOLD_EXPIRED"
	CodeRejoinLimit              Code = "REJOIN_LIMIT"
	CodeEventState               Code = "EVENT_STATE"
	CodeOrderState               Code = "ORDER_STATE"
	CodeRequestInProgress        Code = "REQUEST_IN_PROGRESS"
	CodeQuotaExceeded            Code = "QUOTA_EXCEEDED"
	CodeIdempotencyKeyReused     Code = "IDEMPOTENCY_KEY_REUSED"
	CodePaymentAttemptsExhausted Code = "PAYMENT_ATTEMPTS_EXHAUSTED"
	CodeRateLimited              Code = "RATE_LIMITED"
	CodeDependencyUnavailable    Code = "DEPENDENCY_UNAVAILABLE"
	// CodeInternal is only for unexpected faults (recovered panics, bugs).
	// Business outcomes never use it (I8). See docs/adr/0003-internal-error-code.md.
	CodeInternal Code = "INTERNAL"
)

type codeInfo struct {
	status int
	title  string
}

var catalogue = map[Code]codeInfo{
	CodeValidation:               {http.StatusBadRequest, "The request is invalid"},
	CodeIdempotencyKeyRequired:   {http.StatusBadRequest, "An Idempotency-Key header is required"},
	CodeTokenInvalid:             {http.StatusUnauthorized, "The token is invalid"},
	CodeTokenExpired:             {http.StatusUnauthorized, "The token has expired"},
	CodeNotAdmitted:              {http.StatusForbidden, "You have not been admitted from the queue yet"},
	CodeAdmissionExpired:         {http.StatusForbidden, "Your admission has ended; rejoin the queue"},
	CodeNotFound:                 {http.StatusNotFound, "Not found"},
	CodeSeatsTaken:               {http.StatusConflict, "Some seats were just taken"},
	CodeInsufficientSeats:        {http.StatusConflict, "Not enough seats left"},
	CodeSoldOut:                  {http.StatusConflict, "Sold out"},
	CodeHoldLimit:                {http.StatusConflict, "You already hold seats for this event"},
	CodeHoldExpired:              {http.StatusConflict, "Your hold has expired"},
	CodeRejoinLimit:              {http.StatusConflict, "You have already rejoined the queue the maximum number of times"},
	CodeEventState:               {http.StatusConflict, "The event is not in a state that allows this"},
	CodeOrderState:               {http.StatusConflict, "That action is not allowed in the order's current state"},
	CodeRequestInProgress:        {http.StatusConflict, "A request with this Idempotency-Key is still in progress"},
	CodeQuotaExceeded:            {http.StatusUnprocessableEntity, "This would exceed the per-user ticket limit"},
	CodeIdempotencyKeyReused:     {http.StatusUnprocessableEntity, "This Idempotency-Key was used for a different request"},
	CodePaymentAttemptsExhausted: {http.StatusUnprocessableEntity, "No payment attempts left for this order"},
	CodeRateLimited:              {http.StatusTooManyRequests, "Too many requests"},
	CodeDependencyUnavailable:    {http.StatusServiceUnavailable, "A dependency is temporarily unavailable"},
	CodeInternal:                 {http.StatusInternalServerError, "Something went wrong on our side"},
}

// Status returns the HTTP status for code (500 for unknown codes).
func (c Code) Status() int {
	if ci, ok := catalogue[c]; ok {
		return ci.status
	}
	return http.StatusInternalServerError
}

// TypeURI returns the problem type URI, e.g. https://sluice.dev/errors/seats-taken.
func (c Code) TypeURI() string {
	return "https://sluice.dev/errors/" + strings.ReplaceAll(strings.ToLower(string(c)), "_", "-")
}

// ProblemContentType is the RFC 9457 media type.
const ProblemContentType = "application/problem+json"

// Problem is an RFC 9457 problem details object. Extensions are merged into
// the top-level JSON object (for example "taken": [12, 13]).
type Problem struct {
	Type       string
	Title      string
	Status     int
	Code       Code
	Detail     string
	RequestID  string
	Extensions map[string]any
	// RetryAfter, when > 0, is sent as a Retry-After header (whole seconds).
	RetryAfter time.Duration
}

// NewProblem builds a Problem from the catalogue entry for code.
func NewProblem(code Code, detail string) *Problem {
	ci, ok := catalogue[code]
	if !ok {
		ci = catalogue[CodeInternal]
	}
	return &Problem{Type: code.TypeURI(), Title: ci.title, Status: ci.status, Code: code, Detail: detail}
}

// With adds an extension member and returns p for chaining.
func (p *Problem) With(key string, value any) *Problem {
	if p.Extensions == nil {
		p.Extensions = map[string]any{}
	}
	p.Extensions[key] = value
	return p
}

// WithRetryAfter sets the Retry-After header and returns p.
func (p *Problem) WithRetryAfter(d time.Duration) *Problem {
	p.RetryAfter = d
	return p
}

// MarshalJSON flattens extensions next to the standard members. Standard
// members always win over an extension with the same name.
func (p *Problem) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, 6+len(p.Extensions))
	maps.Copy(m, p.Extensions)
	m["type"] = p.Type
	m["title"] = p.Title
	m["status"] = p.Status
	m["code"] = p.Code
	if p.Detail != "" {
		m["detail"] = p.Detail
	}
	if p.RequestID != "" {
		m["request_id"] = p.RequestID
	}
	return json.Marshal(m)
}

// WriteProblem writes p as application/problem+json, filling request_id from
// the request context.
func WriteProblem(w http.ResponseWriter, r *http.Request, p *Problem) {
	if p.RequestID == "" {
		p.RequestID = obs.RequestID(r.Context())
	}
	h := w.Header()
	h.Set("Content-Type", ProblemContentType)
	h.Set("Cache-Control", "no-store")
	if p.RetryAfter > 0 {
		secs := int((p.RetryAfter + time.Second - 1) / time.Second)
		h.Set("Retry-After", strconv.Itoa(secs))
	}
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p) // the client may have gone; nothing useful to do
}

// WriteError is a shortcut for WriteProblem(w, r, NewProblem(code, detail)).
func WriteError(w http.ResponseWriter, r *http.Request, code Code, detail string) {
	WriteProblem(w, r, NewProblem(code, detail))
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the client may have gone; nothing useful to do
}
