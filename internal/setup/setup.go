// Package setup implements the interactive CLI prompt flow used by the
// `cmd/setup` entry point. It asks for VPS MySQL credentials, validates
// connectivity, generates a master encryption key + API key, encrypts the
// VPS password at rest, and persists a YAML config file plus a dotenv
// file containing the master key.
package setup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/AlecAivazis/survey/v2"

	"github.com/yourorg/sso-gateway/internal/crypto"
	"github.com/yourorg/sso-gateway/internal/store"
	"github.com/yourorg/sso-gateway/internal/vpsmysql"
)

// apiKeyPrefix is prepended to every generated key so callers can recognize
// "this is a gateway API key" at a glance.
const apiKeyPrefix = "ssogw_"

// Question is a minimal survey-like field the Prompter renders.
// We intentionally mirror survey.Question's shape (Name, Prompt, Default)
// so the real survey library can satisfy the Prompter, but tests can
// provide a fake without importing survey's interactive machinery.
type Question struct {
	Name    string
	Prompt  string
	Default string
	Help    string
}

// Prompter renders the questions and returns answers in the same order.
type Prompter interface {
	Ask(qs []*Question) ([]string, error)
}

// SurveyPrompter is a Prompter backed by github.com/AlecAivazis/survey/v2.
// It is the production implementation; the fakePrompter in tests lets
// unit tests run without a TTY.
type SurveyPrompter struct{}

// Ask converts setup.Question to survey.Question and runs them.
func (SurveyPrompter) Ask(qs []*Question) ([]string, error) {
	sqs := make([]*survey.Question, len(qs))
	for i, q := range qs {
		sq := &survey.Question{
			Name:   q.Name,
			Prompt: &survey.Input{Message: q.Prompt, Default: q.Default, Help: q.Help},
		}
		sqs[i] = sq
	}
	answers := struct {
		Out []string
	}{Out: make([]string, len(qs))}
	if err := survey.Ask(sqs, &answers); err != nil {
		return nil, err
	}
	return answers.Out, nil
}

// GenerateAPIKey returns a fresh, opaque API key. The plaintext is shown
// to the operator once; only its hash is stored.
func GenerateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return apiKeyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// HashAPIKey returns the hex-encoded SHA-256 of the plaintext key, which
// is what the database stores and what callers compare against.
func HashAPIKey(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// RunPromptFlow drives the interactive configuration flow:
//
//  1. Asks the operator for VPS host/port/db/user/password (and an optional
//     API key; empty => generate one).
//  2. Connects to the VPS MySQL via vpsmysql.NewClient to validate the
//     credentials. If this fails, the flow aborts with the connection error
//     so the operator can correct the inputs.
//  3. Ensures a master key (generates one if existingMasterKey is empty)
//     and encrypts the VPS password with it.
//  4. Writes the populated store.Config to configPath via store.Save and
//     appends/overwrites GATEWAY_MASTER_KEY in dotenvPath with mode 0600.
//
// Returns the populated *store.Config, the plaintext API key (caller shows
// it once), the master key as base64, and any error.
func RunPromptFlow(
	ctx context.Context,
	p Prompter,
	configPath string,
	dotenvPath string,
	existingMasterKey string,
) (*store.Config, string, string, error) {
	questions := []*Question{
		{Name: "host", Prompt: "VPS MySQL host", Default: ""},
		{Name: "port", Prompt: "VPS MySQL port", Default: "3306"},
		{Name: "database", Prompt: "Database name", Default: "sja"},
		{Name: "username", Prompt: "Username", Default: "sso_replicator"},
		{Name: "password", Prompt: "Password", Help: "Stored encrypted at rest with the master key."},
		{Name: "apiKey", Prompt: "API key (leave blank to auto-generate)", Default: ""},
	}

	answers, err := p.Ask(questions)
	if err != nil {
		return nil, "", "", fmt.Errorf("prompt: %w", err)
	}
	if len(answers) != len(questions) {
		return nil, "", "", fmt.Errorf("expected %d answers, got %d", len(questions), len(answers))
	}

	host := answers[0]
	portStr := answers[1]
	database := answers[2]
	username := answers[3]
	password := answers[4]
	apiKeyPlain := answers[5]

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid port %q: %w", portStr, err)
	}

	// Test the connection before persisting any secrets so the operator
	// can correct bad inputs without rotating the master key.
	dsn := vpsmysql.BuildDSN(host, port, database, username, password)
	vps, err := vpsmysql.NewClient(ctx, dsn, 1)
	if err != nil {
		return nil, "", "", fmt.Errorf("vps connection: %w", err)
	}
	_ = vps.Close()

	if apiKeyPlain == "" {
		apiKeyPlain, err = GenerateAPIKey()
		if err != nil {
			return nil, "", "", fmt.Errorf("generate api key: %w", err)
		}
	}

	// Resolve master key.
	masterB64 := existingMasterKey
	var masterKey []byte
	if masterB64 == "" {
		masterKey, err = crypto.NewRandomKey()
		if err != nil {
			return nil, "", "", fmt.Errorf("generate master key: %w", err)
		}
		masterB64 = crypto.KeyToBase64(masterKey)
	} else {
		masterKey, err = crypto.Base64ToKey(masterB64)
		if err != nil {
			return nil, "", "", fmt.Errorf("decode existing master key: %w", err)
		}
	}

	cfg := &store.Config{
		VPS: store.VPSConfig{
			Host:     host,
			Port:     port,
			Database: database,
			Username: username,
		},
		API: store.APIConfig{
			Keys: []store.APIKeyEntry{{
				ID:          "default",
				KeyHash:     HashAPIKey(apiKeyPlain),
				Description: "generated by setup",
			}},
		},
		Sync: store.SyncConfig{
			Interval:        "5m",
			BatchSize:       500,
			WatermarkColumn: "updated_at",
		},
	}
	if err := cfg.VPS.SetEncryptedPassword(password, masterKey); err != nil {
		return nil, "", "", fmt.Errorf("encrypt vps password: %w", err)
	}

	if err := store.Save(configPath, cfg); err != nil {
		return nil, "", "", fmt.Errorf("save config: %w", err)
	}

	if err := writeDotenv(dotenvPath, masterB64); err != nil {
		return nil, "", "", fmt.Errorf("write dotenv: %w", err)
	}

	return cfg, apiKeyPlain, masterB64, nil
}

// writeDotenv writes GATEWAY_MASTER_KEY=<base64>\n to path with mode 0600.
// It creates the parent directory if needed.
func writeDotenv(path, masterB64 string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	contents := fmt.Sprintf("GATEWAY_MASTER_KEY=%s\n", masterB64)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(contents), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
