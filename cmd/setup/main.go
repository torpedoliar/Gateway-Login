// Package main is the svc-setup entry point: an interactive CLI that
// configures VPS credentials (encrypted at rest), generates a master
// encryption key + API key, runs migrations, persists the API key to
// Postgres, triggers an initial sync, and prints the plaintext API key
// once for the operator to copy.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourorg/sso-gateway/internal/setup"
)

const (
	defaultConfigPath  = "/etc/gateway/config.yaml"
	defaultDotenvPath  = "/etc/gateway/.env"
	defaultMigrations  = "internal/db/migrations"
	defaultAPIKeyEntry = "default"
)

func main() {
	// Allow overriding every filesystem location via env so this binary
	// works both in the docker `setup` container and in local dev.
	cfgPath := envOr("GATEWAY_CONFIG_PATH", defaultConfigPath)
	envPath := envOr("GATEWAY_DOTENV_PATH", defaultDotenvPath)
	migDir := envOr("GATEWAY_MIGRATIONS_DIR", defaultMigrations)
	pgDSN := os.Getenv("POSTGRES_DSN")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	existingMaster := os.Getenv("GATEWAY_MASTER_KEY")

	storeCfg, apiKey, masterB64, err := setup.RunPromptFlow(
		ctx, setup.SurveyPrompter{}, cfgPath, envPath, existingMaster,
	)
	if err != nil {
		log.Fatalf("setup: %v", err)
	}

	fmt.Println()
	fmt.Println("--- Setup summary ---")
	fmt.Println("VPS host:        ", storeCfg.VPS.Host)
	fmt.Println("VPS database:    ", storeCfg.VPS.Database)
	fmt.Println("Config written:  ", cfgPath)
	fmt.Println("Env written:     ", envPath)
	fmt.Println("Master key:      ", masterB64)
	fmt.Println()
	fmt.Println("API key (save this, shown once):")
	fmt.Println("  ", apiKey)
	fmt.Println()

	if pgDSN == "" {
		fmt.Println("POSTGRES_DSN not set; skipping migrations, api key insert, and initial sync.")
		fmt.Println("Set POSTGRES_DSN and re-run the binary to finish wiring the database.")
		return
	}

	pool, err := openPool(ctx, pgDSN)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer pool.Close()

	if err := runMigrations(ctx, pgDSN, migDir); err != nil {
		log.Fatalf("migrations: %v", err)
	}
	fmt.Println("Migrations:      applied")

	if err := insertAPIKey(ctx, pool, apiKeyEntry(storeCfg, apiKey, defaultAPIKeyEntry)); err != nil {
		log.Fatalf("api key insert: %v", err)
	}
	fmt.Println("API key:         inserted into api_keys")

	if err := runInitialSync(ctx, pool, storeCfg, masterB64); err != nil {
		// Initial sync is best-effort: the cron in svc-sync will retry. Log
		// and continue so the operator still gets a working gateway even
		// if the VPS happens to be unreachable at setup time.
		log.Printf("initial sync failed (will be retried by svc-sync): %v", err)
	} else {
		fmt.Println("Initial sync:    completed")
	}

	if abs, err := filepath.Abs(cfgPath); err == nil {
		fmt.Println("Config absolute: ", abs)
	}
}

// envOr returns the env var named k, or fallback if it is empty.
func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

// openPool opens a pgxpool, pings it, and returns the pool. The ping
// surfaces bad DSNs early so the rest of main() can assume the pool is
// usable.
func openPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open pg: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping pg: %w", err)
	}
	return pool, nil
}
