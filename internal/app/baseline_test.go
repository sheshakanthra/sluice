package app

import (
	"io"
	"log/slog"
	"testing"

	"github.com/sheshakanthra/sluice/internal/config"
)

// TestBaselineSecondLock: even a Config built by hand (not through
// config.Load, which refuses it first) cannot mount the unsafe B0/B1 code
// outside rush/test; BASELINE=off mounts nothing anywhere.
func TestBaselineSecondLock(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		env      config.AppEnv
		baseline config.Baseline
		wantErr  bool
	}{
		{config.EnvDev, config.BaselineNaive, true},
		{config.EnvDemo, config.BaselineHoldsOnly, true},
		{config.EnvDemo, config.BaselineOff, false},
		{config.EnvRush, config.BaselineOff, false},
	}
	for _, tc := range tests {
		b, err := newBaseline(&config.Config{AppEnv: tc.env, Baseline: tc.baseline}, log, nil, nil, nil)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s/%s: err %v, want error %t", tc.env, tc.baseline, err, tc.wantErr)
		}
		if b.hold != nil || b.orders != nil {
			t.Errorf("%s/%s: mounted overrides", tc.env, tc.baseline)
		}
	}
}
