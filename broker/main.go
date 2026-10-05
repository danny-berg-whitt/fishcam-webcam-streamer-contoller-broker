// Command broker is the public entry point. It authenticates callers by
// per-user bearer token and forwards each request to the controller with an
// HMAC signature it computes itself, so callers never hold HMAC_SECRET.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("[broker] config error: %v", err)
	}

	users, err := loadUserTokens(cfg.TokensFile)
	if err != nil {
		log.Fatalf("[broker] failed to load user tokens: %v", err)
	}
	log.Printf("[broker] loaded %d user token(s) from %s", len(users), cfg.TokensFile)

	mux := newMux(cfg, users)

	log.Printf("[broker] listening on %s, prefix %q, forwarding to %s", cfg.ListenAddr, cfg.RoutePrefix, cfg.ControllerURL)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, mux))
}

// newMux is separate from main so tests exercise the production routing.
func newMux(cfg *config, users map[string]string) *http.ServeMux {
	h := &handler{cfg: cfg, users: users, client: &http.Client{Timeout: cfg.UpstreamTimeout}}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.handleHealthz) // unauthenticated and never prefixed: probed by the kubelet
	mux.HandleFunc(cfg.RoutePrefix+"/mute", h.authenticated(h.proxyAction("mute", http.MethodPost)))
	mux.HandleFunc(cfg.RoutePrefix+"/unmute", h.authenticated(h.proxyAction("unmute", http.MethodPost)))
	mux.HandleFunc(cfg.RoutePrefix+"/status", h.authenticated(h.proxyAction("status", http.MethodGet)))
	return mux
}

type config struct {
	ListenAddr      string
	ControllerURL   string // pod loopback
	RoutePrefix     string // must equal the controller's ROUTE_PREFIX
	HMACSecret      string // must equal the controller's HMAC_SECRET
	TokensFile      string
	UpstreamTimeout time.Duration
}

func loadConfig() (*config, error) {
	cfg := &config{
		ListenAddr:    getenv("LISTEN_ADDR", ":8082"),
		ControllerURL: getenv("CONTROLLER_URL", "http://127.0.0.1:8080"),
		RoutePrefix:   getenv("ROUTE_PREFIX", ""),
		HMACSecret:    os.Getenv("HMAC_SECRET"),
		TokensFile:    getenv("TOKENS_FILE", "/etc/broker/tokens.json"),
	}

	if cfg.HMACSecret == "" || len(cfg.HMACSecret) < 32 {
		return nil, fmt.Errorf("HMAC_SECRET is required and must be at least 32 characters")
	}

	timeoutStr := getenv("UPSTREAM_TIMEOUT", "5s")
	d, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return nil, fmt.Errorf("invalid UPSTREAM_TIMEOUT %q: %w", timeoutStr, err)
	}
	cfg.UpstreamTimeout = d

	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadUserTokens reads {"<hex sha256(token)>": "<name>", ...}. Only hashes
// are stored, so the file is not itself a usable credential.
func loadUserTokens(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var users map[string]string
	if err := json.Unmarshal(data, &users); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if len(users) == 0 {
		return nil, fmt.Errorf("%s contains no users", path)
	}
	return users, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type handler struct {
	cfg    *config
	users  map[string]string // hex sha256(token) -> name
	client *http.Client
}

func (h *handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

type userCtxKey struct{}

// authenticated resolves the bearer token to a user name for the context
// and the log. There is no replay protection here; the controller enforces
// it on the signed hop.
func (h *handler) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(authHeader, prefix) {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(authHeader, prefix))
		if token == "" {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}

		hashed := hashToken(token)
		var username string
		var ok bool
		// Constant-time, so timing doesn't reveal a partial match.
		for storedHash, name := range h.users {
			if subtle.ConstantTimeCompare([]byte(hashed), []byte(storedHash)) == 1 {
				username, ok = name, true
				break
			}
		}
		if !ok {
			log.Printf("[broker] rejected: unknown token from %s", r.RemoteAddr)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		log.Printf("[broker] %s authenticated for %s %s", username, r.Method, r.URL.Path)
		ctx := withUser(r.Context(), username)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// proxyAction signs and forwards one controller action and relays the
// response unchanged.
func (h *handler) proxyAction(action, method string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The signed request always uses `method`, so without this check an
		// authenticated GET /mute (a link preview, say) would mute the mic.
		if r.Method != method {
			w.Header().Set("Allow", method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		username, _ := userFromContext(r.Context())
		path := h.cfg.RoutePrefix + "/" + action

		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		nonce, err := randomHex(8)
		if err != nil {
			log.Printf("[broker] nonce generation failed: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		signature := sign(h.cfg.HMACSecret, method, path, timestamp, nonce)

		upstreamURL := h.cfg.ControllerURL + path
		req, err := http.NewRequest(method, upstreamURL, nil)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		req.Header.Set("X-Auth-Timestamp", timestamp)
		req.Header.Set("X-Auth-Nonce", nonce)
		req.Header.Set("Authorization", "HMAC "+signature)

		resp, err := h.client.Do(req)
		if err != nil {
			log.Printf("[broker] upstream call to %s failed: %v", path, err)
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, "upstream read error", http.StatusBadGateway)
			return
		}

		log.Printf("[broker] %s -> %s %s -> %d", username, method, path, resp.StatusCode)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}
}

// sign must match the controller's verifier.
func sign(secret, method, path, timestamp, nonce string) string {
	message := strings.Join([]string{method, path, timestamp, nonce}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}
