package orders_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/events"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/jobs"
	"github.com/sheshakanthra/sluice/internal/mockpay"
	"github.com/sheshakanthra/sluice/internal/mockpay/mpdb"
	"github.com/sheshakanthra/sluice/internal/orders"
	"github.com/sheshakanthra/sluice/internal/payments"
	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/testutil/pgtest"
	"github.com/sheshakanthra/sluice/internal/testutil/redistest"
)

var (
	pgs *pgtest.Server
	rs  *redistest.Server
)

// TestMain starts one Postgres 18 and one Redis 8 container for the package.
// Tests never call t.Parallel: each flushes the shared Redis.
func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	var (
		wg           sync.WaitGroup
		pgErr, rdErr error
	)
	wg.Go(func() { pgs, pgErr = pgtest.Start(ctx) })
	wg.Go(func() { rs, rdErr = redistest.Start(ctx) })
	wg.Wait()
	cancel()
	if pgErr != nil || rdErr != nil {
		_, _ = fmt.Fprintln(os.Stderr, "containers:", pgErr, rdErr)
		os.Exit(1)
	}
	code := m.Run()
	pgs.Close()
	rs.Close()
	os.Exit(code)
}

const (
	webhookSecret = "test-webhook-secret"
	apiKey        = "test-mockpay-key"
	adminToken    = "test-admin-token-0123456789"
	pspTimeout    = 300 * time.Millisecond
	pspHang       = 900 * time.Millisecond
)

func mustNil(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// clock is a shiftable clock for inventory and orders (PG expiry checks
// take "now" from Go, so moving it expires holds without sleeping).
type clock struct{ off atomic.Int64 }

func (c *clock) now() time.Time          { return time.Now().Add(time.Duration(c.off.Load())) }
func (c *clock) advance(d time.Duration) { c.off.Add(int64(d)) }

// faults adds X-MockPay-Fault to the next n PSP calls whose path ends with
// a suffix (for example "/capture").
type faults struct {
	mu    sync.Mutex
	rules []*faultRule
	base  http.RoundTripper
}

type faultRule struct {
	suffix, fault string
	left          int
}

func (f *faults) add(suffix, fault string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, &faultRule{suffix: suffix, fault: fault, left: n})
}

// reset drops every pending fault.
func (f *faults) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = nil
}

func (f *faults) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	for _, rule := range f.rules {
		if rule.left > 0 && strings.HasSuffix(r.URL.Path, rule.suffix) {
			rule.left--
			r = r.Clone(r.Context())
			r.Header.Set(mockpay.FaultHeader, rule.fault)
			break
		}
	}
	f.mu.Unlock()
	return f.base.RoundTrip(r)
}

type harness struct {
	t     *testing.T
	ctx   context.Context
	clk   *clock
	pool  *pgxpool.Pool
	q     *gen.Queries
	rdb   *redis.Client
	inv   *inventory.Service
	svc   *orders.Service
	reg   *prometheus.Registry
	keys  *auth.Keyring
	psp   *payments.Client
	flt   *faults
	mp    *mockpay.Server
	mpq   *mpdb.Queries
	api   *httptest.Server
	mpSrv *httptest.Server
	river *river.Client[pgx.Tx]
	// worker is the started River worker client (nil in manual mode).
	worker  *river.Client[pgx.Tx]
	eventID uuid.UUID
}

type hopts struct {
	capacity   int
	worker     bool // run a real River worker client instead of manual draining
	dispatcher bool // run MockPay's webhook dispatcher
	holdTTL    time.Duration
	payTTL     time.Duration
	maxDelay   time.Duration
}

type hopt func(*hopts)

func withWorker() hopt                      { return func(o *hopts) { o.worker = true } }
func withDispatcher() hopt                  { return func(o *hopts) { o.dispatcher = true } }
func withCapacity(n int) hopt               { return func(o *hopts) { o.capacity = n } }
func withWebhookDelay(d time.Duration) hopt { return func(o *hopts) { o.maxDelay = d } }

