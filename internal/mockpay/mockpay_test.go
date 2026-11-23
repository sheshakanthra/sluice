package mockpay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/sheshakanthra/sluice/internal/mockpay"
	"github.com/sheshakanthra/sluice/internal/mockpay/mpdb"
	"github.com/sheshakanthra/sluice/internal/payments"
	"github.com/sheshakanthra/sluice/internal/testutil/pgtest"
)

var pgs *pgtest.Server

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	var err error
	pgs, err = pgtest.Start(ctx)
	cancel()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "postgres:", err)
		os.Exit(1)
	}
	code := m.Run()
	pgs.Close()
	os.Exit(code)
}

const (
	key    = "test-mockpay-key"
	secret = "test-webhook-secret"
)

type clock struct{ off atomic.Int64 }

func (c *clock) now() time.Time          { return time.Now().Add(time.Duration(c.off.Load())) }
func (c *clock) advance(d time.Duration) { c.off.Add(int64(d)) }

type env struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	q    *mpdb.Queries
	srv  *mockpay.Server
	http *httptest.Server
	psp  *payments.Client
	clk  *clock
	// hook receives webhooks; status answers them (200 by default).
	hook     *httptest.Server
	received chan receivedHook
	status   atomic.Int32
}

type receivedHook struct {
	eventID, sigErr string
	ev              payments.WebhookEvent
}

func newEnv(t *testing.T, mod func(*mockpay.Options)) *env {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	e := &env{t: t, ctx: ctx, clk: &clock{}, received: make(chan receivedHook, 64)}
	e.status.Store(http.StatusOK)
	e.hook = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rh := receivedHook{eventID: r.Header.Get(payments.EventIDHeader)}
		if err := payments.VerifySignature(secret, r.Header.Get(payments.SignatureHeader), body, e.clk.now(), payments.SignatureTolerance); err != nil {
			rh.sigErr = err.Error()
		}
		_ = json.Unmarshal(body, &rh.ev)
		e.received <- rh
		w.WriteHeader(int(e.status.Load()))
	}))
	t.Cleanup(e.hook.Close)
	e.pool, _ = pgs.NewDB(t)
	if err := mockpay.Migrate(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	e.q = mpdb.New(e.pool)
	var h http.Handler
	e.http = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	t.Cleanup(e.http.Close)
	o := mockpay.Options{APIKey: key, WebhookURL: e.hook.URL, WebhookSecret: secret, PublicURL: e.http.URL,
		Hang: 600 * time.Millisecond, AllowFaultHeader: true, Now: e.clk.now, DispatchInterval: 20 * time.Millisecond}
	if mod != nil {
		mod(&o)
	}
	var err error
	e.srv, err = mockpay.New(ctx, e.pool, prometheus.NewRegistry(), o)
	if err != nil {
		t.Fatal(err)
	}
	h = e.srv.Handler()
	e.psp, err = payments.NewClient(payments.Options{BaseURL: e.http.URL, PublicURL: e.http.URL, APIKey: key,
		Timeout: 2 * time.Second, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) create(k, ref string) payments.Intent {
	e.t.Helper()
	in, err := e.psp.CreateIntent(e.ctx, "test-key:"+k, payments.CreateIntentRequest{AmountPaise: 150_000, Currency: "INR",
		MerchantRef: ref, ReturnURL: "http://app.test/order?o=" + ref, Metadata: map[string]string{"event_id": "ev1"}})
	if err != nil {
		e.t.Fatal(err)
	}
	return in
}

type raw struct {
	code int
	hdr  http.Header
	body []byte
}

func (e *env) do(method, path, idem string, body any, hdr map[string]string) raw {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(e.ctx, method, e.http.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return raw{res.StatusCode, res.Header, b}
}

func (e *env) ledger(id string) map[string]int {
	e.t.Helper()
	rows, err := e.q.ListLedgerByIntent(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]int{}
	for _, r := range rows {
		out[r.Kind]++
	}
	return out
}

// Same key, same body, concurrently: one intent, every answer identical.
// Same key, different body: 422 idempotency_key_reused.
func TestCreateIdempotent(t *testing.T) {
	e := newEnv(t, nil)
	const n = 12
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { ids[i] = e.create("order:x:attempt:1", "ref-1").ID })
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("different intents for one key: %v", ids)
		}
	}
	rows, err := e.q.ListIntents(e.ctx, mpdb.ListIntentsParams{MerchantID: mockpay.MerchantID, MaxRows: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d intents, want 1", len(rows))
	}
	r := e.do(http.MethodPost, "/v1/intents", "test-key:order:x:attempt:1",
		payments.CreateIntentRequest{AmountPaise: 1, Currency: "INR", MerchantRef: "ref-1"}, nil)
	if r.code != http.StatusUnprocessableEntity || !strings.Contains(string(r.body), payments.ErrCodeIdempotencyUsed) {
		t.Fatalf("reused key: HTTP %d %s", r.code, r.body)
	}
	_, err = e.psp.CreateIntent(e.ctx, "test-key:order:x:attempt:1", payments.CreateIntentRequest{AmountPaise: 1, Currency: "INR", MerchantRef: "ref-1"})
	if !errors.Is(err, payments.ErrKeyReused) {
		t.Fatalf("client err = %v", err)
	}
	if r := e.do(http.MethodPost, "/v1/intents", "", payments.CreateIntentRequest{AmountPaise: 1, Currency: "INR", MerchantRef: "r"}, nil); r.code != http.StatusBadRequest {
		t.Fatalf("missing key: HTTP %d", r.code)
	}
	req, _ := http.NewRequestWithContext(e.ctx, http.MethodGet, e.http.URL+"/v1/intents/"+ids[0], http.NoBody)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no API key: HTTP %d", res.StatusCode)
	}
}

