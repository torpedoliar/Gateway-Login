package redisx

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

func NewClient(ctx context.Context, addr, password string, db int) (*redis.Client, error) {
	if addr == "" {
		return nil, fmt.Errorf("redis addr is required")
	}
	c := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return c, nil
}
