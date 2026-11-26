// Package config parses and validates SLUICE's environment configuration.
//
// Configuration is read once at startup and validated fail-fast: any invalid
// value, or any unsafe combination (for example BASELINE outside rush/test),
// makes Load return an error that lists every problem found.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// AppEnv is the deployment profile.
type AppEnv string

// Deployment profiles. Only rush and test may enable baselines or test endpoints.
const (
	EnvDev  AppEnv = "dev"
	EnvTest AppEnv = "test"
	EnvRush AppEnv = "rush"
	EnvDemo AppEnv = "demo"
)

// AllowsUnsafeModes reports whether baselines, test endpoints and an
// unenforced queue are permitted. They exist only to measure or test the
// system and must never be reachable on a demo or dev deployment.
func (e AppEnv) AllowsUnsafeModes() bool { return e == EnvRush || e == EnvTest }

// HoldPath selects the seat-hold implementation.
type HoldPath string

// Hold paths (see ARCHITECTURE.md §7).
const (
	HoldPathRedisPG HoldPath = "redis_pg"
	HoldPathPGOnly  HoldPath = "pg_only"
)

// Baseline selects a deliberately unsafe comparison mode (LOADTEST.md).
type Baseline string

// Baselines. Anything other than BaselineOff is refused outside rush/test.
const (
	BaselineOff       Baseline = "off"
	BaselineHoldsOnly Baseline = "holds_only"
	BaselineNaive     Baseline = "naive"
)

// MinTokenKeyBytes is the minimum decoded length of an HS256 signing key.
const MinTokenKeyBytes = 32

// devTokenSecret is the obviously fake dev key (base64 of
// "sluice-dev-only-not-a-secret-32b"). It is refused when APP_ENV=demo.
const devTokenSecret = "c2x1aWNlLWRldi1vbmx5LW5vdC1hLXNlY3JldC0zMmI=" //nolint:gosec // G101: deliberately public dev-only key, refused when APP_ENV=demo

const (
	defaultTokenKeys  = "dev1:" + devTokenSecret
	defaultAdminToken = "dev-admin-token-change-me" //nolint:gosec // G101: deliberately public dev-only token, refused when APP_ENV=demo
)

// TokenKey is one entry of the JWT key ring.
type TokenKey struct {
	KID    string
	Secret []byte
}

// String never prints the secret.
func (k TokenKey) String() string { return "TokenKey{kid=" + k.KID + "}" }

// IntRange is an inclusive [Min, Max] range, for example MOCKPAY_LATENCY_MS=20-60.
type IntRange struct {
	Min, Max int
}

// Config is the fully parsed and validated configuration.
type Config struct {
	AppEnv   AppEnv
	LogLevel slog.Level

	HTTPAddr        string
	WorkerHTTPAddr  string
	MockpayHTTPAddr string
	PprofAddr       string

	DatabaseURL        string
	MockpayDatabaseURL string
	DBMaxConns         int32
	RedisURL           string
	// MaxInFlight bounds concurrent API requests per replica; beyond it a
	// request waits up to ShedWait, then gets 503 OVERLOADED (ADR 0013).
	MaxInFlight int
	ShedWait    time.Duration

	TokenKeys         []TokenKey
	AdminToken        string
	CORSOrigins       []string
	TrustedProxies    []netip.Prefix
	RateLimitsEnabled bool

	HoldTTL              time.Duration
	PaymentHoldTTL       time.Duration
	AdmissionTTL         time.Duration
	AdmissionIdleTTL     time.Duration // sliding lease; ADMISSION_TTL stays the hard cap
	OfferClaimTTL        time.Duration
	QueueTick            time.Duration
	QueueMaxActive       int
	QueueOveradmit       float64
	QueueMinAdmit        int
	QueueMaxAdmitPerTick int
	QueueEnforced        bool
	HoldPath             HoldPath
	Baseline             Baseline
	MaxSeatsPerOrder     int
	SweeperInterval      time.Duration
	ReconcileInterval    time.Duration

	MockpayURL                    string
	MockpayPublicURL              string
	MockpayAPIKey                 string
	PSPMaxConns                   int // PSP client connection pool bound
	MockpayWebhookSecret          string
	PublicWebhookURL              string
	PublicAppURL                  string
	MockpayLatencyMS              IntRange
	MockpayFailRate               float64
	MockpayTimeoutAfterCommitRate float64
	MockpayHangMS                 int
	MockpayWebhookDupRate         float64
	MockpayWebhookReorderRate     float64
	MockpayWebhookMaxDelayMS      int

	DemoMode             bool
	TestEndpointsEnabled bool
}

