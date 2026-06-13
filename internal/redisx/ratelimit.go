package redisx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// luaIncrExpire atomically increments a counter and, if it's the first
// increment in the bucket, sets the expiry to `window`. Returns the
// post-increment count. Atomicity prevents the "Incr succeeded but Expire
// failed" failure mode that would leave the counter immortal.
var luaIncrExpire = redis.NewScript(`
local c = redis.call("INCR", KEYS[1])
if c == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return c
`)

// Allow returns true if the key has not exceeded max requests in the window.
// Uses a fixed window with millisecond-resolution TTL. window must be > 0.
func Allow(ctx context.Context, c *redis.Client, key string, max int, window time.Duration) (bool, error) {
	if window <= 0 {
		return false, errors.New("ratelimit: window must be > 0")
	}
	bucket := time.Now().UnixMilli() / window.Milliseconds()
	redisKey := fmt.Sprintf("rl:%s:%d", key, bucket)

	res, err := luaIncrExpire.Run(ctx, c, []string{redisKey}, window.Milliseconds()).Result()
	if err != nil {
		return false, err
	}
	count, ok := res.(int64)
	if !ok {
		return false, fmt.Errorf("ratelimit: unexpected redis reply type %T", res)
	}
	return count <= int64(max), nil
}