// The intent state machine and its ledger, one table row per path.
func TestIntentStateMachine(t *testing.T) {
	type step struct {
		op, method string
		wantStatus string // "" = expect a 409 state error
	}
	tests := []struct {
		name   string
		steps  []step
		ledger map[string]int
	}{
		{"pay capture", []step{{"confirm", payments.MethodOK, payments.StatusAuthorized}, {"capture", "", payments.StatusCaptured}},
			map[string]int{"AUTH": 1, "CAPTURE": 1}},
		{"decline", []step{{"confirm", payments.MethodDecline, payments.StatusFailed}, {"capture", "", ""}},
			map[string]int{}},
		{"void authorized", []step{{"confirm", payments.MethodOK, payments.StatusAuthorized}, {"void", "", payments.StatusCanceled},
			{"void", "", payments.StatusCanceled}, {"capture", "", ""}}, map[string]int{"AUTH": 1, "VOID": 1}},
		{"void unpaid: no ledger entry", []step{{"void", "", payments.StatusCanceled}, {"confirm", payments.MethodOK, ""}},
			map[string]int{}},
		{"void captured is refused", []step{{"confirm", payments.MethodOK, payments.StatusAuthorized}, {"capture", "", payments.StatusCaptured},
			{"void", "", ""}}, map[string]int{"AUTH": 1, "CAPTURE": 1}},
		{"refund", []step{{"confirm", payments.MethodOK, payments.StatusAuthorized}, {"capture", "", payments.StatusCaptured},
			{"refund", "", payments.StatusRefunded}, {"refund", "", payments.StatusRefunded}},
			map[string]int{"AUTH": 1, "CAPTURE": 1, "REFUND": 1}},
		{"refund before capture is refused", []step{{"confirm", payments.MethodOK, payments.StatusAuthorized}, {"refund", "", ""}},
			map[string]int{"AUTH": 1}},
		{"confirm twice is a no-op", []step{{"confirm", payments.MethodOK, payments.StatusAuthorized},
			{"confirm", payments.MethodOK, payments.StatusAuthorized}, {"capture", "", payments.StatusCaptured},
			{"capture", "", payments.StatusCaptured}}, map[string]int{"AUTH": 1, "CAPTURE": 1}},
	}
	e := newEnv(t, nil)
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := e.create(fmt.Sprintf("sm:%d", i), fmt.Sprintf("ref-sm-%d", i))
			for j, s := range tc.steps {
				var (
					got payments.Intent
					err error
				)
				switch s.op {
				case "confirm":
					got, err = e.psp.Confirm(e.ctx, in.ID, s.method)
				case "capture":
					got, err = e.psp.Capture(e.ctx, in.ID)
				case "void":
					got, err = e.psp.Void(e.ctx, in.ID)
				case "refund":
					got, err = e.psp.Refund(e.ctx, in.ID)
				}
				if s.wantStatus == "" {
					if !errors.Is(err, payments.ErrIntentState) {
						t.Fatalf("step %d %s: err = %v, want a state error", j, s.op, err)
					}
					continue
				}
				if err != nil || got.Status != s.wantStatus {
					t.Fatalf("step %d %s: %s err=%v, want %s", j, s.op, got.Status, err, s.wantStatus)
				}
			}
			l := e.ledger(in.ID)
			for _, k := range []string{"AUTH", "CAPTURE", "VOID", "REFUND"} {
				if l[k] != tc.ledger[k] {
					t.Fatalf("ledger %v, want %v", l, tc.ledger)
				}
			}
		})
	}
}

