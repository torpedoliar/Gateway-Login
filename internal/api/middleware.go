package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/yourorg/sso-gateway/internal/apikey"
	"github.com/yourorg/sso-gateway/internal/redisx"
)

type ctxKey int

const ctxAPIKeyID ctxKey = 1

// APIKeyAuth verifies X-API-Key header against api_keys table.
func APIKeyAuth(store *apikey.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get("X-API-Key")
			if raw == "" {
				writeErr(w, http.StatusUnauthorized, "missing_api_key")
				return
			}
			sum := sha256.Sum256([]byte(raw))
			hash := hex.EncodeToString(sum[:])
			entry, err := store.GetByHash(r.Context(), hash)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "invalid_api_key")
				return
			}
			_ = store.MarkUsed(r.Context(), entry.ID)
			ctx := context.WithValue(r.Context(), ctxAPIKeyID, entry.ID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RateLimit limits per API key id.
func RateLimit(rc *redis.Client, maxPerMin int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _ := r.Context().Value(ctxAPIKeyID).(string)
			if id == "" {
				next.ServeHTTP(w, r)
				return
			}
			allowed, err := redisx.Allow(r.Context(), rc, "apikey:"+id, maxPerMin, time.Minute)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			if !allowed {
				writeErr(w, http.StatusTooManyRequests, "rate_limited")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
