package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockRoundTrip func(req *http.Request) (*http.Response, error)

func (fn mockRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }
func testResponse(code int, contentType, data string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(data))}
}

func TestProviderValidationAndCredentialIsolation(t *testing.T) {
	s, err := newStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	oldKey := s.get().LocalKey
	invalid := []string{"http://api.example.com/v1", "https://127.0.0.1/v1", "https://localhost/v1", "https://foo.local/v1", "https://foo.example.com:8443/v1", "https://user:pass@example.com/v1", "https://example.com/v1?api_key=secret"}
	for _, url := range invalid {
		p := ExternalProvider{ID: "vendor", Name: "Vendor", Kind: "openai-compatible", BaseURL: url, APIKey: "top-secret", Enabled: true}
		if err := s.upsertProvider(p); err == nil {
			t.Errorf("accepted unsafe endpoint %s", url)
		}
	}
	p := ExternalProvider{ID: "deepseek", Name: "DeepSeek", Kind: "openai-compatible", BaseURL: "https://api.deepseek.com", APIKey: "private-secret-one", Enabled: true, ManualModels: []string{"manual-model"}}
	if err := s.upsertProvider(p); err != nil {
		t.Fatal(err)
	}
	if s.get().LocalKey != oldKey {
		t.Fatal("provider changed Agent key")
	}
	p.APIKey = ""
	p.Name = "DeepSeek Updated"
	if err := s.upsertProvider(p); err != nil {
		t.Fatal(err)
	}
	if s.get().Providers[0].APIKey != "private-secret-one" {
		t.Fatal("API secret lost during editing")
	}
	state, err := newStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if state.get().Providers[0].Name != "DeepSeek Updated" {
		t.Fatal("external config not persisted")
	}
	router := newRouter(newAuth(state))
	listed := router.Providers()
	serialized, _ := json.Marshal(listed)
	if bytes.Contains(serialized, []byte("private-secret-one")) {
		t.Fatal("provider API secret leaked to UI")
	}
	if err := state.deleteProvider("deepseek"); err != nil {
		t.Fatal(err)
	}
	if len(state.get().Providers) != 0 {
		t.Fatal("provider delete failed")
	}
}

