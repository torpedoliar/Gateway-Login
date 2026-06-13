package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://x/y")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("SYNC_INTERVAL", "5m")
	t.Setenv("SYNC_BATCH_SIZE", "500")
	t.Setenv("API_RATE_LIMIT_PER_MIN", "300")
	t.Setenv("GATEWAY_MASTER_KEY", "abcdef")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, ":8080", cfg.HTTPAddr)
	assert.Equal(t, "postgres://x/y", cfg.PostgresDSN)
	assert.Equal(t, 5*time.Minute, cfg.SyncInterval)
	assert.Equal(t, 500, cfg.SyncBatchSize)
	assert.Equal(t, 300, cfg.APIRateLimitPerMin)
	assert.Equal(t, "debug", cfg.LogLevel)
}

func TestLoad_MissingRequired(t *testing.T) {
	_, err := Load()
	assert.Error(t, err)
}
