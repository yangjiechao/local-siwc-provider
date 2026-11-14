package main

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxSIWCRetryWait = 8 * time.Second

var retryDelayPattern = regexp.MustCompile(`(?i)try again in ([0-9]+(?:\.[0-9]+)?)s`)

// The rate-limit error sometimes carries the retry delay in JSON rather than
// Retry-After. Do not retry for an arbitrary length of time.
func siwcRetryDelay(headers http.Header, errorBody []byte) time.Duration {
	if header := strings.TrimSpace(headers.Get("Retry-After")); header != "" {
		if seconds, err := strconv.ParseFloat(header, 64); err == nil && seconds >= 0 {
			return time.Duration(seconds * float64(time.Second))
		}
		if at, err := http.ParseTime(header); err == nil {
			wait := time.Until(at)
			if wait < 0 {
				return 0
			}
			return wait
		}
	}
	if match := retryDelayPattern.FindSubmatch(errorBody); len(match) == 2 {
		if seconds, err := strconv.ParseFloat(string(match[1]), 64); err == nil && seconds >= 0 {
			return time.Duration(seconds * float64(time.Second))
		}
	}
	return 2 * time.Second
}

func forwardRateLimitHeaders(dst http.Header, src http.Header) {
	for _, name := range []string{
		"Retry-After",
		"X-Ratelimit-Limit-Requests", "X-Ratelimit-Limit-Tokens",
		"X-Ratelimit-Remaining-Requests", "X-Ratelimit-Remaining-Tokens",
		"X-Ratelimit-Reset-Requests", "X-Ratelimit-Reset-Tokens",
	} {
		if value := src.Get(name); value != "" {
			dst.Set(name, value)
		}
	}
}

// Restores the response body if a 429 cannot be retried. The caller still gets
// the original upstream status and error payload.
func restoreResponseBody(response *http.Response, data []byte) {
	response.Body = io.NopCloser(bytes.NewReader(data))
}

type releaseBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (body *releaseBody) Close() error {
	err := body.ReadCloser.Close()
	body.once.Do(body.release)
	return err
}
