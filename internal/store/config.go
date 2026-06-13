package store

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/yourorg/sso-gateway/internal/crypto"
)

type Config struct {
	VPS  VPSConfig  `yaml:"vps"`
	API  APIConfig  `yaml:"api"`
	Sync SyncConfig `yaml:"sync"`
}

type VPSConfig struct {
	Host              string `yaml:"host"`
	Port              int    `yaml:"port"`
	Database          string `yaml:"database"`
	Username          string `yaml:"username"`
	PasswordEncrypted string `yaml:"password_encrypted"`
}

type APIConfig struct {
	Keys []APIKeyEntry `yaml:"keys"`
}

type APIKeyEntry struct {
	ID          string `yaml:"id"`
	KeyHash     string `yaml:"key_hash"`
	Description string `yaml:"description"`
}

type SyncConfig struct {
	Interval        string `yaml:"interval"`
	BatchSize       int    `yaml:"batch_size"`
	WatermarkColumn string `yaml:"watermark_column"`
}

// SetEncryptedPassword stores password as AES-encrypted ciphertext.
func (v *VPSConfig) SetEncryptedPassword(plaintext string, key []byte) error {
	ct, err := crypto.Encrypt(key, plaintext)
	if err != nil {
		return err
	}
	v.PasswordEncrypted = ct
	return nil
}

// GetDecryptedPassword returns the plaintext VPS password.
func (v *VPSConfig) GetDecryptedPassword(key []byte) (string, error) {
	if v.PasswordEncrypted == "" {
		return "", fmt.Errorf("no encrypted password set")
	}
	return crypto.Decrypt(key, v.PasswordEncrypted)
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	return &c, nil
}

func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