// The ledger is append-only in the database itself.
func TestLedgerAppendOnly(t *testing.T) {
	e := newEnv(t, nil)
	in := e.create("ao:1", "ref-ao")
	if _, err := e.psp.Confirm(e.ctx, in.ID, payments.MethodOK); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE ledger_entries SET amount_paise = 0`,
		`DELETE FROM ledger_entries`,
		`TRUNCATE ledger_entries`,
	} {
		// Test-only statements that must be rejected.
		_, err := e.pool.Exec(e.ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("%s: err = %v, want 42501", stmt, err)
		}
	}
	// Second AUTH for one intent: the UNIQUE(intent_id, kind) guard.
	_, err := e.pool.Exec(e.ctx, `INSERT INTO ledger_entries (intent_id, kind, amount_paise, idempotency_key) VALUES ($1, 'AUTH', 1, 'x')`, in.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("duplicate AUTH: err = %v", err)
	}
}

// timeout_after_commit: the state change commits, then the response hangs
// past the caller's timeout. A retry with the same key answers at once
// with the committed result.
func TestTimeoutAfterCommit(t *testing.T) {
	e := newEnv(t, nil)
	in := e.create("tac:1", "ref-tac")
	short, err := payments.NewClient(payments.Options{BaseURL: e.http.URL, APIKey: key, Timeout: 200 * time.Millisecond, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = short.Confirm(e.ctx, in.ID, payments.MethodTimeout)
	if !errors.Is(err, payments.ErrUnavailable) {
		t.Fatalf("first confirm err = %v, want a timeout", err)
	}
	got, err := e.psp.GetIntent(e.ctx, in.ID)
	if err != nil || got.Status != payments.StatusAuthorized {
		t.Fatalf("after the timeout: %s %v (the authorization must have committed)", got.Status, err)
	}
	start := time.Now()
	again, err := short.Confirm(e.ctx, in.ID, payments.MethodTimeout)
	if err != nil || again.Status != payments.StatusAuthorized {
		t.Fatalf("retry: %+v %v", again, err)
	}
	if d := time.Since(start); d > 150*time.Millisecond {
		t.Fatalf("replay took %s; replays must not hang", d)
	}
	if l := e.ledger(in.ID); l["AUTH"] != 1 {
		t.Fatalf("ledger %v", l)
	}
	// The header fault does the same for pm_ok.
	in2 := e.create("tac:2", "ref-tac-2")
	r := make(chan raw, 1)
	go func() {
		r <- e.do(http.MethodPost, "/v1/intents/"+in2.ID+"/capture", "", nil, nil)
	}()
	<-r // capture of an unconfirmed intent: 409, no hang
	ctx, cancel := context.WithTimeout(e.ctx, 200*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.http.URL+"/v1/intents/"+in2.ID+"/confirm",
		strings.NewReader(`{"payment_method":"pm_ok"}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Idempotency-Key", payments.ConfirmKey(in2.ID))
	req.Header.Set(mockpay.FaultHeader, mockpay.FaultTimeoutAfterCommit)
	if res, err := http.DefaultClient.Do(req); err == nil {
		_ = res.Body.Close()
		t.Fatalf("header fault: answered HTTP %d before the hang", res.StatusCode)
	}
	if got, _ := e.psp.GetIntent(e.ctx, in2.ID); got.Status != payments.StatusAuthorized {
		t.Fatalf("header fault: %s, want authorized", got.Status)
	}
}

