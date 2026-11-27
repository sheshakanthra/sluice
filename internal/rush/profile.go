package rush

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/metrics"
	"runtime/pprof"
	"sync"
	"time"
)

// profiler captures the swarm's own pprof profiles for one run (--pprof):
// cpu.pprof covers the whole run, and heap.pprof is rewritten whenever the
// live heap passes its previous peak by heapStep, so the file left at the end
// is the profile at (about) the peak. Profiles describe the generator only;
// they never touch what the server sees.
type profiler struct {
	dir  string
	cpuF *os.File

	mu       sync.Mutex
	heapPeak uint64 // live heap at the last heap.pprof write
	heapErr  error
}

const heapStep = 1.10 // rewrite heap.pprof after +10% live heap

// startProfiler starts the CPU profile into dir/cpu.pprof.
func startProfiler(dir string) (*profiler, error) {
	if err := os.MkdirAll(dir, resultDirMode); err != nil {
		return nil, fmt.Errorf("pprof: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "cpu.pprof"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, resultFileMode)
	if err != nil {
		return nil, fmt.Errorf("pprof: %w", err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("pprof: %w", err)
	}
	return &profiler{dir: dir, cpuF: f}, nil
}

// observeHeap is called once a second by the sampler with the live heap.
func (p *profiler) observeHeap(live uint64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if float64(live) < float64(p.heapPeak)*heapStep {
		return
	}
	p.heapPeak = live
	if err := p.writeHeap(); err != nil {
		p.heapErr = err
	}
}

func (p *profiler) writeHeap() error {
	f, err := os.OpenFile(filepath.Join(p.dir, "heap.pprof"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, resultFileMode)
	if err != nil {
		return err
	}
	if err := pprof.Lookup("heap").WriteTo(f, 0); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// stop ends the CPU profile. A heap profile is written if none was yet.
func (p *profiler) stop() error {
	if p == nil {
		return nil
	}
	pprof.StopCPUProfile()
	err := p.cpuF.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.heapPeak == 0 {
		err = errors.Join(err, p.writeHeap())
	}
	return errors.Join(err, p.heapErr)
}

// runtimeStats is the Go runtime's own view of the swarm (GC cost, heap).
type runtimeStats struct {
	GCCycles     uint64
	GCCPUSeconds float64
	HeapLive     uint64
}

var runtimeSampleNames = []string{
	"/gc/cycles/total:gc-cycles",
	"/cpu/classes/gc/total:cpu-seconds",
	"/gc/heap/live:bytes",
}

func readRuntimeStats() runtimeStats {
	s := make([]metrics.Sample, len(runtimeSampleNames))
	for i, n := range runtimeSampleNames {
		s[i].Name = n
	}
	metrics.Read(s)
	var rs runtimeStats
	if s[0].Value.Kind() == metrics.KindUint64 {
		rs.GCCycles = s[0].Value.Uint64()
	}
	if s[1].Value.Kind() == metrics.KindFloat64 {
		rs.GCCPUSeconds = s[1].Value.Float64()
	}
	if s[2].Value.Kind() == metrics.KindUint64 {
		rs.HeapLive = s[2].Value.Uint64()
	}
	return rs
}

// cpuMeter turns cumulative process CPU time into cores used between reads.
//
// The CPU read and the wall-clock read are two calls, and a goroutine can be
// descheduled between them for as long as the generator lags (seconds, in a
// starved run). Pairing a CPU value with a wall time taken seconds later is
// what reported 12.2 "cores" on a 4-core runner. So every read brackets the
// CPU call with two wall-clock reads, retries a wide bracket, and stamps the
// CPU value at the bracket midpoint. A read whose best bracket is still wider
// than maxBracket is marked invalid and left out of max/mean.
type cpuMeter struct {
	cpu func() time.Duration
	now func() time.Time

	last     time.Duration
	lastWall time.Time
	primed   bool
}

const (
	maxBracket  = 20 * time.Millisecond
	cpuReadTrys = 5
)

func newCPUMeter() *cpuMeter { return &cpuMeter{cpu: processCPU, now: time.Now} }

// sample returns the CPU time and the wall time it belongs to, and whether
// the pair is tight enough to trust.
func (m *cpuMeter) sample() (time.Duration, time.Time, bool) {
	var (
		best      time.Duration
		bestAt    time.Time
		bestWidth time.Duration = -1
	)
	for range cpuReadTrys {
		t1 := m.now()
		c := m.cpu()
		t2 := m.now()
		w := t2.Sub(t1)
		if bestWidth < 0 || w < bestWidth {
			best, bestAt, bestWidth = c, t1.Add(w/2), w
		}
		if w <= maxBracket {
			break
		}
	}
	return best, bestAt, bestWidth <= maxBracket
}

// read returns cores used since the previous valid read; ok is false for the
// first read and for a read that could not be bracketed tightly.
func (m *cpuMeter) read() (float64, bool) {
	used, at, tight := m.sample()
	if !tight {
		return 0, false
	}
	cores, ok := 0.0, false
	if m.primed {
		if wall := at.Sub(m.lastWall).Seconds(); wall > 0 {
			cores, ok = (used-m.last).Seconds()/wall, true
		}
	}
	m.last, m.lastWall, m.primed = used, at, true
	return cores, ok
}
