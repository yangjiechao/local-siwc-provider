package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func runtimeFixture(t *testing.T) (*Server, *APIRuntime, *Dashboard) {
	t.Helper()
	store, err := newStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := newServer(newAuth(store))
	svc.apiEnabled.Store(false)
	api := newAPIRuntime(svc, freePort(t))
	dash := newDashboard(svc)
	dash.runtime = api
	dash.adminPort = api.adminPort
	svc.dashboard = dash
	t.Cleanup(func() { _ = api.Close() })
	return svc, api, dash
}

func adminRequest(t *testing.T, handler http.Handler, method, path string, data any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var b []byte
	if data != nil {
		var err error
		b, err = json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, bytes.NewReader(b))
	if method == "POST" {
		r.Header.Set(dashboardHeader, "1")
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func adminCookie(t *testing.T, app *Server) *http.Cookie {
	t.Helper()
	w := adminRequest(t, app.AdminHandler(), "POST", "/ui/api/session", map[string]string{"key": app.auth.store.get().AdminKey}, nil)
	if w.Code != 200 {
		t.Fatalf("unlock dashboard %d: %s", w.Code, w.Body.String())
	}
	return w.Result().Cookies()[0]
}

func TestPortLANAndManualKeyFromDashboard(t *testing.T) {
	app, runtime, _ := runtimeFixture(t)
	cookie := adminCookie(t, app)
	admin := app.AdminHandler()
	s := app.auth.store.get()
	if s.APIPort != 8087 || !s.LANEnabled {
		t.Fatalf("unexpected defaults: %d LAN=%v", s.APIPort, s.LANEnabled)
	}
	port := freePort(t)
	w := adminRequest(t, admin, "POST", "/ui/api/settings", map[string]any{"port": port, "lan": false}, cookie)
	if w.Code != 200 {
		t.Fatalf("configure: %d %s", w.Code, w.Body.String())
	}
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	if !runtime.Running() {
		t.Fatal("API did not start")
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz %d", resp.StatusCode)
	}

	// LAN API must not provide web dashboard routes.
	resp, err = client.Get(fmt.Sprintf("http://127.0.0.1:%d/ui/", port))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("dashboard exposed on API: %d", resp.StatusCode)
	}

	newKey := "sk-my-new-manual-agent-key-123456"
	w = adminRequest(t, admin, "POST", "/ui/api/key", map[string]string{"key": newKey}, cookie)
	if w.Code != 200 {
		t.Fatalf("save key %d %s", w.Code, w.Body.String())
	}
	if app.auth.store.get().LocalKey != newKey {
		t.Fatal("new key not saved")
	}

	// Authentication is switched immediately, without restarting the API.
	request := func(key string) int {
		req, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/v1/models", port), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if c := request(s.LocalKey); c != 401 {
		t.Fatalf("old key still valid: %d", c)
	}
	if c := request(newKey); c == 401 {
		t.Fatalf("new key rejected: %d", c)
	}

	busyPort := freePort(t)
	block, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", busyPort))
	if err != nil {
		t.Fatal(err)
	}
	defer block.Close()
	// Switching to an occupied different port must not close/recreate the
	// existing listener at all, even with active keep-alive connections.
	runtime.mu.Lock()
	previousListener, previousServer := runtime.listener, runtime.server
	runtime.mu.Unlock()
	w = adminRequest(t, admin, "POST", "/ui/api/settings", map[string]any{"port": busyPort, "lan": true}, cookie)
	if w.Code != 409 {
		t.Fatalf("occupied port should fail: %d %s", w.Code, w.Body.String())
	}
	if app.auth.store.get().APIPort != port || app.auth.store.get().LANEnabled {
		t.Fatal("config did not roll back")
	}
	runtime.mu.Lock()
	listenerUnchanged := runtime.listener == previousListener && runtime.server == previousServer
	runtime.mu.Unlock()
	if !listenerUnchanged {
		t.Fatal("occupied target port must leave the original listener running, without a restart")
	}
	resp, err = client.Get(url)
	if err != nil {
		t.Fatal("old port did not recover:", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("old port health status %d", resp.StatusCode)
	}

	newPort := freePort(t)
	w = adminRequest(t, admin, "POST", "/ui/api/settings", map[string]any{"port": newPort, "lan": true}, cookie)
	if w.Code != 200 {
		t.Fatalf("LAN port migration: %d %s", w.Code, w.Body.String())
	}
	if !runtime.Running() {
		t.Fatal("API did not restart on port change")
	}
	resp, err = client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", newPort))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("new port got %d", resp.StatusCode)
	}
	if _, err = client.Get(url); err == nil {
		t.Fatal("old API port is still listening")
	}

	w = adminRequest(t, admin, "POST", "/ui/api/restart-api", map[string]any{}, cookie)
	if w.Code != 200 || !runtime.Running() {
		t.Fatalf("API restart failed: %d", w.Code)
	}

	w = adminRequest(t, admin, "GET", "/ui/api/status", nil, cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"api_port":%d`, newPort)) {
		t.Fatalf("wrong API port in status: %s", w.Body.String())
	}
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}
	if app.auth.store.get().APIEnabled {
		t.Fatal("stop was not persisted")
	}
}

func TestAdminAndAPIAccessControls(t *testing.T) {
	app, _, _ := runtimeFixture(t)
	dashboard := app.AdminHandler()
	api := app.APIHandler()
	request := httptest.NewRequest("GET", "http://127.0.0.1:8787/v1/models", nil)
	w := httptest.NewRecorder()
	dashboard.ServeHTTP(w, request)
	if w.Code != 404 {
		t.Fatalf("API accessible on admin port: %d", w.Code)
	}
	request = httptest.NewRequest("GET", "http://not-our-host.example:8787/ui/", nil)
	w = httptest.NewRecorder()
	dashboard.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatalf("admin accepted foreign hostname: %d", w.Code)
	}

	app.apiEnabled.Store(true)
	request = httptest.NewRequest("GET", "http://192.168.1.8:8087/v1/models", nil)
	request.RemoteAddr = "192.168.1.100:5000"
	w = httptest.NewRecorder()
	api.ServeHTTP(w, request)
	if w.Code != 401 {
		t.Fatalf("LAN request without key: %d", w.Code)
	}
	request = httptest.NewRequest("GET", "http://evil.example:8087/v1/models", nil)
	request.RemoteAddr = "192.168.1.100:5000"
	w = httptest.NewRecorder()
	api.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatalf("DNS rebinding Host accepted: %d", w.Code)
	}
	request = httptest.NewRequest("GET", "http://192.168.1.8:8087/v1/models", nil)
	request.RemoteAddr = "198.51.100.20:5000"
	w = httptest.NewRecorder()
	api.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatalf("non-LAN remote accepted: %d", w.Code)
	}
}

func TestKeyValidationAndLifecycleRequest(t *testing.T) {
	app, _, dash := runtimeFixture(t)
	cookie := adminCookie(t, app)
	admin := app.AdminHandler()
	prior := app.auth.store.get().LocalKey
	for _, bad := range []string{"short", "contains spaces 123456789", "line\nbreaks123456789"} {
		w := adminRequest(t, admin, "POST", "/ui/api/key", map[string]string{"key": bad}, cookie)
		if w.Code != 400 {
			t.Fatalf("accepted invalid key: %d", w.Code)
		}
	}
	if app.auth.store.get().LocalKey != prior {
		t.Fatal("rejected key modified stored secret")
	}
	ch := make(chan lifecycleAction, 1)
	dash.control = ch
	w := adminRequest(t, admin, "POST", "/ui/api/restart", map[string]any{}, cookie)
	if w.Code != 200 {
		t.Fatalf("restart request rejected: %d", w.Code)
	}
	select {
	case a := <-ch:
		if a != lifecycleRestart {
			t.Fatal("wrong action")
		}
	case <-time.After(time.Second):
		t.Fatal("no restart action")
	}
	w = adminRequest(t, admin, "POST", "/ui/api/quit", map[string]any{}, cookie)
	if w.Code != 200 {
		t.Fatalf("quit request rejected: %d", w.Code)
	}
	select {
	case a := <-ch:
		if a != lifecycleQuit {
			t.Fatal("wrong quit action")
		}
	case <-time.After(time.Second):
		t.Fatal("no quit action")
	}
}
