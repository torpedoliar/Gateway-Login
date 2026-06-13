package store

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/sso-gateway/internal/crypto"
)

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	masterKey, _ := crypto.NewRandomKey()

	cfg := &Config{
		VPS: VPSConfig{
			Host:     "vps.example.com",
			Port:     3306,
			Database: "sja",
			Username: "sso_replicator",
		},
		API: APIConfig{
			Keys: []APIKeyEntry{{ID: "app1", KeyHash: "sha256hex", Description: "app"}},
		},
		Sync: SyncConfig{Interval: "5m", BatchSize: 500, WatermarkColumn: "updated_at"},
	}
	cfg.VPS.SetEncryptedPassword("super-secret", masterKey)

	require.NoError(t, Save(path, cfg))

	loaded, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "vps.example.com", loaded.VPS.Host)
	assert.Equal(t, 3306, loaded.VPS.Port)
	assert.Equal(t, "sja", loaded.VPS.Database)
	assert.Equal(t, "sso_replicator", loaded.VPS.Username)

	pt, err := loaded.VPS.GetDecryptedPassword(masterKey)
	require.NoError(t, err)
	assert.Equal(t, "super-secret", pt)

	assert.Len(t, loaded.API.Keys, 1)
	assert.Equal(t, "app1", loaded.API.Keys[0].ID)
}