// LookupFunc matches os.LookupEnv so tests can inject an environment.
type LookupFunc func(key string) (string, bool)

// FromEnv loads the configuration from the process environment.
func FromEnv() (*Config, error) { return Load(os.LookupEnv) }

// Load parses and validates the configuration from lookup.
func Load(lookup LookupFunc) (*Config, error) {
	r := &reader{lookup: lookup}
	c := &Config{}

	c.AppEnv = AppEnv(r.oneOf("APP_ENV", "dev", "dev", "test", "rush", "demo"))
	c.LogLevel = r.logLevel("LOG_LEVEL", "info")

	c.HTTPAddr = r.addr("HTTP_ADDR", ":8080")
	c.WorkerHTTPAddr = r.addr("WORKER_HTTP_ADDR", ":9091")
	c.MockpayHTTPAddr = r.addr("MOCKPAY_HTTP_ADDR", ":8090")
	c.PprofAddr = r.optionalAddr("PPROF_ADDR")

	c.DatabaseURL = r.url("DATABASE_URL", "postgres://sluice:sluice@localhost:5433/sluice?sslmode=disable", "postgres", "postgresql")
	c.MockpayDatabaseURL = r.url("MOCKPAY_DATABASE_URL", "postgres://sluice:sluice@localhost:5433/mockpay?sslmode=disable", "postgres", "postgresql")
	c.DBMaxConns = int32(r.intIn("DB_MAX_CONNS", 24, 1, 1000)) //nolint:gosec // bounded to [1, 1000] above
	c.RedisURL = r.url("REDIS_URL", "redis://localhost:6381/0", "redis", "rediss")
	c.MaxInFlight = r.intIn("MAX_IN_FLIGHT", 96, 0, 100000)
	c.ShedWait = r.duration("SHED_WAIT", "50ms")

	c.TokenKeys = r.tokenKeys("TOKEN_KEYS", defaultTokenKeys)
	c.AdminToken = r.secret("ADMIN_TOKEN", defaultAdminToken, 16)
	c.CORSOrigins = r.origins("CORS_ORIGINS", "http://localhost:3000")
	c.TrustedProxies = r.prefixes("TRUSTED_PROXIES", "127.0.0.1/32,172.16.0.0/12")
	c.RateLimitsEnabled = r.bool("RATE_LIMITS_ENABLED", true)

	c.HoldTTL = r.duration("HOLD_TTL", "5m")
	c.PaymentHoldTTL = r.duration("PAYMENT_HOLD_TTL", "10m")
	c.AdmissionTTL = r.duration("ADMISSION_TTL", "10m")
	c.AdmissionIdleTTL = r.duration("ADMISSION_IDLE_TTL", "2m")
	c.OfferClaimTTL = r.duration("OFFER_CLAIM_TTL", "60s")
	c.QueueTick = r.duration("QUEUE_TICK", "100ms")
	c.QueueMaxActive = r.intIn("QUEUE_MAX_ACTIVE", 1000, 1, 1_000_000)
	c.QueueOveradmit = r.floatIn("QUEUE_OVERADMIT", 1.5, 1, 100)
	c.QueueMinAdmit = r.intIn("QUEUE_MIN_ADMIT", 10, 0, 1_000_000)
	c.QueueMaxAdmitPerTick = r.intIn("QUEUE_MAX_ADMIT_PER_TICK", 200, 1, 1_000_000)
	c.QueueEnforced = r.bool("QUEUE_ENFORCED", true)
	c.HoldPath = HoldPath(r.oneOf("HOLD_PATH", "redis_pg", "redis_pg", "pg_only"))
	c.Baseline = Baseline(r.oneOf("BASELINE", "off", "off", "holds_only", "naive"))
	c.MaxSeatsPerOrder = r.intIn("MAX_SEATS_PER_ORDER", 4, 1, 10)
	c.SweeperInterval = r.duration("SWEEPER_INTERVAL", "1s")
	c.ReconcileInterval = r.duration("RECONCILE_INTERVAL", "30s")

	c.MockpayURL = r.url("MOCKPAY_URL", "http://localhost:8090", "http", "https")
	c.MockpayPublicURL = r.publicURL("MOCKPAY_PUBLIC_URL", "http://localhost:8090")
	c.MockpayAPIKey = r.secret("MOCKPAY_API_KEY", "dev-mockpay-key", 8)
	c.PSPMaxConns = r.intIn("PSP_MAX_CONNS", 64, 1, 4096)
	c.MockpayWebhookSecret = r.secret("MOCKPAY_WEBHOOK_SECRET", "dev-webhook-secret", 8)
	c.PublicWebhookURL = r.url("PUBLIC_WEBHOOK_URL", "http://localhost:8080/api/v1/webhooks/mockpay", "http", "https")
	c.PublicAppURL = r.publicURL("PUBLIC_APP_URL", "http://localhost:3000")
	c.MockpayLatencyMS = r.intRange("MOCKPAY_LATENCY_MS", "20-60")
	c.MockpayFailRate = r.floatIn("MOCKPAY_FAIL_RATE", 0, 0, 1)
	c.MockpayTimeoutAfterCommitRate = r.floatIn("MOCKPAY_TIMEOUT_AFTER_COMMIT_RATE", 0, 0, 1)
	c.MockpayHangMS = r.intIn("MOCKPAY_HANG_MS", 3000, 0, 600_000)
	c.MockpayWebhookDupRate = r.floatIn("MOCKPAY_WEBHOOK_DUP_RATE", 0, 0, 1)
	c.MockpayWebhookReorderRate = r.floatIn("MOCKPAY_WEBHOOK_REORDER_RATE", 0, 0, 1)
	c.MockpayWebhookMaxDelayMS = r.intIn("MOCKPAY_WEBHOOK_MAX_DELAY_MS", 0, 0, 600_000)

	c.DemoMode = r.bool("DEMO_MODE", false)
	c.TestEndpointsEnabled = r.bool("TEST_ENDPOINTS_ENABLED", false)

	c.validate(r)
	if len(r.errs) > 0 {
		return nil, fmt.Errorf("invalid configuration: %w", errors.Join(r.errs...))
	}
	return c, nil
}

