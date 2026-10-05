package main

import (
	"crypto/rand"
	"encoding/hex"
)

// randomHex returns n random bytes as hex, the controller's nonce format.
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
