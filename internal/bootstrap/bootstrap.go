// Package bootstrap consolidates the common startup sequence shared by
// cmd/api, cmd/sync, and cmd/setup: load config from env, init the
// zerolog logger, open the Postgres pool, and open the Redis client.
// Returning concrete values keeps call sites simple; callers that need
// custom config (e.g. apiRateLimit, syncBatchSize) read from the
// returned *config.Config and pass to their own wiring.
package bootstrap

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/yourorg/sso-gateway/internal/config"
	"github.com/yourorg/sso-gateway/internal/db"
	"github.com/yourorg/sso-gateway/internal/logger"
	"github.com/yourorg/sso-gateway/internal/redisx"
)

// Deps groups the resources every main() needs.
type Deps struct {
	Cfg   *config.Config
	Pool  *pgxpool.Pool
	Redis *redis.Client
}

// Close releases the pool and redis client. Safe to call even if one
// failed to open (the other is still closed).
func (d *Deps) Close() {
	if d == nil {
		return
	}
	if d.Pool != nil {
		d.Pool.Close()
	}
	if d.Redis != nil {
		_ = d.Redis.Close()
	}
}

// Run initializes logger + opens Postgres + opens Redis. Returns
// Deps the caller should defer Close() on. Opens fail fast — caller
// receives a wrapped error and the program exits via log.Fatalf.
func Run(ctx context.Context) (*Deps, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	logger.Init(cfg.LogLevel)

	pool, err := db.NewPool(ctx, cfg.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}

	rc, err := redisx.NewClient(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("redis: %w", err)
	}

	return &Deps{Cfg: cfg, Pool: pool, Redis: rc}, nil
}

// MustRun wraps Run with log.Fatalf on error. Convenient for mains that
// want one-line setup and no recovery path.
func MustRun(ctx context.Context) *Deps {
	d, err := Run(ctx)
	if err != nil {
		log.Fatalf("bootstrap: %v", err)
	}
	return d
}