// validate checks cross-field rules. Field-level parse errors are already in r.errs.
func (c *Config) validate(r *reader) {
	unsafeOK := c.AppEnv.AllowsUnsafeModes()
	if c.Baseline != BaselineOff && !unsafeOK {
		r.fail("BASELINE", fmt.Errorf("%q is only allowed when APP_ENV is rush or test (APP_ENV=%s)", c.Baseline, c.AppEnv))
	}
	if c.TestEndpointsEnabled && !unsafeOK {
		r.fail("TEST_ENDPOINTS_ENABLED", fmt.Errorf("only allowed when APP_ENV is rush or test (APP_ENV=%s)", c.AppEnv))
	}
	if !c.QueueEnforced && !unsafeOK {
		r.fail("QUEUE_ENFORCED", fmt.Errorf("false is only allowed when APP_ENV is rush or test (APP_ENV=%s)", c.AppEnv))
	}
	if c.AppEnv == EnvDemo {
		// The demo is reachable from the public internet through a tunnel.
		for _, k := range c.TokenKeys {
			if base64.StdEncoding.EncodeToString(k.Secret) == devTokenSecret {
				r.fail("TOKEN_KEYS", errors.New("the dev key must not be used when APP_ENV=demo"))
			}
		}
		if c.AdminToken == defaultAdminToken {
			r.fail("ADMIN_TOKEN", errors.New("the dev token must not be used when APP_ENV=demo"))
		}
	}
	if c.AdmissionIdleTTL <= 0 || c.AdmissionIdleTTL > c.AdmissionTTL {
		r.fail("ADMISSION_IDLE_TTL", fmt.Errorf("must be > 0 and <= ADMISSION_TTL (%s, %s)", c.AdmissionIdleTTL, c.AdmissionTTL))
	}
	if c.PaymentHoldTTL < c.HoldTTL {
		r.fail("PAYMENT_HOLD_TTL", fmt.Errorf("must be >= HOLD_TTL (%s < %s)", c.PaymentHoldTTL, c.HoldTTL))
	}
}

