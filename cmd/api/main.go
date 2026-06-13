package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/yourorg/sso-gateway/internal/api"
	"github.com/yourorg/sso-gateway/internal/apikey"
	"github.com/yourorg/sso-gateway/internal/config"
	"github.com/yourorg/sso-gateway/internal/db"
	"github.com/yourorg/sso-gateway/internal/karyawan"
	"github.com/yourorg/sso-gateway/internal/logger"
	"github.com/yourorg/sso-gateway/internal/redisx"
	"github.com/yourorg/sso-gateway/internal/server"
)

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

	rc, err := redisx.NewClient(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer rc.Close()

	repo := karyawan.NewRepo(pool)
	ak := apikey.NewStore(pool)
	h := api.NewHandlers(repo)

	srv := server.New(server.Config{
		Addr:            cfg.HTTPAddr,
		APIRateLimitRPM: cfg.APIRateLimitPerMin,
	}, server.Deps{API: h, APIKeys: ak, Redis: rc})

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.L().Info().Str("addr", cfg.HTTPAddr).Msg("api listening")
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	<-ctx.Done()
	logger.L().Info().Msg("api shutting down")
	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer sCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}