func newHarness(t *testing.T, opts ...hopt) *harness {
	t.Helper()
	o := hopts{capacity: 20, holdTTL: time.Minute, payTTL: 2 * time.Minute}
	for _, f := range opts {
		f(&o)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	h := &harness{t: t, ctx: ctx, clk: &clock{}, reg: prometheus.NewRegistry()}
	h.pool, _ = pgs.NewMigratedDB(t)
	h.q = gen.New(h.pool)
	h.rdb = rs.NewClient(t)

	// API first (MockPay needs its URL for webhooks); the handler is set below.
	var apiHandler http.Handler
	h.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { apiHandler.ServeHTTP(w, r) }))
	t.Cleanup(h.api.Close)

	mpPool, _ := pgs.NewDB(t)
	mustNil(t, mockpay.Migrate(ctx, mpPool))
	h.mpq = mpdb.New(mpPool)
	var mpHandler http.Handler
	h.mpSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mpHandler.ServeHTTP(w, r) }))
	t.Cleanup(h.mpSrv.Close)
	var err error
	h.mp, err = mockpay.New(ctx, mpPool, prometheus.NewRegistry(), mockpay.Options{
		APIKey: apiKey, WebhookURL: h.api.URL + orders.RouteWebhook[len("POST "):], WebhookSecret: webhookSecret,
		PublicURL: h.mpSrv.URL, Hang: pspHang, AllowFaultHeader: true, DispatchInterval: 20 * time.Millisecond,
		WebhookMaxDelay: o.maxDelay, SlowDelay: 100 * time.Millisecond, Log: discard(),
	})
	mustNil(t, err)
	mpHandler = h.mp.Handler()
	if o.dispatcher {
		dctx, dcancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); _ = h.mp.RunDispatcher(dctx) }()
		t.Cleanup(func() { dcancel(); <-done })
	}

	sc, err := redisx.NewScripts(h.rdb, h.reg)
	mustNil(t, err)
	im, err := inventory.NewMetrics(h.reg)
	mustNil(t, err)
	var svc *orders.Service
	h.inv, err = inventory.NewService(h.pool, h.rdb, sc, inventory.Options{
		HoldTTL: o.holdTTL, MaxSeats: 4, Now: h.clk.now, Metrics: im,
		OnReclaim: func(ctx context.Context, tx pgx.Tx, hid uuid.UUID) error { return svc.OnReclaim(ctx, tx, hid) },
	})
	mustNil(t, err)

	pm, err := payments.NewMetrics(h.reg)
	mustNil(t, err)
	h.flt = &faults{base: http.DefaultTransport}
	h.psp, err = payments.NewClient(payments.Options{BaseURL: h.mpSrv.URL, PublicURL: h.mpSrv.URL, APIKey: apiKey,
		Timeout: pspTimeout, Transport: h.flt, Metrics: pm})
	mustNil(t, err)
	h.river, err = jobs.NewInsertClient(h.pool, nil)
	mustNil(t, err)
	om, err := orders.NewMetrics(h.reg)
	mustNil(t, err)
	svc, err = orders.NewService(h.pool, h.inv, h.psp, h.river, h.rdb, orders.Options{
		PaymentHoldTTL: o.payTTL, ReturnURL: "http://app.test/order", OrphanAge: time.Second, Now: h.clk.now, Metrics: om,
	})
	mustNil(t, err)
	h.svc = svc

	h.keys, err = auth.NewKeyring([]config.TokenKey{{KID: "t1", Secret: bytes.Repeat([]byte{7}, 32)}}, nil)
	mustNil(t, err)
	replays, err := httpx.NewReplayCounter(h.reg)
	mustNil(t, err)
	idem := httpx.NewIdempotency(h.q, auth.UserID, nil, nil).WithReplays(replays)
	rt := httpx.NewRouter()
	inventory.RegisterRoutes(rt, inventory.HandlerDeps{Svc: h.inv, Keys: h.keys, Idem: idem, QueueEnforced: true,
		AdminToken: adminToken, Log: discard()})
	orders.RegisterRoutes(rt, orders.HandlerDeps{Svc: svc, Keys: h.keys, Idem: idem, QueueEnforced: true,
		AdminToken: adminToken, WebhookSecret: webhookSecret, Log: discard()})
	apiHandler = httpx.Chain(rt, httpx.Recover(discard()))

	if o.worker {
		h.worker, err = jobs.NewWorkerClient(h.pool, svc, jobs.Config{
			ReconcileInterval: time.Hour, AuditInterval: time.Hour, FetchPollInterval: 20 * time.Millisecond,
			FetchCooldown: 5 * time.Millisecond, RetryBase: 50 * time.Millisecond, RetryMax: 200 * time.Millisecond,
		})
		mustNil(t, err)
		mustNil(t, h.worker.Start(ctx))
		t.Cleanup(func() {
			sctx, c := context.WithTimeout(context.Background(), 10*time.Second)
			defer c()
			_ = h.worker.Stop(sctx)
		})
	}
	h.eventID = h.newEvent(o.capacity)
	_, err = h.inv.Rebuild(ctx, h.eventID)
	mustNil(t, err)
	return h
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func (h *harness) newEvent(capacity int) uuid.UUID {
	h.t.Helper()
	now := time.Now().UTC()
	slug := "t-" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	l, err := events.TrainLayout(capacity)
	mustNil(h.t, err)
	lj, err := json.Marshal(l)
	mustNil(h.t, err)
	_, err = h.q.InsertEventIfAbsent(h.ctx, gen.InsertEventIfAbsentParams{
		Slug: slug, Name: "Test " + slug, Venue: "Test", StartsAt: now.Add(48 * time.Hour),
		PrequeueOpensAt: now.Add(-time.Hour), SaleOpensAt: now.Add(-time.Hour),
		Capacity: int32(capacity), PricePaise: 50_000, MaxPerUser: 4, Status: gen.EventStatusONSALE, Layout: lj, //nolint:gosec // test sizes
	})
	mustNil(h.t, err)
	ev, err := h.q.GetEventBySlug(h.ctx, slug)
	mustNil(h.t, err)
	p := gen.InsertSeatsParams{EventID: ev.ID}
	for _, s := range l.Seats() {
		p.SeatNos = append(p.SeatNos, s.SeatNo)
		p.Labels = append(p.Labels, s.Label)
		p.Sections = append(p.Sections, s.Section)
		p.RowNos = append(p.RowNos, s.Row)
		p.ColNos = append(p.ColNos, s.Col)
	}
	_, err = h.q.InsertSeats(h.ctx, p)
	mustNil(h.t, err)
	return ev.ID
}

