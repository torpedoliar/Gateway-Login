package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/yourorg/sso-gateway/internal/apikey"
	"github.com/yourorg/sso-gateway/internal/redisx"
)

type ctxKey int

const (
	ctxAPIKeyID ctxKey = iota + 1
)

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
				if isNotFoundErr(err) {
					writeErr(w, http.StatusUnauthorized, "invalid_api_key")
					return
				}
				// Real DB error — don't misclassify as a bad key.
				log.Printf("api apikey lookup: %v", err)
				writeErr(w, http.StatusServiceUnavailable, "auth_backend_unavailable")
				return
			}
			// Fire-and-forget last_used_at update. Errors are logged but do
			// not fail the request — audit timestamps are not on the critical
			// path. Using a detached context so a client-cancelled request
			// does not abort the audit write.
			go func(id string) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if err := store.MarkUsed(ctx, id); err != nil {
					log.Printf("api apikey MarkUsed(%s): %v", id, err)
				}
			}(entry.ID)
			ctx := context.WithValue(r.Context(), ctxAPIKeyID, entry.ID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func isNotFoundErr(err error) bool {
	return err != nil && err.Error() == "api key not found"
}

// RateLimit limits per API key id. On Redis error we log and fail open
// (allow the request) so a transient Redis outage does not 503 every
// authenticated request. Operators monitor via logs and metrics.
func RateLimit(rc *redis.Client, maxPerMin int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _ := r.Context().Value(ctxAPIKeyID).(string)
			if id == "" {
				// APIKeyAuth did not run before us — wiring bug. Log so it
				// surfaces in operations, then pass through.
				log.Print("api rate-limit: no api key id in context; check middleware order")
				next.ServeHTTP(w, r)
				return
			}
			allowed, err := redisx.Allow(r.Context(), rc, "apikey:"+id, maxPerMin, time.Minute)
			if err != nil {
				log.Printf("api rate-limit redis error: %v", err)
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
