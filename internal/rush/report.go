package rush

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sheshakanthra/sluice/internal/audit"
	"github.com/sheshakanthra/sluice/internal/buildinfo"
)

// GeneratorBoundLagMS: lag p99 above this flags the run as generator-bound (LOADTEST §3.4).
const GeneratorBoundLagMS = 100

// Summary is summary.json (LOADTEST §3.5), the source of every published number.
type Summary struct {
	RunID     string               `json:"run_id"`
	GitSHA    string               `json:"git_sha"`
	Scenario  string               `json:"scenario"`
	StartedAt time.Time            `json:"started_at"`
	Env       SummaryEnv           `json:"env"`
	Config    SummaryConfig        `json:"config"`
	Load      SummaryLoad          `json:"load"`
	Outcomes  orderedOutcomes      `json:"outcomes"`
	Timing    SummaryTiming        `json:"timing"`
	Latency   map[string]Quantiles `json:"latency_ms"`
	Errors    SummaryErrors        `json:"errors"`
	Business  map[string]int64     `json:"business"`
	// Unexpected answers by step ("hold:401 TOKEN_INVALID"); not part of the
	// LOADTEST schema, kept so nothing odd hides inside an outcome count.
	Anomalies map[string]int64 `json:"anomalies"`
	Generator SummaryGenerator `json:"generator"`
	// Records are swarm-side facts the auditor checks (LOADTEST §6).
	Records *SummaryRecords `json:"records"`
	// Audit is filled by `sluice audit` / `sluice rush --audit`; null until then.
	Audit    *SummaryAudit   `json:"audit"`
	Headline SummaryHeadline `json:"headline"`
}

// orderedOutcomes marshals in the schema's key order.
type orderedOutcomes map[string]int64

func (o orderedOutcomes) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	keys := make([]string, 0, len(outcomeNames)+1)
	keys = append(keys, outcomeNames[:]...)
	keys = append(keys, "seats_sold")
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:%d", k, o[k])
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// SummaryEnv describes where the run happened.
type SummaryEnv struct {
	Runner   string  `json:"runner"`
	CPUs     int     `json:"cpus"`
	RAMGB    float64 `json:"ram_gb"`
	Go       string  `json:"go"`
	Postgres string  `json:"postgres"`
	Redis    string  `json:"redis"`
	// SwarmCPUSet is the CPU list the swarm container was pinned to
	// (RUSH_CPUSET), empty when unpinned. When set, CPUs is the swarm's own
	// view (the pinned count), not the host's.
	SwarmCPUSet string `json:"swarm_cpuset,omitempty"`
}

// SummaryConfig is the scenario configuration.
type SummaryConfig struct {
	Users           int    `json:"users"`
	Seats           int    `json:"seats"`
	HoldTTLS        int    `json:"hold_ttl_s"`
	PaymentHoldTTLS int    `json:"payment_hold_ttl_s"`
	APIReplicas     int    `json:"api_replicas"`
	Event           string `json:"event"`
	Conn            string `json:"conn"`
	Arrival         string `json:"arrival"`
	OpensInS        int    `json:"opens_in_s"`
	Seed            uint64 `json:"seed"`
	Direct          bool   `json:"direct"`
}

// SummaryLoad is the offered load.
type SummaryLoad struct {
	PeakInSystem      int64   `json:"peak_in_system"`
	Requests          int64   `json:"requests"`
	PeakRPS           int64   `json:"peak_rps"`
	MeanRPS           float64 `json:"mean_rps"`
	GeneratorLagP99MS float64 `json:"generator_lag_p99_ms"`
}

// SummaryTiming is relative to T0, in seconds (null when it never happened).
type SummaryTiming struct {
	T0          time.Time `json:"t0"`
	FirstSaleS  *float64  `json:"first_sale_s"`
	SoldOutS    *float64  `json:"sold_out_s"`
	DrainS      *float64  `json:"drain_s"`
	WindowFromS float64   `json:"window_from_s"`
}

// SummaryErrors are failures (business 409/422/429 are not errors).
type SummaryErrors struct {
	Rate      float64 `json:"rate"`
	S5xx      int64   `json:"5xx"`
	Timeouts  int64   `json:"timeouts"`
	Transport int64   `json:"transport"`
}

