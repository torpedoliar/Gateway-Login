package karyawan

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestPool(t *testing.T) *pgxpool.Pool {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set; skipping repo tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	return pool
}

func TestRepo_UpsertAndGetByNIK(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	r := NewRepo(pool)

	nik := "TEST" + time.Now().Format("150405")
	now := time.Now().UTC().Truncate(time.Second)
	k := &Karyawan{
		NIKHRIS:         nik,
		NIKSantos:       "SNT-" + nik,
		NamaKaryawan:    "Test User",
		NamaDepartemen:  "IT",
		NamaJabatan:     "Dev",
		Lokasi:          "JKT",
		Gender:          "L",
		SourceUpdatedAt: &now,
		RawPayload:      json.RawMessage(`{"x":1}`),
	}
	require.NoError(t, r.Upsert(ctx, k))

	got, err := r.GetByNIK(ctx, nik)
	require.NoError(t, err)
	assert.Equal(t, "Test User", got.NamaKaryawan)
	assert.Equal(t, "IT", got.NamaDepartemen)

	defer pool.Exec(ctx, "DELETE FROM karyawan WHERE nik_hris = $1", nik)
}

func TestRepo_List_FilterAndPagination(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	r := NewRepo(pool)

	// Seed 3 rows
	prefix := "LST" + time.Now().Format("150405")
	for i, name := range []string{"Alpha", "Beta", "Gamma"} {
		now := time.Now().UTC().Truncate(time.Second)
		_ = r.Upsert(ctx, &Karyawan{
			NIKHRIS:         prefix + string(rune('A'+i)),
			NamaKaryawan:    name + " " + prefix,
			NamaDepartemen:  "IT",
			SourceUpdatedAt: &now,
		})
	}
	defer pool.Exec(ctx, "DELETE FROM karyawan WHERE nik_hris LIKE $1", prefix+"%")

	res, total, err := r.List(ctx, Filter{NamaKaryawan: prefix, Limit: 10})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, total, 3)
	assert.GreaterOrEqual(t, len(res), 3)
}
