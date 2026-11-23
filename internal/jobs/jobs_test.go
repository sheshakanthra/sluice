package jobs_test

import (
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"

	"github.com/sheshakanthra/sluice/internal/jobs"
)

// The retry delay is full jitter in [d/2, d] with d = Base * 2^(attempt-1),
// capped at Max.
func TestBackoff(t *testing.T) {
	b := &jobs.Backoff{Base: time.Second, Max: time.Minute}
	tests := []struct {
		attempt int
		lo, hi  time.Duration
	}{
		{1, 500 * time.Millisecond, time.Second},
		{2, time.Second, 2 * time.Second},
		{4, 4 * time.Second, 8 * time.Second},
		{7, 30 * time.Second, time.Minute},
		{30, 30 * time.Second, time.Minute},
	}
	for _, tc := range tests {
		for range 200 {
			before := time.Now()
			d := b.NextRetry(&rivertype.JobRow{Attempt: tc.attempt}).Sub(before)
			if d < tc.lo-10*time.Millisecond || d > tc.hi+10*time.Millisecond {
				t.Fatalf("attempt %d: delay %s outside [%s, %s]", tc.attempt, d, tc.lo, tc.hi)
			}
		}
	}
}
