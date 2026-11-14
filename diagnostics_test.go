package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestDiagnosticsHistoryBounded(t *testing.T) {
	var d requestDiagnostics
	for i := 0; i < diagnosticHistoryLimit+20; i++ {
		span := d.begin("siwc", "gpt-test", "/responses", 250, 1)
		if i%10 == 0 {
			span.finish(http.StatusTooManyRequests)
		} else {
			span.finish(http.StatusOK)
		}
	}
	snap := d.snapshot()
	if got := snap["total"].(uint64); got != diagnosticHistoryLimit+20 {
		t.Fatalf("total = %d", got)
	}
	if got := snap["active"].(int); got != 0 {
		t.Fatalf("active = %d", got)
	}
	if got := len(snap["requests"].([]requestDiagnostic)); got != diagnosticHistoryLimit {
		t.Fatalf("recent records = %d", got)
	}
	if got := snap["rate_limited"].(uint64); got != 12 {
		t.Fatalf("rate limited = %d", got)
	}
	for _, item := range snap["requests"].([]requestDiagnostic) {
		if item.Provider != "siwc" || item.Model != "gpt-test" || item.RequestBytes != 250 {
			t.Fatalf("unexpected record metadata: %+v", item)
		}
	}
}

func TestDiagnosticResponseWriterStatusAndFlush(t *testing.T) {
	recorder := httptest.NewRecorder()
	w := &diagnosticResponseWriter{ResponseWriter: recorder}
	w.Header().Set("Content-Type", "text/event-stream")
	if _, err := w.Write([]byte("data: test\n\n")); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	if !recorder.Flushed || w.status != http.StatusOK {
		t.Fatalf("flush=%v status=%d", recorder.Flushed, w.status)
	}
	w.WriteHeader(http.StatusTooManyRequests)
	if w.status != http.StatusOK {
		t.Fatalf("first response status should be preserved, got %d", w.status)
	}
}

func TestSIWCRetryDelayFromHeaderOrBody(t *testing.T) {
	h := make(http.Header)
	h.Set("Retry-After", "3.5")
	if got := siwcRetryDelay(h, []byte("Please try again in 9s")); got != 3500*time.Millisecond {
		t.Fatalf("header retry delay: %v", got)
	}
	if got := siwcRetryDelay(make(http.Header), []byte("Please try again in 2.909s.")); got != 2909*time.Millisecond {
		t.Fatalf("body retry delay: %v", got)
	}
	if got := siwcRetryDelay(make(http.Header), []byte("{}")); got != 2*time.Second {
		t.Fatalf("default retry delay: %v", got)
	}
}

func TestForwardRateLimitHeadersAllowlist(t *testing.T) {
	source := make(http.Header)
	source.Set("Retry-After", "4")
	source.Set("X-Ratelimit-Remaining-Tokens", "500")
	source.Set("Authorization", "Bearer must-not-leak")
	target := make(http.Header)
	forwardRateLimitHeaders(target, source)
	if target.Get("Retry-After") != "4" || target.Get("X-Ratelimit-Remaining-Tokens") != "500" {
		t.Fatalf("missing rate-limit headers: %v", target)
	}
	if strings.Contains(target.Get("Authorization"), "must-not-leak") {
		t.Fatal("sensitive header leaked")
	}
}
