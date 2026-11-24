package tcretry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStart(t *testing.T) {
	boom := errors.New("rootless Docker is not supported on Windows")
	tests := []struct {
		name      string
		failFirst int // attempts that fail before one succeeds
		wantCalls int
		wantErr   bool
	}{
		{"first try", 0, 1, false},
		{"third try", 2, 3, false},
		{"never", 99, Attempts, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls, cleaned := 0, 0
			v, err := startWith(context.Background(), "postgres test", func(context.Context) (int, error) {
				calls++
				if calls <= tc.failFirst {
					return -1, boom
				}
				return 42, nil
			}, func(int) { cleaned++ }, func(int) time.Duration { return time.Millisecond })
			if calls != tc.wantCalls || cleaned != min(tc.failFirst, Attempts) {
				t.Errorf("calls %d cleanups %d, want %d and %d", calls, cleaned, tc.wantCalls, min(tc.failFirst, Attempts))
			}
			if tc.wantErr {
				if !errors.Is(err, ErrInfra) || !errors.Is(err, boom) ||
					!strings.HasPrefix(err.Error(), "infra: container failed to start: postgres test after 3 attempts") {
					t.Errorf("err = %v", err)
				}
				return
			}
			if err != nil || v != 42 {
				t.Errorf("v %d err %v", v, err)
			}
		})
	}
	t.Run("context done stops retrying", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		_, err := startWith(ctx, "redis test", func(context.Context) (int, error) {
			calls++
			cancel()
			return 0, boom
		}, nil, func(int) time.Duration { return time.Hour })
		if calls != 1 || !errors.Is(err, ErrInfra) || !errors.Is(err, context.Canceled) {
			t.Errorf("calls %d err %v", calls, err)
		}
	})
	if Backoff(1) != 2*time.Second || Backoff(2) != 4*time.Second {
		t.Errorf("backoff %v %v", Backoff(1), Backoff(2))
	}
}
