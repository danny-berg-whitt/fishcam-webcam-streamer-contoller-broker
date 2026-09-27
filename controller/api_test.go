package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// fakeStreamer stands in for the streamer's internal control API.
func fakeStreamer(t *testing.T, muted *bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(StreamerStatus{Streaming: true, Muted: *muted, Uptime: "1m0s"})
	}
	mux.HandleFunc("/mute", func(w http.ResponseWriter, r *http.Request) { *muted = true; write(w) })
	mux.HandleFunc("/unmute", func(w http.ResponseWriter, r *http.Request) { *muted = false; write(w) })
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) { write(w) })
	return httptest.NewServer(mux)
}

func newTestAPI(t *testing.T, now time.Time, muted *bool) http.Handler {
	t.Helper()
	upstream := fakeStreamer(t, muted)
	t.Cleanup(upstream.Close)

	auth := newTestAuth(now)
	return NewAPI(NewStreamerClient(upstream.URL, 2*time.Second), auth, "").Handler()
}

// newTestAPIWithPrefix mounts the API under a path prefix, as it runs behind
// the cluster ingress at /webcam.
func newTestAPIWithPrefix(t *testing.T, now time.Time, muted *bool, prefix string) http.Handler {
	t.Helper()
	upstream := fakeStreamer(t, muted)
	t.Cleanup(upstream.Close)

	auth := newTestAuth(now)
	return NewAPI(NewStreamerClient(upstream.URL, 2*time.Second), auth, prefix).Handler()
}

func TestMuteUnmuteRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	muted := false
	h := newTestAPI(t, now, &muted)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, http.MethodPost, "/mute", now, "n1", testSecret))
	if rec.Code != http.StatusOK {
		t.Fatalf("mute status = %d, body = %s", rec.Code, rec.Body)
	}
	var st StreamerStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !st.Muted {
		t.Error("response says unmuted after /mute")
	}
	if !muted {
		t.Error("streamer was not muted")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, http.MethodPost, "/unmute", now, "n2", testSecret))
	if rec.Code != http.StatusOK {
		t.Fatalf("unmute status = %d, body = %s", rec.Code, rec.Body)
	}
	if muted {
		t.Error("streamer was not unmuted")
	}
}

func TestStatusRequiresAuth(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	muted := false
	h := newTestAPI(t, now, &muted)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestWrongMethodRejected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	muted := false
	h := newTestAPI(t, now, &muted)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, http.MethodGet, "/mute", now, "n3", testSecret))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHealthzIsUnauthenticated(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	muted := false
	h := newTestAPI(t, now, &muted)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestStreamerUnavailableReturns502(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	auth := newTestAuth(now)
	// Point at a closed port so the call fails fast.
	dead := httptest.NewServer(http.NewServeMux())
	url := dead.URL
	dead.Close()

	h := NewAPI(NewStreamerClient(url, 500*time.Millisecond), auth, "").Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, http.MethodPost, "/mute", now, "n4", testSecret))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestNormalizePrefix(t *testing.T) {
	cases := map[string]string{
		"":             "",
		"/":            "",
		"webcam":       "/webcam",
		"/webcam":      "/webcam",
		"/webcam/":     "/webcam",
		"  /webcam/  ": "/webcam",
		"webcam/api":   "/webcam/api",
	}
	for in, want := range cases {
		if got := NormalizePrefix(in); got != want {
			t.Errorf("NormalizePrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// Mounted under a prefix, the API answers the prefixed path.
func TestPrefixedRouting(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	muted := false
	h := newTestAPIWithPrefix(t, now, &muted, "/webcam")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, http.MethodPost, "/webcam/mute", now, "p1", testSecret))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if !muted {
		t.Error("streamer was not muted via the prefixed path")
	}
}

// The whole reason the controller serves the prefix itself rather than
// sitting behind an ingress rewrite: the signature covers the full path, so
// a signature computed for /mute must not authorise /webcam/mute.
func TestPrefixedRoutingRejectsUnprefixedSignature(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	muted := false
	h := newTestAPIWithPrefix(t, now, &muted, "/webcam")

	tsStr := strconv.FormatInt(now.Unix(), 10)
	sig := Sign(testSecret, http.MethodPost, "/mute", tsStr, "p2") // wrong path

	req := httptest.NewRequest(http.MethodPost, "/webcam/mute", nil)
	req.Header.Set("X-Auth-Timestamp", tsStr)
	req.Header.Set("X-Auth-Nonce", "p2")
	req.Header.Set("Authorization", authScheme+sig)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a signature over the unprefixed path", rec.Code)
	}
	if muted {
		t.Error("streamer was muted by a request signed for the wrong path")
	}
}

// The root path must not answer once a prefix is configured.
func TestPrefixedRoutingRootIs404(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	muted := false
	h := newTestAPIWithPrefix(t, now, &muted, "/webcam")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, http.MethodPost, "/mute", now, "p3", testSecret))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d for the unprefixed path, want 404", rec.Code)
	}
}

// The liveness probe stays at the root regardless of prefix — the kubelet
// hits the pod directly, never the ingress.
func TestHealthzUnaffectedByPrefix(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	muted := false
	h := newTestAPIWithPrefix(t, now, &muted, "/webcam")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want 200", rec.Code)
	}
}
