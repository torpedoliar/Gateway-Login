package redisx

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Allow returns true if the key has not exceeded max requests in the window.
func Allow(ctx context.Context, c *redis.Client, key string, max int, window time.Duration) (bool, error) {
	bucket := time.Now().Unix() / int64(window.Seconds())
	redisKey := fmt.Sprintf("rl:%s:%d", key, bucket)

	count, err := c.Incr(ctx, redisKey).Result()
	if err != nil {
		return false, err
	}
	if count == 1 {
		if err := c.Expire(ctx, redisKey, window).Err(); err != nil {
			return false, err
		}
	}
	return count <= int64(max), nil
}
