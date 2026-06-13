package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourorg/sso-gateway/internal/crypto"
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
	masterKey, err := crypto.Base64ToKey(masterB64)
	if err != nil {
		return fmt.Errorf("decode master key: %w", err)
	}
	password, err := storeCfg.VPS.GetDecryptedPassword(masterKey)
	if err != nil {
		return fmt.Errorf("decrypt vps password: %w", err)
	}
	dsn := vpsmysql.BuildDSN(
		storeCfg.VPS.Host,
		storeCfg.VPS.Port,
		storeCfg.VPS.Database,
		storeCfg.VPS.Username,
		password,
	)
	vps, err := vpsmysql.NewClient(ctx, dsn, 500)
	if err != nil {
		return fmt.Errorf("vps mysql: %w", err)
	}
	defer vps.Close()

	s := syncpkg.New(pool, vps, 500)
	rows, err := s.SyncKaryawan(ctx)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	_ = rows
	return nil
}
