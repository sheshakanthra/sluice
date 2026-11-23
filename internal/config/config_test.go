package config

import (
	"bufio"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// webOnlyKeys are in .env.example but read by the Next.js app, not Go.
var webOnlyKeys = []string{"NEXT_PUBLIC_API_BASE_URL"}

func env(m map[string]string) LookupFunc {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatalf("defaults must be valid: %v", err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"AppEnv", c.AppEnv, EnvDev},
		{"LogLevel", c.LogLevel, slog.LevelInfo},
		{"HTTPAddr", c.HTTPAddr, ":8080"},
		{"PprofAddr", c.PprofAddr, ""},
		{"DBMaxConns", c.DBMaxConns, int32(24)},
		{"RedisURL", c.RedisURL, "redis://localhost:6381/0"},
		{"TokenKeys.len", len(c.TokenKeys), 1},
		{"TokenKeys[0].KID", c.TokenKeys[0].KID, "dev1"},
		{"TokenKeys[0].Secret", string(c.TokenKeys[0].Secret), "sluice-dev-only-not-a-secret-32b"},
		{"CORSOrigins", strings.Join(c.CORSOrigins, ","), "http://localhost:3000"},
		{"TrustedProxies.len", len(c.TrustedProxies), 2},
		{"HoldTTL", c.HoldTTL, 5 * time.Minute},
		{"PaymentHoldTTL", c.PaymentHoldTTL, 10 * time.Minute},
		{"QueueTick", c.QueueTick, 100 * time.Millisecond},
		{"QueueOveradmit", c.QueueOveradmit, 1.5},
		{"QueueEnforced", c.QueueEnforced, true},
		{"HoldPath", c.HoldPath, HoldPathRedisPG},
		{"Baseline", c.Baseline, BaselineOff},
		{"MaxSeatsPerOrder", c.MaxSeatsPerOrder, 4},
		{"MockpayLatencyMS", c.MockpayLatencyMS, IntRange{Min: 20, Max: 60}},
		{"MockpayHangMS", c.MockpayHangMS, 3000},
		{"TestEndpointsEnabled", c.TestEndpointsEnabled, false},
	}
	for _, ck := range checks {
		if ck.got != ck.want {
			t.Errorf("%s = %v, want %v", ck.name, ck.got, ck.want)
		}
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"APP_ENV":                "rush",
		"LOG_LEVEL":              "WARN",
		"PPROF_ADDR":             ":6060",
		"HOLD_TTL":               "60s",
		"PAYMENT_HOLD_TTL":       "90s",
		"BASELINE":               "naive",
		"TEST_ENDPOINTS_ENABLED": "true",
		"QUEUE_ENFORCED":         "false",
		"MOCKPAY_LATENCY_MS":     "5",
		"MOCKPAY_FAIL_RATE":      "0.02",
		"CORS_ORIGINS":           " http://a.test/ , https://b.test ",
		"TRUSTED_PROXIES":        "10.1.2.3/8",
		"TOKEN_KEYS":             "k2:" + devTokenSecret + ",k1:" + devTokenSecret,
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LogLevel != slog.LevelWarn || c.PprofAddr != ":6060" || c.HoldTTL != time.Minute {
		t.Errorf("basic overrides not applied: %+v", c)
	}
	if c.Baseline != BaselineNaive || !c.TestEndpointsEnabled || c.QueueEnforced {
		t.Errorf("rush-only modes not applied")
	}
	if c.MockpayLatencyMS != (IntRange{5, 5}) || c.MockpayFailRate != 0.02 {
		t.Errorf("mockpay overrides: %+v %v", c.MockpayLatencyMS, c.MockpayFailRate)
	}
	if got := strings.Join(c.CORSOrigins, ","); got != "http://a.test,https://b.test" {
		t.Errorf("CORSOrigins = %q", got)
	}
	if c.TrustedProxies[0] != netip.MustParsePrefix("10.0.0.0/8") {
		t.Errorf("prefix must be masked, got %v", c.TrustedProxies[0])
	}
	if c.TokenKeys[0].KID != "k2" || c.TokenKeys[1].KID != "k1" {
		t.Errorf("key ring order must be preserved (first signs): %v", c.TokenKeys)
	}
}

func TestLoadRejects(t *testing.T) {
	short := "c2hvcnQ=" // the word short, base64-encoded (5 bytes)
	tests := []struct {
		name    string
		env     map[string]string
		wantKey string
	}{
		{"baseline in dev", map[string]string{"BASELINE": "naive"}, "BASELINE"},
		{"baseline holds_only in demo", map[string]string{"APP_ENV": "demo", "BASELINE": "holds_only", "TOKEN_KEYS": "p:" + strings.Repeat("A", 44), "ADMIN_TOKEN": "a-real-looking-admin-token"}, "BASELINE"},
		{"test endpoints in dev", map[string]string{"TEST_ENDPOINTS_ENABLED": "true"}, "TEST_ENDPOINTS_ENABLED"},
		{"queue unenforced in dev", map[string]string{"QUEUE_ENFORCED": "false"}, "QUEUE_ENFORCED"},
		{"dev token key in demo", map[string]string{"APP_ENV": "demo", "ADMIN_TOKEN": "a-real-looking-admin-token"}, "TOKEN_KEYS"},
		{"dev admin token in demo", map[string]string{"APP_ENV": "demo", "TOKEN_KEYS": "p:" + strings.Repeat("A", 44)}, "ADMIN_TOKEN"},
		{"unknown app env", map[string]string{"APP_ENV": "prod"}, "APP_ENV"},
		{"bad log level", map[string]string{"LOG_LEVEL": "loud"}, "LOG_LEVEL"},
		{"bad http addr", map[string]string{"HTTP_ADDR": "8080"}, "HTTP_ADDR"},
		{"bad pprof addr", map[string]string{"PPROF_ADDR": "x:99999"}, "PPROF_ADDR"},
		{"wrong db scheme", map[string]string{"DATABASE_URL": "mysql://x/y"}, "DATABASE_URL"},
		{"wrong redis scheme", map[string]string{"REDIS_URL": "http://localhost:6380"}, "REDIS_URL"},
		{"db conns zero", map[string]string{"DB_MAX_CONNS": "0"}, "DB_MAX_CONNS"},
		{"token key too short", map[string]string{"TOKEN_KEYS": "k:" + short}, "TOKEN_KEYS"},
		{"token key not base64", map[string]string{"TOKEN_KEYS": "k:!!!"}, "TOKEN_KEYS"},
		{"token key missing kid", map[string]string{"TOKEN_KEYS": devTokenSecret}, "TOKEN_KEYS"},
		{"duplicate kid", map[string]string{"TOKEN_KEYS": "a:" + devTokenSecret + ",a:" + devTokenSecret}, "TOKEN_KEYS"},
		{"cors wildcard", map[string]string{"CORS_ORIGINS": "*"}, "CORS_ORIGINS"},
		{"cors with path", map[string]string{"CORS_ORIGINS": "http://a.test/app"}, "CORS_ORIGINS"},
		{"bad cidr", map[string]string{"TRUSTED_PROXIES": "10.0.0.1"}, "TRUSTED_PROXIES"},
		{"bad bool", map[string]string{"RATE_LIMITS_ENABLED": "yes please"}, "RATE_LIMITS_ENABLED"},
		{"bad duration", map[string]string{"HOLD_TTL": "5 minutes"}, "HOLD_TTL"},
		{"negative duration", map[string]string{"QUEUE_TICK": "-1s"}, "QUEUE_TICK"},
		{"payment ttl below hold ttl", map[string]string{"HOLD_TTL": "10m", "PAYMENT_HOLD_TTL": "5m"}, "PAYMENT_HOLD_TTL"},
		{"overadmit below 1", map[string]string{"QUEUE_OVERADMIT": "0.5"}, "QUEUE_OVERADMIT"},
		{"overadmit NaN", map[string]string{"QUEUE_OVERADMIT": "NaN"}, "QUEUE_OVERADMIT"},
		{"seats per order too many", map[string]string{"MAX_SEATS_PER_ORDER": "11"}, "MAX_SEATS_PER_ORDER"},
		{"unknown hold path", map[string]string{"HOLD_PATH": "redis_only"}, "HOLD_PATH"},
		{"fail rate above 1", map[string]string{"MOCKPAY_FAIL_RATE": "1.5"}, "MOCKPAY_FAIL_RATE"},
		{"latency inverted", map[string]string{"MOCKPAY_LATENCY_MS": "60-20"}, "MOCKPAY_LATENCY_MS"},
		{"latency garbage", map[string]string{"MOCKPAY_LATENCY_MS": "fast"}, "MOCKPAY_LATENCY_MS"},
		{"mockpay key too short", map[string]string{"MOCKPAY_API_KEY": "abc"}, "MOCKPAY_API_KEY"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Load(env(tc.env))
			if err == nil {
				t.Fatalf("expected an error mentioning %s, got config %+v", tc.wantKey, c)
			}
			if !strings.Contains(err.Error(), tc.wantKey+":") {
				t.Fatalf("error %q does not mention %s", err, tc.wantKey)
			}
		})
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := Load(env(map[string]string{"HOLD_TTL": "x", "DB_MAX_CONNS": "x", "BASELINE": "naive"}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, k := range []string{"HOLD_TTL:", "DB_MAX_CONNS:", "BASELINE:"} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error should list %s; got %q", k, err)
		}
	}
}

func TestErrorsNeverEchoDatabasePasswords(t *testing.T) {
	_, err := Load(env(map[string]string{"DATABASE_URL": "mysql://u:hunter2-secret@h/db"}))
	if err == nil || strings.Contains(err.Error(), "hunter2-secret") {
		t.Fatalf("error must not contain the password: %v", err)
	}
}

func TestTokenKeyStringHidesSecret(t *testing.T) {
	k := TokenKey{KID: "dev1", Secret: []byte("super-secret-bytes")}
	if s := k.String(); strings.Contains(s, "super-secret") {
		t.Fatalf("String leaked the secret: %s", s)
	}
}

// TestEnvExampleCoversConfig enforces CLAUDE.md rule 7: .env.example lists
// every variable the config reads, and nothing the code no longer reads.
func TestEnvExampleCoversConfig(t *testing.T) {
	example := readEnvExample(t)
	read := Keys()
	if len(read) < 40 {
		t.Fatalf("Keys() returned only %d keys; recording is broken", len(read))
	}
	for _, k := range read {
		if _, ok := example[k]; !ok {
			t.Errorf(".env.example is missing %s (read by internal/config)", k)
		}
	}
	for k := range example {
		if !slices.Contains(read, k) && !slices.Contains(webOnlyKeys, k) {
			t.Errorf(".env.example lists %s but nothing reads it", k)
		}
	}
	// The example values must themselves be a valid dev configuration.
	if _, err := Load(env(example)); err != nil {
		t.Errorf(".env.example values do not load: %v", err)
	}
}

// TestEnvExampleMatchesStackDoc keeps .env.example in sync with STACK.md §4.
func TestEnvExampleMatchesStackDoc(t *testing.T) {
	example := readEnvExample(t)
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "STACK.md"))
	if err != nil {
		t.Fatalf("read STACK.md: %v", err)
	}
	_, section, ok := strings.Cut(string(b), "## 4. Environment variables")
	if !ok {
		t.Fatal("STACK.md has no '## 4. Environment variables' section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	row := regexp.MustCompile("(?m)^\\| `([A-Z][A-Z0-9_]+)` \\|")
	matches := row.FindAllStringSubmatch(section, -1)
	if len(matches) == 0 {
		t.Fatal("no variables found in STACK.md §4")
	}
	doc := map[string]bool{}
	for _, m := range matches {
		doc[m[1]] = true
		if _, ok := example[m[1]]; !ok {
			t.Errorf("STACK.md §4 lists %s but .env.example does not", m[1])
		}
	}
	for k := range example {
		if !doc[k] {
			t.Errorf(".env.example lists %s but STACK.md §4 does not", k)
		}
	}
}

func readEnvExample(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join(repoRoot(t), ".env.example"))
	if err != nil {
		t.Fatalf("open .env.example: %v", err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.Contains(v, " #") {
			t.Fatalf(".env.example:%d: want KEY=value with comments on their own line, got %q", n, line)
		}
		if _, dup := out[k]; dup {
			t.Fatalf(".env.example:%d: duplicate key %s", n, k)
		}
		out[k] = v
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan .env.example: %v", err)
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test directory")
		}
		dir = parent
	}
}
