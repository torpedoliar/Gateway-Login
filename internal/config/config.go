package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	HTTPAddr           string
	PostgresDSN        string
	RedisAddr          string
	RedisPassword      string
	RedisDB            int
	MasterKey          string
	SyncInterval       time.Duration
	SyncBatchSize      int
	APIRateLimitPerMin int
	LogLevel           string
}

func Load() (*Config, error) {
	v := viper.New()
	v.AutomaticEnv()

	v.SetDefault("GATEWAY_HTTP_ADDR", ":8080")
	v.SetDefault("REDIS_PASSWORD", "")
	v.SetDefault("REDIS_DB", 0)
	v.SetDefault("SYNC_INTERVAL", "5m")
	v.SetDefault("SYNC_BATCH_SIZE", 500)
	v.SetDefault("API_RATE_LIMIT_PER_MIN", 300)
	v.SetDefault("LOG_LEVEL", "info")

	required := []string{"POSTGRES_DSN", "REDIS_ADDR", "GATEWAY_MASTER_KEY"}
	for _, k := range required {
		if v.GetString(k) == "" {
			return nil, fmt.Errorf("required env var missing or empty: %s", k)
		}
	}

	syncInterval, err := time.ParseDuration(v.GetString("SYNC_INTERVAL"))
	if err != nil {
		return nil, fmt.Errorf("invalid SYNC_INTERVAL: %w", err)
	}

	return &Config{
		HTTPAddr:           v.GetString("GATEWAY_HTTP_ADDR"),
		PostgresDSN:        v.GetString("POSTGRES_DSN"),
		RedisAddr:          v.GetString("REDIS_ADDR"),
		RedisPassword:      v.GetString("REDIS_PASSWORD"),
		RedisDB:            v.GetInt("REDIS_DB"),
		MasterKey:          v.GetString("GATEWAY_MASTER_KEY"),
		SyncInterval:       syncInterval,
		SyncBatchSize:      v.GetInt("SYNC_BATCH_SIZE"),
		APIRateLimitPerMin: v.GetInt("API_RATE_LIMIT_PER_MIN"),
		LogLevel:           v.GetString("LOG_LEVEL"),
	}, nil
}
