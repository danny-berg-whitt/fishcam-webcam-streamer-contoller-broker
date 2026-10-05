// Command streamer supervises an ffmpeg process that publishes a USB
// webcam's audio and video over RTMP, and serves an internal API for muting
// the microphone.
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

	// Fatal by design: a clear message now beats ffmpeg in a restart loop.
	if err := cfg.Resolve(); err != nil {
		log.Fatalf("device discovery failed: %v", err)
	}
	log.Printf("video=%s audio=%s card=%s codec=%s",
		cfg.VideoDevice, cfg.AudioDevice, cfg.AlsaCard, cfg.VideoCodec)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Not fatal: streaming doesn't need the mute control, only /mute does.
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
