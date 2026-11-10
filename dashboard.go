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
	"strconv"
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
	runtime    *APIRuntime
	adminPort  int
	control    chan<- lifecycleAction
	updater    *UpdateManager
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
	mux.HandleFunc("POST /ui/api/key", d.authorized(d.setKey))
	mux.HandleFunc("POST /ui/api/settings", d.authorized(d.setSettings))
	mux.HandleFunc("POST /ui/api/restart-api", d.authorized(d.restartAPI))
	mux.HandleFunc("POST /ui/api/restart", d.authorized(d.restartProgram))
	mux.HandleFunc("POST /ui/api/quit", d.authorized(d.quitProgram))
	mux.HandleFunc("GET /ui/api/models", d.authorized(d.models))
	mux.HandleFunc("GET /ui/api/update", d.authorized(d.updateStatus))
	mux.HandleFunc("POST /ui/api/update/check", d.authorized(d.checkUpdate))
	mux.HandleFunc("POST /ui/api/update/apply", d.authorized(d.applyUpdate))

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
	port := state.APIPort
	if port == 0 {
		port = defaultAPIPort
	}
	localBase := "http://127.0.0.1:" + strconv.Itoa(port) + "/v1"
	var lanBase string
	if state.LANEnabled {
		if host := localHostLabel(); host != "" {
			lanBase = "http://" + host + ".local:" + strconv.Itoa(port) + "/v1"
		}
	}
	running := d.server.apiEnabled.Load()
	if d.runtime != nil {
		running = d.runtime.Running()
	}
	writeJSON(w, 200, map[string]any{
		"signed_in": state.Credential != nil, "account": account,
		"api_running": running, "login_busy": busy,
		"login_url": authURL, "login_error": loginError,
		"base_url": localBase, "api_port": port,
		"lan_enabled": state.LANEnabled, "lan_base_url": lanBase,
		"lan_ips": localIPAddrs(), "dashboard_port": d.adminPort,
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
	if d.runtime != nil {
		if err := d.runtime.Stop(); err != nil {
			writeError(w, 500, "service_error", "Could not stop Agent API")
			return
		}
	} else {
		d.server.apiEnabled.Store(false)
	}
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
	if d.runtime != nil {
		var err error
		if *body.Enabled {
			err = d.runtime.Start()
		} else {
			err = d.runtime.Stop()
		}
		if err != nil {
			writeError(w, 409, "service_error", err.Error())
			return
		}
	} else {
		d.server.apiEnabled.Store(*body.Enabled)
	}
	writeJSON(w, 200, map[string]any{"api_running": *body.Enabled})
}

func (d *Dashboard) key(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"key": d.server.auth.store.get().LocalKey})
}

// setKey accepts a user-chosen Agent key but never changes the OAuth or admin key.
// Updating the store is sufficient because API authentication reads the live value.
func (d *Dashboard) setKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, 400, "bad_request", "Invalid key request")
		return
	}
	if len(body.Key) < 16 || len(body.Key) > 256 || strings.TrimSpace(body.Key) != body.Key {
		writeError(w, 400, "bad_key", "API key must be 16-256 characters with no leading or trailing spaces")
		return
	}
	for _, char := range body.Key {
		if char < 33 || char > 126 {
			writeError(w, 400, "bad_key", "API key must contain printable ASCII characters only")
			return
		}
	}
	if err := d.server.auth.store.saveLocalKey(body.Key); err != nil {
		writeError(w, 500, "save_failed", "Could not store the new Agent API key")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (d *Dashboard) setSettings(w http.ResponseWriter, r *http.Request) {
	if d.runtime == nil {
		writeError(w, 503, "not_available", "API listener is not managed by this dashboard")
		return
	}
	var body struct {
		Port int   `json:"port"`
		LAN  *bool `json:"lan"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.LAN == nil {
		writeError(w, 400, "bad_request", "Specify API port and LAN enabled flag")
		return
	}
	if err := d.runtime.Configure(body.Port, *body.LAN); err != nil {
		writeError(w, 409, "settings_error", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "port": body.Port, "lan": *body.LAN})
}

func (d *Dashboard) restartAPI(w http.ResponseWriter, r *http.Request) {
	if d.runtime == nil {
		writeError(w, 503, "not_available", "API listener is not managed by this dashboard")
		return
	}
	if !d.runtime.Running() {
		writeError(w, 409, "service_stopped", "Start the API before restarting it")
		return
	}
	if err := d.runtime.Restart(); err != nil {
		writeError(w, 409, "restart_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (d *Dashboard) lifecycle(w http.ResponseWriter, action lifecycleAction) {
	if d.control == nil {
		writeError(w, 503, "not_available", "Lifecycle control is unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "action": action})
	// Reply before tearing down the browser connection.
	time.AfterFunc(250*time.Millisecond, func() {
		select {
		case d.control <- action:
		default:
		}
	})
}

func (d *Dashboard) restartProgram(w http.ResponseWriter, r *http.Request) {
	d.lifecycle(w, lifecycleRestart)
}
func (d *Dashboard) quitProgram(w http.ResponseWriter, r *http.Request) {
	d.lifecycle(w, lifecycleQuit)
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

// Version checking and upgrades are accessible only from the authenticated
// localhost dashboard. Neither route is exposed on the LAN-facing Agent API.
func (d *Dashboard) updateStatus(w http.ResponseWriter, r *http.Request) {
	if d.updater == nil {
		writeError(w, 503, "update_unavailable", "Update manager is not configured")
		return
	}
	writeJSON(w, 200, d.updater.snapshot())
}
func (d *Dashboard) checkUpdate(w http.ResponseWriter, r *http.Request) {
	if d.updater == nil {
		writeError(w, 503, "update_unavailable", "Update manager is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	writeJSON(w, 200, d.updater.Check(ctx))
}
func (d *Dashboard) applyUpdate(w http.ResponseWriter, r *http.Request) {
	if d.updater == nil {
		writeError(w, 503, "update_unavailable", "Update manager is not configured")
		return
	}
	if err := d.updater.Apply(); err != nil {
		writeError(w, 409, "update_unavailable", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "message": "Upgrading in the background; active agent requests may be interrupted when restarting"})
}