// SummaryGenerator is the swarm self-check.
type SummaryGenerator struct {
	LagP99MS       float64 `json:"lag_p99_ms"`
	LagMaxMS       float64 `json:"lag_max_ms"`
	CPUCoresMax    float64 `json:"cpu_cores_max"`
	CPUCoresMean   float64 `json:"cpu_cores_mean"`
	GoroutinesMax  int     `json:"goroutines_max"`
	GeneratorBound bool    `json:"generator_bound"`
	StatsSamples   int     `json:"stats_samples"`
	StatsFailed    int     `json:"stats_failed"`
}

// SummaryRecords are the swarm's own records for the auditor.
type SummaryRecords struct {
	// AdmittedAfterSoldout counts ADMITTED answers to requests the swarm
	// sent more than SoldOutGraceMS after it first saw the event sold out
	// on /stats (null in direct mode: no waiting room).
	AdmittedAfterSoldout *int64     `json:"admitted_after_soldout"`
	Admissions           int64      `json:"admissions"`
	SoldOutSeenAt        *time.Time `json:"sold_out_seen_at"`
	SoldOutGraceMS       int64      `json:"sold_out_grace_ms"`
	Note                 string     `json:"note,omitempty"`
}

// SummaryAudit mirrors the auditor's verdict (audit.json has the details).
// A null count means the check does not apply in that mode.
type SummaryAudit struct {
	Mode                   string  `json:"mode"`
	Oversold               *int64  `json:"oversold"`
	DuplicateCharges       *int64  `json:"duplicate_charges"`
	OrphanCharges          *int64  `json:"orphan_charges"`
	QuotaViolations        *int64  `json:"quota_violations"`
	StuckHolds             *int64  `json:"stuck_holds"`
	InventoryDrift         *int64  `json:"inventory_drift"`
	TicketOrderConsistency *int64  `json:"ticket_order_consistency"`
	AdmittedAfterSoldout   *int64  `json:"admitted_after_soldout"`
	UnresolvedUsers        *int64  `json:"unresolved_users"`
	SettleS                float64 `json:"settle_s"`
	SettleTimedOut         bool    `json:"settle_timed_out"`
	Passed                 bool    `json:"passed"`
	ExitCode               int     `json:"exit_code"`
}

// SummaryHeadline is the claim's numbers.
type SummaryHeadline struct {
	CheckoutP95MS float64 `json:"checkout_p95_ms"`
	StatusP95MS   float64 `json:"status_p95_ms"`
	PeakRPS       int64   `json:"peak_rps"`
}

func secsPtr(t0 time.Time, ns int64) *float64 {
	if ns == 0 {
		return nil
	}
	v := math.Round(time.Unix(0, ns).Sub(t0).Seconds()*10) / 10
	return &v
}