func TestProviderCatalogAndRouting(t *testing.T) {
	s, err := newStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []ExternalProvider{
		{ID: "deepseek", Name: "DeepSeek", Kind: "openai-compatible", BaseURL: "https://api.deepseek.com", APIKey: "key-a", Enabled: true, ManualModels: []string{"manual-model"}},
		{ID: "other", Name: "Other", Kind: "openai-compatible", BaseURL: "https://other.example.org/v1", APIKey: "key-b", Enabled: true, ManualModels: []string{"duplicate"}},
	} {
		if err := s.upsertProvider(p); err != nil {
			t.Fatal(err)
		}
	}
	r := newRouter(newAuth(s))
	r.client = &http.Client{Transport: mockRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "api.deepseek.com" {
			if req.Header.Get("Authorization") != "Bearer key-a" {
				t.Error("wrong DeepSeek key")
			}
			return testResponse(200, "application/json", `{"object":"list","data":[{"id":"deepseek-flash"},{"id":"duplicate"}]}`), nil
		}
		return testResponse(200, "application/json", `{"data":[{"id":"duplicate"}]}`), nil
	})}
	r.Refresh(context.Background())
	models := r.Models()
	want := map[string]bool{"deepseek/deepseek-flash": true, "deepseek/manual-model": true, "deepseek/duplicate": true, "other/duplicate": true}
	for _, m := range models {
		if !want[m.ID] {
			t.Errorf("unexpected model %s", m.ID)
		}
		delete(want, m.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing models %v", want)
	}
	p, real, err := r.Resolve("deepseek/deepseek-flash")
	if err != nil || p.ID != "deepseek" || real != "deepseek-flash" {
		t.Fatalf("wrong route: %#v %s %v", p, real, err)
	}
	p, _, err = r.Resolve("duplicate")
	if err != nil || p == nil || p.ID != "deepseek" {
		t.Fatalf("duplicate model must route to first provider: %+v %v", p, err)
	}
	p, _, err = r.Resolve("deepseek-flash")
	if err != nil || p.ID != "deepseek" {
		t.Fatal("unique external model did not resolve")
	}
	if err := s.saveCredential(&Credential{AccessToken: "fake", Scopes: requiredScope, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.snap.own = []string{"deepseek-flash"}
	r.snap.ownOK = true
	r.mu.Unlock()
	p, _, err = r.Resolve("deepseek-flash")
	if err != nil || p != nil {
		t.Fatal("SIWC model did not take precedence")
	}
	r.mu.Lock()
	r.snap.ownOK = false
	r.mu.Unlock()
	if _, _, err = r.Resolve("deepseek-flash"); err == nil {
		t.Fatal("missing SIWC catalog silently fell back to charged provider")
	}
}

func TestExternalInferenceKeyAndModelRewrite(t *testing.T) {
	s, err := newStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.upsertProvider(ExternalProvider{ID: "deepseek", Name: "DeepSeek", Kind: "openai-compatible", BaseURL: "https://api.deepseek.com", APIKey: "deepseek-only", Enabled: true, ManualModels: []string{"deepseek-flash"}}); err != nil {
		t.Fatal(err)
	}
	server := newServer(newAuth(s))
	server.router.mu.Lock()
	server.router.snap.providers["deepseek"] = []string{"deepseek-flash"}
	server.router.mu.Unlock()
	var mu sync.Mutex
	var gotBody map[string]any
	var gotKey, gotPath string
	server.router.client = &http.Client{Transport: mockRoundTrip(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		gotKey = req.Header.Get("Authorization")
		gotPath = req.URL.Path
		_ = json.NewDecoder(req.Body).Decode(&gotBody)
		return testResponse(200, "application/json", `{"id":"chatcmpl_test","choices":[{"message":{"role":"assistant","content":"ok"}}]}`), nil
	})}
	handler := server.APIHandler()
	payload := `{"model":"deepseek/deepseek-flash","messages":[{"role":"user","content":"hello"}],"max_output_tokens":999}`
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8087/v1/chat/completions", strings.NewReader(payload))
	req.RemoteAddr = "127.0.0.1:58301"
	req.Header.Set("Authorization", "Bearer "+s.get().LocalKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if gotKey != "Bearer deepseek-only" {
		t.Fatal("user API key leaked to external API, or wrong provider API key")
	}
	if gotPath != "/chat/completions" {
		t.Fatal("wrong endpoint path", gotPath)
	}
	if gotBody["model"] != "deepseek-flash" {
		t.Fatal("model prefix not stripped")
	}
	if gotBody["max_output_tokens"] != float64(999) {
		t.Fatal("non-SIWC parameters must not be stripped")
	}
}

func TestArkVideoTaskRouting(t *testing.T) {
	s, err := newStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.upsertProvider(ExternalProvider{ID: "ark", Name: "Ark Video", Kind: "ark-video", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", APIKey: "ark-key", Enabled: true, ManualModels: []string{"seedance-test"}}); err != nil {
		t.Fatal(err)
	}
	server := newServer(newAuth(s))
	server.router.mu.Lock()
	server.router.snap.providers["ark"] = []string{"seedance-test"}
	server.router.mu.Unlock()
	var paths []string
	server.router.client = &http.Client{Transport: mockRoundTrip(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.Method+" "+req.URL.Path)
		if req.Method == "POST" {
			var x map[string]any
			_ = json.NewDecoder(req.Body).Decode(&x)
			if x["model"] != "seedance-test" {
				t.Error("prefix not removed")
			}
		}
		return testResponse(200, "application/json", `{"id":"task_01","status":"queued"}`), nil
	})}
	handler := server.APIHandler()
	for _, tc := range []struct{ method, url, body string }{
		{"POST", "http://127.0.0.1:8087/v1/contents/generations/tasks", `{"model":"ark/seedance-test","content":[{"type":"text","text":"hello"}]}`},
		{"GET", "http://127.0.0.1:8087/v1/contents/generations/tasks/task_01?provider=ark", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.url, strings.NewReader(tc.body))
		req.RemoteAddr = "127.0.0.1:3"
		req.Header.Set("Authorization", "Bearer "+s.get().LocalKey)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("%s %d %s", tc.method, w.Code, w.Body.String())
		}
	}
	if len(paths) != 2 || paths[0] != "POST /api/v3/contents/generations/tasks" || paths[1] != "GET /api/v3/contents/generations/tasks/task_01" {
		t.Fatal(paths)
	}
}

