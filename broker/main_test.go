package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testSecret = "0123456789abcdef0123456789abcdef"
	testPrefix = "/webcam"
	aliceToken = "aaaa"
	// sha256("aaaa"), computed independently with `printf aaaa | sha256sum`.
	aliceHash = "61be55a8e2f6b4e172338bddf184d6dbee29c98853e0a0485ecee7f27b9af0b4"
)

// TestSignKnownVector pins the signature to a value computed outside Go:
//
//	printf '%s\n%s\n%s\n%s' POST /webcam/mute 1700000000 0123456789abcdef \
//	  | openssl dgst -sha256 -hmac "$testSecret"
//
// That is the README's documented scheme, so a match means they agree.
func TestSignKnownVector(t *testing.T) {
	got := sign(testSecret, "POST", "/webcam/mute", "1700000000", "0123456789abcdef")
	want := "7e152a094e744df23688f3a2b0ab3fbf9a47da80015cd9657db70f519f5f58c3"
	if got != want {
		t.Fatalf("sign() = %s, want %s", got, want)
	}
}

func TestHashToken(t *testing.T) {
	if got := hashToken(aliceToken); got != aliceHash {
		t.Fatalf("hashToken() = %s, want %s", got, aliceHash)
	}
}

func TestRandomHex(t *testing.T) {
	a, err := randomHex(8)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := randomHex(8)
	if len(a) != 16 {
		t.Fatalf("randomHex(8) length = %d, want 16", len(a))
	}
	if a == b {
		t.Fatal("two nonces were identical")
	}
}

func TestLoadUserTokens(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("valid", func(t *testing.T) {
		users, err := loadUserTokens(write("ok.json", `{"`+aliceHash+`":"alice"}`))
		if err != nil || users[aliceHash] != "alice" {
			t.Fatalf("got %v, %v", users, err)
		}
	})
	for name, body := range map[string]string{
		"empty object": `{}`,
		"bad json":     `{not json`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadUserTokens(write(name+".json", body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		if _, err := loadUserTokens(filepath.Join(dir, "nope.json")); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestLoadConfigRejectsShortSecret(t *testing.T) {
	t.Setenv("HMAC_SECRET", "too-short")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected an error for a short HMAC_SECRET")
	}
}

func TestLoadConfigRejectsBadTimeout(t *testing.T) {
	t.Setenv("HMAC_SECRET", testSecret)
	t.Setenv("UPSTREAM_TIMEOUT", "soon")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected an error for an unparseable UPSTREAM_TIMEOUT")
	}
}

// fakeController verifies as the README describes, written independently
// of sign() so a shared bug can't make both sides agree.
type fakeController struct {
	mu    sync.Mutex
	calls []string // "METHOD PATH"
	seen  map[string]bool
}

func (f *fakeController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ts := r.Header.Get("X-Auth-Timestamp")
	nonce := r.Header.Get("X-Auth-Nonce")
	auth := r.Header.Get("Authorization")

	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(sec, 0)).Abs() > 30*time.Second {
		http.Error(w, "bad timestamp", http.StatusUnauthorized)
		return
	}
	mac := hmac.New(sha256.New, []byte(testSecret))
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s", r.Method, r.URL.Path, ts, nonce)
	want := "HMAC " + hex.EncodeToString(mac.Sum(nil))
	if auth != want {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen[nonce] {
		http.Error(w, "replayed nonce", http.StatusUnauthorized)
		return
	}
	f.seen[nonce] = true
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"streaming":true,"muted":false,"uptime":"1m0s","restarts":0}`)
}

func newTestBroker(t *testing.T, controllerURL string) *httptest.Server {
	t.Helper()
	cfg := &config{
		ControllerURL:   controllerURL,
		RoutePrefix:     testPrefix,
		HMACSecret:      testSecret,
		UpstreamTimeout: 2 * time.Second,
	}
	srv := httptest.NewServer(newMux(cfg, map[string]string{aliceHash: "alice"}))
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, token string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestBrokerEndToEnd(t *testing.T) {
	fc := &fakeController{seen: map[string]bool{}}
	upstream := httptest.NewServer(fc)
	defer upstream.Close()
	broker := newTestBroker(t, upstream.URL)

	cases := []struct {
		name, method, path, token string
		wantStatus                int
		wantForwarded             string // "" = must not reach the controller
	}{
		{"healthz unauthenticated", "GET", "/healthz", "", 200, ""},
		{"healthz is not prefixed", "GET", testPrefix + "/healthz", "", 404, ""},
		{"status ok", "GET", testPrefix + "/status", aliceToken, 200, "GET /webcam/status"},
		{"mute ok", "POST", testPrefix + "/mute", aliceToken, 200, "POST /webcam/mute"},
		{"unmute ok", "POST", testPrefix + "/unmute", aliceToken, 200, "POST /webcam/unmute"},
		{"no token", "POST", testPrefix + "/mute", "", 401, ""},
		{"wrong token", "POST", testPrefix + "/mute", "bbbb", 401, ""},
		{"unprefixed path", "POST", "/mute", aliceToken, 404, ""},
		{"GET cannot mute", "GET", testPrefix + "/mute", aliceToken, 405, ""},
		{"POST cannot read status", "POST", testPrefix + "/status", aliceToken, 405, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc.mu.Lock()
			before := len(fc.calls)
			fc.mu.Unlock()

			resp, body := do(t, tc.method, broker.URL+tc.path, tc.token)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", resp.StatusCode, tc.wantStatus, body)
			}

			fc.mu.Lock()
			newCalls := fc.calls[before:]
			fc.mu.Unlock()

			switch {
			case tc.wantForwarded == "" && len(newCalls) != 0:
				t.Fatalf("request reached the Controller: %v", newCalls)
			case tc.wantForwarded != "" && (len(newCalls) != 1 || newCalls[0] != tc.wantForwarded):
				t.Fatalf("forwarded %v, want [%s]", newCalls, tc.wantForwarded)
			}
			if tc.wantStatus == 200 && tc.wantForwarded != "" && !strings.Contains(body, `"streaming":true`) {
				t.Fatalf("Controller body not relayed: %q", body)
			}
		})
	}
}

func TestBrokerRelaysControllerErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"mute control not found"}`, http.StatusInternalServerError)
	}))
	defer upstream.Close()
	broker := newTestBroker(t, upstream.URL)

	resp, _ := do(t, "POST", broker.URL+testPrefix+"/mute", aliceToken)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 relayed from the Controller", resp.StatusCode)
	}
}

func TestBrokerControllerUnreachable(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	url := upstream.URL
	upstream.Close() // nothing listening now

	broker := newTestBroker(t, url)
	resp, _ := do(t, "GET", broker.URL+testPrefix+"/status", aliceToken)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}
