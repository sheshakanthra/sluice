package rush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// swarm is the shared state of one run.
type swarm struct {
	o         *Options
	rec       *Recorder
	t0        time.Time
	base      string // target + /api/v1
	eventPath string // /events/<slug or id>
	pooled    *http.Transport
	prof      *profiler // nil unless --pprof
	heapPeak  atomic.Uint64

	inSystem, peak, waiting, shopping, started atomic.Int64
	outcomes                                   [numOutcomes]atomic.Int64
	firstTicketNS, lastTerminalNS              atomic.Int64

	anomMu    sync.Mutex
	anomalies map[string]int64

	// admitMu guards admitSent: when each ADMITTED answer's request was sent
	// (the auditor's admitted_after_soldout check, LOADTEST §6).
	admitMu   sync.Mutex
	admitSent []time.Time

	cost runCost // filled when every user is done
}

// runCost is what the run cost the swarm process, from just before the
// users start until the last one finishes (summary.json "generator").
type runCost struct {
	CPU          time.Duration // process user+system CPU (getrusage on Linux)
	Wall         time.Duration
	GCCycles     uint64
	GCCPUSeconds float64 // the Go runtime's estimate of GC CPU time
}

func (s *swarm) recordAdmit(sent time.Time) {
	s.admitMu.Lock()
	s.admitSent = append(s.admitSent, sent)
	s.admitMu.Unlock()
}

func (s *swarm) observeHeap(live uint64) {
	for {
		p := s.heapPeak.Load()
		if live <= p || s.heapPeak.CompareAndSwap(p, live) {
			return
		}
	}
}

func (s *swarm) enter() {
	n := s.inSystem.Add(1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			return
		}
	}
}

func (s *swarm) finish(u *user, out Outcome) {
	s.outcomes[out].Add(1)
	if u.joined {
		s.inSystem.Add(-1)
	}
	if out == OutUnresolved {
		return
	}
	now := time.Now().UnixNano()
	if out == OutTicket {
		s.firstTicketNS.CompareAndSwap(0, now)
	}
	for {
		p := s.lastTerminalNS.Load()
		if now <= p || s.lastTerminalNS.CompareAndSwap(p, now) {
			return
		}
	}
}

func (s *swarm) anomaly(step string, r *result) {
	k := anomalyKey(step, r)
	s.anomMu.Lock()
	s.anomalies[k]++
	s.anomMu.Unlock()
}

func (s *swarm) syntheticIP(i int) string {
	// 198.18.0.0/15 (RFC 2544 benchmarking): never a trusted proxy, ~10 users per IP (NAT-like).
	g := i / s.o.UsersPerIP
	return fmt.Sprintf("198.%d.%d.%d", 18+(g>>16)&1, (g>>8)&0xff, g&0xff)
}

// clientFor gives a user its client: in dedicated mode its own keep-alive
// connection (closed when ctx, the run, ends), else the shared transport.
func (s *swarm) clientFor(ctx context.Context, xff string) *client {
	c := &client{base: s.base, xff: xff, rec: s.rec}
	if s.o.Conn == ConnDedicated {
		uc, err := newUserConn(ctx, s.base)
		if err == nil {
			c.uc = uc
			return c
		}
		// Unreachable after Run's own parse of the target; fall back to net/http.
		c.hc = &http.Client{Transport: newTransport(ConnDedicated)}
		return c
	}
	c.hc = &http.Client{Transport: s.pooled}
	return c
}

// twinClient is the "second tab" for the double-submit: a separate
// connection, so the two /pay calls really are concurrent. Like a second
// browser tab it stays open (idle) until the run ends.
func (s *swarm) twinClient(ctx context.Context, xff string) *client {
	return s.clientFor(ctx, xff)
}

// eventInfo is what the swarm needs from GET /events/{id}.
type eventInfo struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Status   string `json:"status"`
	Capacity int    `json:"capacity"`
}

// statsRow is one GET /stats sample plus the swarm's own gauges.
type statsRow struct {
	InSystem, Waiting, Cursor, Offers, Active int64
	Available, Held, Sold                     int64
	SoldOut                                   bool
	At                                        time.Time // when the /stats answer arrived
	Goroutines                                int
	CPUCores                                  float64
	CPUValid                                  bool // CPUCores is a tight, primed sample (cpuMeter)
	ok                                        bool
}

type statsBody struct {
	EventStatus string `json:"event_status"`
	Queue       struct {
		Waiting int64 `json:"waiting"`
		Cursor  int64 `json:"cursor"`
		Offers  int64 `json:"offers"`
		Active  int64 `json:"active"`
	} `json:"queue"`
	Inventory struct {
		Available int64 `json:"available"`
		Held      int64 `json:"held"`
		Sold      int64 `json:"sold"`
		Capacity  int64 `json:"capacity"`
	} `json:"inventory"`
}