func TestNoAutoFallbackOnInvalidSIWC(t *testing.T) {
	s, err := newStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.upsertProvider(ExternalProvider{ID: "paid", Name: "Paid", Kind: "openai-compatible", BaseURL: "https://provider.example.com", APIKey: "paid-key", Enabled: true, ManualModels: []string{"model-si"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.saveCredential(&Credential{AccessToken: "fake", Scopes: requiredScope, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	server := newServer(newAuth(s))
	server.client = &http.Client{Transport: mockRoundTrip(func(req *http.Request) (*http.Response, error) {
		return testResponse(429, "application/json", `{"error":{"code":"usage_limit_exceeded"}}`), nil
	})}
	server.router.mu.Lock()
	server.router.snap.ownOK = true
	server.router.snap.own = []string{"model-si"}
	server.router.mu.Unlock()
	externalCalls := 0
	server.router.client = &http.Client{Transport: mockRoundTrip(func(req *http.Request) (*http.Response, error) {
		externalCalls++
		return testResponse(200, "application/json", `{"output":[]}`), nil
	})}
	req := httptest.NewRequest("POST", "http://127.0.0.1:8087/v1/responses", strings.NewReader(`{"model":"model-si","input":"hi"}`))
	req.RemoteAddr = "127.0.0.1:400"
	req.Header.Set("Authorization", "Bearer "+s.get().LocalKey)
	w := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(w, req)
	if externalCalls != 0 {
		t.Fatal("SIWC error fell back to charged provider")
	}
	if w.Code < 400 {
		t.Fatal("invalid session should fail without paid fallback")
	}
}

func TestDashboardProviderEndpointsAreProtected(t *testing.T) {
	store, err := newStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	server := newServer(newAuth(store))
	server.dashboard = newDashboard(server)
	admin := server.AdminHandler()
	// Unauthorized callers cannot read or mutate provider credentials.
	w := providerDashboardRequest(t, admin, "GET", "/ui/api/providers", nil, nil, false, "")
	if w.Code != 401 {
		t.Fatalf("unauthorized provider listing HTTP %d", w.Code)
	}
	w = providerDashboardRequest(t, admin, "POST", "/ui/api/providers", ExternalProvider{ID: "mock", Name: "Mock", Kind: "openai-compatible", BaseURL: "https://example.com", APIKey: "secret", Enabled: true}, nil, true, "")
	if w.Code != 401 {
		t.Fatalf("unauthorized provider update HTTP %d", w.Code)
	}
	w = providerDashboardRequest(t, admin, "POST", "/ui/api/session", map[string]string{"key": store.get().AdminKey}, nil, true, "")
	if w.Code != 200 || len(w.Result().Cookies()) == 0 {
		t.Fatalf("admin session failed: %s", w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	p := ExternalProvider{ID: "mock", Name: "Mock Provider", Kind: "openai-compatible", BaseURL: "https://example.com", APIKey: "secret", Enabled: true, ManualModels: []string{"m"}}
	w = providerDashboardRequest(t, admin, "POST", "/ui/api/providers", p, cookie, false, "")
	if w.Code != 403 {
		t.Fatalf("provider mutation without CSRF allowed: %d", w.Code)
	}
	w = providerDashboardRequest(t, admin, "POST", "/ui/api/providers", p, cookie, true, "")
	if w.Code != 200 {
		t.Fatalf("provider creation HTTP %d: %s", w.Code, w.Body.String())
	}
	w = providerDashboardRequest(t, admin, "GET", "/ui/api/providers", nil, cookie, false, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"api_key"`) || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("provider secret leaked to dashboard list: %s", w.Body.String())
	}
	// The public Agent API never provides dashboard management endpoints.
	apiReq := httptest.NewRequest("GET", "http://127.0.0.1:8087/ui/api/providers", nil)
	apiReq.RemoteAddr = "127.0.0.1:42"
	apiRes := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(apiRes, apiReq)
	if apiRes.Code != 404 {
		t.Fatalf("dashboard endpoint exposed to Agent API: %d", apiRes.Code)
	}
}

// Independent fixture: the dashboard tests in older branches may name their
// helper differently. Avoid coupling provider tests to that private helper.
func providerDashboardRequest(t *testing.T, handler http.Handler, method, path string, body any, cookie *http.Cookie, adminHeader bool, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, bytes.NewReader(data))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if adminHeader {
		req.Header.Set(dashboardHeader, "1")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}