func (s *swarm) summarize(started time.Time, ev eventInfo, rows map[int]statsRow) *Summary {
	r := s.rec
	o := s.o
	sum := &Summary{
		RunID: o.RunID, GitSHA: buildinfo.Get().Commit, Scenario: o.Scenario.Name, StartedAt: started,
		Env: SummaryEnv{Runner: o.Runner, CPUs: runtime.NumCPU(), RAMGB: ramGB(), Go: runtime.Version(),
			Postgres: o.PGVersion, Redis: o.RedisVersion, SwarmCPUSet: o.CPUSet},
		Config: SummaryConfig{Users: o.Users, Seats: ev.Capacity, HoldTTLS: int(o.HoldTTL.Seconds()),
			PaymentHoldTTLS: int(o.PaymentHoldTTL.Seconds()), APIReplicas: o.APIReplicas, Event: ev.Slug,
			Conn: o.Conn, Arrival: arrivalString(o.Arrival), OpensInS: int(o.OpensIn.Seconds()), Seed: o.Seed,
			Direct: o.Scenario.Direct},
		Outcomes:  orderedOutcomes{},
		Latency:   map[string]Quantiles{},
		Anomalies: s.sortedAnomalies(),
	}
	for i := range numOutcomes {
		sum.Outcomes[i.String()] = s.outcomes[i].Load()
	}

	// Rush window: T0 - 10 s .. last terminal user (LOADTEST §3.4).
	winFrom := s.t0.Add(-10 * time.Second)
	winTo := time.Now()
	if ns := s.lastTerminalNS.Load(); ns != 0 {
		winTo = time.Unix(0, ns)
	}
	from := max(int(winFrom.Sub(r.base)/time.Second), 0)
	to := min(int(winTo.Sub(r.base)/time.Second), len(r.secs)-1)
	var inWin, peak int64
	for i := from; i <= to; i++ {
		var n int64
		for rt := range numRoutes {
			n += r.secs[i].n[rt].Load()
		}
		inWin += n
		peak = max(peak, n)
	}
	mean := 0.0
	if to >= from {
		mean = math.Round(float64(inWin)/float64(to-from+1)*10) / 10
	}
	lag := r.lag.quantiles()
	sum.Load = SummaryLoad{PeakInSystem: s.peak.Load(), Requests: r.requests.Load(), PeakRPS: peak, MeanRPS: mean,
		GeneratorLagP99MS: lag.P99}

	sum.Timing = SummaryTiming{T0: s.t0.UTC(), FirstSaleS: secsPtr(s.t0, s.firstTicketNS.Load()),
		DrainS: secsPtr(s.t0, s.lastTerminalNS.Load()), WindowFromS: -10}
	var seatsSold int64
	gen := SummaryGenerator{LagP99MS: lag.P99, LagMaxMS: lag.Max, GeneratorBound: lag.P99 > GeneratorBoundLagMS}
	var cpuSum float64
	for _, i := range sortedKeys(rows) {
		row := rows[i]
		gen.StatsSamples++
		gen.CPUCoresMax = max(gen.CPUCoresMax, row.CPUCores)
		gen.GoroutinesMax = max(gen.GoroutinesMax, row.Goroutines)
		cpuSum += row.CPUCores
		if !row.ok {
			gen.StatsFailed++
			continue
		}
		seatsSold = max(seatsSold, row.Sold)
		if row.SoldOut && sum.Timing.SoldOutS == nil {
			v := float64(r.base.Add(time.Duration(i) * time.Second).Sub(s.t0).Seconds())
			sum.Timing.SoldOutS = &v
		}
	}
	if gen.StatsSamples > 0 {
		gen.CPUCoresMean = math.Round(cpuSum/float64(gen.StatsSamples)*100) / 100
	}
	gen.CPUCoresMax = math.Round(gen.CPUCoresMax*100) / 100
	sum.Generator = gen
	sum.Outcomes["seats_sold"] = seatsSold
	sum.Records = s.records(rows)

	for rt := range numRoutes {
		sum.Latency[rt.String()] = r.routes[rt].quantiles()
	}
	sum.Latency["checkout"] = r.checkout.quantiles()

	errs := r.s5xx.Load() + r.timeouts.Load() + r.transportErrs.Load()
	rate := 0.0
	if n := r.requests.Load(); n > 0 {
		rate = float64(errs) / float64(n)
	}
	sum.Errors = SummaryErrors{Rate: rate, S5xx: r.s5xx.Load(), Timeouts: r.timeouts.Load(), Transport: r.transportErrs.Load()}
	sum.Business = map[string]int64{"409": r.b409.Load(), "422": r.b422.Load(), "429": r.b429.Load()}
	sum.Headline = SummaryHeadline{CheckoutP95MS: sum.Latency["checkout"].P95, StatusP95MS: sum.Latency["status"].P95, PeakRPS: peak}
	return sum
}

func sortedKeys(m map[int]statsRow) []int {
	ks := make([]int, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	for i := 1; i < len(ks); i++ { // insertion sort: ≤ a few thousand keys
		for j := i; j > 0 && ks[j] < ks[j-1]; j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
	return ks
}

func arrivalString(ws []ArrivalWindow) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = fmt.Sprintf("%s=%g:%s..%s", w.Name, w.Share, w.From, w.To)
	}
	return strings.Join(parts, ",")
}

// ramGB reads MemTotal on Linux (0 elsewhere).
func ramGB() float64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			kb, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 64)
			if err == nil {
				return math.Round(kb/1024/1024*10) / 10
			}
		}
	}
	return 0
}

