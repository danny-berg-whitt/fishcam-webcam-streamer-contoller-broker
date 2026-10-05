package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// API exposes the public control surface.
type API struct {
	streamer *StreamerClient
	auth     *Authenticator
	prefix   string // e.g. "/webcam"; empty serves at the root
}

func NewAPI(streamer *StreamerClient, auth *Authenticator, prefix string) *API {
	return &API{streamer: streamer, auth: auth, prefix: NormalizePrefix(prefix)}
}

// NormalizePrefix accepts "webcam", "/webcam" or "/webcam/" and returns
// "/webcam"; an empty prefix serves at the root.
func NormalizePrefix(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return ""
	}
	return "/" + p
}

// Handler serves the prefixed paths itself, not behind a rewrite: the
// signature covers the path, so a rewrite would make the signed and verified
// paths differ and every request fail authentication.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle(a.prefix+"/mute", a.auth.Middleware(a.mute()))
	mux.Handle(a.prefix+"/unmute", a.auth.Middleware(a.unmute()))
	mux.Handle(a.prefix+"/status", a.auth.Middleware(a.status()))

	// Unauthenticated and never prefixed: probed by the kubelet directly.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	return logRequests(mux)
}

func (a *API) mute() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		st, err := a.streamer.Mute(r.Context())
		respond(w, st, err)
	})
}

func (a *API) unmute() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		st, err := a.streamer.Unmute(r.Context())
		respond(w, st, err)
	})
}

func (a *API) status() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		st, err := a.streamer.Status(r.Context())
		respond(w, st, err)
	})
}

func respond(w http.ResponseWriter, st *StreamerStatus, err error) {
	if err != nil {
		log.Printf("streamer call failed: %v", err)
		writeJSONError(w, http.StatusBadGateway, "streamer unavailable")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if r.URL.Path != "/healthz" {
			log.Printf("%s %s from %s", r.Method, r.URL.Path, clientIP(r))
		}
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