// user is one customer with session and admission tokens.
type user struct {
	id                 uuid.UUID
	session, admission string
}

func (h *harness) newUser() user {
	h.t.Helper()
	id := uuid.Must(uuid.NewV7())
	s, _, err := h.keys.Issue(auth.AudSession, id, uuid.Nil, nil, time.Hour)
	mustNil(h.t, err)
	a, _, err := h.keys.Issue(auth.AudAdmission, id, h.eventID, nil, time.Hour)
	mustNil(h.t, err)
	return user{id: id, session: s, admission: a}
}

// hold places a hold directly through the service.
func (h *harness) hold(u user, seats ...int32) inventory.Hold {
	h.t.Helper()
	hd, err := h.inv.Hold(h.ctx, h.eventID, u.id, uuid.NewString(), inventory.Request{SeatNos: seats})
	mustNil(h.t, err)
	return hd
}

type resp struct {
	code int
	hdr  http.Header
	body []byte
}

func (r resp) view(t testing.TB) orders.View {
	t.Helper()
	var v orders.View
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("decode view (HTTP %d): %v: %s", r.code, err, r.body)
	}
	return v
}

func (r resp) problemCode() string {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.body, &p)
	return p.Code
}

func (h *harness) do(method, path, token, key string, body any) resp {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		mustNil(h.t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(h.ctx, method, h.api.URL+path, rd)
	mustNil(h.t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set(httpx.IdempotencyHeader, key)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	mustNil(h.t, err)
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	mustNil(h.t, err)
	return resp{code: res.StatusCode, hdr: res.Header, body: b}
}

// createOrder holds seats and creates the order over HTTP.
func (h *harness) createOrder(u user, seats ...int32) orders.View {
	h.t.Helper()
	hd := h.hold(u, seats...)
	r := h.do(http.MethodPost, "/api/v1/orders", u.admission, uuid.NewString(), map[string]any{"hold_id": hd.ID})
	if r.code != http.StatusCreated {
		h.t.Fatalf("create order: HTTP %d %s", r.code, r.body)
	}
	return r.view(h.t)
}

func (h *harness) pay(u user, orderID uuid.UUID, key, method string) resp {
	h.t.Helper()
	return h.do(http.MethodPost, "/api/v1/orders/"+orderID.String()+"/pay", u.session, key, map[string]any{"payment_method": method})
}

func (h *harness) getOrder(u user, orderID uuid.UUID) orders.View {
	h.t.Helper()
	r := h.do(http.MethodGet, "/api/v1/orders/"+orderID.String(), u.session, "", nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("get order: HTTP %d %s", r.code, r.body)
	}
	return r.view(h.t)
}

// checkout pays an intent through MockPay's hosted checkout form, the way a
// browser does, and returns the redirect target.
func (h *harness) checkout(intentID, method string) string {
	h.t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(h.ctx, http.MethodPost, h.mpSrv.URL+"/checkout/"+intentID+"/pay",
		strings.NewReader("payment_method="+method))
	mustNil(h.t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.Do(req)
	mustNil(h.t, err)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		h.t.Fatalf("checkout: HTTP %d", res.StatusCode)
	}
	return res.Header.Get("Location")
}

// drain runs every queued River job (manual mode) until none is left or
// rounds run out. Failing jobs stay queued and are retried next round.
func (h *harness) drain() {
	h.t.Helper()
	for round := 0; round < 20; round++ {
		res, err := h.river.JobList(h.ctx, river.NewJobListParams().First(1000).States(
			rivertype.JobStateAvailable, rivertype.JobStateScheduled, rivertype.JobStateRetryable))
		mustNil(h.t, err)
		if len(res.Jobs) == 0 {
			return
		}
		for _, j := range res.Jobs {
			if err := jobs.Dispatch(h.ctx, h.svc, j.Kind, j.EncodedArgs); err != nil {
				h.t.Logf("job %s failed (stays queued): %v", j.Kind, err)
				continue
			}
			_, err := h.river.JobDelete(h.ctx, j.ID)
			mustNil(h.t, err)
		}
	}
	h.t.Fatal("drain: jobs still queued after 20 rounds")
}

// eventually polls cond until it is true or timeout passes.
func (h *harness) eventually(timeout time.Duration, what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *harness) payment(orderID uuid.UUID, attempt int32) gen.Payment {
	h.t.Helper()
	ps, err := h.q.ListPaymentsByOrder(h.ctx, orderID)
	mustNil(h.t, err)
	for i := range ps {
		if ps[i].Attempt == attempt {
			return ps[i]
		}
	}
	h.t.Fatalf("order %s has no attempt %d", orderID, attempt)
	return gen.Payment{}
}

func (h *harness) order(id uuid.UUID) gen.Order {
	h.t.Helper()
	o, err := h.q.GetOrder(h.ctx, id)
	mustNil(h.t, err)
	return o
}

// ledger returns kind -> count for an intent.
func (h *harness) ledger(intentID string) map[string]int {
	h.t.Helper()
	rows, err := h.mpq.ListLedgerByIntent(h.ctx, intentID)
	mustNil(h.t, err)
	out := map[string]int{}
	for _, r := range rows {
		out[r.Kind]++
	}
	return out
}

// intentsFor counts PSP intents created for an order.
func (h *harness) intentsFor(orderID uuid.UUID) int {
	h.t.Helper()
	ref := orderID.String()
	rows, err := h.mpq.ListIntents(h.ctx, mpdb.ListIntentsParams{MerchantID: mockpay.MerchantID, MerchantRef: &ref, MaxRows: 100})
	mustNil(h.t, err)
	return len(rows)
}

func (h *harness) counter(name string, labels ...string) float64 {
	h.t.Helper()
	mfs, err := h.reg.Gather()
	mustNil(h.t, err)
	total := 0.0
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			ok := true
			for i := 0; i+1 < len(labels); i += 2 {
				found := false
				for _, lp := range m.GetLabel() {
					if lp.GetName() == labels[i] && lp.GetValue() == labels[i+1] {
						found = true
					}
				}
				ok = ok && found
			}
			if ok {
				total += m.GetCounter().GetValue()
			}
		}
	}
	return total
}

// signedWebhook posts payload to the inbox with a valid signature.
func (h *harness) postWebhook(eventID string, payload []byte) resp {
	h.t.Helper()
	req, err := http.NewRequestWithContext(h.ctx, http.MethodPost, h.api.URL+"/api/v1/webhooks/mockpay", bytes.NewReader(payload))
	mustNil(h.t, err)
	req.Header.Set(payments.SignatureHeader, payments.Sign(webhookSecret, time.Now(), payload))
	req.Header.Set(payments.EventIDHeader, eventID)
	res, err := http.DefaultClient.Do(req)
	mustNil(h.t, err)
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return resp{code: res.StatusCode, body: b}
}

// deliveries returns MockPay's pending webhook deliveries for an intent.
func (h *harness) deliveries(intentID string) []mpdb.WebhookDelivery {
	h.t.Helper()
	all, err := h.mpq.ListPendingDeliveries(h.ctx)
	mustNil(h.t, err)
	var out []mpdb.WebhookDelivery
	for i := range all {
		if all[i].IntentID == intentID {
			out = append(out, all[i])
		}
	}
	return out
}