// TimeseriesHeader is the timeseries.csv header (LOADTEST §3.5).
var TimeseriesHeader = []string{"t", "rps_total", "rps_join", "rps_status", "rps_hold", "rps_order", "rps_pay", "rps_confirm",
	"p95_status_ms", "p95_checkout_ms", "errors", "in_system", "waiting", "cursor", "offers", "active",
	"available", "held", "sold", "sold_out"}

// timeseries builds 1 s rows from the first to the last second with activity.
func (s *swarm) timeseries(rows map[int]statsRow) [][]string {
	r := s.rec
	first, last := -1, -1
	for i := range r.secs {
		var n int64
		for rt := range numRoutes {
			n += r.secs[i].n[rt].Load()
		}
		if _, ok := rows[i]; n > 0 || ok {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	out := [][]string{TimeseriesHeader}
	if first < 0 {
		return out
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
	d := func(v int64) string { return strconv.FormatInt(v, 10) }
	for i := first; i <= last; i++ {
		sec := &r.secs[i]
		var total int64
		for rt := range numRoutes {
			total += sec.n[rt].Load()
		}
		st, co := sec.hists()
		row := rows[i]
		t := r.base.Add(time.Duration(i) * time.Second).Sub(s.t0).Seconds()
		srv := func(v int64) string {
			if !row.ok {
				return ""
			}
			return d(v)
		}
		soldOut := ""
		if row.ok {
			soldOut = strconv.FormatBool(row.SoldOut)
		}
		out = append(out, []string{
			strconv.FormatFloat(math.Round(t), 'f', 0, 64), d(total),
			d(sec.n[rJoin].Load()), d(sec.n[rStatus].Load()), d(sec.n[rHold].Load()),
			d(sec.n[rOrder].Load()), d(sec.n[rPay].Load()), d(sec.n[rConfirm].Load()),
			f(st.quantiles().P95), f(co.quantiles().P95), d(sec.errors.Load()),
			d(row.InSystem), srv(row.Waiting), srv(row.Cursor), srv(row.Offers), srv(row.Active),
			srv(row.Available), srv(row.Held), srv(row.Sold), soldOut,
		})
	}
	return out
}

// writeOutputs writes summary.json, timeseries.csv and report.md into dir.
// Results are public run artifacts (no secrets). The CI runner user is not
// the container user and must read them to publish the job summary and the
// artifact, so files are 0644 and directories 0755 (P7b post-report CI fix).
const (
	resultFileMode os.FileMode = 0o644
	resultDirMode  os.FileMode = 0o755
)

func writeOutputs(dir string, sum *Summary, ts [][]string) error {
	if err := os.MkdirAll(dir, resultDirMode); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	js, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), append(js, '\n'), resultFileMode); err != nil {
		return err
	}
	if err := writeCSV(filepath.Join(dir, "timeseries.csv"), ts); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(RenderReport(sum)), resultFileMode)
}

func writeCSV(path string, rows [][]string) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, resultFileMode)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(f)
	w := csv.NewWriter(bw)
	if err := w.WriteAll(rows); err != nil {
		_ = f.Close()
		return err
	}
	if err := bw.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// RenderReport is report.md: the same tables the CI job summary shows.
