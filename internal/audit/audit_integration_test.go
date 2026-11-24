package audit_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sheshakanthra/sluice/internal/audit"
	"github.com/sheshakanthra/sluice/internal/mockpay"
	"github.com/sheshakanthra/sluice/internal/payments"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
	"github.com/sheshakanthra/sluice/internal/testutil/pgtest"
	"github.com/sheshakanthra/sluice/internal/testutil/redistest"
)

var (
	pgs *pgtest.Server
	rs  *redistest.Server
)

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

const capacity = 10

// env is a migrated SLUICE database, a migrated MockPay database, a flushed
// Redis, and stubs for the admin reconcile route and MockPay's ledger.
type env struct {
	t          *testing.T
	ctx        context.Context
	pg, mp     *pgxpool.Pool
	pgURL      string
	mpURL      string
	eventID    uuid.UUID
	ledger     payments.LedgerSummary
	reconciles atomic.Int64
	api, psp   *httptest.Server
	lastOut    string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pgs.Require(t)
	e := &env{t: t, ctx: t.Context()}
	e.pg, e.pgURL = pgs.NewMigratedDB(t)
	e.mp, e.mpURL = pgs.NewDB(t)
	if err := mockpay.Migrate(e.ctx, e.mp); err != nil {
		t.Fatal(err)
	}
	rs.NewClient(t) // flushes Redis
	e.eventID = uuid.Must(uuid.NewV7())
	_, err := e.pg.Exec(e.ctx, `INSERT INTO events (id, slug, name, venue, starts_at, prequeue_opens_at, sale_opens_at, capacity, price_paise, max_per_user, layout)
		VALUES ($1, 'audit-test', 'Audit test', 'Nowhere', now() + interval '1 day', now() - interval '1 hour', now() - interval '1 hour', $2, 50000, 4, '{}')`,
		e.eventID, capacity)
	if err != nil {
		t.Fatal(err)
	}
	for s := range capacity {
		if _, err := e.pg.Exec(e.ctx, `INSERT INTO seats (event_id, seat_no, label, section, row_no, col_no) VALUES ($1, $2, $3, 'C1', 1, $2)`,
			e.eventID, s, fmt.Sprintf("C1-%d", s+1)); err != nil {
			t.Fatal(err)
		}
	}
	e.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/reconcile" || r.Header.Get("Authorization") != "Bearer admin-token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		e.reconciles.Add(1)
		_, _ = io.WriteString(w, `{"orphans_fixed":0}`)
	}))
	t.Cleanup(e.api.Close)
	e.psp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ledger/summary" || r.URL.Query().Get("metadata.event_id") != e.eventID.String() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(e.ledger)
	}))
	t.Cleanup(e.psp.Close)
	return e
}

