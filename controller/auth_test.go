package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func signedRequest(t *testing.T, method, path string, ts time.Time, nonce string, secret []byte) *http.Request {
	t.Helper()
	tsStr := strconv.FormatInt(ts.Unix(), 10)
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Auth-Timestamp", tsStr)
	req.Header.Set("X-Auth-Nonce", nonce)
	req.Header.Set("Authorization", authScheme+Sign(secret, method, path, tsStr, nonce))
	return req
}

func newTestAuth(now time.Time) *Authenticator {
	a := NewAuthenticator(testSecret, 30*time.Second, NewNonceCache(time.Minute))
	a.now = func() time.Time { return now }
	return a
}

func TestVerifyAcceptsValidRequest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := newTestAuth(now)

	if err := a.Verify(signedRequest(t, http.MethodPost, "/mute", now, "nonce-1", testSecret)); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyRejectsReplayedNonce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := newTestAuth(now)

	req := signedRequest(t, http.MethodPost, "/mute", now, "nonce-replay", testSecret)
	if err := a.Verify(req); err != nil {
		t.Fatalf("first Verify: %v", err)
	}

	replay := signedRequest(t, http.MethodPost, "/mute", now, "nonce-replay", testSecret)
	if err := a.Verify(replay); !errors.Is(err, errReplay) {
		t.Fatalf("replay error = %v, want errReplay", err)
	}
}

func TestVerifyRejectsStaleAndFutureTimestamps(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	cases := map[string]time.Duration{
		"too old":    -31 * time.Second,
		"too future": 31 * time.Second,
	}
	for name, offset := range cases {
		t.Run(name, func(t *testing.T) {
			a := newTestAuth(now)
			req := signedRequest(t, http.MethodPost, "/mute", now.Add(offset), "n", testSecret)
			if err := a.Verify(req); !errors.Is(err, errSkew) {
				t.Fatalf("error = %v, want errSkew", err)
			}
		})
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := newTestAuth(now)

	req := signedRequest(t, http.MethodPost, "/mute", now, "n", []byte("a-completely-different-secret-xyz"))
	if err := a.Verify(req); !errors.Is(err, errSignature) {
		t.Fatalf("error = %v, want errSignature", err)
	}
}

// A signature is bound to the method and path, so a signature captured for
// one endpoint must not authorize another.
func TestVerifyRejectsSignatureReuseAcrossPaths(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := newTestAuth(now)

	tsStr := strconv.FormatInt(now.Unix(), 10)
	sig := Sign(testSecret, http.MethodPost, "/mute", tsStr, "n")

	req := httptest.NewRequest(http.MethodPost, "/unmute", nil)
	req.Header.Set("X-Auth-Timestamp", tsStr)
	req.Header.Set("X-Auth-Nonce", "n")
	req.Header.Set("Authorization", authScheme+sig)

	if err := a.Verify(req); !errors.Is(err, errSignature) {
		t.Fatalf("error = %v, want errSignature", err)
	}
}

func TestVerifyRejectsMalformedHeaders(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	cases := map[string]func(*http.Request){
		"missing timestamp": func(r *http.Request) { r.Header.Del("X-Auth-Timestamp") },
		"missing nonce":     func(r *http.Request) { r.Header.Del("X-Auth-Nonce") },
		"missing auth":      func(r *http.Request) { r.Header.Del("Authorization") },
		"wrong scheme":      func(r *http.Request) { r.Header.Set("Authorization", "Bearer abc") },
		"bad timestamp":     func(r *http.Request) { r.Header.Set("X-Auth-Timestamp", "yesterday") },
		"non-hex signature": func(r *http.Request) { r.Header.Set("Authorization", authScheme+"zzzz") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			a := newTestAuth(now)
			req := signedRequest(t, http.MethodPost, "/mute", now, "n", testSecret)
			mutate(req)
			if err := a.Verify(req); !errors.Is(err, errMalformed) {
				t.Fatalf("error = %v, want errMalformed", err)
			}
		})
	}
}

// A forged request must not consume the nonce a legitimate client is about
// to use.
func TestFailedSignatureDoesNotBurnNonce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := newTestAuth(now)

	forged := signedRequest(t, http.MethodPost, "/mute", now, "shared-nonce", []byte("wrong-secret-wrong-secret-wrong!"))
	if err := a.Verify(forged); !errors.Is(err, errSignature) {
		t.Fatalf("forged error = %v, want errSignature", err)
	}

	legit := signedRequest(t, http.MethodPost, "/mute", now, "shared-nonce", testSecret)
	if err := a.Verify(legit); err != nil {
		t.Fatalf("legitimate request rejected after forged attempt: %v", err)
	}
}

func TestMiddlewareReturns401(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := newTestAuth(now)

	called := false
	h := a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mute", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if called {
		t.Error("handler ran for an unauthenticated request")
	}
}

func TestNonceCacheSweepEvictsExpired(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := NewNonceCache(time.Minute)

	c.Add("a", now)
	c.Add("b", now.Add(30*time.Second))

	// At +70s "a" (expiry +60s) is stale while "b" (expiry +90s) is still live.
	c.sweep(now.Add(70 * time.Second))
	if got := c.Len(); got != 1 {
		t.Fatalf("cache size after sweep = %d, want 1", got)
	}

	// Once expired, the nonce may be issued again.
	if !c.Add("a", now.Add(90*time.Second)) {
		t.Error("expired nonce was not reusable")
	}
}
