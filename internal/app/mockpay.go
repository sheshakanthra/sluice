package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/mockpay"
	"github.com/sheshakanthra/sluice/internal/obs"
)

// MockpayOptions maps the MOCKPAY_* configuration onto the simulator.
func MockpayOptions(cfg *config.Config, log *slog.Logger) mockpay.Options {
	return mockpay.Options{
		APIKey: cfg.MockpayAPIKey, WebhookURL: cfg.PublicWebhookURL, WebhookSecret: cfg.MockpayWebhookSecret,
		PublicURL:  cfg.MockpayPublicURL,
		LatencyMin: time.Duration(cfg.MockpayLatencyMS.Min) * time.Millisecond,
		LatencyMax: time.Duration(cfg.MockpayLatencyMS.Max) * time.Millisecond,
		FailRate:   cfg.MockpayFailRate, TimeoutAfterCommitRate: cfg.MockpayTimeoutAfterCommitRate,
		Hang:           time.Duration(cfg.MockpayHangMS) * time.Millisecond,
		WebhookDupRate: cfg.MockpayWebhookDupRate, WebhookReorder: cfg.MockpayWebhookReorderRate,
		WebhookMaxDelay:  time.Duration(cfg.MockpayWebhookMaxDelayMS) * time.Millisecond,
		AllowFaultHeader: cfg.AppEnv.AllowsUnsafeModes(),
		Log:              log,
	}
}

// RunMockpay runs `sluice mockpay`: migrate its own database, then serve the
// PSP API and hosted checkout on MOCKPAY_HTTP_ADDR and dispatch webhooks.
func RunMockpay(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	log = log.With(slog.String("svc", "mockpay"))
	pool, err := db.NewPool(ctx, cfg.MockpayDatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := mockpay.Migrate(ctx, pool); err != nil {
		return err
	}
	reg := obs.NewRegistry()
	if err := obs.RegisterPoolStats(reg, map[string]*pgxpool.Pool{"main": pool}, nil); err != nil {
		return err
	}
	srv, err := mockpay.New(ctx, pool, reg, MockpayOptions(cfg, log))
	if err != nil {
		return err
	}
	hs := httpx.NewServer(cfg.MockpayHTTPAddr, srv.Handler(), log)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpx.Serve(gctx, hs, log, nil) })
	g.Go(func() error { return srv.RunDispatcher(gctx) })
	log.InfoContext(ctx, "mockpay started", slog.String("addr", cfg.MockpayHTTPAddr),
		slog.String("webhook_url", cfg.PublicWebhookURL), slog.Bool("fault_header", cfg.AppEnv.AllowsUnsafeModes()))
	if err := g.Wait(); err != nil {
		return fmt.Errorf("mockpay: %w", err)
	}
	log.InfoContext(context.WithoutCancel(ctx), "mockpay stopped")
	return nil
}