func (e *env) options(mode string, settle time.Duration) *audit.Options {
	e.lastOut = e.t.TempDir()
	return &audit.Options{
		Event: "audit-test", Mode: mode, API: e.api.URL, Out: e.lastOut, SettleTimeout: settle,
		Poll: 50 * time.Millisecond, OrphanAge: 35 * time.Second, SweeperInterval: time.Second,
		AdminToken: "admin-token", MockpayURL: e.psp.URL, MockpayAPIKey: "k",
		DatabaseURL: e.pgURL, MockpayDatabaseURL: e.mpURL, RedisURL: rs.URL(),
	}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pg.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func count(t *testing.T, r *audit.Report, name string) int64 {
	t.Helper()
	c := r.Get(name)
	if c == nil {
		t.Fatalf("no %s check", name)
	}
	if c.Count == nil {
		return -1
	}
	return *c.Count
}

// TestAuditCatchesNaiveOversellAndDuplicate: the auditor's sensitivity.
// Seat 3 sold to two orders and one order charged twice must each be
// counted, with exit 1; one order's own repeated insert (its retry) is
// not a second buyer, and checks with no holds or Redis say n/a.
func TestAuditCatchesNaiveOversellAndDuplicate(t *testing.T) {
	e := newEnv(t)
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	for _, o := range []uuid.UUID{a, b, c} {
		e.exec(`INSERT INTO naive_orders (order_ref, event_id, user_id, seat_nos, amount_paise, status, pay_status)
			VALUES ($1, $2, $3, '{3}', 50000, 'CONFIRMED', 'CAPTURED')`, o, e.eventID, uuid.New())
	}
	e.exec(`INSERT INTO naive_sales (event_id, seat_no, order_ref, user_id) VALUES ($1, 3, $2, $3), ($1, 3, $4, $5), ($1, 5, $6, $7), ($1, 5, $6, $7)`,
		e.eventID, a, uuid.New(), b, uuid.New(), c, uuid.New())
	e.ledger = payments.LedgerSummary{Refs: []payments.LedgerSummaryRow{
		{MerchantRef: a.String(), Intents: 2, Authorized: 2, Captured: 2},              // charged twice
		{MerchantRef: b.String(), Intents: 1, Authorized: 1, Captured: 1},              // fine
		{MerchantRef: c.String(), Intents: 2, Authorized: 2, Captured: 2, Refunded: 1}, // refunded: net 1
		{MerchantRef: uuid.NewString(), Intents: 1, Authorized: 1, Captured: 1},        // no order: orphan
	}}
	rep, err := audit.Run(e.ctx, e.options(audit.ModeNaive, 10*time.Second), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{
		audit.CheckOversold: 1, audit.CheckDuplicateCharges: 1, audit.CheckOrphanCharges: 1,
		audit.CheckQuotaViolations: 0, audit.CheckTicketOrder: 0, audit.CheckStuckHolds: -1, audit.CheckInventoryDrift: -1,
	}
	for name, n := range want {
		if got := count(t, rep, name); got != n {
			t.Errorf("%s = %d, want %d (%+v)", name, got, n, rep.Get(name))
		}
	}
	if rep.Passed || rep.ExitCode != audit.ExitInvariantFail || rep.Settle.TimedOut {
		t.Errorf("passed %t exit %d settle %+v, want a failed audit with exit 1", rep.Passed, rep.ExitCode, rep.Settle)
	}
	if _, err := os.Stat(filepath.Join(e.lastOut, "audit.json")); err != nil {
		t.Errorf("audit.json not written: %v", err)
	}
	if e.reconciles.Load() < 2 {
		t.Errorf("reconcile called %d times, want 2 (settle rounds)", e.reconciles.Load())
	}
}

// TestAuditPassesCleanEvent: a sold seat that agrees everywhere (PG
// ticket, CONFIRMED order with a CAPTURED payment, Redis sold) and Redis
// sets that match PG pass every check with exit 0; one seat moved in Redis
// is inventory drift.
func TestAuditPassesCleanEvent(t *testing.T) {
	e := newEnv(t)
	rdb := rs.NewClient(t)
	uid, hid, oid := uuid.New(), uuid.New(), uuid.New()
	e.exec(`INSERT INTO holds (id, event_id, user_id, seat_nos, status, fence, expires_at) VALUES ($1, $2, $3, '{0}', 'CONSUMED', 1, now())`, hid, e.eventID, uid)
	e.exec(`INSERT INTO orders (id, event_id, user_id, hold_id, seat_nos, amount_paise, status) VALUES ($1, $2, $3, $4, '{0}', 50000, 'CONFIRMED')`, oid, e.eventID, uid, hid)
	e.exec(`INSERT INTO payments (order_id, attempt, provider_intent_id, psp_idempotency_key, amount_paise, status) VALUES ($1, 1, 'pi_1', 'k1', 50000, 'CAPTURED')`, oid)
	e.exec(`UPDATE seats SET state = 'SOLD', order_id = $2 WHERE event_id = $1 AND seat_no = 0`, e.eventID, oid)
	e.exec(`INSERT INTO tickets (event_id, seat_no, order_id, user_id, code) VALUES ($1, 0, $2, $3, 'SLC-TEST-0001')`, e.eventID, oid, uid)
	e.ledger = payments.LedgerSummary{Refs: []payments.LedgerSummaryRow{{MerchantRef: oid.String(), Intents: 1, Authorized: 1, Captured: 1}}}
	rdb.HSet(e.ctx, keys.Meta(e.eventID), "loaded", "1")
	rdb.SAdd(e.ctx, keys.Sold(e.eventID), 0)
	for s := 1; s < capacity; s++ {
		rdb.SAdd(e.ctx, keys.Avail(e.eventID), s)
	}

	rep, err := audit.Run(e.ctx, e.options(audit.ModeNormal, 10*time.Second), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passed || rep.ExitCode != audit.ExitPass {
		t.Fatalf("clean event: passed %t exit %d\n%s", rep.Passed, rep.ExitCode, audit.Render(rep))
	}
	for _, name := range []string{audit.CheckOversold, audit.CheckDuplicateCharges, audit.CheckOrphanCharges, audit.CheckQuotaViolations,
		audit.CheckStuckHolds, audit.CheckInventoryDrift, audit.CheckTicketOrder} {
		if got := count(t, rep, name); got != 0 {
			t.Errorf("%s = %d, want 0", name, got)
		}
	}

	rdb.SMove(e.ctx, keys.Avail(e.eventID), keys.Sold(e.eventID), 4) // Redis says sold, PG says available
	rep, err = audit.Run(e.ctx, e.options(audit.ModeNormal, 10*time.Second), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := count(t, rep, audit.CheckInventoryDrift); got != 1 || rep.ExitCode != audit.ExitInvariantFail {
		t.Errorf("drift = %d exit %d, want 1 and exit 1", got, rep.ExitCode)
	}
}

// TestAuditSettleTimesOut: a live hold long past its expiry (no sweeper)
// keeps the system from settling, so the audit exits 2, and still reports
// the stuck hold.
func TestAuditSettleTimesOut(t *testing.T) {
	e := newEnv(t)
	hid := uuid.New()
	e.exec(`INSERT INTO holds (id, event_id, user_id, seat_nos, status, fence, expires_at) VALUES ($1, $2, $3, '{1}', 'ACTIVE', 1, now() - interval '1 minute')`,
		hid, e.eventID, uuid.New())
	e.exec(`UPDATE seats SET state = 'HELD', hold_id = $2, held_until = now() - interval '1 minute' WHERE event_id = $1 AND seat_no = 1`, e.eventID, hid)
	rep, err := audit.Run(e.ctx, e.options(audit.ModeNormal, 500*time.Millisecond), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Settle.TimedOut || rep.ExitCode != audit.ExitSettleTimeout || rep.Settle.Pending.ExpiredHolds != 1 {
		t.Fatalf("settle %+v exit %d, want a timeout (exit 2) with 1 expired hold pending", rep.Settle, rep.ExitCode)
	}
	if got := count(t, rep, audit.CheckStuckHolds); got != 1 {
		t.Errorf("stuck holds = %d, want 1", got)
	}
}
