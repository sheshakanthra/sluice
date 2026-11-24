//go:build !unix

package rush

import (
	"runtime/metrics"
	"time"
)

// processCPU estimates CPU time from the Go runtime where getrusage is not
// available (Windows dev runs; the swarm container is Linux). The runtime
// only refreshes these classes at GC, so per-second values are coarse.
func processCPU() time.Duration {
	s := []metrics.Sample{{Name: "/cpu/classes/total:cpu-seconds"}, {Name: "/cpu/classes/idle:cpu-seconds"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindFloat64 || s[1].Value.Kind() != metrics.KindFloat64 {
		return 0
	}
	return time.Duration((s[0].Value.Float64() - s[1].Value.Float64()) * float64(time.Second))
}
