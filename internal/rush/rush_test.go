package rush

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseArrival(t *testing.T) {
	tests := []struct {
		in      string
		want    []ArrivalWindow
		wantErr string
	}{
		{in: DefaultArrival, want: []ArrivalWindow{
			{Name: "pre", Share: 0.7, From: -10 * time.Second, To: 0},
			{Name: "post", Share: 0.3, From: 0, To: 10 * time.Second},
		}},
		{in: "all=1:0s..1s", want: []ArrivalWindow{{Name: "all", Share: 1, From: 0, To: time.Second}}},
		{in: "a=0.5:0s..1s", wantErr: "sum to 0.500"},
		{in: "a=1:2s..1s", wantErr: "to > from"},
		{in: "a=x:0s..1s", wantErr: "share"},
		{in: "nope", wantErr: "want name=share"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseArrival(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v", got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("window %d: %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseArgs(t *testing.T) {
	env := map[string]string{"ADMIN_TOKEN": "x", "HOLD_TTL": "60s"}
	getenv := func(k string) string { return env[k] }
	o, err := ParseArgs([]string{"--scenario", "smoke", "--users", "2000", "--conn", "pooled"}, getenv, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if o.Scenario.Name != "SMOKE" || o.Users != 2000 || o.Conn != ConnPooled || o.HoldTTL != time.Minute {
		t.Fatalf("got %+v", o)
	}
	if _, err := ParseArgs(nil, func(string) string { return "" }, os.Stderr); err == nil || !strings.Contains(err.Error(), "ADMIN_TOKEN") {
		t.Fatalf("missing admin token: err %v", err)
	}
	if _, err := ParseArgs([]string{"--opens-in", "5s"}, getenv, os.Stderr); err == nil || !strings.Contains(err.Error(), "before T0") {
		t.Fatalf("arrival before open-sale: err %v", err)
	}
}

// The behaviour mix is reproducible from --seed and close to LOADTEST §3.3.
func TestMakePlansDeterministicMix(t *testing.T) {
	arr, err := ParseArrival(DefaultArrival)
	if err != nil {
		t.Fatal(err)
	}
	o := &Options{Users: 20_000, Seed: 42, Arrival: arr, Scenario: Scenarios["SMOKE"]}
	a, b := makePlans(o), makePlans(o)
	var qty1, pick, abandon, decline, pre int
	for i := range a {
		if a[i].arriveAt != b[i].arriveAt || a[i].qty != b[i].qty || a[i].pm != b[i].pm || a[i].double != b[i].double {
			t.Fatalf("plan %d differs between runs with the same seed", i)
		}
		if a[i].arriveAt < -10*time.Second || a[i].arriveAt >= 10*time.Second {
			t.Fatalf("plan %d arrives at %s, outside the profile", i, a[i].arriveAt)
		}
		if a[i].arriveAt < 0 {
			pre++
		}
		if a[i].qty == 1 {
			qty1++
		}
		if a[i].pick {
			pick++
		}
		if a[i].abandon {
			abandon++
		}
		if a[i].pm == pmDecline {
			decline++
		}
	}
	n := float64(len(a))
	for _, c := range []struct {
		name      string
		got, want float64
	}{
		{"pre-T0", float64(pre) / n, 0.70},
		{"qty1", float64(qty1) / n, 0.80},
		{"pick", float64(pick) / n, 0.30},
		{"abandon", float64(abandon) / n, 0.08},
		{"decline", float64(decline) / n, 0.03},
	} {
		if c.got < c.want-0.015 || c.got > c.want+0.015 {
			t.Errorf("%s share %.3f, want %.2f ± 0.015", c.name, c.got, c.want)
		}
	}
}

func TestFreeSeats(t *testing.T) {
	// seats 0..5: avail, held, sold, avail | avail, sold (+ padding)
	bits := []byte{0b00_01_10_00, 0b00_10_00_00}
	got := freeSeats(bits, 6)
	want := []int{0, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// Percentiles against a known distribution: 1..10000 ms uniform.
func TestQuantilesKnownDistribution(t *testing.T) {
	h := newHist(histSig)
	for i := 1; i <= 10_000; i++ {
		h.record(time.Duration(i) * time.Millisecond)
	}
	q := h.quantiles()
	for _, c := range []struct {
		name      string
		got, want float64
	}{{"p50", q.P50, 5000}, {"p95", q.P95, 9500}, {"p99", q.P99, 9900}, {"max", q.Max, 10000}} {
		if d := c.got - c.want; d < -c.want*0.001-0.1 || d > c.want*0.001+0.1 { // 3 significant digits
			t.Errorf("%s = %.1f ms, want %.0f ± 0.1%%", c.name, c.got, c.want)
		}
	}
	if q.N != 10_000 {
		t.Errorf("n = %d", q.N)
	}
}

// A server that stalls once for 1 s must show up in p99 when requests are
// timed from their intended start (coordinated omission), even though only
// one request was actually slow.
func TestIntendedStartCapturesStall(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 10 {
			time.Sleep(time.Second)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	start := time.Now()
	rec := NewRecorder(start, time.Minute)
	c := &client{hc: &http.Client{Transport: newTransport(ConnDedicated)}, base: srv.URL, xff: "198.18.0.1", rec: rec}
	naive := newHist(histSig)
	const reqs, every = 100, 10 * time.Millisecond
	for i := range reqs {
		intended := start.Add(time.Duration(i) * every)
		time.Sleep(time.Until(intended))
		sent := time.Now()
		c.do(t.Context(), req{rt: rStatus, method: http.MethodGet, path: "/", timeout: 5 * time.Second, intended: intended})
		naive.record(time.Since(sent))
	}
	got := rec.routes[rStatus].quantiles()
	if got.P99 < 900 {
		t.Fatalf("intended-start p99 = %.1f ms, want ≥ 900 (the 1 s stall delayed every later request)", got.P99)
	}
	// ~1 s of backlog at 10 ms spacing: dozens of requests carry the stall.
	if got.P50 < 50 {
		t.Errorf("intended-start p50 = %.1f ms; the backlog should lift the median", got.P50)
	}
	if nq := naive.quantiles(); nq.P95 > 100 {
		t.Logf("naive p95 = %.1f ms (for contrast)", nq.P95)
	}
	if rec.requests.Load() != reqs {
		t.Errorf("requests = %d", rec.requests.Load())
	}
}

func TestClassifyAndRetry(t *testing.T) {
	tests := []struct {
		r         result
		wantRetry bool
	}{
		{result{status: 201, oc: okResp}, false},
		{result{status: 409, oc: business, body: []byte(`{"code":"SEATS_TAKEN"}`)}, false},
		{result{status: 409, oc: business, body: []byte(`{"code":"REQUEST_IN_PROGRESS"}`)}, true},
		{result{status: 429, oc: business, retryAfter: 2 * time.Second}, true},
		{result{status: 503, oc: serverErr}, true},
		{result{oc: timedOut}, true},
		{result{oc: transport}, true},
	}
	for _, tc := range tests {
		wait, again := retryable(&tc.r, 0, 0, 0.5)
		if again != tc.wantRetry {
			t.Errorf("%+v: retry %t, want %t", tc.r, again, tc.wantRetry)
		}
		if tc.r.status == 429 && wait != 2*time.Second {
			t.Errorf("429 waits %s, want Retry-After 2s", wait)
		}
	}
	for status, want := range map[int]outcome{200: okResp, 404: okResp, 409: business, 422: business, 429: business, 500: serverErr, 503: serverErr} {
		if got := classify(status); got != want {
			t.Errorf("classify(%d) = %d, want %d", status, got, want)
		}
	}
}

// summary.json carries every LOADTEST §3.5 key; outputs are all written.
func TestWriteOutputsSchema(t *testing.T) {
	arr, _ := ParseArrival(DefaultArrival)
	o := &Options{Users: 10, Scenario: Scenarios["SMOKE"], Arrival: arr, Conn: ConnPooled, RunID: "t", APIReplicas: 3}
	t0 := time.Now()
	s := &swarm{o: o, t0: t0, rec: NewRecorder(t0.Add(-15*time.Second), time.Minute), anomalies: map[string]int64{}}
	s.rec.Record(rHold, t0, t0.Add(20*time.Millisecond), 201, okResp)
	s.rec.Record(rStatus, t0, t0.Add(5*time.Millisecond), 200, okResp)
	s.rec.Record(rHold, t0, t0.Add(time.Second), 409, business)
	s.outcomes[OutTicket].Add(1)
	rows := map[int]statsRow{15: {ok: true, Sold: 1, Available: 9}}
	sum := s.summarize(time.Now(), eventInfo{Slug: "e", Capacity: 10}, rows)
	dir := t.TempDir()
	if err := writeOutputs(dir, sum, s.timeseries(rows)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"run_id", "git_sha", "scenario", "started_at", "env", "config", "load", "outcomes", "timing", "latency_ms", "errors", "business", "audit", "headline"} {
		if _, ok := m[k]; !ok {
			t.Errorf("summary.json lacks %q", k)
		}
	}
	nested := map[string][]string{
		"load":     {"peak_in_system", "requests", "peak_rps", "mean_rps", "generator_lag_p99_ms"},
		"outcomes": {"TICKET", "SOLD_OUT", "NO_SEAT", "PAYMENT_FAILED", "ABANDONED", "UNRESOLVED", "seats_sold"},
		"errors":   {"rate", "5xx", "timeouts", "transport"},
		"business": {"409", "422", "429"},
		"headline": {"checkout_p95_ms", "status_p95_ms", "peak_rps"},
		"config":   {"users", "seats", "hold_ttl_s", "payment_hold_ttl_s", "api_replicas"},
		"timing":   {"t0", "first_sale_s", "sold_out_s", "drain_s"},
	}
	for obj, keys := range nested {
		var sub map[string]json.RawMessage
		if err := json.Unmarshal(m[obj], &sub); err != nil {
			t.Fatalf("%s: %v", obj, err)
		}
		for _, k := range keys {
			if _, ok := sub[k]; !ok {
				t.Errorf("summary.json %s lacks %q", obj, k)
			}
		}
	}
	if sum.Headline.CheckoutP95MS < 900 || sum.Business["409"] != 1 || sum.Outcomes["seats_sold"] != 1 {
		t.Errorf("headline %+v business %v outcomes %v", sum.Headline, sum.Business, sum.Outcomes)
	}
	csvRaw, err := os.ReadFile(filepath.Join(dir, "timeseries.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(csvRaw), strings.Join(TimeseriesHeader, ",")+"\n") {
		t.Errorf("timeseries.csv header: %q", strings.SplitN(string(csvRaw), "\n", 2)[0])
	}
	if md, err := os.ReadFile(filepath.Join(dir, "report.md")); err != nil || !strings.Contains(string(md), "UNRESOLVED") {
		t.Errorf("report.md: %v", err)
	}
}