func RenderReport(s *Summary) string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	opt := func(v *float64) string {
		if v == nil {
			return "n/a"
		}
		return strconv.FormatFloat(*v, 'f', 1, 64) + " s"
	}
	p("# Rush %s (scenario %s)\n\n", s.RunID, s.Scenario)
	p("Commit `%s`, started %s. Runner `%s`: %d CPUs, %.1f GB RAM, %s, Postgres %s, Redis %s.\n\n",
		s.GitSHA, s.StartedAt.Format(time.RFC3339), s.Env.Runner, s.Env.CPUs, s.Env.RAMGB, s.Env.Go, s.Env.Postgres, s.Env.Redis)
	if s.Env.SwarmCPUSet != "" {
		p("> **Swarm pinned** to CPUs `%s` (RUSH_CPUSET); the stack is not pinned and may use every CPU, including these. CPUs above is the swarm's own view.\n\n", s.Env.SwarmCPUSet)
	} else {
		p("The swarm shares CPUs with the system under test, so these numbers are conservative.\n\n")
	}
	if s.Generator.GeneratorBound {
		p("> **GENERATOR-BOUND:** schedule lag p99 %.1f ms > %d ms. Latencies include the swarm's own delay.\n\n", s.Generator.LagP99MS, GeneratorBoundLagMS)
	}
	p("## Config\n| users | seats | event | conn | arrival | opens in | hold TTL | payment hold TTL | API replicas | direct |\n|---|---|---|---|---|---|---|---|---|---|\n")
	p("| %d | %d | %s | %s | `%s` | %d s | %d s | %d s | %d | %t |\n\n", s.Config.Users, s.Config.Seats, s.Config.Event, s.Config.Conn,
		s.Config.Arrival, s.Config.OpensInS, s.Config.HoldTTLS, s.Config.PaymentHoldTTLS, s.Config.APIReplicas, s.Config.Direct)
	p("## Outcomes\n| TICKET | SOLD_OUT | NO_SEAT | PAYMENT_FAILED | ABANDONED | **UNRESOLVED** | seats sold |\n|---|---|---|---|---|---|---|\n")
	o := s.Outcomes
	p("| %d | %d | %d | %d | %d | **%d** | %d |\n\n", o["TICKET"], o["SOLD_OUT"], o["NO_SEAT"], o["PAYMENT_FAILED"], o["ABANDONED"], o["UNRESOLVED"], o["seats_sold"])
	p("## Load\n| peak in system | requests | peak rps | mean rps | generator lag p99 |\n|---|---|---|---|---|\n")
	p("| %d | %d | %d | %.1f | %.1f ms |\n\n", s.Load.PeakInSystem, s.Load.Requests, s.Load.PeakRPS, s.Load.MeanRPS, s.Load.GeneratorLagP99MS)
	p("## Timing (from T0 = %s)\n| first sale | sold out | drain |\n|---|---|---|\n| %s | %s | %s |\n\n",
		s.Timing.T0.Format(time.RFC3339), opt(s.Timing.FirstSaleS), opt(s.Timing.SoldOutS), opt(s.Timing.DrainS))
	p("## Latency (ms, client-side, from intended start)\n| route | p50 | p95 | p99 | max | n |\n|---|---|---|---|---|---|\n")
	for _, k := range []string{"checkout", "status", "join", "session", "availability", "hold", "order", "pay", "payments", "confirm", "leave"} {
		q := s.Latency[k]
		p("| %s | %.1f | %.1f | %.1f | %.1f | %d |\n", k, q.P50, q.P95, q.P99, q.Max, q.N)
	}
	p("\n## Errors and business answers\n| error rate | 5xx | timeouts | transport | 409 | 422 | 429 |\n|---|---|---|---|---|---|---|\n")
	p("| %.4f%% | %d | %d | %d | %d | %d | %d |\n\n", s.Errors.Rate*100, s.Errors.S5xx, s.Errors.Timeouts, s.Errors.Transport,
		s.Business["409"], s.Business["422"], s.Business["429"])
	p("## Generator self-check\n| lag p99 | lag max | CPU cores max / mean | goroutines max | /stats samples (failed) |\n|---|---|---|---|---|\n")
	p("| %.1f ms | %.1f ms | %.2f / %.2f | %d | %d (%d) |\n\n", s.Generator.LagP99MS, s.Generator.LagMaxMS, s.Generator.CPUCoresMax,
		s.Generator.CPUCoresMean, s.Generator.GoroutinesMax, s.Generator.StatsSamples, s.Generator.StatsFailed)
	if len(s.Anomalies) > 0 {
		p("## Unexpected answers\n| step:answer | count |\n|---|---|\n")
		keys := make([]string, 0, len(s.Anomalies))
		for k := range s.Anomalies {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p("| %s | %d |\n", k, s.Anomalies[k])
		}
		p("\n")
	}
	if s.Audit == nil {
		p("## Audit\nNot run yet (`sluice audit --results <run dir>`, or `sluice rush --audit`).\n")
	} else {
		a := s.Audit
		n := func(v *int64) string {
			if v == nil {
				return "n/a"
			}
			return strconv.FormatInt(*v, 10)
		}
		timedOut := ""
		if a.SettleTimedOut {
			timedOut = " (timed out)"
		}
		p("## Audit (%s mode; details in audit.json)\n", a.Mode)
		p("| oversold | duplicate charges | orphan charges | quota violations | stuck holds | inventory drift | ticket/order mismatches | admitted after sold out | unresolved | settle | passed | exit |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n")
		p("| %s | %s | %s | %s | %s | %s | %s | %s | %s | %.1f s%s | **%t** | %d |\n", n(a.Oversold), n(a.DuplicateCharges), n(a.OrphanCharges),
			n(a.QuotaViolations), n(a.StuckHolds), n(a.InventoryDrift), n(a.TicketOrderConsistency), n(a.AdmittedAfterSoldout),
			n(a.UnresolvedUsers), a.SettleS, timedOut, a.Passed, a.ExitCode)
	}
	return b.String()
}

