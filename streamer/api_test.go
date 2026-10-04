package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// recordAmixer swaps in an amixer that records each call's arguments and
// returns err (nil for success).
func recordAmixer(t *testing.T, err error) *[]string {
	t.Helper()
	var calls []string
	orig := amixerOutput
	t.Cleanup(func() { amixerOutput = orig })
	amixerOutput = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if err != nil {
			return "amixer: Invalid card number", err
		}
		return "", nil
	}
	return &calls
}

// newStreamerAPI returns the streamer's API over a supervisor that isn't
// running anything; tests set its state directly.
func newStreamerAPI(t *testing.T, control string) (http.Handler, *Supervisor) {
	t.Helper()
	sup := NewSupervisor(&Config{})
	return NewAPI(sup, NewMuter("Webcam", control)).Handler(), sup
}

func serve(t *testing.T, h http.Handler, method, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s %s: Content-Type = %q, want application/json", method, path, ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s %s: body %q is not JSON: %v", method, path, rec.Body.String(), err)
	}
	return rec, body
}

func TestAPIMuteAndUnmuteDriveAmixer(t *testing.T) {
	calls := recordAmixer(t, nil)
	h, _ := newStreamerAPI(t, "Mic")

	rec, body := serve(t, h, http.MethodPost, "/mute")
	if rec.Code != http.StatusOK || body["muted"] != true {
		t.Fatalf("POST /mute: %d %v, want 200 with muted=true", rec.Code, body)
	}
	rec, body = serve(t, h, http.MethodPost, "/unmute")
	if rec.Code != http.StatusOK || body["muted"] != false {
		t.Fatalf("POST /unmute: %d %v, want 200 with muted=false", rec.Code, body)
	}

	want := []string{"-q -c Webcam set Mic nocap", "-q -c Webcam set Mic cap"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("amixer calls = %q, want %q", *calls, want)
	}
}

// A failed mixer call must surface as an error and leave the reported state
// unchanged, never claim a mute that didn't happen.
func TestAPIMuteFailureReports500(t *testing.T) {
	recordAmixer(t, errors.New("exit status 1"))
	h, _ := newStreamerAPI(t, "Mic")

	rec, body := serve(t, h, http.MethodPost, "/mute")
	if rec.Code != http.StatusInternalServerError || body["error"] == nil {
		t.Fatalf("POST /mute with failing amixer: %d %v, want 500 with an error", rec.Code, body)
	}
	_, body = serve(t, h, http.MethodGet, "/status")
	if body["muted"] != false {
		t.Fatalf("status after failed mute reports muted=%v, want false", body["muted"])
	}
}

func TestAPIMuteWithoutControlReports500(t *testing.T) {
	calls := recordAmixer(t, nil)
	h, _ := newStreamerAPI(t, autoValue)

	if rec, _ := serve(t, h, http.MethodPost, "/mute"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST /mute with no control: %d, want 500", rec.Code)
	}
	if len(*calls) != 0 {
		t.Fatalf("amixer was called without a control: %q", *calls)
	}
}

func TestAPIStatusReflectsSupervisor(t *testing.T) {
	recordAmixer(t, nil)
	h, sup := newStreamerAPI(t, "Mic")

	_, body := serve(t, h, http.MethodGet, "/status")
	if body["streaming"] != false || body["restarts"] != float64(0) {
		t.Fatalf("idle status = %v, want streaming=false restarts=0", body)
	}
	if _, ok := body["uptime"]; ok {
		t.Errorf("idle status has an uptime: %v", body)
	}

	sup.mu.Lock()
	sup.started = time.Now().Add(-90 * time.Second)
	sup.mu.Unlock()
	sup.running.Store(true)
	sup.restarts.Store(3)

	_, body = serve(t, h, http.MethodGet, "/status")
	if body["streaming"] != true || body["restarts"] != float64(3) || body["uptime"] != "1m30s" {
		t.Fatalf("running status = %v, want streaming=true restarts=3 uptime=1m30s", body)
	}
}

// healthz tracks whether ffmpeg is up, so a liveness probe can restart a pod
// whose pipeline never comes back.
func TestAPIHealthzTracksFFmpeg(t *testing.T) {
	h, sup := newStreamerAPI(t, "Mic")

	if rec, _ := serve(t, h, http.MethodGet, "/healthz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz with ffmpeg down: %d, want 503", rec.Code)
	}
	sup.running.Store(true)
	if rec, body := serve(t, h, http.MethodGet, "/healthz"); rec.Code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthz with ffmpeg up: %d %v, want 200 ok", rec.Code, body)
	}
}

func TestAPIRejectsWrongMethods(t *testing.T) {
	calls := recordAmixer(t, nil)
	h, _ := newStreamerAPI(t, "Mic")

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/mute"},
		{http.MethodGet, "/unmute"},
		{http.MethodPost, "/status"},
	} {
		if rec, _ := serve(t, h, tc.method, tc.path); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d, want 405", tc.method, tc.path, rec.Code)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("a rejected request still called amixer: %q", *calls)
	}
}
