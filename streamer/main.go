// Command streamer captures audio and video from a USB webcam and publishes
// an H.264/AAC stream to an RTMP server. It supervises the ffmpeg process
// and exposes a small internal HTTP API for microphone mute control.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[streamer] ")

	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	// Discover whatever was left on "auto". Failing here is fatal by design:
	// without a camera or a microphone there is nothing to supervise, and a
	// clear message at startup beats ffmpeg failing in a restart loop.
	if err := cfg.Resolve(); err != nil {
		log.Fatalf("device discovery failed: %v", err)
	}
	log.Printf("video=%s audio=%s card=%s codec=%s",
		cfg.VideoDevice, cfg.AudioDevice, cfg.AlsaCard, cfg.VideoCodec)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The mute control name is as device-specific as the card id, but unlike
	// the card it is not needed to stream — so a failure here is a warning,
	// not a fatal error. Video and audio keep flowing; only /mute is lost.
	if isAuto(cfg.MuteControl) {
		if ctrl, err := DetectMuteControl(cfg.AlsaCard); err != nil {
			log.Printf("warning: no mute control detected on card %s: %v", cfg.AlsaCard, err)
		} else {
			cfg.MuteControl = ctrl
			log.Printf("detected mute control %q on card %s", ctrl, cfg.AlsaCard)
		}
	}

	muter := NewMuter(cfg.AlsaCard, cfg.MuteControl)
	if err := muter.Set(cfg.StartMuted); err != nil {
		// A missing mixer control should not stop the video stream; log and
		// carry on so the API can report the failure later.
		log.Printf("warning: could not set initial mute state: %v", err)
	}

	sup := NewSupervisor(cfg)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sup.Run(ctx)
	}()

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           NewAPI(sup, muter).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("control API listening on %s", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("control API error: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Print("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("control API shutdown: %v", err)
	}

	wg.Wait()
	log.Print("stopped")
}
