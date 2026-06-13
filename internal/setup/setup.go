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

const apiKeyPrefix = "ssogw_"

// Question is a minimal survey-like field the Prompter renders.
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

// HashAPIKey returns the hex-encoded SHA-256 of the plaintext key.
func HashAPIKey(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// ReadMasterKeyFromDotenv parses a dotenv-style file and returns the
// value of GATEWAY_MASTER_KEY, or "" if not present.
func ReadMasterKeyFromDotenv(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	for _, line := range splitLines(b) {
		const prefix = "GATEWAY_MASTER_KEY="
		if len(line) > len(prefix) && line[:len(prefix)] == prefix {
			return line[len(prefix):], nil
		}
	}
	return "", nil
}

func splitLines(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, string(b[start:i]))
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, string(b[start:]))
	}
	return out
}

// RunPromptFlow drives the interactive configuration flow.
//
//  1. Asks for VPS host/port/db/user/password (and optional API key).
//  2. Validates VPS connectivity. Failure aborts before any FS write.
//  3. Resolves the master key: reuses the value from dotenvPath if present
//     (so re-running setup does not invalidate the previous key), else
//     uses existingMasterKey, else generates a fresh one.
//  4. Writes .env FIRST and config.yaml SECOND. If config.yaml write fails,
//     the .env still has the same master key as before, so a retry can
//     succeed without bricking the previous encryption.
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

	// Validate VPS connectivity BEFORE any FS write.
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

	// Resolve master key. Reuse the on-disk value if present so a re-run
	// after a transient failure does not rotate the key and break the
	// previous encryption. Existing config.yaml is loaded and merged with
	// the new fields so operator-added API keys / sync.interval survive.
	masterB64 := existingMasterKey
	if masterB64 == "" {
		if v, err := ReadMasterKeyFromDotenv(dotenvPath); err == nil && v != "" {
			masterB64 = v
		}
	}
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
			return nil, "", "", fmt.Errorf("decode master key: %w", err)
		}
	}

	// Load existing config and merge so re-runs preserve any extra API keys
	// or sync settings the operator has hand-edited.
	cfg, _ := store.Load(configPath)
	if cfg == nil {
		cfg = &store.Config{}
	}
	cfg.VPS = store.VPSConfig{
		Host:     host,
		Port:     port,
		Database: database,
		Username: username,
	}
	if err := cfg.VPS.SetEncryptedPassword(password, masterKey); err != nil {
		return nil, "", "", fmt.Errorf("encrypt vps password: %w", err)
	}
	// Replace the "default" key if present, otherwise append. Other keys
	// the operator added (e.g. via hand-edit) are preserved.
	newKey := store.APIKeyEntry{
		ID:          "default",
		KeyHash:     HashAPIKey(apiKeyPlain),
		Description: "generated by setup",
	}
	replaced := false
	for i, k := range cfg.API.Keys {
		if k.ID == "default" {
			cfg.API.Keys[i] = newKey
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.API.Keys = append(cfg.API.Keys, newKey)
	}
	if cfg.Sync.Interval == "" {
		cfg.Sync.Interval = "5m"
	}
	if cfg.Sync.BatchSize == 0 {
		cfg.Sync.BatchSize = 500
	}
	if cfg.Sync.WatermarkColumn == "" {
		cfg.Sync.WatermarkColumn = "updated_at"
	}

	// Write .env FIRST. If config.yaml write then fails, the master key on
	// disk still matches what the operator's deployed api/sync containers
	// already loaded; a retry uses the same key and succeeds.
	if err := writeDotenv(dotenvPath, masterB64); err != nil {
		return nil, "", "", fmt.Errorf("write dotenv: %w", err)
	}
	if err := store.Save(configPath, cfg); err != nil {
		return nil, "", "", fmt.Errorf("save config: %w", err)
	}

	return cfg, apiKeyPlain, masterB64, nil
}

// writeDotenv writes GATEWAY_MASTER_KEY=<base64>\n to path with mode 0600.
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