// Keys returns every environment variable name Load reads, in read order.
// The .env.example coverage test uses it.
func Keys() []string {
	r := &reader{lookup: func(string) (string, bool) { return "", false }}
	_, _ = Load(r.recordingLookup())
	return r.keys
}

// reader wraps a LookupFunc, records every key read and collects errors so
// Load can report all problems at once.
type reader struct {
	lookup LookupFunc
	keys   []string
	errs   []error
}

// recordingLookup returns a LookupFunc that records keys into r and then
// delegates to r.lookup. Used by Keys.
func (r *reader) recordingLookup() LookupFunc {
	return func(key string) (string, bool) {
		r.keys = append(r.keys, key)
		return r.lookup(key)
	}
}

func (r *reader) fail(key string, err error) {
	r.errs = append(r.errs, fmt.Errorf("%s: %w", key, err))
}

// raw returns the trimmed value or def when unset or empty.
func (r *reader) raw(key, def string) string {
	v, ok := r.lookup(key)
	v = strings.TrimSpace(v)
	if !ok || v == "" {
		return def
	}
	return v
}

func (r *reader) oneOf(key, def string, allowed ...string) string {
	v := strings.ToLower(r.raw(key, def))
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	r.fail(key, fmt.Errorf("%q is not one of %s", v, strings.Join(allowed, " | ")))
	return def
}

func (r *reader) logLevel(key, def string) slog.Level {
	v := r.raw(key, def)
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(v)); err != nil {
		r.fail(key, fmt.Errorf("%q is not a log level (debug | info | warn | error)", v))
		return slog.LevelInfo
	}
	return lvl
}

func (r *reader) addr(key, def string) string {
	v := r.raw(key, def)
	if err := checkAddr(v); err != nil {
		r.fail(key, err)
	}
	return v
}

func (r *reader) optionalAddr(key string) string {
	v := r.raw(key, "")
	if v == "" {
		return ""
	}
	if err := checkAddr(v); err != nil {
		r.fail(key, err)
	}
	return v
}

func checkAddr(v string) error {
	i := strings.LastIndex(v, ":")
	if i < 0 {
		return fmt.Errorf("%q must be host:port or :port", v)
	}
	port, err := strconv.Atoi(v[i+1:])
	if err != nil || port < 0 || port > 65535 {
		return fmt.Errorf("%q has an invalid port", v)
	}
	return nil
}

func (r *reader) url(key, def string, schemes ...string) string {
	v := r.raw(key, def)
	u, err := url.Parse(v)
	if err != nil {
		// Don't echo the value: database URLs carry passwords.
		r.fail(key, errors.New("not a valid URL"))
		return v
	}
	for _, s := range schemes {
		if u.Scheme == s && u.Host != "" {
			return v
		}
	}
	r.fail(key, fmt.Errorf("scheme must be one of %s with a host", strings.Join(schemes, ", ")))
	return v
}

// publicURL reads a URL that browsers follow: an absolute http(s) URL with a
// host, or a same-origin absolute path such as "/pay" (ADR 0016: behind a
// Cloudflare Quick Tunnel the public host is random, so the demo profile
// uses paths). Protocol-relative "//host" is refused: it would leave the origin.
func (r *reader) publicURL(key, def string) string {
	v := r.raw(key, def)
	u, err := url.Parse(v)
	if err != nil {
		r.fail(key, errors.New("not a valid URL"))
		return v
	}
	if (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return v
	}
	if u.Scheme == "" && u.Host == "" && strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") && !strings.HasPrefix(v, "/\\") {
		return v
	}
	r.fail(key, errors.New("must be an http(s) URL with a host or a path starting with a single /"))
	return v
}