// soldOutGrace: an admission counts as "after sold out" only if its request
// left more than this after the swarm first saw sold out on /stats (the
// sampler's 1 s resolution), so a request in flight at that moment is not
// a false positive.
const soldOutGrace = time.Second

// records builds the swarm records for the auditor.
func (s *swarm) records(rows map[int]statsRow) *SummaryRecords {
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	r := &SummaryRecords{Admissions: int64(len(s.admitSent)), SoldOutGraceMS: soldOutGrace.Milliseconds()}
	if s.o.Scenario.Direct {
		r.Note = "direct mode: no waiting room, no admissions"
		return r
	}
	for _, i := range sortedKeys(rows) {
		if row := rows[i]; row.ok && row.SoldOut {
			at := row.At.UTC()
			r.SoldOutSeenAt = &at
			break
		}
	}
	var n int64
	if r.SoldOutSeenAt == nil {
		r.Note = "the swarm never saw the event sold out"
	} else {
		limit := r.SoldOutSeenAt.Add(soldOutGrace)
		for _, t := range s.admitSent {
			if t.After(limit) {
				n++
			}
		}
	}
	r.AdmittedAfterSoldout = &n
	return r
}

// ApplyAudit stores an audit verdict in dir/summary.json and re-renders
// dir/report.md (used by `sluice audit --results` and `sluice rush --audit`).
func ApplyAudit(dir string, a *SummaryAudit) (*Summary, error) {
	path := filepath.Join(dir, "summary.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("rush: read summary: %w", err)
	}
	var sum Summary
	if err := json.Unmarshal(b, &sum); err != nil {
		return nil, fmt.Errorf("rush: parse summary: %w", err)
	}
	sum.Audit = a
	out, err := json.MarshalIndent(&sum, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rush: encode summary: %w", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), resultFileMode); err != nil {
		return nil, fmt.Errorf("rush: write summary: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(RenderReport(&sum)), resultFileMode); err != nil {
		return nil, fmt.Errorf("rush: write report: %w", err)
	}
	return &sum, nil
}

// AuditSummary condenses an audit report into summary.json's audit block.
func AuditSummary(r *audit.Report) *SummaryAudit {
	count := func(name string) *int64 {
		if c := r.Get(name); c != nil {
			return c.Count
		}
		return nil
	}
	return &SummaryAudit{
		Mode: r.Mode, Oversold: count(audit.CheckOversold), DuplicateCharges: count(audit.CheckDuplicateCharges),
		OrphanCharges: count(audit.CheckOrphanCharges), QuotaViolations: count(audit.CheckQuotaViolations),
		StuckHolds: count(audit.CheckStuckHolds), InventoryDrift: count(audit.CheckInventoryDrift),
		TicketOrderConsistency: count(audit.CheckTicketOrder), AdmittedAfterSoldout: count(audit.CheckAdmittedAfterSold),
		UnresolvedUsers: count(audit.CheckUnresolvedUsers), SettleS: math.Round(r.Settle.Seconds*10) / 10,
		SettleTimedOut: r.Settle.TimedOut, Passed: r.Passed, ExitCode: r.ExitCode,
	}
}
