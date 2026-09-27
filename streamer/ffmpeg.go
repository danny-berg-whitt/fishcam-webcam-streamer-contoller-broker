package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Supervisor runs ffmpeg and restarts it with exponential backoff when it
// exits. It exposes just enough state for the status endpoint.
type Supervisor struct {
	cfg *Config

	mu      sync.Mutex
	cmd     *exec.Cmd
	started time.Time

	running  atomic.Bool
	restarts atomic.Int64
}

func NewSupervisor(cfg *Config) *Supervisor {
	return &Supervisor{cfg: cfg}
}

// Run supervises ffmpeg until ctx is cancelled. It returns after the
// current ffmpeg process (if any) has been shut down.
func (s *Supervisor) Run(ctx context.Context) {
	backoff := s.cfg.InitialBackoff
	for {
		if ctx.Err() != nil {
			return
		}

		start := time.Now()
		err := s.runOnce(ctx)
		uptime := time.Since(start)

		if ctx.Err() != nil {
			return
		}

		if uptime >= s.cfg.StableAfter {
			backoff = s.cfg.InitialBackoff
		}
		log.Printf("ffmpeg exited after %s (err=%v); restarting in %s", uptime.Round(time.Second), err, backoff)
		s.restarts.Add(1)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > s.cfg.MaxBackoff {
			backoff = s.cfg.MaxBackoff
		}
	}
}

func (s *Supervisor) runOnce(ctx context.Context) error {
	cmd := exec.Command(s.cfg.FFmpegPath, s.cfg.FFmpegArgs()...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Printf("DEBUG: s.cfg.FFmpegPath=%s, s.cfg.FFmpegArgs()=%s", 
				s.cfg.FFmpegPath, s.cfg.FFmpegArgs())

	s.mu.Lock()
	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.cmd = cmd
	s.started = time.Now()
	s.mu.Unlock()

	s.running.Store(true)
	defer s.running.Store(false)

	log.Printf("ffmpeg started (pid %d) -> %s", cmd.Process.Pid, s.cfg.RTMPURL)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// Ask ffmpeg to finish cleanly so the RTMP session closes,
		// then force-kill if it lingers.
		_ = cmd.Process.Signal(syscall.SIGINT)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		return context.Canceled
	}
}

// Status is a point-in-time snapshot for the API.
type Status struct {
	Streaming bool   `json:"streaming"`
	Muted     bool   `json:"muted"`
	Uptime    string `json:"uptime,omitempty"`
	Restarts  int64  `json:"restarts"`
}

func (s *Supervisor) Snapshot(muted bool) Status {
	st := Status{
		Streaming: s.running.Load(),
		Muted:     muted,
		Restarts:  s.restarts.Load(),
	}
	if st.Streaming {
		s.mu.Lock()
		started := s.started
		s.mu.Unlock()
		st.Uptime = time.Since(started).Round(time.Second).String()
	}
	return st
}

var errNotRunning = errors.New("ffmpeg is not running")
