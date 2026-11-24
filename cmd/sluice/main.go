// Command sluice is the single SLUICE binary.
//
//	sluice api | worker | mockpay | migrate | seed | rush | audit | version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sheshakanthra/sluice/internal/app"
	"github.com/sheshakanthra/sluice/internal/audit"
	"github.com/sheshakanthra/sluice/internal/buildinfo"
	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/events"
	"github.com/sheshakanthra/sluice/internal/obs"
	"github.com/sheshakanthra/sluice/internal/rush"
)

// Exit codes.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const usage = `usage: sluice <command>

commands:
  api       serve the HTTP API
  worker    run admission ticks, sweeper, jobs and reconciler
  mockpay   run the PSP simulator
  migrate   apply database migrations
  seed      seed demo events (idempotent)
            --event <slug>      only this event (a new slug creates a custom event)
            --capacity <n>      seats for --event (APP_ENV=rush or test only)
            --opens-in <dur>    sale opens after this duration (e.g. 10m, 30s)
  rush      run the load-test swarm (sluice rush -h for flags)
  audit     settle, then check every invariant (sluice audit -h); exit 0 pass,
            1 invariant failed, 2 settle timed out, 3 the auditor could not run
  probe     GET <url>, exit 0 on 2xx (container healthchecks; distroless has no curl)
  version   print version and commit
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch cmd := args[0]; cmd {
	case "version", "--version", "-v":
		_, _ = fmt.Fprintln(stdout, buildinfo.Get().String())
		return exitOK
	case "help", "--help", "-h":
		_, _ = fmt.Fprint(stdout, usage)
		return exitOK
	case "api":
		return runService(ctx, stdout, stderr, app.RunAPI)
	case "migrate":
		return runService(ctx, stdout, stderr, app.RunMigrate)
	case "seed":
		opts, err := parseSeedFlags(args[1:], stderr)
		if err != nil {
			return exitUsage
		}
		return runService(ctx, stdout, stderr, app.SeedFunc(opts))
	case "worker":
		return runService(ctx, stdout, stderr, app.RunWorker)
	case "mockpay":
		return runService(ctx, stdout, stderr, app.RunMockpay)
	case "rush":
		return runRush(ctx, args[1:], stdout, stderr)
	case "probe":
		return runProbe(ctx, args[1:], stderr)
	case "audit":
		return runAudit(ctx, args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "sluice: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}
}

// runRush runs the swarm. Exit 1 if any user is unresolved (SPEC §7 hard fail).
func runRush(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, err := rush.ParseArgs(args, os.Getenv, stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintf(stderr, "sluice rush: %v\n", err)
		}
		return exitUsage
	}
	sum, err := rush.Run(ctx, opts, stdout)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sluice rush: %v\n", err)
		return exitFailure
	}
	code := exitOK
	if opts.Audit != nil {
		var s *rush.Summary
		if s, code = auditRun(ctx, opts.Audit, stdout, stderr); s != nil {
			sum = s
		}
	}
	_, _ = fmt.Fprint(stdout, "\n"+rush.RenderReport(sum))
	if code != exitOK {
		return code
	}
	if sum.Outcomes["UNRESOLVED"] > 0 {
		_, _ = fmt.Fprintf(stderr, "sluice rush: %d unresolved users\n", sum.Outcomes["UNRESOLVED"])
		return exitFailure
	}
	return exitOK
}

// exitAuditError: the auditor itself could not run (bad flags, a datastore
// it cannot reach). 0, 1 and 2 are the verdicts (LOADTEST §6).
const exitAuditError = 3

// runAudit is `sluice audit`: settle, check, write audit.json (and, with
// --results, the summary's audit block and report.md).
func runAudit(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, err := audit.ParseArgs(args, os.Getenv, stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintf(stderr, "sluice audit: %v\n", err)
		}
		return exitAuditError
	}
	_, code := auditRun(ctx, o, stdout, stderr)
	return code
}

// auditRun runs the auditor and, when o.Results is set, stores the verdict
// in that run's summary.json. It returns the updated summary (nil without
// --results) and the exit code.
func auditRun(ctx context.Context, o *audit.Options, stdout, stderr io.Writer) (*rush.Summary, int) {
	rep, err := audit.Run(ctx, o, stdout)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sluice audit: %v\n", err)
		return nil, exitAuditError
	}
	if o.Results == "" {
		return nil, rep.ExitCode
	}
	sum, err := rush.ApplyAudit(o.Results, rush.AuditSummary(rep))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sluice audit: %v\n", err)
		return nil, exitAuditError
	}
	return sum, rep.ExitCode
}

// runProbe is the container healthcheck: GET the URL, exit 0 on 2xx.
func runProbe(ctx context.Context, args []string, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: sluice probe <url>")
		return exitUsage
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, args[0], http.NoBody) //nolint:gosec // G704: the URL is the operator's own healthcheck target
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "probe: %v\n", err)
		return exitUsage
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: see above
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "probe: %v\n", err)
		return exitFailure
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = fmt.Fprintf(stderr, "probe: HTTP %d\n", resp.StatusCode)
		return exitFailure
	}
	return exitOK
}

type serviceFunc func(ctx context.Context, cfg *config.Config, log *slog.Logger) error

// runService loads config fail-fast, builds the logger and runs svc.
func runService(ctx context.Context, stdout, stderr io.Writer, svc serviceFunc) int {
	cfg, err := config.FromEnv()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sluice: %v\n", err)
		return exitFailure
	}
	log := obs.NewLogger(stdout, cfg.LogLevel)
	bi := buildinfo.Get()
	log.InfoContext(ctx, "starting",
		"version", bi.Version, "commit", bi.Commit, "go", bi.GoVersion, "app_env", string(cfg.AppEnv))
	if err := svc(ctx, cfg, log); err != nil {
		log.ErrorContext(context.WithoutCancel(ctx), "fatal", "error", err.Error())
		return exitFailure
	}
	return exitOK
}

// parseSeedFlags parses `sluice seed` flags. Errors are already printed.
func parseSeedFlags(args []string, stderr io.Writer) (events.SeedOptions, error) {
	fs := flag.NewFlagSet("sluice seed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		opts    events.SeedOptions
		opensIn time.Duration
	)
	fs.StringVar(&opts.Event, "event", "", "seed only this event slug (a new slug creates a custom event)")
	fs.IntVar(&opts.Capacity, "capacity", 0, "seat count for --event (APP_ENV=rush or test only)")
	fs.DurationVar(&opensIn, "opens-in", 0, "sale opens this long after seeding (default: 10m for the rush event, 0 for quiet-tuesday)")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "sluice seed: unexpected arguments %v\n", fs.Args())
		return opts, errors.New("unexpected arguments")
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "opens-in" {
			opts.OpensIn = &opensIn
		}
	})
	return opts, nil
}
