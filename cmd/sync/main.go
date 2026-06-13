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
	"github.com/yourorg/sso-gateway/internal/crypto"
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
	masterKey, err := crypto.Base64ToKey(cfg.MasterKey)
	if err != nil {
		log.Fatalf("invalid master key: %v", err)
	}
	password, err := storeCfg.VPS.GetDecryptedPassword(masterKey)
	if err != nil {
		log.Fatalf("decrypt vps password: %v", err)
	}
	dsn := vpsmysql.BuildDSN(storeCfg.VPS.Host, storeCfg.VPS.Port, storeCfg.VPS.Database, storeCfg.VPS.Username, password)

	vps, err := vpsmysql.NewClient(ctx, dsn, cfg.SyncBatchSize)
	if err != nil {
		log.Fatalf("vps mysql: %v", err)
	}
	defer vps.Close()

	syncer := syncpkg.New(pool, vps, cfg.SyncBatchSize)

	runOnce(ctx, syncer)

	interval := storeCfg.Sync.Interval
	if interval == "" {
		interval = cfg.SyncInterval.String()
	}
	spec := "@every " + interval
	c := cron.New()
	if _, err := c.AddFunc(spec, func() {
		runOnce(context.Background(), syncer)
	}); err != nil {
		log.Fatalf("cron add: %v", err)
	}
	c.Start()
	logger.L().Info().Str("spec", spec).Msg("sync scheduler started")

	<-ctx.Done()
	logger.L().Info().Msg("sync shutting down")
	c.Stop()
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