// fail: 503 before anything commits; the fault header is ignored when not
// allowed (production).
func TestFailFault(t *testing.T) {
	e := newEnv(t, nil)
	in := e.create("ff:1", "ref-ff")
	r := e.do(http.MethodPost, "/v1/intents/"+in.ID+"/confirm", payments.ConfirmKey(in.ID),
		payments.ConfirmRequest{PaymentMethod: payments.MethodOK}, map[string]string{mockpay.FaultHeader: mockpay.FaultFail})
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("HTTP %d", r.code)
	}
	if got, _ := e.psp.GetIntent(e.ctx, in.ID); got.Status != payments.StatusRequiresPayment {
		t.Fatalf("after fail: %s", got.Status)
	}
	// The key was not consumed: the retry runs.
	if got, err := e.psp.Confirm(e.ctx, in.ID, payments.MethodOK); err != nil || got.Status != payments.StatusAuthorized {
		t.Fatalf("retry: %s %v", got.Status, err)
	}

	prod := newEnv(t, func(o *mockpay.Options) { o.AllowFaultHeader = false })
	in = prod.create("ff:2", "ref-ff-2")
	r = prod.do(http.MethodPost, "/v1/intents/"+in.ID+"/confirm", payments.ConfirmKey(in.ID),
		payments.ConfirmRequest{PaymentMethod: payments.MethodOK}, map[string]string{mockpay.FaultHeader: mockpay.FaultFail})
	if r.code != http.StatusOK {
		t.Fatalf("fault header honoured while disallowed: HTTP %d", r.code)
	}
}

// Webhooks: signed, delivered by the dispatcher, retried on the
// 0/1/2/4/8/16 s schedule, dead after six attempts; dup_webhook writes the
// same event twice.
func TestWebhookDispatch(t *testing.T) {
	e := newEnv(t, nil)
	in := e.create("wh:1", "ref-wh")
	if _, err := e.psp.Confirm(e.ctx, in.ID, payments.MethodOK); err != nil {
		t.Fatal(err)
	}
	if n, err := e.srv.DispatchOnce(e.ctx); err != nil || n != 1 {
		t.Fatalf("dispatch: %d %v", n, err)
	}
	h := <-e.received
	if h.sigErr != "" || h.ev.Type != payments.EventAuthorized || h.ev.Data.Intent.ID != in.ID || h.eventID != h.ev.ID {
		t.Fatalf("webhook %+v", h)
	}
	if n, _ := e.srv.DispatchOnce(e.ctx); n != 0 {
		t.Fatalf("delivered event sent again (%d)", n)
	}

	// Failing receiver: the schedule, then dead.
	e.status.Store(http.StatusInternalServerError)
	if _, err := e.psp.Capture(e.ctx, in.ID); err != nil {
		t.Fatal(err)
	}
	schedule := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	for i, want := range schedule {
		before := e.clk.now()
		if n, err := e.srv.DispatchOnce(e.ctx); err != nil || n != 1 {
			t.Fatalf("attempt %d: dispatched %d %v", i+1, n, err)
		}
		<-e.received
		ds, err := e.q.ListPendingDeliveries(e.ctx)
		if err != nil || len(ds) != 1 {
			t.Fatalf("attempt %d: pending %d %v", i+1, len(ds), err)
		}
		gap := ds[0].NextAttemptAt.Sub(before)
		if gap < want-100*time.Millisecond || gap > want+time.Second {
			t.Fatalf("attempt %d: next attempt in %s, want %s", i+1, gap, want)
		}
		if n, _ := e.srv.DispatchOnce(e.ctx); n != 0 {
			t.Fatalf("attempt %d: redelivered before the backoff", i+1)
		}
		e.clk.advance(want + time.Millisecond)
	}
	if n, _ := e.srv.DispatchOnce(e.ctx); n != 1 { // sixth and last attempt
		t.Fatalf("sixth attempt: %d", n)
	}
	<-e.received
	e.clk.advance(time.Hour)
	if n, _ := e.srv.DispatchOnce(e.ctx); n != 0 {
		t.Fatalf("dead delivery retried (%d)", n)
	}

	// dup_webhook: two deliveries with one event id.
	e.status.Store(http.StatusOK)
	in2 := e.create("wh:2", "ref-wh-2")
	r := e.do(http.MethodPost, "/v1/intents/"+in2.ID+"/confirm", payments.ConfirmKey(in2.ID),
		payments.ConfirmRequest{PaymentMethod: payments.MethodOK}, map[string]string{mockpay.FaultHeader: mockpay.FaultDupWebhook})
	if r.code != http.StatusOK {
		t.Fatalf("HTTP %d", r.code)
	}
	// The copy is scheduled 10 ms after the original (two separate posts).
	n1, _ := e.srv.DispatchOnce(e.ctx)
	e.clk.advance(20 * time.Millisecond)
	n2, _ := e.srv.DispatchOnce(e.ctx)
	if n1+n2 != 2 {
		t.Fatalf("dup: dispatched %d+%d, want 2", n1, n2)
	}
	a, b := <-e.received, <-e.received
	if a.eventID != b.eventID || a.sigErr != "" || b.sigErr != "" {
		t.Fatalf("dup deliveries %q %q", a.eventID, b.eventID)
	}
}

