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
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sheshakanthra/sluice/internal/app"
	"github.com/sheshakanthra/sluice/internal/buildinfo"
	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/events"
	"github.com/sheshakanthra/sluice/internal/obs"
)

// Exit codes.
const (
	exitOK       = 0
	exitFailure  = 1
	exitUsage    = 2 // also "not implemented yet"
	notImplPhase = "not implemented yet"
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
  rush      run the load-test swarm
  audit     check invariants against a running stack
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
	case "mockpay", "rush", "audit":
		_, _ = fmt.Fprintf(stderr, "sluice %s: %s\n", cmd, notImplPhase)
		return exitUsage
	default:
		_, _ = fmt.Fprintf(stderr, "sluice: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}
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
