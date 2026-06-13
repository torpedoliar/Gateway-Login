package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/sso-gateway/internal/api"
	"github.com/yourorg/sso-gateway/internal/apikey"
	"github.com/yourorg/sso-gateway/internal/karyawan"
	"github.com/yourorg/sso-gateway/internal/server"
)

func hashKeyPlain(p string) string {
	s := sha256.Sum256([]byte(p))
	return hex.EncodeToString(s[:])
}

func TestE2E_ListKaryawan(t *testing.T) {
	pg := os.Getenv("TEST_PG_DSN")
	if pg == "" {
		t.Skip("set TEST_PG_DSN to run e2e")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pg)
	require.NoError(t, err)
	defer pool.Close()

	// Verify schema is present (migrations were run). If not, skip with a
	// clearer message than a cascade of failures.
	var schemaOK bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_name = 'karyawan'
		)
	`).Scan(&schemaOK))
	if !schemaOK {
		t.Skip("karyawan table missing — run migrations first")
	}

	// Seed 3 rows with a unique prefix on NIK + NamaKaryawan.
	prefix := "E2E" + time.Now().Format("150405")
	repo := karyawan.NewRepo(pool)
	for i, name := range []string{"Alice", "Bob", "Carol"} {
		now := time.Now().UTC().Truncate(time.Second)
		require.NoError(t, repo.Upsert(ctx, &karyawan.Karyawan{
			NIKHRIS:         prefix + string(rune('A'+i)),
			NamaKaryawan:    name + " " + prefix,
			NamaDepartemen:  "IT",
			SourceUpdatedAt: &now,
		}))
	}
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM karyawan WHERE nik_hris LIKE $1", prefix+"%")
	}()

	// Create a real API key in the api_keys table.
	plain := "e2e-plaintext-" + time.Now().Format("150405.000")
	keyID := "e2e-" + time.Now().Format("150405")
	ak := apikey.NewStore(pool)
	require.NoError(t, ak.Create(ctx, &apikey.Entry{ID: keyID, KeyHash: hashKeyPlain(plain)}))
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM api_keys WHERE id = $1", keyID)
	}()

	// Build the real server (no Redis — rate limit is skipped when Redis is nil).
	h := api.NewHandlers(repo)
	srv := server.New(server.Config{Addr: ":0", APIRateLimitRPM: 0}, server.Deps{API: h, APIKeys: ak})
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	// 1. Missing API key -> 401.
	resp, err := http.Get(ts.URL + "/api/v1/karyawan?limit=5")
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// 2. Valid API key -> 200, response includes the seeded rows.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/karyawan?nama_karyawan="+prefix+"&limit=10", nil)
	req.Header.Set("X-API-Key", plain)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "list status")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var lr struct {
		Data  []map[string]any `json:"data"`
		Total int             `json:"total"`
	}
	require.NoError(t, json.Unmarshal(body, &lr), "list body: %s", string(body))
	assert.GreaterOrEqual(t, lr.Total, 3, "expected at least 3 seeded rows, got total=%d", lr.Total)

	// 3. Get by NIK -> 200 and body contains the NIK.
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/karyawan/"+prefix+"A", nil)
	req2.Header.Set("X-API-Key", plain)
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp2.StatusCode, "get-by-nik status")
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	assert.Contains(t, string(body2), prefix+"A", "get-by-nik body should contain the NIK: %s", string(body2))
}