// Run executes one rush and writes summary.json, timeseries.csv and report.md.
func Run(ctx context.Context, o *Options, stdout io.Writer) (*Summary, error) {
	started := time.Now().UTC()
	ctl := &http.Client{Timeout: 10 * time.Second}
	base := strings.TrimRight(o.Target, "/") + "/api/v1"
	ev, err := getEvent(ctx, ctl, base, o.Event)
	if err != nil {
		return nil, err
	}
	t0, err := openSale(ctx, ctl, base, o.Event, o.AdminToken, o.OpensIn)
	if err != nil {
		return nil, err
	}
	minFrom := time.Duration(0)
	for _, w := range o.Arrival {
		minFrom = min(minFrom, w.From)
	}
	// Buckets are whole seconds from T0, starting 5 s before the first arrival
	// (or now, if that is earlier), so timeseries.csv "t" is exact.
	lead := -minFrom + 5*time.Second
	if untilT0 := time.Until(t0); untilT0 > lead {
		lead = untilT0.Truncate(time.Second) + time.Second
	}
	recBase := t0.Add(-lead.Truncate(time.Second))
	s := &swarm{
		o: o, t0: t0, base: base, eventPath: "/events/" + url.PathEscape(o.Event),
		rec:       NewRecorder(recBase, t0.Sub(recBase)+o.MaxDuration+30*time.Second),
		pooled:    newTransport(ConnPooled),
		anomalies: map[string]int64{},
	}
	_, _ = fmt.Fprintf(stdout, "rush %s: scenario %s, %d users, conn=%s, event %s (%d seats), T0 %s (in %s)\n",
		o.RunID, o.Scenario.Name, o.Users, o.Conn, ev.Slug, ev.Capacity, t0.Format(time.RFC3339), time.Until(t0).Round(time.Second))

	if o.PProf {
		if s.prof, err = startProfiler(o.Out); err != nil {
			return nil, err
		}
	}
	cpuStart, rtStart, wallStart := processCPU(), readRuntimeStats(), time.Now()

	runCtx, cancel := context.WithDeadline(ctx, t0.Add(o.MaxDuration))
	defer cancel()

	var (
		rowsMu sync.Mutex
		rows   = map[int]statsRow{}
	)
	samplerCtx, stopSampler := context.WithCancel(context.WithoutCancel(ctx))
	var bg sync.WaitGroup
	bg.Go(func() { s.sample(samplerCtx, ctl, &rowsMu, rows) })
	bg.Go(func() { s.progress(samplerCtx, stdout, &rowsMu, rows) })

	plans := makePlans(o)
	var wg sync.WaitGroup
	for i := range plans {
		wg.Go(func() {
			c := s.clientFor(runCtx, s.syntheticIP(i))
			u := &user{s: s, p: plans[i], c: c}
			s.started.Add(1)
			out := u.run(runCtx)
			s.finish(u, out)
			c.close()
		})
	}
	wg.Wait()
	stopSampler()
	bg.Wait()
	s.pooled.CloseIdleConnections()
	rtEnd := readRuntimeStats()
	s.cost = runCost{CPU: processCPU() - cpuStart, Wall: time.Since(wallStart),
		GCCycles: rtEnd.GCCycles - rtStart.GCCycles, GCCPUSeconds: rtEnd.GCCPUSeconds - rtStart.GCCPUSeconds}
	if err := s.prof.stop(); err != nil {
		_, _ = fmt.Fprintf(stdout, "rush %s: pprof: %v\n", o.RunID, err)
	}

	rowsMu.Lock()
	defer rowsMu.Unlock()
	sum := s.summarize(started, ev, rows)
	if err := writeOutputs(o.Out, sum, s.timeseries(rows)); err != nil {
		return sum, err
	}
	_, _ = fmt.Fprintf(stdout, "rush %s: done; outputs in %s\n", o.RunID, o.Out)
	return sum, nil
}

