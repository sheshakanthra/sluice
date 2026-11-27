package rush

import (
	"testing"
	"time"
)

// fakeProc is a process that uses `cores` CPUs continuously; reading its CPU
// time can stall (a descheduled goroutine) for the next stalls[i].
type fakeProc struct {
	t      time.Time
	cores  float64
	start  time.Time
	stalls []time.Duration
}

func (f *fakeProc) now() time.Time { return f.t }

func (f *fakeProc) cpu() time.Duration {
	c := time.Duration(f.t.Sub(f.start).Seconds() * f.cores * float64(time.Second))
	if len(f.stalls) > 0 { // descheduled right after the CPU read
		f.t = f.t.Add(f.stalls[0])
		f.stalls = f.stalls[1:]
	}
	return c
}

// oldRead is the pre-fix meter: CPU and wall read once, no bracket.
func oldRead(f *fakeProc, last *time.Duration, lastWall *time.Time) float64 {
	used := f.cpu()
	now := f.now()
	cores := (used - *last).Seconds() / now.Sub(*lastWall).Seconds()
	*last, *lastWall = used, now
	return cores
}

func TestCPUMeterIgnoresDeschedulingBetweenReads(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	tests := []struct {
		name   string
		stalls []time.Duration // per CPU read, in order
	}{
		{"no stall", nil},
		// The second sample's goroutine is descheduled 2.5 s after reading
		// CPU (a 2.6 s lag p99 run). The next sample is on time.
		{"2.5s stall", []time.Duration{0, 2500 * time.Millisecond}},
		{"stall on every first try", []time.Duration{0, time.Second, 0, time.Second, 0, time.Second, 0}},
	}
	const cores = 4.0
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeProc{t: t0, start: t0.Add(-time.Hour), cores: cores, stalls: append([]time.Duration(nil), tc.stalls...)}
			m := &cpuMeter{cpu: f.cpu, now: f.now}
			if _, ok := m.read(); ok {
				t.Fatal("first read must not report cores")
			}
			for i := range 3 {
				f.t = f.t.Add(time.Second)
				got, ok := m.read()
				if !ok {
					t.Fatalf("read %d: not valid", i)
				}
				if got < cores*0.98 || got > cores*1.02 {
					t.Fatalf("read %d: %.2f cores, want %.1f", i, got, cores)
				}
			}
		})
	}

	// The old meter on the same 2.5 s stall: this is the bug.
	f := &fakeProc{t: t0, start: t0.Add(-time.Hour), cores: cores, stalls: []time.Duration{0, 2500 * time.Millisecond}}
	var last time.Duration
	var lastWall time.Time
	oldRead(f, &last, &lastWall)
	f.t = f.t.Add(time.Second)
	oldRead(f, &last, &lastWall) // stalls 2.5 s after the CPU read
	f.t = f.t.Add(time.Second)
	if got := oldRead(f, &last, &lastWall); got < 10 {
		t.Fatalf("old meter: %.2f cores; the fixture should reproduce the >4-core reading", got)
	}
}

func TestCPUMeterMarksAnUnbracketableReadInvalid(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	stalls := make([]time.Duration, 1+cpuReadTrys)
	for i := 1; i < len(stalls); i++ {
		stalls[i] = time.Second // every try of the second read stalls
	}
	f := &fakeProc{t: t0, start: t0, cores: 1, stalls: stalls}
	m := &cpuMeter{cpu: f.cpu, now: f.now}
	m.read()
	f.t = f.t.Add(time.Second)
	if _, ok := m.read(); ok {
		t.Fatal("a read with no tight bracket must be invalid")
	}
}
