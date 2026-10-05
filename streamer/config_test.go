package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Framerate != 10 {
		t.Errorf("Framerate = %d, want 10", cfg.Framerate)
	}
	// Unset GOP falls back to twice the framerate (a 2s keyframe interval).
	if cfg.GOP != 20 {
		t.Errorf("GOP = %d, want 2*framerate = 20", cfg.GOP)
	}
	if !strings.HasPrefix(cfg.RTMPURL, "rtmp://") {
		t.Errorf("RTMPURL = %q, want an rtmp:// URL", cfg.RTMPURL)
	}
	// Device settings default to discovery so one ConfigMap serves any host.
	for _, f := range []struct{ name, got string }{
		{"AlsaCard", cfg.AlsaCard},
		{"AudioDevice", cfg.AudioDevice},
		{"VideoDevice", cfg.VideoDevice},
	} {
		if !isAuto(f.got) {
			t.Errorf("%s = %q, want auto", f.name, f.got)
		}
	}
}

func TestLoadConfigEnvOverrides(t *testing.T) {
	t.Setenv("FRAMERATE", "15")
	t.Setenv("GOP", "45")
	t.Setenv("VIDEO_SIZE", "640x480")
	t.Setenv("START_MUTED", "true")
	t.Setenv("RESTART_MAX_BACKOFF", "90s")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Framerate != 15 {
		t.Errorf("Framerate = %d, want 15", cfg.Framerate)
	}
	if cfg.GOP != 45 {
		t.Errorf("GOP = %d, want 45 (explicit value must not be overwritten)", cfg.GOP)
	}
	if cfg.VideoSize != "640x480" {
		t.Errorf("VideoSize = %q, want 640x480", cfg.VideoSize)
	}
	if !cfg.StartMuted {
		t.Error("StartMuted = false, want true")
	}
	if cfg.MaxBackoff.String() != "1m30s" {
		t.Errorf("MaxBackoff = %s, want 1m30s", cfg.MaxBackoff)
	}
}

func TestLoadConfigInvalidValues(t *testing.T) {
	t.Setenv("FRAMERATE", "not-a-number")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("LoadConfig succeeded with an invalid FRAMERATE, want error")
	}
}

