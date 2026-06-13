package vpsmysql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewClient_BadDSN(t *testing.T) {
	_, err := NewClient(context.Background(), "not-a-dsn", 1)
	assert.Error(t, err)
}

func TestBuildDSN(t *testing.T) {
	dsn := BuildDSN("vps.host", 3306, "sja", "user", "pass")
	assert.Contains(t, dsn, "tcp(vps.host:3306)")
	assert.Contains(t, dsn, "sja")
	assert.Contains(t, dsn, "user")
	assert.Contains(t, dsn, "parseTime=true")
}
