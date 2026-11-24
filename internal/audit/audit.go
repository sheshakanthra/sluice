// Package audit is `sluice audit` (LOADTEST §6): it lets the system settle,
// then checks every invariant from the outside and reports counts and
// examples. It is the judge of every rush, so it shares no query code with
// the system it audits: its SQL is its own (hand-written below, on purpose)
// and reads raw tables, including River's and MockPay's, which are not in
// the sqlc schema.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/payments"
)

// Exit codes (LOADTEST §6).
const (
	ExitPass          = 0
	ExitInvariantFail = 1
	ExitSettleTimeout = 2
)

// Modes.
const (
	ModeNormal = "normal"
	ModeNaive  = "naive" // B0: sales live in naive_sales / naive_orders
)

// maxExamples caps the examples kept per check.
const maxExamples = 5

// Options are the `sluice audit` flags plus their environment defaults.
type Options struct {
	Event              string
	Mode               string
	API                string // front door base URL
	Results            string // run directory with summary.json (optional)
	Out                string // where audit.json goes (default Results, else ".")
	SettleTimeout      time.Duration
	Poll               time.Duration
	OrphanAge          time.Duration // the reconciler's orphan age (30 s) plus a margin
	SweeperInterval    time.Duration
	AdminToken         string // in memory only
	MockpayURL         string
	MockpayAPIKey      string // in memory only
	DatabaseURL        string // in memory only
	MockpayDatabaseURL string // in memory only
	RedisURL           string
}

