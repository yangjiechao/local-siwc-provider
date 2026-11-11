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
	writeJSON(w, 200, map[string]any{"providers": d.server.router.Providers(), "models": d.server.router.Models()})
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
	writeJSON(w, 200, map[string]any{"models": d.server.router.Models(), "siwc_models": own, "siwc_catalog_ok": ok, "siwc_error": errMsg, "providers": d.server.router.Providers()})
}

// Preset catalog is public metadata but is served through the authenticated,
// loopback-only dashboard. Keys and OAuth tokens are never included.
func (d *Dashboard) listProviderPresets(w http.ResponseWriter, req *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": providerPresets})
}
