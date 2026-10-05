package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// recordingStreamer accepts only each endpoint's real method and records
// every request; code and body make it misbehave.
type recordingStreamer struct {
	mu   sync.Mutex
	seen []string // "METHOD PATH"
	code int
	body string
}

func (s *recordingStreamer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.seen = append(s.seen, r.Method+" "+r.URL.Path)
	code, body := s.code, s.body
	s.mu.Unlock()

	want := map[string]string{"/mute": http.MethodPost, "/unmute": http.MethodPost, "/status": http.MethodGet}[r.URL.Path]
	if want == "" || r.Method != want {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if code == 0 {
		code = http.StatusOK
	}
	if body == "" {
		body = `{"streaming":true,"muted":false,"uptime":"4h12m30s","restarts":2}`
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

func (s *recordingStreamer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func newRecordingAPI(t *testing.T, now time.Time, up *recordingStreamer) http.Handler {
	t.Helper()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	return NewAPI(NewStreamerClient(srv.URL, 2*time.Second), newTestAuth(now), "/webcam").Handler()
}

// A signed GET /status is forwarded as GET /status and the streamer's
// state comes back unchanged.
func TestStatusForwardsStreamerState(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	up := &recordingStreamer{}
	h := newRecordingAPI(t, now, up)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, http.MethodGet, "/webcam/status", now, "s1", testSecret))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var st StreamerStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := StreamerStatus{Streaming: true, Muted: false, Uptime: "4h12m30s", Restarts: 2}
	if st != want {
		t.Errorf("status = %+v, want %+v", st, want)
	}
	if got := up.requests(); len(got) != 1 || got[0] != "GET /status" {
		t.Errorf("streamer saw %v, want [GET /status]", got)
	}
}

// Each action uses the method the streamer requires (a mismatch would be a
// 405 there, a 502 here).
func TestControllerUsesStreamerMethods(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	up := &recordingStreamer{}
	h := newRecordingAPI(t, now, up)

	for i, tc := range []struct{ method, path string }{
		{http.MethodPost, "/webcam/mute"},
		{http.MethodPost, "/webcam/unmute"},
		{http.MethodGet, "/webcam/status"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, signedRequest(t, tc.method, tc.path, now, "m"+string(rune('a'+i)), testSecret))
		if rec.Code != http.StatusOK {
			t.Errorf("%s %s = %d, body = %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}
	want := []string{"POST /mute", "POST /unmute", "GET /status"}
	got := up.requests()
	if len(got) != len(want) {
		t.Fatalf("streamer saw %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Anything but a well-formed 200 from the streamer is a bad gateway, and the
// streamer's own error text isn't passed through to the caller.
func TestStreamerFailuresBecome502(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for name, up := range map[string]*recordingStreamer{
		"streamer error":    {code: http.StatusInternalServerError, body: `{"error":"failed to change mute state"}`},
		"malformed payload": {body: `not json`},
	} {
		t.Run(name, func(t *testing.T) {
			h := newRecordingAPI(t, now, up)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, signedRequest(t, http.MethodGet, "/webcam/status", now, "f-"+name, testSecret))
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502 (body %s)", rec.Code, rec.Body)
			}
			var body map[string]string
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if body["error"] != "streamer unavailable" {
				t.Errorf("error = %q, want the generic \"streamer unavailable\"", body["error"])
			}
		})
	}
}

// The sweeper evicts expired nonces on its own and stops when told to.
func TestNonceCacheSweeperRunsAndStops(t *testing.T) {
	c := NewNonceCache(50 * time.Millisecond)
	stop := make(chan struct{})
	c.StartSweeper(stop)

	c.Add("a", time.Now())
	c.Add("b", time.Now())

	deadline := time.Now().Add(2 * time.Second)
	for c.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := c.Len(); n != 0 {
		t.Fatalf("cache still holds %d expired nonces", n)
	}

	close(stop)
	time.Sleep(60 * time.Millisecond)      // let the sweeper see stop
	c.Add("c", time.Now().Add(-time.Hour)) // already expired
	time.Sleep(150 * time.Millisecond)     // several ticks' worth
	if c.Len() != 1 {
		t.Error("sweeper kept running after stop was closed")
	}
}

// Logs name the original client: the first X-Forwarded-For entry, else the
// connection's address.
func TestClientIP(t *testing.T) {
	for _, tc := range []struct{ xff, remote, want string }{
		{"", "10.1.2.3:5555", "10.1.2.3:5555"},
		{"203.0.113.9", "10.1.2.3:5555", "203.0.113.9"},
		{"203.0.113.9, 10.42.0.1", "10.1.2.3:5555", "203.0.113.9"},
		{" 198.51.100.4 ,10.42.0.1", "10.1.2.3:5555", "198.51.100.4"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/status", nil)
		r.RemoteAddr = tc.remote
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := clientIP(r); got != tc.want {
			t.Errorf("clientIP(xff=%q) = %q, want %q", tc.xff, got, tc.want)
		}
	}
}
