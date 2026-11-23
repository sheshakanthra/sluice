// Command sluice is the single SLUICE binary.
//
//	sluice api | worker | mockpay | migrate | seed | rush | audit | version
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sheshakanthra/sluice/internal/app"
	"github.com/sheshakanthra/sluice/internal/buildinfo"
	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/obs"
)

// Exit codes.
const (
	exitOK       = 0
	exitFailure  = 1
	exitUsage    = 2 // also "not implemented yet"
	notImplPhase = "not implemented in P0"
)

const usage = `usage: sluice <command>

commands:
  api       serve the HTTP API
  worker    run admission ticks, sweeper, jobs and reconciler
  mockpay   run the PSP simulator
  migrate   apply database migrations
  seed      seed demo events
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
		return runService(ctx, stderr, app.RunAPI)
	case "worker", "mockpay", "migrate", "seed", "rush", "audit":
		_, _ = fmt.Fprintf(stderr, "sluice %s: %s\n", cmd, notImplPhase)
		return exitUsage
	default:
		_, _ = fmt.Fprintf(stderr, "sluice: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}
}

type serviceFunc func(ctx context.Context, cfg *config.Config, log *slog.Logger) error

// runService loads config fail-fast, builds the logger and runs svc.
func runService(ctx context.Context, stderr io.Writer, svc serviceFunc) int {
	cfg, err := config.FromEnv()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sluice: %v\n", err)
		return exitFailure
	}
	log := obs.NewLogger(os.Stdout, cfg.LogLevel)
	bi := buildinfo.Get()
	log.InfoContext(ctx, "starting",
		"version", bi.Version, "commit", bi.Commit, "go", bi.GoVersion, "app_env", string(cfg.AppEnv))
	if err := svc(ctx, cfg, log); err != nil {
		log.ErrorContext(context.WithoutCancel(ctx), "fatal", "error", err.Error())
		return exitFailure
	}
	return exitOK
}