// fakeHost writes a /proc/asound and /dev tree so Resolve can be exercised
// without a webcam.
func fakeHost(t *testing.T, cardsFile string, captureIdx []int, videoNodes []string) (procAsound, devDir string) {
	t.Helper()
	root := t.TempDir()

	procAsound = filepath.Join(root, "asound")
	if err := os.MkdirAll(procAsound, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procAsound, "cards"), []byte(cardsFile), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, i := range captureIdx {
		dir := filepath.Join(procAsound, "card"+string(rune('0'+i)))
		if err := os.MkdirAll(filepath.Join(dir, "pcm0c"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	devDir = filepath.Join(root, "dev")
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range videoNodes {
		if err := os.WriteFile(filepath.Join(devDir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return procAsound, devDir
}

// Identical config, two different cameras, each resolved correctly.
func TestResolveDiscoversCardPerHost(t *testing.T) {
	cases := []struct {
		name       string
		cards      string
		capture    []int
		wantCard   string
		wantDevice string
	}{
		{"Pi 5 with C922", pi5WithC922, []int{2}, "C922", "plughw:CARD=C922,DEV=0"},
		{"Pi 4b with C270", pi4WithC270, []int{1}, "Webcam", "plughw:CARD=Webcam,DEV=0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proc, dev := fakeHost(t, tc.cards, tc.capture, []string{"video0"})
			cfg, err := LoadConfig()
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			cfg.ProcAsound, cfg.DevDir = proc, dev

			if err := cfg.Resolve(); err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if cfg.AlsaCard != tc.wantCard {
				t.Errorf("AlsaCard = %q, want %q", cfg.AlsaCard, tc.wantCard)
			}
			if cfg.AudioDevice != tc.wantDevice {
				t.Errorf("AudioDevice = %q, want %q", cfg.AudioDevice, tc.wantDevice)
			}
			if cfg.VideoDevice != "/dev/video0" {
				t.Errorf("VideoDevice = %q, want /dev/video0", cfg.VideoDevice)
			}
		})
	}
}

// An explicit setting must survive discovery untouched.
func TestResolveKeepsExplicitValues(t *testing.T) {
	proc, dev := fakeHost(t, pi5WithC922, []int{2}, []string{"video0"})
	t.Setenv("ALSA_CARD", "Webcam")
	t.Setenv("AUDIO_DEVICE", "hw:1,0")
	t.Setenv("VIDEO_DEVICE", "/dev/video9")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cfg.ProcAsound, cfg.DevDir = proc, dev
	if err := cfg.Resolve(); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if cfg.AlsaCard != "Webcam" {
		t.Errorf("AlsaCard = %q, want the explicit Webcam", cfg.AlsaCard)
	}
	if cfg.AudioDevice != "hw:1,0" {
		t.Errorf("AudioDevice = %q, want the explicit hw:1,0", cfg.AudioDevice)
	}
	if cfg.VideoDevice != "/dev/video9" {
		t.Errorf("VideoDevice = %q, want the explicit /dev/video9", cfg.VideoDevice)
	}
}

func TestResolveFailsWithoutCaptureCard(t *testing.T) {
	proc, dev := fakeHost(t, pi5WithC922, nil, []string{"video0"})
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cfg.ProcAsound, cfg.DevDir = proc, dev
	if err := cfg.Resolve(); err == nil {
		t.Fatal("Resolve succeeded with no capture card, want error")
	}
}

func TestFFmpegArgs(t *testing.T) {
	t.Setenv("RTMP_URL", "rtmp://example.test/live/key")
	t.Setenv("VIDEO_DEVICE", "/dev/video2")
	t.Setenv("AUDIO_DEVICE", "hw:1,0")
	t.Setenv("ALSA_CARD", "C922")
	t.Setenv("FRAMERATE", "12")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	args := cfg.FFmpegArgs()

	if got := args[len(args)-1]; got != "rtmp://example.test/live/key" {
		t.Errorf("last arg = %q, want the RTMP URL", got)
	}

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-f v4l2", "-i /dev/video2", "-f alsa", "-i hw:1,0",
		"-framerate 12", "-g 24", "-c:a aac", "-f flv",
		"-preset veryfast", "-tune zerolatency",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q\ngot: %s", want, joined)
		}
	}

	var maps []string
	for i, a := range args {
		if a == "-map" && i+1 < len(args) {
			maps = append(maps, args[i+1])
		}
	}
	if len(maps) != 2 || maps[0] != "0:v:0" || maps[1] != "1:a:0" {
		t.Errorf("maps = %v, want [0:v:0 1:a:0]", maps)
	}
}

// Empty -preset and -tune must drop the flags, which hardware encoders reject.
func TestFFmpegArgsOmitsEmptyPresetAndTune(t *testing.T) {
	t.Setenv("VIDEO_CODEC", "h264_v4l2m2m")
	t.Setenv("PRESET", "")
	t.Setenv("TUNE", "")
	t.Setenv("VIDEO_DEVICE", "/dev/video0")
	t.Setenv("AUDIO_DEVICE", "hw:1,0")
	t.Setenv("ALSA_CARD", "Webcam")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// "" means "omit the flag", not "use the default".
	if cfg.Preset != "" || cfg.Tune != "" {
		t.Fatalf("empty PRESET/TUNE fell back to defaults: preset=%q tune=%q", cfg.Preset, cfg.Tune)
	}
	joined := strings.Join(cfg.FFmpegArgs(), " ")

	for _, unwanted := range []string{"-preset", "-tune"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("args contain %q with a hardware encoder\ngot: %s", unwanted, joined)
		}
	}
	if !strings.Contains(joined, "-c:v h264_v4l2m2m") {
		t.Errorf("args missing the configured codec\ngot: %s", joined)
	}
}
