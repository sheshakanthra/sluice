package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
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
		{"rush bad scenario", []string{"rush", "--scenario", "nope"}, exitUsage, "", "want H, SMOKE, B0, B1 or B2"},
		{"rush bad conn", []string{"rush", "--scenario", "SMOKE", "--conn", "x"}, exitUsage, "", "want dedicated or pooled"},
		{"probe no args", []string{"probe"}, exitUsage, "", "usage: sluice probe"},
		{"probe unreachable", []string{"probe", "http://127.0.0.1:1/healthz"}, exitFailure, "", "probe:"},
		{"audit", []string{"audit"}, exitUsage, "", "sluice audit: not implemented yet"},
		{"seed bad flag", []string{"seed", "--nope"}, exitUsage, "", "flag provided but not defined"},
		{"seed bad duration", []string{"seed", "--opens-in", "soon"}, exitUsage, "", "invalid value"},
		{"seed stray arg", []string{"seed", "extra"}, exitUsage, "", "unexpected arguments"},
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

func TestParseSeedFlags(t *testing.T) {
	var errOut bytes.Buffer
	opts, err := parseSeedFlags([]string{"--event", "loadtest-big", "--capacity", "50000", "--opens-in", "30s"}, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Event != "loadtest-big" || opts.Capacity != 50000 || opts.OpensIn == nil || *opts.OpensIn != 30*time.Second {
		t.Fatalf("opts = %+v", opts)
	}
	opts, err = parseSeedFlags(nil, &errOut)
	if err != nil || opts.OpensIn != nil || opts.Event != "" || opts.Capacity != 0 {
		t.Fatalf("defaults: %+v %v", opts, err)
	}
}

// --capacity is refused outside rush/test before any database work.
func TestSeedCapacityRefusedInDev(t *testing.T) {
	t.Setenv("APP_ENV", "dev")
	t.Setenv("DATABASE_URL", "postgres://nobody@127.0.0.1:1/none")
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"seed", "--event", "big", "--capacity", "100"}, &out, &errOut)
	if code != exitFailure || !strings.Contains(out.String()+errOut.String(), "only allowed when APP_ENV is rush or test") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}
