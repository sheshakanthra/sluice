package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"no args", nil, exitUsage, "", "usage: sluice"},
		{"version", []string{"version"}, exitOK, "sluice ", ""},
		{"help", []string{"help"}, exitOK, "usage: sluice", ""},
		{"unknown", []string{"frobnicate"}, exitUsage, "", `unknown command "frobnicate"`},
		{"worker", []string{"worker"}, exitUsage, "", "sluice worker: not implemented in P0"},
		{"mockpay", []string{"mockpay"}, exitUsage, "", "sluice mockpay: not implemented in P0"},
		{"migrate", []string{"migrate"}, exitUsage, "", "sluice migrate: not implemented in P0"},
		{"seed", []string{"seed"}, exitUsage, "", "sluice seed: not implemented in P0"},
		{"rush", []string{"rush"}, exitUsage, "", "sluice rush: not implemented in P0"},
		{"audit", []string{"audit"}, exitUsage, "", "sluice audit: not implemented in P0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(context.Background(), tc.args, &out, &errOut)
			if code != tc.wantCode {
				t.Errorf("exit code %d, want %d", code, tc.wantCode)
			}
			if !strings.Contains(out.String(), tc.wantStdout) {
				t.Errorf("stdout %q does not contain %q", out.String(), tc.wantStdout)
			}
			if !strings.Contains(errOut.String(), tc.wantStderr) {
				t.Errorf("stderr %q does not contain %q", errOut.String(), tc.wantStderr)
			}
		})
	}
}

func TestRunAPIFailsFastOnBadConfig(t *testing.T) {
	t.Setenv("BASELINE", "naive") // refused when APP_ENV=dev
	t.Setenv("APP_ENV", "dev")
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"api"}, &out, &errOut); code != exitFailure {
		t.Fatalf("exit code %d, want %d", code, exitFailure)
	}
	if !strings.Contains(errOut.String(), "BASELINE") {
		t.Fatalf("stderr should explain the refusal: %q", errOut.String())
	}
}
