package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ExternalProvider is an explicitly configured, independently billed provider.
// Credentials remain in the local protected state file; never expose them via API/UI.
// Kind: "openai-compatible" for Chat Completions/Responses, "ark-video" for
// Volcengine's asynchronous Seedance video generation tasks.
type ExternalProvider struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	BaseURL      string   `json:"base_url"`
	APIKey       string   `json:"api_key"`
	Enabled      bool     `json:"enabled"`
	ManualModels []string `json:"manual_models,omitempty"`
	Preset       string   `json:"preset,omitempty"`
}

type PublicProvider struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	BaseURL      string   `json:"base_url"`
	Enabled      bool     `json:"enabled"`
	HasAPIKey    bool     `json:"has_api_key"`
	ManualModels []string `json:"manual_models"`
	Preset       string   `json:"preset,omitempty"`
	Models       []string `json:"models"`
	DisabledModels []string `json:"disabled_models"`
	Error        string   `json:"discovery_error,omitempty"`
	RefreshedAt  string   `json:"refreshed_at,omitempty"`
}

type RoutedModel struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
}

type modelSnapshot struct {
	own       []string
	ownOK     bool
	ownError  string
	providers map[string][]string
	errs      map[string]string
	refreshed map[string]time.Time
}

// Router caches discovery data without storing API keys in the cache.
// Routing decisions are deterministic: explicit provider/model always wins,
// plain names resolve to SIWC first, then one unique external provider.
type Router struct {
	store     *Store
	auth      *Auth
	client    *http.Client
	localClient *http.Client
	mu        sync.RWMutex
	refreshMu sync.Mutex
	snap      modelSnapshot
}

func newRouter(a *Auth) *Router {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // No attacker-controlled proxy bypass of DNS checks.
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.ResponseHeaderTimeout = 50 * time.Second
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("DNS returned no IP addresses")
		}
		// Reject the entire resolution set if even one address is not global.
		for _, ip := range ips {
			if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
				return nil, errors.New("provider address resolves to a private or non-public network")
			}
		}
		var d net.Dialer
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	localTransport := http.DefaultTransport.(*http.Transport).Clone()
	localTransport.Proxy = nil
	localTransport.ResponseHeaderTimeout = 3 * time.Minute
	localTransport.DialContext = dialLocalProvider
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Router{store: a.store, auth: a,
		client: &http.Client{Transport: transport, CheckRedirect: noRedirect},
		localClient: &http.Client{Transport: localTransport, CheckRedirect: noRedirect},
		snap: modelSnapshot{providers: map[string][]string{}, errs: map[string]string{}, refreshed: map[string]time.Time{}}}

}

var providerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

func validateExternalProvider(p ExternalProvider) error {
	if !providerIDPattern.MatchString(p.ID) || p.ID == "siwc" {
		return errors.New("provider id must be 2–32 lowercase letters, numbers, hyphens or underscores, starting with a letter (siwc reserved)")
	}
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 80 {
		return errors.New("provider name required (max 80 chars)")
	}
	if p.Kind != "openai-compatible" && p.Kind != "ark-video" && p.Kind != "local-openai-compatible" {
		return errors.New("unknown provider kind")
	}
	if p.Preset != "" && p.Preset != "custom" {
		preset, exists := findProviderPreset(p.Preset)
		if !exists || p.Kind != preset.Kind || p.BaseURL != preset.BaseURL {
			return errors.New("provider preset does not match its official endpoint and API type")
		}
	}
	var addressErr error
	if p.Kind == "local-openai-compatible" {
		addressErr = validateLocalProviderURL(p.BaseURL)
	} else {
		addressErr = validateProviderURL(p.BaseURL)
	}
	if addressErr != nil {
		return addressErr
	}
	if len(p.APIKey) > 4096 {
		return errors.New("API key is too long")
	}
	if len(p.ManualModels) > 200 {
		return errors.New("too many manual models")
	}
	for _, model := range p.ManualModels {
		if strings.TrimSpace(model) != model || model == "" || strings.ContainsAny(model, "\r\n\x00") || len(model) > 200 {
			return errors.New("invalid manual model ID")
		}
	}
	return nil
}

