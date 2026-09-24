package main

import (
	"crypto/rand"
	"encoding/hex"
)

// randomHex returns n random bytes hex-encoded, matching the nonce format
// the Controller already expects (the same shape `openssl rand -hex 8`
// produces on the command line).
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
