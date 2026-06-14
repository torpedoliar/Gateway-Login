package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourorg/sso-gateway/internal/store"
	syncpkg "github.com/yourorg/sso-gateway/internal/sync"
	"github.com/yourorg/sso-gateway/internal/vpsmysql"
)

// runInitialSync decrypts the VPS password stored in storeCfg, opens a
// MySQL connection to the VPS, and runs one sync pass against the local
// Postgres pool. It is called by main() after the API key is persisted.
func runInitialSync(ctx context.Context, pool *pgxpool.Pool, storeCfg *store.Config, masterB64 string) error {
	if storeCfg == nil {
		return fmt.Errorf("nil store config")
	}
	batch := storeCfg.Sync.BatchSize
	if batch <= 0 {
		batch = 500
	}
	vps, err := vpsmysql.NewClientFromStoreConfig(ctx, &storeCfg.VPS, "", masterB64, batch)
	if err != nil {
		return fmt.Errorf("vps mysql: %w", err)
	}
	defer vps.Close()

	s := syncpkg.New(pool, vps, batch)
	rows, err := s.SyncKaryawan(ctx)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	fmt.Printf("Initial sync: %d rows upserted\n", rows)
	return nil
}
