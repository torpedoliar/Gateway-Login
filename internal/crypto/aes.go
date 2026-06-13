package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const KeySize = 32

// Encrypt encrypts plaintext with AES-256-GCM. Output is base64(nonce || ciphertext || tag).
func Encrypt(key []byte, plaintext string) (string, error) {
	if len(key) != KeySize {
		return "", fmt.Errorf("key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt reverses Encrypt. Input is base64(nonce || ciphertext || tag).
func Decrypt(key []byte, ciphertext string) (string, error) {
	if len(key) != KeySize {
		return "", fmt.Errorf("key must be %d bytes, got %d", KeySize, len(key))
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, body := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// KeyToBase64 encodes a raw key for storage in env files.
func KeyToBase64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// Base64ToKey decodes a base64 env value back to a raw key.
func Base64ToKey(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes, got %d", KeySize, len(b))
	}
	return b, nil
}

// NewRandomKey returns a fresh 32-byte key.
func NewRandomKey() ([]byte, error) {
	b := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}
