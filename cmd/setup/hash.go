package main

import (
	"crypto/sha256"
	"encoding/hex"
)

// setupHash centralizes how we hash API keys so the production path
// (cmd/setup) and the test path (internal/setup) agree on the digest
// algorithm. Today both use SHA-256 hex; isolating the choice makes
// future rotation (e.g. SHA-256 + pepper) a one-line change.
func setupHash(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}
