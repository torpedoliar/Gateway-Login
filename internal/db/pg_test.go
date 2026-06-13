package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewPool_BadDSN(t *testing.T) {
	_, err := NewPool(context.Background(), "not-a-dsn")
	assert.Error(t, err)
}

func TestNewPool_PingRequiresReachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := NewPool(ctx, "postgres://nobody:nopass@127.0.0.1:1/x")
	assert.Error(t, err)
}
