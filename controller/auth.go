package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Signature scheme
//
//	message   = METHOD "\n" PATH "\n" TIMESTAMP "\n" NONCE
//	signature = hex(HMAC-SHA256(secret, message))
//
// sent as:
//
//	X-Auth-Timestamp: <unix seconds>
//	X-Auth-Nonce:     <random, unique within the skew window>
//	Authorization:    HMAC <signature>
//
// Requests outside the allowed clock skew, or replaying a nonce already
// seen, are rejected.

const authScheme = "HMAC "

var (
	errMalformed = errors.New("malformed authentication headers")
	errSkew      = errors.New("timestamp outside allowed window")
	errReplay    = errors.New("nonce already used")
	errSignature = errors.New("signature mismatch")
)

// NonceCache remembers nonces for the skew window. One background sweeper
// evicts them, so a flood of requests can't spawn a goroutine each.
type NonceCache struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]time.Time
}

func NewNonceCache(ttl time.Duration) *NonceCache {
	return &NonceCache{ttl: ttl, entries: make(map[string]time.Time)}
}

// Add records the nonce and reports whether it was previously unseen.
func (c *NonceCache) Add(nonce string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if exp, ok := c.entries[nonce]; ok && now.Before(exp) {
		return false
	}
	c.entries[nonce] = now.Add(c.ttl)
	return true
}

// sweep drops expired entries.
func (c *NonceCache) sweep(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for nonce, exp := range c.entries {
		if !now.Before(exp) {
			delete(c.entries, nonce)
		}
	}
}

func (c *NonceCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// StartSweeper evicts expired nonces until stop is closed.
func (c *NonceCache) StartSweeper(stop <-chan struct{}) {
	ticker := time.NewTicker(c.ttl)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case t := <-ticker.C:
				c.sweep(t)
			}
		}
	}()
}

// Authenticator validates HMAC-signed requests.
type Authenticator struct {
	secret []byte
	skew   time.Duration
	nonces *NonceCache
	now    func() time.Time // injectable for tests
}

func NewAuthenticator(secret []byte, skew time.Duration, nonces *NonceCache) *Authenticator {
	return &Authenticator{secret: secret, skew: skew, nonces: nonces, now: time.Now}
}

// Sign produces the expected signature for a request line.
func Sign(secret []byte, method, path, timestamp, nonce string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(method + "\n" + path + "\n" + timestamp + "\n" + nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks the signature, freshness and uniqueness of a request.
func (a *Authenticator) Verify(r *http.Request) error {
	tsStr := r.Header.Get("X-Auth-Timestamp")
	nonce := r.Header.Get("X-Auth-Nonce")
	auth := r.Header.Get("Authorization")

	if tsStr == "" || nonce == "" || !strings.HasPrefix(auth, authScheme) {
		return errMalformed
	}

	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return errMalformed
	}

	sig, err := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(auth, authScheme)))
	if err != nil {
		return errMalformed
	}

	now := a.now()
	if delta := now.Sub(time.Unix(ts, 0)); delta > a.skew || delta < -a.skew {
		return errSkew
	}

	expected, err := hex.DecodeString(Sign(a.secret, r.Method, r.URL.Path, tsStr, nonce))
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(sig, expected) != 1 {
		return errSignature
	}

	// Consume the nonce only after the signature checks out, so a forged
	// request can't burn a legitimate client's nonce.
	if !a.nonces.Add(nonce, now) {
		return errReplay
	}
	return nil
}

// Middleware rejects requests that fail verification.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := a.Verify(r); err != nil {
			log.Printf("auth rejected %s %s from %s: %v", r.Method, r.URL.Path, clientIP(r), err)
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.IndexByte(fwd, ','); i > 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return strings.TrimSpace(fwd)
	}
	return r.RemoteAddr
}
