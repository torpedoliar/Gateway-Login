package apikey

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestPool(t *testing.T) *pgxpool.Pool {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	return pool
}

func hashKey(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func TestStore_CreateGetByHash(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	s := NewStore(pool)

	id := "test-" + hashKey("plaintext-key")[:8]
	require.NoError(t, s.Create(ctx, &Entry{ID: id, KeyHash: hashKey("plaintext-key"), Description: "test"}))

	got, err := s.GetByHash(ctx, hashKey("plaintext-key"))
	require.NoError(t, err)
	assert.Equal(t, id, got.ID)
	assert.False(t, got.Revoked)

	// missing
	_, err = s.GetByHash(ctx, "nope")
	assert.Error(t, err)

	defer pool.Exec(ctx, "DELETE FROM api_keys WHERE id = $1", id)
}