func (r *reader) intIn(key string, def, lo, hi int) int {
	v := r.raw(key, strconv.Itoa(def))
	n, err := strconv.Atoi(v)
	if err != nil {
		r.fail(key, fmt.Errorf("%q is not an integer", v))
		return def
	}
	if n < lo || n > hi {
		r.fail(key, fmt.Errorf("%d is outside [%d, %d]", n, lo, hi))
	}
	return n
}

func (r *reader) floatIn(key string, def, lo, hi float64) float64 {
	v := r.raw(key, strconv.FormatFloat(def, 'f', -1, 64))
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		r.fail(key, fmt.Errorf("%q is not a finite number", v))
		return def
	}
	if f < lo || f > hi {
		r.fail(key, fmt.Errorf("%v is outside [%v, %v]", f, lo, hi))
	}
	return f
}

func (r *reader) bool(key string, def bool) bool {
	v := r.raw(key, strconv.FormatBool(def))
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.fail(key, fmt.Errorf("%q is not a boolean", v))
		return def
	}
	return b
}

func (r *reader) duration(key, def string) time.Duration {
	v := r.raw(key, def)
	d, err := time.ParseDuration(v)
	if err != nil {
		r.fail(key, fmt.Errorf("%q is not a duration (e.g. 500ms, 60s, 5m)", v))
		return 0
	}
	if d <= 0 {
		r.fail(key, fmt.Errorf("%s must be positive", d))
	}
	return d
}

func (r *reader) intRange(key, def string) IntRange {
	v := r.raw(key, def)
	lo, hi, ok := strings.Cut(v, "-")
	if !ok {
		hi = lo
	}
	a, errA := strconv.Atoi(strings.TrimSpace(lo))
	b, errB := strconv.Atoi(strings.TrimSpace(hi))
	if errA != nil || errB != nil || a < 0 || b < a {
		r.fail(key, fmt.Errorf("%q must be N or MIN-MAX with 0 <= MIN <= MAX", v))
		return IntRange{}
	}
	return IntRange{Min: a, Max: b}
}

func (r *reader) secret(key, def string, minLen int) string {
	v := r.raw(key, def)
	if len(v) < minLen {
		r.fail(key, fmt.Errorf("must be at least %d characters", minLen))
	}
	return v
}

func (r *reader) tokenKeys(key, def string) []TokenKey {
	v := r.raw(key, def)
	var out []TokenKey
	seen := map[string]bool{}
	for i, part := range strings.Split(v, ",") {
		kid, enc, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok || kid == "" || enc == "" {
			r.fail(key, fmt.Errorf("entry %d must be kid:base64secret", i+1))
			continue
		}
		if seen[kid] {
			r.fail(key, fmt.Errorf("duplicate kid %q", kid))
			continue
		}
		seen[kid] = true
		secret, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			r.fail(key, fmt.Errorf("kid %q: secret is not valid base64", kid))
			continue
		}
		if len(secret) < MinTokenKeyBytes {
			r.fail(key, fmt.Errorf("kid %q: secret must decode to at least %d bytes, got %d", kid, MinTokenKeyBytes, len(secret)))
			continue
		}
		out = append(out, TokenKey{KID: kid, Secret: secret})
	}
	return out
}

func (r *reader) origins(key, def string) []string {
	v := r.raw(key, def)
	var out []string
	for _, o := range strings.Split(v, ",") {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o == "" {
			continue
		}
		if o == "*" {
			r.fail(key, errors.New(`"*" is not allowed; list origins explicitly`))
			continue
		}
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" {
			r.fail(key, fmt.Errorf("%q must be an origin like https://example.com", o))
			continue
		}
		out = append(out, o)
	}
	return out
}

func (r *reader) prefixes(key, def string) []netip.Prefix {
	v := r.raw(key, def)
	var out []netip.Prefix
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			r.fail(key, fmt.Errorf("%q is not a CIDR", s))
			continue
		}
		out = append(out, p.Masked())
	}
	return out
}
