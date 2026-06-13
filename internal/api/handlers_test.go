package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/sso-gateway/internal/apikey"
	"github.com/yourorg/sso-gateway/internal/karyawan"
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

func TestHandlers_GetMissingAPIKey(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	repo := karyawan.NewRepo(pool)
	ak := apikey.NewStore(pool)

	h := NewHandlers(repo)
	r := chi.NewRouter()
	r.Use(APIKeyAuth(ak))
	r.Get("/api/v1/karyawan/{nik_hris}", h.Get)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/karyawan/XXX", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHandlers_GetByNIK_Success(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := karyawan.NewRepo(pool)
	ak := apikey.NewStore(pool)

	nik := "API" + time.Now().Format("150405000")
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.Upsert(ctx, &karyawan.Karyawan{
		NIKHRIS: nik, NamaKaryawan: "Api User", NamaDepartemen: "IT", SourceUpdatedAt: &now,
	}))
	defer pool.Exec(ctx, "DELETE FROM karyawan WHERE nik_hris = $1", nik)

	plain := "test-api-key-plaintext"
	keyID := "testkey-" + time.Now().Format("150405")
	require.NoError(t, ak.Create(ctx, &apikey.Entry{ID: keyID, KeyHash: hashKey(plain), Description: "test"}))
	defer pool.Exec(ctx, "DELETE FROM api_keys WHERE id = $1", keyID)

	h := NewHandlers(repo)
	r := chi.NewRouter()
	r.Use(APIKeyAuth(ak))
	r.Get("/api/v1/karyawan/{nik_hris}", h.Get)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/karyawan/"+nik, nil)
	req.Header.Set("X-API-Key", plain)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var v karyawanView
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(rec.Body.Bytes()), &v))
	assert.Equal(t, nik, v.NIKHRIS)
	assert.Equal(t, "Api User", v.NamaKaryawan)
}