// Custom providers must use a real public HTTPS API endpoint, not localhost
// or an arbitrary internal network service. The Agent listener remains local.
func validateProviderURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("provider URL must be a public HTTPS base URL without credentials, query or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") || strings.HasSuffix(host, ".") {
		return errors.New("local or internal provider host is not allowed")
	}
	if net.ParseIP(host) != nil {
		return errors.New("provider URL must use a public DNS hostname, not an IP literal")
	}
	if u.Port() != "" && u.Port() != "443" {
		return errors.New("only the default HTTPS port is supported")
	}
	if strings.Contains(u.EscapedPath(), "%") || strings.Contains(u.Path, "..") {
		return errors.New("provider base URL path is invalid")
	}
	return nil
}

func modelsForProvider(p ExternalProvider, fetched []string) []string {
	seen := map[string]bool{}
	models := make([]string, 0, len(p.ManualModels)+len(fetched))
	for _, model := range append(append([]string(nil), p.ManualModels...), fetched...) {
		if model != "" && !seen[model] {
			seen[model] = true
			models = append(models, model)
		}
	}
	sort.Strings(models)
	return models
}

func parseModelDirectory(body io.Reader) ([]string, error) {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 4<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	models := []string{}
	for _, m := range payload.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	for _, m := range payload.Models {
		if m.Slug != "" && (m.Visibility == "" || m.Visibility == "list") {
			models = append(models, m.Slug)
		}
	}
	return models, nil
}

func (r *Router) refreshExternal(ctx context.Context, p ExternalProvider) ([]string, error) {
	if !p.Enabled {
		return nil, nil
	}
	base := strings.TrimRight(p.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	if p.APIKey != "" { req.Header.Set("Authorization", "Bearer "+p.APIKey) }
	req.Header.Set("Accept", "application/json")
	resp, err := r.clientFor(p).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("provider model directory HTTP %d", resp.StatusCode)
	}
	return parseModelDirectory(resp.Body)
}

