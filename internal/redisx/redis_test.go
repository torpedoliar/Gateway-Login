package redisx

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewClient_RequiresAddr(t *testing.T) {
	_, err := NewClient(context.Background(), "", "", 0)
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
