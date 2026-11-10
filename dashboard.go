package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed dashboard/*
var dashboardFiles embed.FS

const dashboardCookie = "siwc_dashboard_session"
const dashboardHeader = "X-SIWC-Dashboard"

// Dashboard is local-only. A separate admin secret (not the Agent API key)
// exchanges for an HttpOnly browser session and is passed through a URL fragment
// during the initial local launch. Neither the OAuth access token nor admin key
// is placed in page markup, server logs or HTTP request URLs.
type Dashboard struct {
	server     *Server
	adminKey   string
	mu         sync.Mutex
	sessions   map[string]time.Time
	loginBusy  bool
	loginURL   string
	loginError string
}

func newDashboard(server *Server) *Dashboard {
	return &Dashboard{
		server:   server,
		adminKey: server.auth.store.get().AdminKey,
		sessions: make(map[string]time.Time),
	}
}

func (d *Dashboard) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
	mux.HandleFunc("POST /ui/api/session", d.exchange)
	mux.HandleFunc("POST /ui/api/session/logout", d.authorized(d.endSession))
	mux.HandleFunc("GET /ui/api/status", d.authorized(d.status))
	mux.HandleFunc("POST /ui/api/login", d.authorized(d.startLogin))
	mux.HandleFunc("POST /ui/api/logout", d.authorized(d.logout))
	mux.HandleFunc("POST /ui/api/service", d.authorized(d.setService))
	mux.HandleFunc("GET /ui/api/key", d.authorized(d.key))
	mux.HandleFunc("GET /ui/api/models", d.authorized(d.models))

	static, err := fs.Sub(dashboardFiles, "dashboard")
	if err != nil {
		panic("missing embedded dashboard assets")
	}
	files := http.StripPrefix("/ui/", http.FileServer(http.FS(static)))
	mux.Handle("GET /ui/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		files.ServeHTTP(w, r)
	}))
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func (d *Dashboard) checkRequest(w http.ResponseWriter, r *http.Request) bool {
	noStore(w)
	w.Header().Set("X-Frame-Options", "DENY")
	origin := r.Header.Get("Origin")
	if origin != "" && origin != "http://"+r.Host {
		writeError(w, 403, "invalid_origin", "Only this local dashboard origin is allowed")
		return false
	}
	if r.Method == http.MethodPost {
		if r.Header.Get(dashboardHeader) != "1" {
			writeError(w, 403, "csrf_blocked", "Missing dashboard request header")
			return false
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeError(w, 415, "invalid_content_type", "JSON request required")
			return false
		}
	}
	return true
}

func (d *Dashboard) exchange(w http.ResponseWriter, r *http.Request) {
	if !d.checkRequest(w, r) {
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, 400, "bad_request", "Invalid unlock request")
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Key), []byte(d.adminKey)) != 1 {
		writeError(w, 401, "unauthorized", "Incorrect dashboard unlock key")
		return
	}
	session, err := randomString(32)
	if err != nil {
		writeError(w, 500, "internal_error", "Failed to create session")
		return
	}
	expires := time.Now().Add(8 * time.Hour)
	d.mu.Lock()
	for key, ttl := range d.sessions {
		if time.Now().After(ttl) {
			delete(d.sessions, key)
		}
	}
	d.sessions[session] = expires
	d.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: dashboardCookie, Value: session, Path: "/ui/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: 8 * 60 * 60})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (d *Dashboard) authorized(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.checkRequest(w, r) {
			return
		}
		cookie, err := r.Cookie(dashboardCookie)
		if err != nil {
			writeError(w, 401, "unauthorized", "Open this dashboard from the local launcher")
			return
		}
		d.mu.Lock()
		exp, exists := d.sessions[cookie.Value]
		d.mu.Unlock()
		if !exists || time.Now().After(exp) {
			writeError(w, 401, "session_expired", "Dashboard session expired; reopen the launcher")
			return
		}
		next(w, r)
	}
}

func (d *Dashboard) endSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(dashboardCookie); err == nil {
		d.mu.Lock()
		delete(d.sessions, c.Value)
		d.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: dashboardCookie, Value: "", Path: "/ui/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (d *Dashboard) status(w http.ResponseWriter, r *http.Request) {
	state := d.server.auth.store.get()
	d.mu.Lock()
	busy, authURL, loginError := d.loginBusy, d.loginURL, d.loginError
	d.mu.Unlock()
	var account any
	if c := state.Credential; c != nil {
		account = map[string]any{"email": c.Email, "expires_at": c.ExpiresAt.Format(time.RFC3339), "scope_ok": hasScope(c.Scopes, requiredScope), "client_id": c.ClientID}
	}
	writeJSON(w, 200, map[string]any{
		"signed_in": state.Credential != nil, "account": account,
		"api_running": d.server.apiEnabled.Load(), "login_busy": busy,
		"login_url": authURL, "login_error": loginError,
		"base_url": "http://" + r.Host + "/v1",
	})
}

func (d *Dashboard) startLogin(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	if d.loginBusy {
		d.mu.Unlock()
		writeError(w, 409, "login_busy", "An authorization flow is already running")
		return
	}
	d.loginBusy, d.loginURL, d.loginError = true, "", ""
	d.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		err := d.server.auth.loginWithLauncher(ctx, func(loginURL string) {
			d.mu.Lock()
			d.loginURL = loginURL
			d.mu.Unlock()
			// On macOS this opens the official sign-in page automatically.
			// If it fails, the authorized local dashboard can show a fallback link.
			_ = browserOpen(loginURL)
		})
		d.mu.Lock()
		d.loginBusy = false
		d.loginURL = ""
		if err != nil {
			d.loginError = err.Error()
		}
		d.mu.Unlock()
	}()
	writeJSON(w, 202, map[string]any{"started": true})
}

func (d *Dashboard) logout(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	busy := d.loginBusy
	d.mu.Unlock()
	if busy {
		writeError(w, 409, "login_busy", "Wait for the current login to finish")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := d.server.auth.logout(ctx); err != nil {
		writeError(w, 502, "logout_failed", err.Error())
		return
	}
	d.server.apiEnabled.Store(false)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (d *Dashboard) setService(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Enabled == nil {
		writeError(w, 400, "bad_request", "enabled must be a boolean")
		return
	}
	if *body.Enabled {
		c := d.server.auth.store.get().Credential
		if c == nil || !hasScope(c.Scopes, requiredScope) {
			writeError(w, 409, "login_required", "Complete ChatGPT authorization before starting the API")
			return
		}
	}
	d.server.apiEnabled.Store(*body.Enabled)
	writeJSON(w, 200, map[string]any{"api_running": *body.Enabled})
}

func (d *Dashboard) key(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"key": d.server.localKey})
}

func (d *Dashboard) models(w http.ResponseWriter, r *http.Request) {
	if d.server.auth.store.get().Credential == nil {
		writeError(w, 409, "login_required", "Sign in before loading models")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	resp, err := d.server.upstream(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		writeError(w, 502, "upstream_unavailable", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		writeError(w, resp.StatusCode, "upstream_models_error", fmt.Sprintf("Model discovery returned HTTP %d", resp.StatusCode))
		return
	}
	var payload struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload); err != nil {
		writeError(w, 502, "invalid_upstream", "Invalid model list returned by OpenAI")
		return
	}
	models := make([]string, 0, len(payload.Models))
	for _, m := range payload.Models {
		if m.Visibility == "list" && m.Slug != "" {
			models = append(models, m.Slug)
		}
	}
	writeJSON(w, 200, map[string]any{"models": models})
}
