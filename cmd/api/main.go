package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
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

	// Run the listener in a goroutine and forward non-graceful errors to the
	// main goroutine via a channel. Calling log.Fatalf/os.Exit inside a
	// goroutine bypasses deferred Close() on the pool/redis client.
	listenErr := make(chan error, 1)
	go func() {
		logger.L().Info().Str("addr", cfg.HTTPAddr).Msg("api listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErr <- err
		}
	}()

	select {
	case err := <-listenErr:
		logger.L().Error().Err(err).Msg("http listener failed")
		os.Exit(1)
	case <-ctx.Done():
		logger.L().Info().Msg("api shutting down")
		shutdownCtx, sCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer sCancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}
}
