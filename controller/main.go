// Command controller verifies HMAC-signed mute, unmute and status requests
// (see auth.go) and forwards them to the streamer over pod loopback.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[controller] ")

	secret := os.Getenv("HMAC_SECRET")
	if secret == "" {
		log.Fatal("HMAC_SECRET is required")
	}
	if len(secret) < 32 {
		log.Fatalf("HMAC_SECRET is too short (%d chars); use at least 32, e.g. openssl rand -hex 32", len(secret))
	}

	listenAddr := envStr("LISTEN_ADDR", ":8080")
	streamerURL := envStr("STREAMER_URL", "http://127.0.0.1:8081")

	skew, err := time.ParseDuration(envStr("AUTH_MAX_SKEW", "30s"))
	if err != nil {
		log.Fatalf("AUTH_MAX_SKEW: %v", err)
	}
	timeout, err := time.ParseDuration(envStr("STREAMER_TIMEOUT", "5s"))
	if err != nil {
		log.Fatalf("STREAMER_TIMEOUT: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Twice the skew window: no still-acceptable timestamp can be replayed.
	nonces := NewNonceCache(2 * skew)
	sweeperStop := make(chan struct{})
	nonces.StartSweeper(sweeperStop)
	defer close(sweeperStop)

	// e.g. "/webcam". Signatures cover the full prefixed path.
	prefix := NormalizePrefix(envStr("ROUTE_PREFIX", ""))

	auth := NewAuthenticator([]byte(secret), skew, nonces)
	api := NewAPI(NewStreamerClient(streamerURL, timeout), auth, prefix)

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("listening on %s%s (streamer=%s, skew=%s)", listenAddr, prefix, streamerURL, skew)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server error: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Print("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	log.Print("stopped")
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