func (r *Router) refreshOwn(ctx context.Context) ([]string, error) {
	state := r.store.get()
	if state.Credential == nil {
		return nil, errors.New("SIWC not signed in")
	}
	token, err := r.auth.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resource+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := r.auth.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("SIWC model discovery HTTP %d", resp.StatusCode)
	}
	var data struct {
		Models []struct{ Slug, Visibility string } `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&data); err != nil {
		return nil, err
	}
	models := []string{}
	for _, m := range data.Models {
		if m.Slug != "" && m.Visibility == "list" {
			models = append(models, m.Slug)
		}
	}
	sort.Strings(models)
	return models, nil
}

func (r *Router) Refresh(ctx context.Context) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	state := r.store.get()
	snap := modelSnapshot{providers: map[string][]string{}, errs: map[string]string{}, refreshed: map[string]time.Time{}}
	var wg sync.WaitGroup
	var dataMu sync.Mutex
	if state.Credential != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctxOwn, cancel := context.WithTimeout(ctx, 30*time.Second)
			models, err := r.refreshOwn(ctxOwn)
			cancel()
			dataMu.Lock()
			defer dataMu.Unlock()
			if err != nil {
				snap.ownError = err.Error()
			} else {
				snap.ownOK = true
				snap.own = models
			}
		}()
	} else {
		snap.ownError = "SIWC not signed in"
	}
	for _, provider := range state.Providers {
		p := provider
		if !p.Enabled {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, 30*time.Second)
			models, err := r.refreshExternal(c, p)
			cancel()
			dataMu.Lock()
			defer dataMu.Unlock()
			if err != nil {
				snap.errs[p.ID] = err.Error()
			}
			snap.providers[p.ID] = modelsForProvider(p, models)
			snap.refreshed[p.ID] = time.Now()
		}()
	}
	wg.Wait()
	r.mu.Lock()
	r.snap = snap
	r.mu.Unlock()
}

func (r *Router) Models() []RoutedModel {
	state := r.store.get()
	r.mu.RLock()
	s := r.snap
	models := make([]RoutedModel, 0, len(s.own))
	for _, id := range s.own {
		if !modelDisabled(state, "siwc", id) {
			models = append(models, RoutedModel{ID: id, Provider: "siwc", Kind: "siwc"})
		}
	}
	for _, p := range state.Providers {
		if !p.Enabled {
			continue
		}
		for _, id := range modelsForProvider(p, s.providers[p.ID]) {
			if !modelDisabled(state, p.ID, id) {
				models = append(models, RoutedModel{ID: p.ID + "/" + id, Provider: p.ID, Kind: p.Kind})
			}
		}
	}
	r.mu.RUnlock()
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

func (r *Router) Providers() []PublicProvider {
	state := r.store.get()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]PublicProvider, 0, len(state.Providers))
	for _, p := range state.Providers {
		x := PublicProvider{ID: p.ID, Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, Preset: p.Preset, Enabled: p.Enabled, HasAPIKey: p.APIKey != "", ManualModels: append([]string{}, p.ManualModels...), Models: modelsForProvider(p, r.snap.providers[p.ID]), DisabledModels: append([]string{}, state.DisabledModels[p.ID]...), Error: r.snap.errs[p.ID]}
		if t := r.snap.refreshed[p.ID]; !t.IsZero() {
			x.RefreshedAt = t.UTC().Format(time.RFC3339)
		}
		out = append(out, x)
	}
	return out
}

func (r *Router) OwnModels() ([]string, bool, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.snap.own...), r.snap.ownOK, r.snap.ownError
}

// Unprefixed models prefer SIWC, then the first enabled external provider in
// persisted user-defined order. If SIWC discovery is unavailable while signed
// in, never infer absence and silently route a request to a charged provider.
// Explicit provider/model requests are pinned to exactly that provider.
func (r *Router) Resolve(model string) (*ExternalProvider, string, error) {
	state := r.store.get()
	r.mu.RLock()
	s := r.snap
	defer r.mu.RUnlock()
	if strings.HasPrefix(model, "siwc/") {
		real := strings.TrimPrefix(model, "siwc/")
		if !s.ownOK {
			return nil, "", errors.New("SIWC catalog unavailable; refresh models")
		}
		for _, x := range s.own {
			if x == real {
				if modelDisabled(state, "siwc", real) {
					return nil, "", errors.New("model is disabled for SIWC")
				}
				return nil, real, nil
			}
		}
		return nil, "", errors.New("model is not in this SIWC account catalog")
	}
	for _, p := range state.Providers {
		if p.Enabled && strings.HasPrefix(model, p.ID+"/") {
			real := strings.TrimPrefix(model, p.ID+"/")
			for _, x := range modelsForProvider(p, s.providers[p.ID]) {
				if x == real {
					if modelDisabled(state, p.ID, real) {
						return nil, "", errors.New("model is disabled for this provider")
					}
					return &p, real, nil
				}
			}
			return nil, "", errors.New("model is not available from the selected provider")
		}
	}
	if state.Credential != nil {
		if !s.ownOK {
			return nil, "", errors.New("SIWC model catalog unavailable; refresh before using unprefixed models")
		}
		for _, x := range s.own {
			if model == x && !modelDisabled(state, "siwc", model) {
				return nil, model, nil
			}
		}
	}
	for _, p := range state.Providers {
		if !p.Enabled {
			continue
		}
		for _, x := range modelsForProvider(p, s.providers[p.ID]) {
			if x == model && !modelDisabled(state, p.ID, model) {
				cp := p
				return &cp, model, nil
			}
		}
	}
	return nil, "", errors.New("unknown model: refresh catalog or add it to a provider's manual models")
}

// clientFor enforces separate, pinned-network transports for public cloud and
// explicitly configured local inference. A cloud vendor cannot gain LAN access.
func (r *Router) clientFor(p ExternalProvider) *http.Client {
	if p.Kind == "local-openai-compatible" { return r.localClient }
	return r.client
}

func (r *Router) proxyExternal(w http.ResponseWriter, req *http.Request, p ExternalProvider, upstreamModel string, kind string, body []byte, path string) {
	endpoint := strings.TrimRight(p.BaseURL, "/") + path
	r.proxyExternalTo(w, req, p, body, endpoint)
}

func (r *Router) proxyExternalTo(w http.ResponseWriter, req *http.Request, p ExternalProvider, body []byte, endpoint string) {
	outbound, err := http.NewRequestWithContext(req.Context(), req.Method, endpoint, bytes.NewReader(body))
	if err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	if p.APIKey != "" { outbound.Header.Set("Authorization", "Bearer "+p.APIKey) }
	outbound.Header.Set("Content-Type", "application/json")
	outbound.Header.Set("Accept", req.Header.Get("Accept"))
	response, err := r.clientFor(p).Do(outbound)
	if err != nil {
		writeError(w, 502, "provider_unavailable", "External provider request failed: "+err.Error())
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		writeError(w, 502, "provider_redirect", "Provider redirect refused")
		return
	}
	ct := response.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(response.StatusCode)
		buffer := make([]byte, 16<<10)
		for {
			n, e := response.Body.Read(buffer)
			if n > 0 {
				if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
					return
				}
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			if e != nil {
				return
			}
		}
	}
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, 32<<20))
}

func (s *Server) routedModels(w http.ResponseWriter, req *http.Request) {
	if s.router == nil {
		s.models(w, req)
		return
	}
	entries := s.router.Models()
	data := make([]any, 0, len(entries))
	for _, e := range entries {
		data = append(data, map[string]any{"id": e.ID, "object": "model", "created": 0, "owned_by": e.Provider})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

func (s *Server) routeInference(w http.ResponseWriter, req *http.Request, route string) {
	if s.router == nil {
		if route == "/responses" {
			s.responses(w, req)
		} else {
			s.chatCompletions(w, req)
		}
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, req.Body, maxBodySize))
	if err != nil {
		writeError(w, 413, "invalid_request", "body too large or unreadable")
		return
	}
	var body map[string]any
	if err = json.Unmarshal(b, &body); err != nil || body == nil {
		writeError(w, 400, "invalid_request", "expected JSON object")
		return
	}
	model, _ := body["model"].(string)
	if model == "" {
		writeError(w, 400, "invalid_request", "model required")
		return
	}
	provider, actual, err := s.router.Resolve(model)
	if err != nil {
		writeError(w, 404, "model_not_found", err.Error())
		return
	}
	body["model"] = actual
	newBody, err := json.Marshal(body)
	if err != nil {
		writeError(w, 400, "invalid_request", "could not serialize request")
		return
	}
	if provider == nil {
		// SIWC may only use its explicitly authorized Responses route. Do not
		// fall back to a paid provider on errors, limits or missing credentials.
		fake := req.Clone(req.Context())
		fake.Body = io.NopCloser(bytes.NewReader(newBody))
		fake.ContentLength = int64(len(newBody))
		if route == "/responses" {
			s.responses(w, fake)
		} else {
			s.chatCompletions(w, fake)
		}
		return
	}
	if provider.Kind != "openai-compatible" && provider.Kind != "local-openai-compatible" {
		writeError(w, 400, "unsupported_capability", "Video models use /v1/contents/generations/tasks, not Chat Completions")
		return
	}
	s.router.proxyExternal(w, req, *provider, actual, route, newBody, route)
}

// Ark video generation is asynchronous and has a different request schema.
// POST model = "provider/model"; GET requires ?provider=<provider-id>.
func (s *Server) routeVideoTask(w http.ResponseWriter, req *http.Request) {
	if s.router == nil {
		writeError(w, 404, "not_found", "video router unavailable")
		return
	}
	const prefix = "/v1/contents/generations/tasks"
	if req.Method == http.MethodPost && req.URL.Path == prefix {
		b, err := io.ReadAll(http.MaxBytesReader(w, req.Body, maxBodySize))
		if err != nil {
			writeError(w, 413, "bad_request", "invalid request size")
			return
		}
		var v map[string]any
		if json.Unmarshal(b, &v) != nil || v == nil {
			writeError(w, 400, "invalid_request", "JSON object expected")
			return
		}
		model, _ := v["model"].(string)
		p, actual, err := s.router.Resolve(model)
		if err != nil {
			writeError(w, 404, "model_not_found", err.Error())
			return
		}
		if p == nil || p.Kind != "ark-video" {
			writeError(w, 400, "unsupported_capability", "model is not a video provider")
			return
		}
		v["model"] = actual
		data, _ := json.Marshal(v)
		s.router.proxyExternalTo(w, req, *p, data, strings.TrimRight(p.BaseURL, "/")+"/contents/generations/tasks")
		return
	}
	if req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, prefix+"/") {
		id := strings.TrimPrefix(req.URL.Path, prefix+"/")
		if id == "" || strings.Contains(id, "/") || strings.Contains(id, "..") || len(id) > 180 {
			writeError(w, 400, "invalid_request", "invalid task ID")
			return
		}
		providerID := req.URL.Query().Get("provider")
		var chosen *ExternalProvider
		for _, p := range s.router.store.get().Providers {
			if p.ID == providerID && p.Enabled && p.Kind == "ark-video" {
				cp := p
				chosen = &cp
				break
			}
		}
		if chosen == nil {
			writeError(w, 400, "invalid_request", "valid video provider query required")
			return
		}
		s.router.proxyExternalTo(w, req, *chosen, nil, strings.TrimRight(chosen.BaseURL, "/")+"/contents/generations/tasks/"+url.PathEscape(id))
		return
	}
	writeError(w, 404, "not_found", "video task endpoint not found")
}
