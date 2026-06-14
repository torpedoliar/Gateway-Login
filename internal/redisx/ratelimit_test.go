package redisx

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return c, mr
}

func TestAllow_AtomicFirstIncrSetsTTL(t *testing.T) {
	c, mr := newTestRedis(t)
	defer c.Close()

	allowed, err := Allow(context.Background(), c, "test:1", 1, 100*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, allowed)

	// After 100ms the key should expire. miniredis FastForward moves time.
	mr.FastForward(150 * time.Millisecond)

	// Counter reset: new request in a new bucket should be allowed again.
	allowed, err = Allow(context.Background(), c, "test:1", 1, 100*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllow_RespectsMax(t *testing.T) {
	c, _ := newTestRedis(t)
	defer c.Close()
	ctx := context.Background()

	// max=2 in 1-second window
	allowed, err := Allow(ctx, c, "test:2", 2, time.Second)
	require.NoError(t, err)
	assert.True(t, allowed)

	allowed, err = Allow(ctx, c, "test:2", 2, time.Second)
	require.NoError(t, err)
	assert.True(t, allowed)

	allowed, err = Allow(ctx, c, "test:2", 2, time.Second)
	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestAllow_HonorsContextCancellation(t *testing.T) {
	c, _ := newTestRedis(t)
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Allow(ctx, c, "test:3", 5, time.Minute)
	assert.Error(t, err)
}

func TestAllow_RejectsZeroWindow(t *testing.T) {
	_, err := Allow(context.Background(), nil, "k", 5, 0)
	assert.Error(t, err)
}

func TestAllow_RejectsNegativeWindow(t *testing.T) {
	_, err := Allow(context.Background(), nil, "k", 5, -time.Second)
	assert.Error(t, err)
}
