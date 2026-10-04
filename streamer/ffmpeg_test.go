package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeFFmpeg writes a shell script that stands in for ffmpeg. Each run
// appends "start <unix nanos>" and its arguments to the returned log file,
// then runs body.
func fakeFFmpeg(t *testing.T, body string) (path, logFile string) {
	t.Helper()
	dir := t.TempDir()
	logFile = filepath.Join(dir, "runs.log")
	path = filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\n" +
		"echo \"start $(date +%s%N)\" >> '" + logFile + "'\n" +
		"echo \"args $*\" >> '" + logFile + "'\n" +
		body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, logFile
}

// starts returns the start times recorded in a fakeFFmpeg log.
func starts(t *testing.T, logFile string) []time.Time {
	t.Helper()
	data, _ := os.ReadFile(logFile)
	var out []time.Time
	for _, line := range strings.Split(string(data), "\n") {
		if ns, ok := strings.CutPrefix(line, "start "); ok {
			n, err := strconv.ParseInt(ns, 10, 64)
			if err != nil {
				t.Fatalf("bad start line %q", line)
			}
			out = append(out, time.Unix(0, n))
		}
	}
	return out
}

func supervisorConfig(ffmpeg string, initial, max, stable time.Duration) *Config {
	return &Config{
		FFmpegPath: ffmpeg, LogLevel: "error",
		VideoDevice: "/dev/video9", AudioDevice: "plughw:CARD=Webcam,DEV=0",
		InputFormat: "mjpeg", VideoSize: "1280x720", Framerate: 10, GOP: 5,
		VideoCodec: "libx264", PixelFormat: "yuv420p", VideoBitrate: "2M", BufSize: "4M",
		AudioSampleRate: 44100, AudioBitrate: "128k",
		RTMPURL:        "rtmp://example.invalid/live/stream",
		InitialBackoff: initial, MaxBackoff: max, StableAfter: stable,
	}
}

// runFor runs the supervisor until it has started ffmpeg n times (or the
// deadline passes), then cancels it and waits for Run to return.
func runFor(t *testing.T, sup *Supervisor, logFile string, n int, deadline time.Duration) []time.Time {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()

	limit := time.Now().Add(deadline)
	for len(starts(t, logFile)) < n && time.Now().Before(limit) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	got := starts(t, logFile)
	if len(got) < n {
		t.Fatalf("ffmpeg started %d times within %s, want at least %d", len(got), deadline, n)
	}
	return got
}

func gaps(ts []time.Time) []time.Duration {
	var out []time.Duration
	for i := 1; i < len(ts); i++ {
		out = append(out, ts[i].Sub(ts[i-1]))
	}
	return out
}

// An ffmpeg that keeps dying is restarted with a delay that doubles each time
// up to the maximum: 100ms, 200ms, 400ms, 400ms here.
func TestSupervisorBacksOffExponentially(t *testing.T) {
	ffmpeg, logFile := fakeFFmpeg(t, "exit 1")
	sup := NewSupervisor(supervisorConfig(ffmpeg, 100*time.Millisecond, 400*time.Millisecond, time.Hour))

	g := gaps(runFor(t, sup, logFile, 5, 10*time.Second))
	want := []time.Duration{100, 200, 400, 400}
	for i, w := range want {
		w *= time.Millisecond
		// Lower bound is strict (the delay must be waited out); the upper
		// bound allows for process start-up time on a busy CI runner.
		if g[i] < w || g[i] > w+300*time.Millisecond {
			t.Errorf("gap %d = %s, want about %s (all gaps %v)", i+1, g[i], w, g)
		}
	}
	if r := sup.restarts.Load(); r < 4 {
		t.Errorf("restarts = %d, want at least 4", r)
	}
}

// Once ffmpeg has stayed up for StableAfter, the next restart starts again
// from the initial delay instead of continuing to double.
func TestSupervisorResetsBackoffAfterStableRun(t *testing.T) {
	// Each run lasts 150ms, past StableAfter (100ms), then fails.
	ffmpeg, logFile := fakeFFmpeg(t, "sleep 0.15; exit 1")
	sup := NewSupervisor(supervisorConfig(ffmpeg, 100*time.Millisecond, 5*time.Second, 100*time.Millisecond))

	// Without the reset the gaps would be 250ms, 350ms, 550ms, 950ms.
	for i, g := range gaps(runFor(t, sup, logFile, 5, 10*time.Second)) {
		if g > 500*time.Millisecond {
			t.Errorf("gap %d = %s; backoff kept growing despite stable runs", i+1, g)
		}
	}
}

// ffmpeg gets the pipeline the config describes, not a stale or partial one.
func TestSupervisorPassesFFmpegArgs(t *testing.T) {
	ffmpeg, logFile := fakeFFmpeg(t, "exit 1")
	cfg := supervisorConfig(ffmpeg, time.Hour, time.Hour, time.Hour)
	runFor(t, NewSupervisor(cfg), logFile, 1, 5*time.Second)

	data, _ := os.ReadFile(logFile)
	want := "args " + strings.Join(cfg.FFmpegArgs(), " ")
	if !strings.Contains(string(data), want+"\n") {
		t.Fatalf("ffmpeg was run with\n%s\nwant\n%s", data, want)
	}
}

// While ffmpeg runs, the status shows it streaming with an uptime. Cancelling
// asks ffmpeg to stop with SIGINT (so the RTMP session closes cleanly) and
// Run returns once it has.
func TestSupervisorRunningStateAndCleanShutdown(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "got-sigint")
	ffmpeg, logFile := fakeFFmpeg(t,
		"trap 'echo yes > \""+marker+"\"; exit 0' INT\n"+
			"while :; do sleep 0.05; done")
	sup := NewSupervisor(supervisorConfig(ffmpeg, time.Second, time.Second, time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()

	limit := time.Now().Add(5 * time.Second)
	for !sup.running.Load() && time.Now().Before(limit) {
		time.Sleep(10 * time.Millisecond)
	}
	if st := sup.Snapshot(false); !st.Streaming || st.Uptime == "" || st.Restarts != 0 {
		t.Fatalf("while running: %+v, want streaming with an uptime and no restarts", st)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(4 * time.Second): // well under the 5s force-kill
		t.Fatal("Run did not return promptly after cancel")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("ffmpeg was not sent SIGINT on shutdown")
	}
	if st := sup.Snapshot(false); st.Streaming || st.Uptime != "" {
		t.Errorf("after shutdown: %+v, want not streaming", st)
	}
	if n := len(starts(t, logFile)); n != 1 {
		t.Errorf("ffmpeg started %d times, want 1 (no restart on shutdown)", n)
	}
}

// A missing ffmpeg binary is a failed start, retried like any other failure,
// and never reported as streaming.
func TestSupervisorRetriesWhenFFmpegCannotStart(t *testing.T) {
	cfg := supervisorConfig(filepath.Join(t.TempDir(), "no-such-ffmpeg"),
		20*time.Millisecond, 20*time.Millisecond, time.Hour)
	sup := NewSupervisor(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	sup.Run(ctx)

	if r := sup.restarts.Load(); r < 3 {
		t.Errorf("restarts = %d after 300ms of failed starts, want several", r)
	}
	if sup.running.Load() {
		t.Error("reported running although ffmpeg never started")
	}
}
