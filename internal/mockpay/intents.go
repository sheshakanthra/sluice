package mockpay

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/mockpay/mpdb"
	"github.com/sheshakanthra/sluice/internal/payments"
)

const maxBody = 64 << 10

// Ledger kinds.
const (
	kindAuth    = "AUTH"
	kindCapture = "CAPTURE"
	kindVoid    = "VOID"
	kindRefund  = "REFUND"
)

// op is a state-changing operation on an existing intent.
type op string

const (
	opConfirm op = "confirm"
	opCapture op = "capture"
	opVoid    op = "void"
	opRefund  op = "refund"
)

// apiError is a 4xx outcome stored (and replayed) like a success.
type apiError struct {
	status int
	code   string
	msg    string
	intent *payments.Intent
}

func (e *apiError) Error() string { return e.code + ": " + e.msg }

func newID(prefix string) string {
	u := uuid.Must(uuid.NewV7())
	return prefix + hex.EncodeToString(u[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string, in *payments.Intent) {
	writeJSON(w, status, payments.ErrorBody{Error: payments.ErrorDetail{Code: code, Message: msg}, Intent: in})
}

func (s *Server) wire(i mpdb.Intent, authorized bool) payments.Intent {
	md := map[string]string{}
	_ = json.Unmarshal(i.Metadata, &md)
	return payments.Intent{
		ID: i.ID, Status: i.Status, MerchantRef: i.MerchantRef, AmountPaise: i.AmountPaise, Currency: i.Currency,
		Metadata: md, CheckoutURL: s.o.PublicURL + "/checkout/" + i.ID, Authorized: authorized,
		CreatedAt: i.CreatedAt.UTC(), UpdatedAt: i.UpdatedAt.UTC(),
	}
}

// faults decided for one request, before any work.
type faults struct {
	fail, hang, dup, reorder bool
}

func (s *Server) decideFaults(r *http.Request, method string) faults {
	var f faults
	if s.o.AllowFaultHeader {
		switch r.Header.Get(FaultHeader) {
		case FaultFail:
			f.fail = true
		case FaultTimeoutAfterCommit:
			f.hang = true
		case FaultDupWebhook:
			f.dup = true
		case FaultReorderWebhook:
			f.reorder = true
		}
	}
	//nolint:gosec // G404: fault injection, not security
	if s.o.FailRate > 0 && rand.Float64() < s.o.FailRate {
		f.fail = true
	}
	if s.o.TimeoutAfterCommitRate > 0 && rand.Float64() < s.o.TimeoutAfterCommitRate { //nolint:gosec // G404: fault injection
		f.hang = true
	}
	if method == payments.MethodTimeout {
		f.hang = true
	}
	return f
}

func (s *Server) latency(ctx context.Context) {
	if s.o.LatencyMax <= 0 {
		return
	}
	d := s.o.LatencyMin
	if span := s.o.LatencyMax - s.o.LatencyMin; span > 0 {
		d += time.Duration(rand.Int64N(int64(span) + 1)) //nolint:gosec // G404: simulated latency
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// hang holds a committed response back for o.Hang (the classic
// double-charge trap: the work is done, the caller times out).
func (s *Server) hang(ctx context.Context) {
	s.metrics.faults.WithLabelValues(FaultTimeoutAfterCommit).Inc()
	t := time.NewTimer(s.o.Hang)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// result is what an idempotent operation answers.
type result struct {
	status int
	body   []byte
	// fresh is false for a replay of a stored response.
	fresh bool
}

// idempotent runs fn under (merchant, key) in one transaction: a stored
// response is replayed (422 if the request hash differs); otherwise fn runs
// and its response is stored in the same transaction, so "committed" always
// means "the same key returns this answer from now on".
func (s *Server) idempotent(ctx context.Context, key string, hash []byte, fn func(ctx context.Context, q *mpdb.Queries) (int, any, error)) (result, error) {
	var res result
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		res = result{}
		q := mpdb.New(tx)
		if err := q.LockKey(ctx, "idem:"+MerchantID+":"+key); err != nil {
			return fmt.Errorf("lock key: %w", err)
		}
		stored, err := q.GetIdempotency(ctx, mpdb.GetIdempotencyParams{MerchantID: MerchantID, Key: key})
		if err == nil {
			if !bytes.Equal(stored.RequestHash, hash) {
				b, _ := json.Marshal(payments.ErrorBody{Error: payments.ErrorDetail{
					Code: payments.ErrCodeIdempotencyUsed, Message: "this key was used with a different request"}})
				res = result{status: http.StatusUnprocessableEntity, body: b}
				return nil
			}
			res = result{status: int(stored.ResponseCode), body: stored.ResponseBody}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get idempotency: %w", err)
		}
		status, v, err := fn(ctx, q)
		var ae *apiError
		if errors.As(err, &ae) {
			status = ae.status
			v = payments.ErrorBody{Error: payments.ErrorDetail{Code: ae.code, Message: ae.msg}, Intent: ae.intent}
		} else if err != nil {
			return err
		}
		body, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("encode response: %w", err)
		}
		//nolint:gosec // G115: HTTP status codes fit in int32
		if err := q.InsertIdempotency(ctx, mpdb.InsertIdempotencyParams{MerchantID: MerchantID, Key: key,
			RequestHash: hash, ResponseCode: int32(status), ResponseBody: body}); err != nil {
			return fmt.Errorf("store idempotency: %w", err)
		}
		res = result{status: status, body: body, fresh: true}
		return nil
	})
	return res, err
}

func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(b) > maxBody {
		writeErr(w, http.StatusBadRequest, payments.ErrCodeInvalid, "body unreadable or too large", nil)
		return nil, false
	}
	return b, true
}

// serve runs an idempotent mutating call end to end: latency, pre-commit
// failure, the transaction, then (fresh executions only) the hang fault.
func (s *Server) serve(w http.ResponseWriter, r *http.Request, name, key string, body []byte, f faults,
	fn func(ctx context.Context, q *mpdb.Queries) (int, any, error),
) {
	ctx := r.Context()
	s.latency(ctx)
	if f.fail {
		s.metrics.faults.WithLabelValues(FaultFail).Inc()
		s.metrics.requests.WithLabelValues(name, "fault_5xx").Inc()
		writeErr(w, http.StatusServiceUnavailable, payments.ErrCodeUnavailable, "injected failure (nothing was committed)", nil)
		return
	}
	hash := httpx.RequestHash(r.Method, r.Pattern, body)
	res, err := s.idempotent(context.WithoutCancel(ctx), key, hash, fn)
	if err != nil {
		s.o.Log.ErrorContext(ctx, "mockpay "+name, "error", err.Error())
		s.metrics.requests.WithLabelValues(name, "error").Inc()
		writeErr(w, http.StatusServiceUnavailable, payments.ErrCodeUnavailable, "temporarily unavailable", nil)
		return
	}
	switch {
	case !res.fresh:
		s.metrics.requests.WithLabelValues(name, "replay").Inc()
	default:
		s.metrics.requests.WithLabelValues(name, strconv.Itoa(res.status)).Inc()
		if f.hang {
			s.hang(ctx)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if !res.fresh {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.WriteHeader(res.status)
	_, _ = w.Write(res.body)
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if !httpx.ValidIdempotencyKey(key) {
		writeErr(w, http.StatusBadRequest, payments.ErrCodeInvalid, "an Idempotency-Key header (8-128 chars) is required", nil)
		return
	}
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var req payments.CreateIntentRequest
	if err := json.Unmarshal(body, &req); err != nil || req.AmountPaise <= 0 || len(req.Currency) != 3 || req.MerchantRef == "" ||
		(req.PaymentMethod != "" && !payments.ValidMethod(req.PaymentMethod)) || (req.Confirm && req.PaymentMethod == "") {
		writeErr(w, http.StatusBadRequest, payments.ErrCodeInvalid,
			"need amount_paise > 0, a 3-letter currency, merchant_ref, and a valid payment_method when confirm=true", nil)
		return
	}
	f := s.decideFaults(r, req.PaymentMethod)
	if req.Confirm && req.PaymentMethod == payments.MethodSlow {
		s.sleep(r.Context(), s.o.SlowDelay)
	}
	hash := httpx.RequestHash(r.Method, r.Pattern, body)
	s.serve(w, r, "create", key, body, f, func(ctx context.Context, q *mpdb.Queries) (int, any, error) {
		md, err := json.Marshal(req.Metadata)
		if err != nil || req.Metadata == nil {
			md = []byte("{}")
		}
		in, err := q.InsertIntent(ctx, mpdb.InsertIntentParams{
			ID: newID("pi_"), MerchantID: MerchantID, IdempotencyKey: key, RequestHash: hash,
			MerchantRef: req.MerchantRef, Metadata: md, AmountPaise: req.AmountPaise, Currency: req.Currency,
			Status: payments.StatusRequiresPayment, ReturnUrl: req.ReturnURL,
		})
		if err != nil {
			return 0, nil, fmt.Errorf("insert intent: %w", err)
		}
		authorized := false
		if req.Confirm {
			if in, authorized, err = s.apply(ctx, q, opConfirm, in, req.PaymentMethod, key, f); err != nil {
				return 0, nil, err
			}
		}
		return http.StatusCreated, s.wire(in, authorized), nil
	})
}

func (s *Server) handleOp(o op) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if !httpx.ValidIdempotencyKey(key) {
			writeErr(w, http.StatusBadRequest, payments.ErrCodeInvalid, "an Idempotency-Key header (8-128 chars) is required", nil)
			return
		}
		body, ok := s.readBody(w, r)
		if !ok {
			return
		}
		method := ""
		if o == opConfirm {
			var req payments.ConfirmRequest
			if err := json.Unmarshal(body, &req); err != nil || !payments.ValidMethod(req.PaymentMethod) {
				writeErr(w, http.StatusBadRequest, payments.ErrCodeInvalid, "payment_method must be pm_ok, pm_decline, pm_timeout or pm_slow", nil)
				return
			}
			method = req.PaymentMethod
		}
		f := s.decideFaults(r, method)
		if method == payments.MethodSlow {
			s.sleep(r.Context(), s.o.SlowDelay)
		}
		id := r.PathValue("id")
		s.serve(w, r, string(o), key, body, f, func(ctx context.Context, q *mpdb.Queries) (int, any, error) {
			in, err := q.GetIntentForUpdate(ctx, id)
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, nil, &apiError{status: http.StatusNotFound, code: payments.ErrCodeNotFound, msg: "no such intent"}
			}
			if err != nil {
				return 0, nil, fmt.Errorf("lock intent: %w", err)
			}
			in, authorized, err := s.apply(ctx, q, o, in, method, key, f)
			if err != nil {
				return 0, nil, err
			}
			return http.StatusOK, s.wire(in, authorized), nil
		})
	}
}

// apply runs op on a locked intent: the state change, its ledger entry and
// its webhook, all in the caller's transaction. Operations that already
// happened return the intent unchanged (state-level idempotency on top of
// key-level idempotency).
func (s *Server) apply(ctx context.Context, q *mpdb.Queries, o op, in mpdb.Intent, method, key string, f faults) (mpdb.Intent, bool, error) {
	authorized, err := q.IntentAuthorized(ctx, in.ID)
	if err != nil {
		return in, false, fmt.Errorf("intent authorized: %w", err)
	}
	stateErr := func() error {
		w := s.wire(in, authorized)
		return &apiError{status: http.StatusConflict, code: payments.ErrCodeIntentState,
			msg: fmt.Sprintf("cannot %s an intent that is %s", o, in.Status), intent: &w}
	}
	var (
		to   string
		kind string
	)
	switch o {
	case opConfirm:
		switch in.Status {
		case payments.StatusRequiresPayment:
			if method == payments.MethodDecline {
				to = payments.StatusFailed
			} else {
				to, kind = payments.StatusAuthorized, kindAuth
			}
		case payments.StatusAuthorized, payments.StatusCaptured:
			return in, authorized, nil
		default:
			return in, authorized, stateErr()
		}
	case opCapture:
		switch in.Status {
		case payments.StatusAuthorized:
			to, kind = payments.StatusCaptured, kindCapture
		case payments.StatusCaptured:
			return in, authorized, nil
		default:
			return in, authorized, stateErr()
		}
	case opVoid:
		switch in.Status {
		case payments.StatusRequiresPayment:
			to = payments.StatusCanceled // nothing was authorized: no ledger entry
		case payments.StatusAuthorized:
			to, kind = payments.StatusCanceled, kindVoid
		case payments.StatusCanceled, payments.StatusFailed:
			return in, authorized, nil
		default:
			return in, authorized, stateErr()
		}
	case opRefund:
		switch in.Status {
		case payments.StatusCaptured:
			to, kind = payments.StatusRefunded, kindRefund
		case payments.StatusRefunded:
			return in, authorized, nil
		default:
			return in, authorized, stateErr()
		}
	}
	if err := q.SetIntentStatus(ctx, mpdb.SetIntentStatusParams{ID: in.ID, Status: to}); err != nil {
		return in, false, fmt.Errorf("set status: %w", err)
	}
	if kind != "" {
		n, err := q.InsertLedger(ctx, mpdb.InsertLedgerParams{IntentID: in.ID, Kind: kind, AmountPaise: in.AmountPaise, IdempotencyKey: key})
		if err != nil {
			return in, false, fmt.Errorf("ledger: %w", err)
		}
		if n == 1 {
			s.metrics.ledger.WithLabelValues(kind).Inc()
		}
		if kind == kindAuth {
			authorized = true
		}
	}
	in.Status, in.UpdatedAt = to, s.o.Now()
	if err := s.enqueueWebhook(ctx, q, s.wire(in, authorized), f); err != nil {
		return in, false, err
	}
	return in, authorized, nil
}

func (s *Server) sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	s.latency(r.Context())
	in, err := s.q.GetIntent(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, payments.ErrCodeNotFound, "no such intent", nil)
		return
	}
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, payments.ErrCodeUnavailable, "temporarily unavailable", nil)
		return
	}
	authorized, err := s.q.IntentAuthorized(r.Context(), in.ID)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, payments.ErrCodeUnavailable, "temporarily unavailable", nil)
		return
	}
	s.metrics.requests.WithLabelValues("get", "200").Inc()
	writeJSON(w, http.StatusOK, s.wire(in, authorized))
}