func getEvent(ctx context.Context, c *http.Client, base, event string) (eventInfo, error) {
	var ev eventInfo
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/events/"+url.PathEscape(event), http.NoBody)
	if err != nil {
		return ev, err
	}
	resp, err := c.Do(r)
	if err != nil {
		return ev, fmt.Errorf("get event %s: %w", event, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ev, fmt.Errorf("get event %s: HTTP %d", event, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&ev); err != nil {
		return ev, fmt.Errorf("get event %s: %w", event, err)
	}
	return ev, nil
}

// openSale sets T0 = now + opensIn (API.md §2 admin open-sale).
func openSale(ctx context.Context, c *http.Client, base, event, admin string, opensIn time.Duration) (time.Time, error) {
	body := fmt.Sprintf(`{"opens_in_s":%d}`, int(opensIn.Seconds()))
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/admin/events/"+url.PathEscape(event)+"/open-sale", strings.NewReader(body))
	if err != nil {
		return time.Time{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+admin)
	resp, err := c.Do(r)
	if err != nil {
		return time.Time{}, fmt.Errorf("open-sale: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("open-sale %s: HTTP %d %s (a rush needs a freshly seeded event)", event, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		SaleOpensAt time.Time `json:"sale_opens_at"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return time.Time{}, fmt.Errorf("open-sale: %w", err)
	}
	return out.SaleOpensAt, nil
}

// sample polls GET /stats every second (not counted as swarm load) and the
// generator self-check (goroutines, CPU).
func (s *swarm) sample(ctx context.Context, c *http.Client, mu *sync.Mutex, rows map[int]statsRow) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	cpu := newCPUMeter()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			cores, valid := cpu.read()
			row := statsRow{
				InSystem: s.inSystem.Load(), Goroutines: runtime.NumGoroutine(), CPUCores: cores, CPUValid: valid,
			}
			rs := readRuntimeStats()
			s.observeHeap(rs.HeapLive)
			s.prof.observeHeap(rs.HeapLive)
			if b, ok := fetchStats(ctx, c, s.base+"/stats/"+url.PathEscape(s.o.Event)); ok {
				row.ok, row.At = true, time.Now()
				row.Waiting, row.Cursor, row.Offers, row.Active = b.Queue.Waiting, b.Queue.Cursor, b.Queue.Offers, b.Queue.Active
				row.Available, row.Held, row.Sold = b.Inventory.Available, b.Inventory.Held, b.Inventory.Sold
				row.SoldOut = b.EventStatus == "SOLD_OUT" || (b.Inventory.Capacity > 0 && b.Inventory.Sold >= b.Inventory.Capacity)
			}
			i := int(now.Sub(s.rec.base) / time.Second)
			mu.Lock()
			rows[i] = row
			mu.Unlock()
		}
	}
}

func fetchStats(ctx context.Context, c *http.Client, u string) (statsBody, bool) {
	var b statsBody
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(cctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return b, false
	}
	resp, err := c.Do(r)
	if err != nil {
		return b, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&b) != nil {
		return b, false
	}
	return b, true
}

// progress prints one line every ProgressEvery.
func (s *swarm) progress(ctx context.Context, w io.Writer, mu *sync.Mutex, rows map[int]statsRow) {
	tick := time.NewTicker(s.o.ProgressEvery)
	defer tick.Stop()
	var lastReq int64
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			req := s.rec.requests.Load()
			rps := float64(req-lastReq) / now.Sub(last).Seconds()
			lastReq, last = req, now
			mu.Lock()
			row := rows[int(now.Sub(s.rec.base)/time.Second)-1]
			mu.Unlock()
			errs := s.rec.s5xx.Load() + s.rec.timeouts.Load() + s.rec.transportErrs.Load()
			_, _ = fmt.Fprintf(w, "T0%+4.0fs | in_system %d (peak %d) waiting %d shopping %d | TICKET %d SOLD_OUT %d NO_SEAT %d PAY_FAILED %d ABANDONED %d | sold %d/%d held %d | rps %.0f err %d 429 %d | lag_p99 %.1fms cpu %.2f gor %d\n",
				now.Sub(s.t0).Seconds(), s.inSystem.Load(), s.peak.Load(), s.waiting.Load(), s.shopping.Load(),
				s.outcomes[OutTicket].Load(), s.outcomes[OutSoldOut].Load(), s.outcomes[OutNoSeat].Load(),
				s.outcomes[OutPaymentFailed].Load(), s.outcomes[OutAbandoned].Load(),
				row.Sold, row.Sold+row.Held+row.Available, row.Held,
				rps, errs, s.rec.b429.Load(), s.rec.lag.quantiles().P99, row.CPUCores, row.Goroutines)
		}
	}
}

// sortedAnomalies copies the anomaly counts (encoding/json sorts map keys).
func (s *swarm) sortedAnomalies() map[string]int64 {
	s.anomMu.Lock()
	defer s.anomMu.Unlock()
	return maps.Clone(s.anomalies)
}

// ErrUnresolved is returned (with the summary) when users never reached a terminal state.
var ErrUnresolved = errors.New("unresolved users > 0")
