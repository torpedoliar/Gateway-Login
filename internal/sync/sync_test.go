package syncpkg

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestPG(t *testing.T) *pgxpool.Pool {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	return pool
}

type fakeSource struct {
	rows []KaryawanRow
}

func (f *fakeSource) FetchKaryawanUpdatedSince(ctx context.Context, since time.Time) ([]KaryawanRow, error) {
	var out []KaryawanRow
	for _, r := range f.rows {
		if r.UpdatedAt.After(since) {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestSyncKaryawan_NewRows(t *testing.T) {
	pool := newTestPG(t)
	defer pool.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	src := &fakeSource{rows: []KaryawanRow{
		{NIKHRIS: "NEW" + time.Now().Format("150405000"), NamaKaryawan: "A", UpdatedAt: now},
		{NIKHRIS: "NEW" + time.Now().Format("150405001"), NamaKaryawan: "B", UpdatedAt: now.Add(time.Second)},
	}}

	s := New(pool, src, 500)
	n, err := s.SyncKaryawan(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	defer pool.Exec(ctx, "DELETE FROM karyawan WHERE nik_hris LIKE 'NEW%'")
	defer pool.Exec(ctx, "DELETE FROM sync_runs")
	defer pool.Exec(ctx, "DELETE FROM sync_state")
}

func TestSyncKaryawan_NoRows(t *testing.T) {
	pool := newTestPG(t)
	defer pool.Close()
	src := &fakeSource{rows: nil}
	s := New(pool, src, 500)
	n, err := s.SyncKaryawan(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

var _ = sql.ErrNoRows
