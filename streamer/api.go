package main

import (
	"encoding/json"
	"log"
	"net/http"
)

// API is the streamer's internal control surface. It is bound to the pod
// network only (localhost within the pod) and carries no authentication —
// the Controller is responsible for authenticating external callers.
type API struct {
	sup   *Supervisor
	muter *Muter
}

func NewAPI(sup *Supervisor, muter *Muter) *API {
	return &API{sup: sup, muter: muter}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mute", a.setMute(true))
	mux.HandleFunc("/unmute", a.setMute(false))
	mux.HandleFunc("/status", a.status)
	mux.HandleFunc("/healthz", a.healthz)
	return mux
}

func (a *API) setMute(mute bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if err := a.muter.Set(mute); err != nil {
			log.Printf("mute error: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "failed to change mute state")
			return
		}
		writeJSON(w, http.StatusOK, a.sup.Snapshot(a.muter.Muted()))
	}
}

func (a *API) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, a.sup.Snapshot(a.muter.Muted()))
}

// healthz reports unhealthy while ffmpeg is not running, so Kubernetes can
// restart the pod if the supervisor can never get a working pipeline up.
func (a *API) healthz(w http.ResponseWriter, r *http.Request) {
	if !a.sup.running.Load() {
		writeJSONError(w, http.StatusServiceUnavailable, errNotRunning.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