// The hosted checkout: TEST MODE banner, amount in INR, three buttons; each
// button 303s to return_url with the outcome.
func TestCheckout(t *testing.T) {
	e := newEnv(t, nil)
	nc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	in := e.create("co:1", "ref-co")
	if in.CheckoutURL != e.http.URL+"/checkout/"+in.ID {
		t.Fatalf("checkout_url %q", in.CheckoutURL)
	}
	res, err := nc.Get(in.CheckoutURL)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	for _, want := range []string{"TEST MODE", "₹1,500.00", `value="pm_ok"`, `value="pm_decline"`, `value="pm_timeout"`} {
		if !strings.Contains(string(page), want) {
			t.Fatalf("page lacks %q", want)
		}
	}
	if res.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("no CSP header")
	}
	for i, tc := range []struct{ method, status string }{
		{payments.MethodOK, payments.StatusAuthorized},
		{payments.MethodDecline, payments.StatusFailed},
		{payments.MethodTimeout, payments.StatusAuthorized},
	} {
		ref := fmt.Sprintf("ref-co-%d", i)
		in := e.create(fmt.Sprintf("co:%d", i+2), ref)
		res, err := nc.PostForm(e.http.URL+"/checkout/"+in.ID+"/pay", url.Values{"payment_method": {tc.method}})
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s: HTTP %d", tc.method, res.StatusCode)
		}
		loc, _ := url.Parse(res.Header.Get("Location"))
		if loc.Host != "app.test" || loc.Query().Get("o") != ref || loc.Query().Get("status") != tc.status || loc.Query().Get("intent") != in.ID {
			t.Fatalf("%s: redirect %s", tc.method, loc)
		}
		// A second click replays (no second authorization).
		res, err = nc.PostForm(e.http.URL+"/checkout/"+in.ID+"/pay", url.Values{"payment_method": {tc.method}})
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if l := e.ledger(in.ID); l["AUTH"] > 1 {
			t.Fatalf("%s: ledger %v", tc.method, l)
		}
	}
	res, err = nc.Get(e.http.URL + "/checkout/pi_nope")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown intent: HTTP %d", res.StatusCode)
	}
}

// Ledger summary per merchant_ref, and /metrics.
func TestLedgerSummaryAndMetrics(t *testing.T) {
	e := newEnv(t, nil)
	a := e.create("ls:1", "ref-a")
	b := e.create("ls:2", "ref-a")
	_ = e.create("ls:3", "ref-b")
	for _, id := range []string{a.ID, b.ID} {
		if _, err := e.psp.Confirm(e.ctx, id, payments.MethodOK); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.psp.Capture(e.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.psp.Void(e.ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	s, err := e.psp.LedgerSummary(e.ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]payments.LedgerSummaryRow{}
	for _, r := range s.Refs {
		got[r.MerchantRef] = r
	}
	if r := got["ref-a"]; r.Intents != 2 || r.Authorized != 2 || r.Captured != 1 || r.Voided != 1 || r.Refunded != 0 {
		t.Fatalf("ref-a %+v", r)
	}
	if r := got["ref-b"]; r.Intents != 1 || r.Authorized != 0 {
		t.Fatalf("ref-b %+v", r)
	}
	res, err := http.Get(e.http.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if !strings.Contains(string(body), `mockpay_ledger_entries_total{kind="CAPTURE"} 1`) {
		t.Fatalf("metrics lack the capture counter")
	}
}
