package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Provider management endpoints are intentionally dashboard-only, under the
// same local administrator session, CSRF validation and loopback binding.
func (d *Dashboard) listProviders(w http.ResponseWriter, req *http.Request) {
	own, ok, errMsg := d.server.router.OwnModels()
	state := d.server.auth.store.get()
	writeJSON(w, 200, map[string]any{
		"providers": d.server.router.Providers(),
		"models": d.server.router.Models(),
		"siwc_models": own,
		"siwc_catalog_ok": ok,
		"siwc_error": errMsg,
		"siwc_signed_in": state.Credential != nil,
		"siwc_disabled_models": append([]string{}, state.DisabledModels["siwc"]...),
	})
}

// toggleModelEnabled requires an actively listed model. Disabled entries
// remain in the underlying discovery catalog so users can re-enable them.
func (d *Dashboard) toggleModelEnabled(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		Model string `json:"model"`
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 4096)).Decode(&body); err != nil || body.Enabled == nil {
		writeError(w, 400, "bad_request", "provider, model and enabled boolean required")
		return
	}
	found := false
	if body.Provider == "siwc" {
		models, ok, _ := d.server.router.OwnModels()
		if ok && d.server.auth.store.get().Credential != nil {
			for _, model := range models {
				if model == body.Model { found = true; break }
			}
		}
	} else {
		for _, provider := range d.server.router.Providers() {
			if provider.ID != body.Provider { continue }
			for _, model := range provider.Models {
				if model == body.Model { found = true; break }
			}
			break
		}
	}
	if !found {
		writeError(w, 404, "model_not_found", "model is not in the current provider catalog")
		return
	}
	if err := d.server.auth.store.setModelEnabled(body.Provider, body.Model, *body.Enabled); err != nil {
		writeError(w, 400, "invalid_model_setting", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (d *Dashboard) saveProvider(w http.ResponseWriter, req *http.Request) {
	var body ExternalProvider
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 32<<10)).Decode(&body); err != nil {
		writeError(w, 400, "bad_request", "Invalid provider request")
		return
	}
	if err := d.server.auth.store.upsertProvider(body); err != nil {
		writeError(w, 400, "invalid_provider", err.Error())
		return
	}
	// Refresh after saving; long network operations must not block dashboard writes.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		d.server.router.Refresh(ctx)
	}()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (d *Dashboard) deleteProvider(w http.ResponseWriter, req *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 4096)).Decode(&body); err != nil || body.ID == "" {
		writeError(w, 400, "bad_request", "provider id required")
		return
	}
	if err := d.server.auth.store.deleteProvider(body.ID); err != nil {
		writeError(w, 404, "not_found", err.Error())
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		d.server.router.Refresh(ctx)
	}()
	writeJSON(w, 200, map[string]any{"ok": true})
}

// reorderProviders is loopback dashboard-only, protected by the existing
// admin session and CSRF middleware. It never reads/writes vendor API secrets.
func (d *Dashboard) reorderProviders(w http.ResponseWriter, req *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 4096)).Decode(&body); err != nil {
		writeError(w, 400, "bad_request", "Invalid provider ordering request")
		return
	}
	if err := d.server.auth.store.reorderProviders(body.IDs); err != nil {
		writeError(w, 409, "invalid_order", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "providers": d.server.router.Providers()})
}

func (d *Dashboard) refreshProviders(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithTimeout(req.Context(), 90*time.Second)
	defer cancel()
	d.server.router.Refresh(ctx)
	own, ok, errMsg := d.server.router.OwnModels()
	writeJSON(w, 200, map[string]any{"models": d.server.router.Models(), "siwc_models": own, "siwc_catalog_ok": ok, "siwc_error": errMsg, "siwc_signed_in": d.server.auth.store.get().Credential != nil, "siwc_disabled_models": append([]string{}, d.server.auth.store.get().DisabledModels["siwc"]...), "providers": d.server.router.Providers()})
}

// Preset catalog is public metadata but is served through the authenticated,
// loopback-only dashboard. Keys and OAuth tokens are never included.
func (d *Dashboard) listProviderPresets(w http.ResponseWriter, req *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": providerPresets})
}