// ParseArgs parses `sluice audit` flags; secrets and URLs come from env.
func ParseArgs(args []string, getenv func(string) string, stderr io.Writer) (*Options, error) {
	fs := flag.NewFlagSet("sluice audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := Defaults(getenv)
	fs.StringVar(&o.Event, "event", "", "event slug or id (required)")
	fs.StringVar(&o.Mode, "mode", ModeNormal, "normal | naive (B0: audit naive_sales)")
	fs.StringVar(&o.API, "api", o.API, "front door base URL (admin reconcile)")
	fs.StringVar(&o.Results, "results", "", "run directory: reads summary.json (swarm records), writes audit.json and the summary's audit block")
	fs.StringVar(&o.Out, "out", "", "directory for audit.json (default --results, else .)")
	fs.DurationVar(&o.SettleTimeout, "settle-timeout", o.SettleTimeout, "settle budget (exit 2 when exceeded)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return o, nil
}

// Defaults fills Options from the environment (the stack's env profile).
func Defaults(getenv func(string) string) *Options {
	or := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	sweep, err := time.ParseDuration(or("SWEEPER_INTERVAL", "1s"))
	if err != nil || sweep <= 0 {
		sweep = time.Second
	}
	return &Options{
		Mode: ModeNormal, API: or("AUDIT_API", "http://caddy:8000"), SettleTimeout: 120 * time.Second,
		Poll: time.Second, OrphanAge: 35 * time.Second, SweeperInterval: sweep,
		AdminToken: getenv("ADMIN_TOKEN"), MockpayURL: or("MOCKPAY_URL", "http://mockpay:8090"),
		MockpayAPIKey: getenv("MOCKPAY_API_KEY"), DatabaseURL: getenv("DATABASE_URL"),
		MockpayDatabaseURL: getenv("MOCKPAY_DATABASE_URL"), RedisURL: getenv("REDIS_URL"),
	}
}

// Validate reports missing settings.
func (o *Options) Validate() error {
	var errs []error
	if o.Event == "" {
		errs = append(errs, errors.New("--event is required"))
	}
	if o.Mode != ModeNormal && o.Mode != ModeNaive {
		errs = append(errs, fmt.Errorf("--mode %q: want normal or naive", o.Mode))
	}
	for k, v := range map[string]string{"ADMIN_TOKEN": o.AdminToken, "MOCKPAY_API_KEY": o.MockpayAPIKey,
		"DATABASE_URL": o.DatabaseURL, "MOCKPAY_DATABASE_URL": o.MockpayDatabaseURL, "REDIS_URL": o.RedisURL} {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is not set", k))
		}
	}
	return errors.Join(errs...)
}

// Check is one invariant's verdict. Count is nil when the check does not
// apply (for example stuck holds in naive mode, which has no holds).
type Check struct {
	Name     string         `json:"name"`
	Count    *int64         `json:"count"`
	Pass     bool           `json:"pass"`
	Detail   map[string]any `json:"detail,omitempty"`
	Examples []string       `json:"examples,omitempty"`
	Note     string         `json:"note,omitempty"`
}

// Pending is what settle waits for.
type Pending struct {
	Jobs         int64 `json:"jobs"`          // River jobs available/running/retryable (periodic kinds excluded)
	ExpiredHolds int64 `json:"expired_holds"` // ACTIVE/PAYMENT_PENDING holds past expires_at
	Webhooks     int64 `json:"webhooks"`      // MockPay deliveries not yet delivered and not dead
	YoungIntents int64 `json:"young_intents"` // money-holding intents younger than the orphan age
}

func (p Pending) quiet() bool { return p == Pending{} }

// Settle records the settle phase.
type Settle struct {
	Seconds   float64           `json:"seconds"`
	TimedOut  bool              `json:"timed_out"`
	Polls     int               `json:"polls"`
	Reconcile []json.RawMessage `json:"reconcile"` // each POST /admin/reconcile answer, in order
	Pending   Pending           `json:"pending_at_end"`
}

// Report is audit.json.
type Report struct {
	Event      string    `json:"event"`
	EventID    string    `json:"event_id"`
	Mode       string    `json:"mode"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Settle     Settle    `json:"settle"`
	Checks     []Check   `json:"checks"`
	Passed     bool      `json:"passed"`
	ExitCode   int       `json:"exit_code"`
}

// Get returns the named check (nil if absent).
func (r *Report) Get(name string) *Check {
	for i := range r.Checks {
		if r.Checks[i].Name == name {
			return &r.Checks[i]
		}
	}
	return nil
}

type auditor struct {
	o     *Options
	pg    *pgxpool.Pool
	mp    *pgxpool.Pool
	rdb   *redis.Client
	psp   *payments.Client
	http  *http.Client
	event struct {
		id         uuid.UUID
		capacity   int64
		maxPerUser int64
	}
}

// Run settles, checks and writes audit.json. The error is for failures of
// the auditor itself (a datastore it cannot reach), never for a failed
// invariant: that is Report.ExitCode.
func Run(ctx context.Context, o *Options, stdout io.Writer) (*Report, error) {
	rep := &Report{Event: o.Event, Mode: o.Mode, StartedAt: time.Now().UTC()}
	a := &auditor{o: o, http: &http.Client{Timeout: 60 * time.Second}}
	var err error
	if a.pg, err = pgxpool.New(ctx, o.DatabaseURL); err != nil {
		return nil, fmt.Errorf("audit: postgres: %w", err)
	}
	defer a.pg.Close()
	if a.mp, err = pgxpool.New(ctx, o.MockpayDatabaseURL); err != nil {
		return nil, fmt.Errorf("audit: mockpay postgres: %w", err)
	}
	defer a.mp.Close()
	ropt, err := redis.ParseURL(o.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("audit: redis url: %w", err)
	}
	a.rdb = redis.NewClient(ropt)
	defer func() { _ = a.rdb.Close() }()
	if a.psp, err = payments.NewClient(payments.Options{BaseURL: o.MockpayURL, APIKey: o.MockpayAPIKey, Timeout: 30 * time.Second}); err != nil {
		return nil, fmt.Errorf("audit: psp client: %w", err)
	}
	// Hand-written SQL: the auditor reads raw tables on purpose (package doc).
	err = a.pg.QueryRow(ctx, `SELECT id, capacity, max_per_user FROM events WHERE slug = $1 OR id::text = $1`, o.Event).
		Scan(&a.event.id, &a.event.capacity, &a.event.maxPerUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("audit: no event %q", o.Event)
	}
	if err != nil {
		return nil, fmt.Errorf("audit: event: %w", err)
	}
	rep.EventID = a.event.id.String()
	_, _ = fmt.Fprintf(stdout, "audit %s (%s mode): settling (budget %s)\n", o.Event, o.Mode, o.SettleTimeout)

	rep.Settle = a.settle(ctx)
	_, _ = fmt.Fprintf(stdout, "audit: settle %.1f s (timed out: %t, %d reconciles)\n", rep.Settle.Seconds, rep.Settle.TimedOut, len(rep.Settle.Reconcile))

	if err := a.checks(ctx, rep); err != nil {
		return nil, err
	}
	rep.Passed = true
	for _, c := range rep.Checks {
		rep.Passed = rep.Passed && c.Pass
	}
	switch {
	case rep.Settle.TimedOut:
		rep.ExitCode = ExitSettleTimeout
	case !rep.Passed:
		rep.ExitCode = ExitInvariantFail
	}
	rep.FinishedAt = time.Now().UTC()
	out := o.Out
	if out == "" {
		out = o.Results
	}
	if out == "" {
		out = "."
	}
	if err := writeJSON(filepath.Join(out, "audit.json"), rep); err != nil {
		return rep, err
	}
	_, _ = fmt.Fprint(stdout, Render(rep))
	return rep, nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}

// settle: reconcile, wait for quiet, reconcile again, wait for quiet
// (LOADTEST §6 step 1). Quiet = no pending River jobs, no holds past
// expiry, no undelivered webhooks, and every money-holding intent old
// enough for the reconciler's orphan pass. The second round lets the jobs
// the last reconcile enqueued (voids, refunds, captures) finish.
func (a *auditor) settle(ctx context.Context) Settle {
	start := time.Now()
	deadline := start.Add(a.o.SettleTimeout)
	var s Settle
	for round := range 2 {
		s.Reconcile = append(s.Reconcile, a.reconcile(ctx))
		for {
			s.Polls++
			p, err := a.pending(ctx)
			s.Pending = p
			if err == nil && p.quiet() {
				break
			}
			if time.Now().After(deadline) {
				s.TimedOut = true
				s.Seconds = time.Since(start).Seconds()
				return s
			}
			select {
			case <-ctx.Done():
				s.TimedOut = true
				return s
			case <-time.After(a.o.Poll):
			}
			if round == 0 && s.Polls%30 == 0 { // a failed or early reconcile: run it again now and then
				s.Reconcile = append(s.Reconcile, a.reconcile(ctx))
			}
		}
	}
	s.Seconds = time.Since(start).Seconds()
	return s
}

// reconcile calls POST /admin/reconcile and returns its answer (or the error).
func (a *auditor) reconcile(ctx context.Context) json.RawMessage {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.o.API, "/")+"/api/v1/admin/reconcile", http.NoBody)
	if err != nil {
		return errJSON(err)
	}
	req.Header.Set("Authorization", "Bearer "+a.o.AdminToken)
	resp, err := a.http.Do(req)
	if err != nil {
		return errJSON(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK || !json.Valid(b) {
		return errJSON(fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b))))
	}
	return b
}

func errJSON(err error) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"error": err.Error()})
	return b
}

func (a *auditor) pending(ctx context.Context) (Pending, error) {
	var p Pending
	// Hand-written SQL (package doc). Periodic kinds (reconcile,
	// audit_cache) are excluded: they recur forever by design.
	err := a.pg.QueryRow(ctx, `SELECT count(*) FROM river_job
		WHERE state IN ('available','running','retryable') AND kind NOT IN ('reconcile','audit_cache')`).Scan(&p.Jobs)
	if err != nil {
		return p, fmt.Errorf("audit: river jobs: %w", err)
	}
	err = a.pg.QueryRow(ctx, `SELECT count(*) FROM holds
		WHERE event_id = $1 AND status IN ('ACTIVE','PAYMENT_PENDING') AND expires_at < now()`, a.event.id).Scan(&p.ExpiredHolds)
	if err != nil {
		return p, fmt.Errorf("audit: expired holds: %w", err)
	}
	// Six delivery attempts, then a delivery is dead (MockPay retry schedule).
	err = a.mp.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE delivered_at IS NULL AND attempt < 6`).Scan(&p.Webhooks)
	if err != nil {
		return p, fmt.Errorf("audit: webhooks: %w", err)
	}
	err = a.mp.QueryRow(ctx, `SELECT count(*) FROM intents
		WHERE status IN ('authorized','captured') AND updated_at > now() - make_interval(secs => $1)`,
		a.o.OrphanAge.Seconds()).Scan(&p.YoungIntents)
	if err != nil {
		return p, fmt.Errorf("audit: young intents: %w", err)
	}
	return p, nil
}
