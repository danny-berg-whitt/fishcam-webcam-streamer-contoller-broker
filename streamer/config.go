package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
)

// autoValue marks a setting the streamer should discover at startup rather
// than be told. Empty means the same thing.
const autoValue = "auto"

// Config holds all streamer settings, populated from environment variables
// so the same image can be reconfigured from the Kubernetes manifest.
type Config struct {
	// Media pipeline
	RTMPURL     string // where ffmpeg publishes the stream
	VideoDevice string // V4L2 device node, or "auto"
	InputFormat string // pixel format requested from the camera (C922: mjpeg)
	VideoSize   string // WxH
	Framerate   int

	VideoCodec   string // libx264 on the Pi 5; h264_v4l2m2m where hardware exists
	Preset       string // libx264 only — omitted when empty
	Tune         string // libx264 only — omitted when empty
	PixelFormat  string
	VideoBitrate string
	BufSize      string // rate-control buffer; typically 2x bitrate
	GOP          int    // keyframe interval in frames; 0 = twice the framerate

	AudioDevice     string // ALSA device for ffmpeg, or "auto"
	AudioSampleRate int
	AudioBitrate    string

	// Mute control
	AlsaCard    string // card id passed to amixer -c, or "auto"
	CardMatch   string // substring narrowing auto-detection when several cards match
	MuteControl string // simple mixer control toggled cap/nocap
	StartMuted  bool

	// Supervisor
	FFmpegPath     string
	LogLevel       string // ffmpeg -loglevel; "info" shows the stream mapping
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	StableAfter    time.Duration // uptime after which backoff resets

	// API
	ListenAddr string

	// Discovery roots, overridable for testing.
	ProcAsound string
	DevDir     string
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envStrAllowEmpty distinguishes "not set" from "set to empty", which matters
// wherever the empty string is a meaningful choice rather than an absence —
// PRESET and TUNE, where it means "omit this flag". Plain envStr cannot
// express that, since a ConfigMap value of "" is indistinguishable from an
// unset variable to os.Getenv.
func envStrAllowEmpty(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envBool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

// LoadConfig reads configuration from the environment. Device-specific
// settings default to "auto" so one ConfigMap can serve hosts with different
// webcams; see Resolve.
func LoadConfig() (*Config, error) {
	c := &Config{
		RTMPURL:      envStr("RTMP_URL", "rtmp://hls-service.default.svc.cluster.local/live/stream"),
		VideoDevice:  envStr("VIDEO_DEVICE", autoValue),
		InputFormat:  envStr("INPUT_FORMAT", "mjpeg"),
		VideoSize:    envStr("VIDEO_SIZE", "1280x720"),
		VideoCodec:   envStr("VIDEO_CODEC", "libx264"),
		Preset:       envStrAllowEmpty("PRESET", "veryfast"),
		Tune:         envStrAllowEmpty("TUNE", "zerolatency"),
		PixelFormat:  envStr("PIXEL_FORMAT", "yuv420p"),
		VideoBitrate: envStr("VIDEO_BITRATE", "2M"),
		BufSize:      envStr("BUFSIZE", "4M"),
		AudioDevice:  envStr("AUDIO_DEVICE", autoValue),
		AudioBitrate: envStr("AUDIO_BITRATE", "128k"),
		AlsaCard:     envStr("ALSA_CARD", autoValue),
		CardMatch:    envStr("ALSA_CARD_MATCH", ""),
		MuteControl:  envStr("MUTE_CONTROL", autoValue),
		FFmpegPath:   envStr("FFMPEG_PATH", "ffmpeg"),
		LogLevel:     envStr("FFMPEG_LOGLEVEL", "info"),
		ListenAddr:   envStr("LISTEN_ADDR", ":8081"),
		ProcAsound:   envStr("PROC_ASOUND", "/proc/asound"),
		DevDir:       envStr("DEV_DIR", "/dev"),
	}

	var err error
	if c.Framerate, err = envInt("FRAMERATE", 10); err != nil {
		return nil, err
	}
	if c.GOP, err = envInt("GOP", 0); err != nil {
		return nil, err
	}
	if c.GOP == 0 {
		// nginx-rtmp can only cut an HLS segment at a keyframe, so the
		// keyframe interval — not hls_fragment — sets the floor on segment
		// duration and therefore on playback latency. Twice the framerate is
		// a 2s keyframe interval: the conventional HLS default, safe with any
		// server's segment length and cheap on bitrate. Set GOP explicitly to
		// trade bitrate for latency — the ConfigMap ships 5, giving 0.5s
		// keyframes to match this cluster's hls_fragment 500ms.
		c.GOP = 2 * c.Framerate
		if c.GOP < 1 {
			c.GOP = 1
		}
	}
	if c.AudioSampleRate, err = envInt("AUDIO_SAMPLE_RATE", 44100); err != nil {
		return nil, err
	}
	if c.StartMuted, err = envBool("START_MUTED", false); err != nil {
		return nil, err
	}
	if c.InitialBackoff, err = envDuration("RESTART_INITIAL_BACKOFF", time.Second); err != nil {
		return nil, err
	}
	if c.MaxBackoff, err = envDuration("RESTART_MAX_BACKOFF", 30*time.Second); err != nil {
		return nil, err
	}
	if c.StableAfter, err = envDuration("RESTART_STABLE_AFTER", time.Minute); err != nil {
		return nil, err
	}
	return c, nil
}

func isAuto(v string) bool { return v == "" || v == autoValue }

// Resolve fills in whatever was left on "auto" by inspecting the host.
//
// The ALSA card is the setting that actually differs between machines: the
// id is derived from the webcam's USB product string, not chosen for its
// role (a C922 Pro Stream, "C922 Pro Stream Webcam", answers to "Webcam",
// not "C922"). Detecting it means the same ConfigMap works across cameras.
// An explicit value always wins.
func (c *Config) Resolve() error {
	if isAuto(c.AlsaCard) {
		card, err := DetectCaptureCard(c.ProcAsound, c.CardMatch)
		if err != nil {
			return fmt.Errorf("detect capture card: %w", err)
		}
		c.AlsaCard = card.ID
		log.Printf("detected capture card %s", card)
	}

	// The mixer wants the card id; ffmpeg wants a full ALSA device string.
	// Deriving the second from the first keeps them from drifting apart.
	if isAuto(c.AudioDevice) {
		c.AudioDevice = fmt.Sprintf("plughw:CARD=%s,DEV=0", c.AlsaCard)
	}

	if isAuto(c.VideoDevice) {
		dev, err := DetectVideoDevice(c.DevDir)
		if err != nil {
			return fmt.Errorf("detect video device: %w", err)
		}
		c.VideoDevice = dev
		log.Printf("detected video device %s", dev)
	}
	return nil
}

// FFmpegArgs builds the ffmpeg argument list for the configured pipeline:
// V4L2 + ALSA capture, H.264 encode, FLV mux, RTMP publish. Call after
// Resolve.
func (c *Config) FFmpegArgs() []string {
	args := []string{
		"-hide_banner",
		"-nostdin",
		// Suppress the continuously-rewritten "frame= ... fps= ..." progress
		// line. It is written to stderr whether or not stderr is a terminal,
		// so at -loglevel info it would otherwise flood the pod log and bury
		// the startup block that makes info level worth having.
		"-nostats",
		"-loglevel", c.LogLevel,

		// Video input
		"-f", "v4l2",
		"-input_format", c.InputFormat,
		"-video_size", c.VideoSize,
		"-framerate", strconv.Itoa(c.Framerate),
		"-thread_queue_size", "4096",
		"-i", c.VideoDevice,

		// Audio input
		"-f", "alsa",
		"-thread_queue_size", "4096",
		"-i", c.AudioDevice,

		"-map", "0:v:0",
		"-map", "1:a:0",

		// Video encode
		"-c:v", c.VideoCodec,
	}

	// -preset and -tune are libx264 options. Hardware encoders — notably
	// h264_v4l2m2m on the Pi 4 — reject them outright, so both are omitted
	// when set to the empty string.
	if c.Preset != "" {
		args = append(args, "-preset", c.Preset)
	}
	if c.Tune != "" {
		args = append(args, "-tune", c.Tune)
	}

	return append(args,
		"-pix_fmt", c.PixelFormat,
		"-b:v", c.VideoBitrate,
		"-maxrate", c.VideoBitrate,
		"-bufsize", c.BufSize,
		"-g", strconv.Itoa(c.GOP),
		"-keyint_min", strconv.Itoa(c.GOP),

		// Audio encode
		"-c:a", "aac",
		"-ar", strconv.Itoa(c.AudioSampleRate),
		"-b:a", c.AudioBitrate,

		// Output
		"-f", "flv",
		c.RTMPURL,
	)
}