const maxListRows = 1000

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	s.latency(r.Context())
	p := mpdb.ListIntentsParams{MerchantID: MerchantID, MaxRows: maxListRows}
	qs := r.URL.Query()
	if v := qs.Get("merchant_ref"); v != "" {
		p.MerchantRef = &v
	}
	if v := qs.Get("status"); v != "" {
		p.Status = &v
	}
	if v := qs.Get("updated_since"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, payments.ErrCodeInvalid, "updated_since must be RFC 3339", nil)
			return
		}
		p.UpdatedSince = &t
	}
	rows, err := s.q.ListIntents(r.Context(), p)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, payments.ErrCodeUnavailable, "temporarily unavailable", nil)
		return
	}
	out := make([]payments.Intent, 0, len(rows))
	for i := range rows {
		x := &rows[i]
		out = append(out, s.wire(mpdb.Intent{ID: x.ID, MerchantID: x.MerchantID, MerchantRef: x.MerchantRef, Metadata: x.Metadata,
			AmountPaise: x.AmountPaise, Currency: x.Currency, Status: x.Status, CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt}, x.Authorized))
	}
	s.metrics.requests.WithLabelValues("list", "200").Inc()
	writeJSON(w, http.StatusOK, map[string]any{"intents": out})
}

func (s *Server) handleLedgerSummary(w http.ResponseWriter, r *http.Request) {
	p := mpdb.LedgerSummaryParams{MerchantID: MerchantID}
	qs := r.URL.Query()
	if v := qs.Get("metadata.event_id"); v != "" {
		p.EventID = &v
	}
	if v := qs.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, payments.ErrCodeInvalid, "since must be RFC 3339", nil)
			return
		}
		p.Since = &t
	}
	rows, err := s.q.LedgerSummary(r.Context(), p)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, payments.ErrCodeUnavailable, "temporarily unavailable", nil)
		return
	}
	out := payments.LedgerSummary{Refs: make([]payments.LedgerSummaryRow, 0, len(rows))}
	for _, x := range rows {
		out.Refs = append(out.Refs, payments.LedgerSummaryRow{MerchantRef: x.MerchantRef, Intents: x.Intents,
			Authorized: x.Authorized, Captured: x.Captured, Voided: x.Voided, Refunded: x.Refunded})
	}
	writeJSON(w, http.StatusOK, out)
}
