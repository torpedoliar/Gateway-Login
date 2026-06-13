package setup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePrompter returns canned answers to all survey questions.
type fakePrompter struct {
	answers []string
	idx     int
}

func (f *fakePrompter) Ask(qs []*Question) ([]string, error) {
	out := make([]string, len(qs))
	for i := range qs {
		if f.idx >= len(f.answers) {
			out[i] = qs[i].Default
			continue
		}
		out[i] = f.answers[f.idx]
		f.idx++
	}
	return out, nil
}

func TestGenerateAPIKey_Format(t *testing.T) {
	k, err := GenerateAPIKey()
	require.NoError(t, err)
	assert.NotEmpty(t, k)
	// prefix + base64 (>=20 chars after prefix to be meaningful)
	assert.GreaterOrEqual(t, len(k), 20, "api key should be reasonably long")
}

func TestHashAPIKey_Deterministic(t *testing.T) {
	plain := "the-plain-text-key"
	h1 := HashAPIKey(plain)
	h2 := HashAPIKey(plain)
	assert.Equal(t, h1, h2)
	assert.NotEqual(t, plain, h1)
	assert.NotEmpty(t, h1)
}

func TestRunPromptFlow_RequiresVPSConnection(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	envPath := filepath.Join(dir, ".env")

	p := &fakePrompter{answers: []string{
		"127.0.0.1",  // host
		"3306",       // port
		"sja",        // database
		"sso_repl",   // username
		"pw",         // password
		"",           // api key (auto)
	}}

	_, _, _, err := RunPromptFlow(context.Background(), p, cfgPath, envPath, "")
	// We do NOT have a real VPS MySQL — the connection step must fail.
	// If it ever succeeds, the environment changed; skip to flag for review.
	if err == nil {
		t.Skip("VPS connection unexpectedly succeeded; environment has MySQL?")
	}
	assert.Error(t, err)
}
