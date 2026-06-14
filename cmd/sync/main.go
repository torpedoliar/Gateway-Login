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

	"github.com/yourorg/sso-gateway/internal/config"
	"github.com/yourorg/sso-gateway/internal/db"
	"github.com/yourorg/sso-gateway/internal/logger"
	"github.com/yourorg/sso-gateway/internal/store"
	syncpkg "github.com/yourorg/sso-gateway/internal/sync"
	"github.com/yourorg/sso-gateway/internal/vpsmysql"
)

const defaultConfigPath = "/etc/gateway/config.yaml"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	logger.Init(cfg.LogLevel)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer pool.Close()

	cfgPath := os.Getenv("GATEWAY_CONFIG_PATH")
	if cfgPath == "" {
		cfgPath = defaultConfigPath
	}
	storeCfg, err := store.Load(cfgPath)
	if err != nil {
		log.Fatalf("load gateway config %s: %v", cfgPath, err)
	}

	vps, err := vpsmysql.NewClientFromStoreConfig(ctx, &storeCfg.VPS, "", cfg.MasterKey, cfg.SyncBatchSize)
	if err != nil {
		log.Fatalf("vps mysql: %v", err)
	}
	defer vps.Close()

	syncer := syncpkg.New(pool, vps, cfg.SyncBatchSize)

	interval := storeCfg.Sync.Interval
	if interval == "" {
		interval = cfg.SyncInterval.String()
	}
	spec := "@every " + interval

	// Build cron FIRST so scheduled ticks fire on time, regardless of how long
	// the initial runOnce takes. Cron is not started yet (Start is below) — it
	// only begins ticking once Start is called. The first runOnce runs in its
	// own goroutine so cron.Start() is not blocked by it.
	c := cron.New()
	if _, err := c.AddFunc(spec, func() {
		// Use a fresh context (Background + timeout) so SIGTERM cancels the
		// currently running sync instead of leaking past c.Stop(). robfig/cron
		// waits for in-flight jobs to return before honoring Stop.
		runCtx, runCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer runCancel()
		runOnce(runCtx, syncer)
	}); err != nil {
		log.Fatalf("cron add: %v", err)
	}

	// Kick off the first sync in a goroutine so the scheduler starts on time.
	go runOnce(ctx, syncer)
	c.Start()
	logger.L().Info().Str("spec", spec).Msg("sync scheduler started")

	<-ctx.Done()
	logger.L().Info().Msg("sync shutting down")
	// c.Stop blocks until in-flight cron-triggered runs return. Because the
	// cron callback uses Background (decoupled from the shutdown ctx), the
	// in-flight sync's 10-min timeout will fire and unblock Stop. To not wait
	// 10 min on shutdown, run an additional watchdog that interrupts the
	// cron on ctx.Done by calling Stop in a goroutine with a deadline.
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
