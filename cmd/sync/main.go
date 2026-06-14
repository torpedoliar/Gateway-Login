// Package main is the svc-sync entry point: cron-driven MySQL-to-Postgres sync.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/yourorg/sso-gateway/internal/bootstrap"
	"github.com/yourorg/sso-gateway/internal/logger"
	"github.com/yourorg/sso-gateway/internal/store"
	syncpkg "github.com/yourorg/sso-gateway/internal/sync"
	"github.com/yourorg/sso-gateway/internal/vpsmysql"
)

const defaultConfigPath = "/etc/gateway/config.yaml"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	d := bootstrap.MustRun(ctx)
	defer d.Close()

	cfgPath := os.Getenv("GATEWAY_CONFIG_PATH")
	if cfgPath == "" {
		cfgPath = defaultConfigPath
	}
	storeCfg, err := store.Load(cfgPath)
	if err != nil {
		log.Fatalf("load gateway config %s: %v", cfgPath, err)
	}

	// Recover from a previous process crash: any sync_runs left in
	// 'running' for >2x the configured interval is almost certainly
	// orphaned (its syncer is no longer alive). Mark them failed so the
	// dashboard does not see stuck rows and the watermark is not pinned.
	if n, err := syncpkg.CleanupStaleRuns(ctx, d.Pool, "karyawan", 2*d.Cfg.SyncInterval); err != nil {
		logger.L().Warn().Err(err).Msg("cleanup stale sync_runs failed (non-fatal)")
	} else if n > 0 {
		logger.L().Info().Int64("rows", n).Msg("recovered stale running sync_runs")
	}

	vps, err := vpsmysql.NewClientFromStoreConfig(ctx, &storeCfg.VPS, "", d.Cfg.MasterKey, d.Cfg.SyncBatchSize)
	if err != nil {
		log.Fatalf("vps mysql: %v", err)
	}
	defer vps.Close()

	syncer := syncpkg.New(d.Pool, vps, d.Cfg.SyncBatchSize)

	interval := storeCfg.Sync.Interval
	if interval == "" {
		interval = d.Cfg.SyncInterval.String()
	}
	spec := "@every " + interval

	c := cron.New()
	if _, err := c.AddFunc(spec, func() {
		runCtx, runCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer runCancel()
		runOnce(runCtx, syncer)
	}); err != nil {
		log.Fatalf("cron add: %v", err)
	}

	go runOnce(ctx, syncer)
	c.Start()
	logger.L().Info().Str("spec", spec).Msg("sync scheduler started")

	<-ctx.Done()
	logger.L().Info().Msg("sync shutting down")
	stopDone := make(chan struct{})
	go func() {
		c.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(15 * time.Second):
		logger.L().Warn().Msg("cron stop timed out; exiting with in-flight sync")
	}
}

func runOnce(ctx context.Context, s *syncpkg.Syncer) {
	c, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	rows, err := s.SyncKaryawan(c)
	if err != nil {
		logger.L().Error().Err(err).Msg("sync failed")
		return
	}
	logger.L().Info().Int("rows", rows).Msg("sync pass complete")
}
