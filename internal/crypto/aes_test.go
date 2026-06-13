package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestKey(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := newTestKey(t)
	plaintext := "super-secret-vps-password"

	ct, err := Encrypt(key, plaintext)
	require.NoError(t, err)
	assert.NotEmpty(t, ct)

	got, err := Decrypt(key, ct)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

func TestDecrypt_Tampered(t *testing.T) {
	key := newTestKey(t)
	ct, err := Encrypt(key, "x")
	require.NoError(t, err)

	// flip a byte
	tampered := []byte(ct)
	tampered[10] ^= 0xFF
	_, err = Decrypt(key, string(tampered))
	assert.Error(t, err)
}

func TestEncrypt_BadKeySize(t *testing.T) {
	_, err := Encrypt([]byte("short"), "x")
	assert.Error(t, err)
}

func TestKeyToBase64(t *testing.T) {
	b := newTestKey(t)
	s := KeyToBase64(b)
	out, err := base64.StdEncoding.DecodeString(s)
	require.NoError(t, err)
	assert.Equal(t, b, out)
}
