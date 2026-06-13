package redisx

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewClient_RequiresAddr(t *testing.T) {
	_, err := NewClient(context.Background(), "", "", 0)
	assert.Error(t, err)
}
