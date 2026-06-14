package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yourorg/sso-gateway/internal/api"
	"github.com/yourorg/sso-gateway/internal/apikey"
	"github.com/yourorg/sso-gateway/internal/bootstrap"
	"github.com/yourorg/sso-gateway/internal/karyawan"
	"github.com/yourorg/sso-gateway/internal/logger"
	"github.com/yourorg/sso-gateway/internal/server"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	d := bootstrap.MustRun(ctx)
	defer d.Close()

	repo := karyawan.NewRepo(d.Pool)
	ak := apikey.NewStore(d.Pool)
	h := api.NewHandlers(repo)

	srv := server.New(server.Config{
		Addr:            d.Cfg.HTTPAddr,
		APIRateLimitRPM: d.Cfg.APIRateLimitPerMin,
	}, server.Deps{API: h, APIKeys: ak, Redis: d.Redis})

	httpSrv := &http.Server{
		Addr:              d.Cfg.HTTPAddr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Run the listener in a goroutine and forward non-graceful errors to the
	// main goroutine via a channel. Calling log.Fatalf/os.Exit inside a
	// goroutine bypasses deferred Close() on the pool/redis client.
	listenErr := make(chan error, 1)
	go func() {
		logger.L().Info().Str("addr", d.Cfg.HTTPAddr).Msg("api listening")
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
