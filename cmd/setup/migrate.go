package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// runMigrations applies all up migrations from migrationsDir against the
// given Postgres DSN. It is idempotent: a "no change" state is not an
// error.
func runMigrations(ctx context.Context, dsn, migrationsDir string) error {
	if migrationsDir == "" {
		return errors.New("migrations dir is empty")
	}
	// golang-migrate has its own driver lifecycle; we just pass the DSN
	// verbatim and let the driver figure out the schema_migrations table.
	src := "file://" + migrationsDir
	m, err := migrate.New(src, dsn)
	if err != nil {
		return fmt.Errorf("new migrator: %w", err)
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if srcErr != nil {
			fmt.Fprintf(stderrSink(), "migrate close source: %v\n", srcErr)
		}
		if dbErr != nil {
			fmt.Fprintf(stderrSink(), "migrate close db: %v\n", dbErr)
		}
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	_ = ctx // migrate.Up blocks on the driver; ctx reserved for future ctx-aware driver
	return nil
}
