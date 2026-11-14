package main

import (
	"net/http"
	"sync"
	"time"
)

// Request metadata is process-local. Never persist prompts, responses, auth headers or keys.
const diagnosticHistoryLimit = 100
const siwcMaxConcurrent = 2

type requestDiagnostic struct {
	StartedAt   string `json:"started_at"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Endpoint    string `json:"endpoint"`
	RequestBytes int   `json:"request_bytes"`
	MessageCount int   `json:"message_count"`
	DurationMS  int64  `json:"duration_ms"`
	Status      int    `json:"status"`
	RetryCount  int    `json:"retry_count"`
}

type requestDiagnostics struct {
	mu          sync.Mutex
	total       uint64
	rateLimited uint64
	active      int
	recent      []requestDiagnostic
}

type diagnosticSpan struct {
	owner    *requestDiagnostics
	start    time.Time
	record   requestDiagnostic
	retries  int
}

type diagnosticContextKey struct{}

func countInputItems(body map[string]any) int {
	if messages, ok := body["messages"].([]any); ok {
		return len(messages)
	}
	if input, ok := body["input"].([]any); ok {
		return len(input)
	}
	if input, ok := body["input"].(string); ok && input != "" {
		return 1
	}
	return 0
}

func (d *requestDiagnostics) begin(provider, model, endpoint string, bodyBytes int, inputItems int) *diagnosticSpan {
	t := time.Now()
	span := &diagnosticSpan{owner: d, start: t, record: requestDiagnostic{
		StartedAt: t.Format(time.RFC3339), Provider: provider, Model: model,
		Endpoint: endpoint, RequestBytes: bodyBytes, MessageCount: inputItems,
	}}
	d.mu.Lock()
	d.total++
	d.active++
	d.mu.Unlock()
	return span
}

func (s *diagnosticSpan) finish(status int) {
	if status == 0 {
		status = http.StatusOK
	}
	s.record.Status = status
	s.record.DurationMS = time.Since(s.start).Milliseconds()
	s.record.RetryCount = s.retries
	d := s.owner
	d.mu.Lock()
	defer d.mu.Unlock()
	d.active--
	if status == http.StatusTooManyRequests {
		d.rateLimited++
	}
	// Most recent request first, bounded memory.
	d.recent = append([]requestDiagnostic{s.record}, d.recent...)
	if len(d.recent) > diagnosticHistoryLimit {
		d.recent = d.recent[:diagnosticHistoryLimit]
	}
}

func (d *requestDiagnostics) snapshot() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	copyRecent := append([]requestDiagnostic(nil), d.recent...)
	return map[string]any{
		"total": d.total, "rate_limited": d.rateLimited, "active": d.active,
		"siwc_concurrency_limit": siwcMaxConcurrent, "requests": copyRecent,
	}
}

// Preserve the upstream HTTP status while passing through streaming/flush writes.
type diagnosticResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *diagnosticResponseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *diagnosticResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *diagnosticResponseWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *diagnosticResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
