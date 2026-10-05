package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// StreamerClient calls the streamer's internal API in the same pod.
type StreamerClient struct {
	baseURL string
	http    *http.Client
}

func NewStreamerClient(baseURL string, timeout time.Duration) *StreamerClient {
	return &StreamerClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
	}
}

// StreamerStatus mirrors the streamer's status payload.
type StreamerStatus struct {
	Streaming bool   `json:"streaming"`
	Muted     bool   `json:"muted"`
	Uptime    string `json:"uptime,omitempty"`
	Restarts  int64  `json:"restarts"`
}

func (c *StreamerClient) call(ctx context.Context, method, path string) (*StreamerStatus, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("streamer returned %s for %s", resp.Status, path)
	}

	var st StreamerStatus
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&st); err != nil {
		return nil, fmt.Errorf("decode streamer response: %w", err)
	}
	return &st, nil
}

func (c *StreamerClient) Mute(ctx context.Context) (*StreamerStatus, error) {
	return c.call(ctx, http.MethodPost, "/mute")
}

func (c *StreamerClient) Unmute(ctx context.Context) (*StreamerStatus, error) {
	return c.call(ctx, http.MethodPost, "/unmute")
}

func (c *StreamerClient) Status(ctx context.Context) (*StreamerStatus, error) {
	return c.call(ctx, http.MethodGet, "/status")
}
