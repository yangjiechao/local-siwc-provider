package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const maxBodySize = 8 << 20

type Server struct {
	auth     *Auth
	client   *http.Client
	localKey string
}

func newServer(a *Auth) *Server {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 45 * time.Second
	tr.TLSHandshakeTimeout = 10 * time.Second
	return &Server{auth: a, localKey: a.store.get().LocalKey, client: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func writeError(w http.ResponseWriter, status int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(jsonMap{"error": jsonMap{"message": msg, "type": kind, "code": kind}})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func readJSON(r *http.Request) (jsonMap, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBodySize {
		return nil, errors.New("request exceeds 8 MiB")
	}
	return decodeJSON(b)
}
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			writeError(w, 403, "forbidden_origin", "Browser cross-origin calls are not supported")
			return
		}
		if r.Host != "" {
			host, _, e := net.SplitHostPort(r.Host)
			if e != nil {
				host = r.Host
			}
			if host != "127.0.0.1" {
				writeError(w, 403, "invalid_host", "Only 127.0.0.1 is permitted")
				return
			}
		}
		if r.URL.Path != "/healthz" {
			bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(bearer), []byte(s.localKey)) != 1 {
				writeError(w, 401, "authentication_error", "Invalid local bearer key")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, jsonMap{"status": "ok", "version": "0.1.0-dev", "signed_in": s.auth.store.get().Credential != nil})
	})
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("POST /v1/responses", s.responses)
	mux.HandleFunc("POST /v1/chat/completions", s.chatCompletions)
	return s.authMiddleware(mux)
}
func (s *Server) upstream(ctx context.Context, method, path string, body any) (*http.Response, error) {
	token, err := s.auth.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, resource+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return s.client.Do(req)
}
func upstreamFailure(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	// Preserve original HTTP status and useful upstream error codes without leaking tokens.
	var val any
	if json.Unmarshal(data, &val) == nil {
		writeJSON(w, resp.StatusCode, val)
		return
	}
	writeError(w, resp.StatusCode, "upstream_error", fmt.Sprintf("Upstream HTTP %d", resp.StatusCode))
}
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	resp, err := s.upstream(r.Context(), "GET", "/models", nil)
	if err != nil {
		writeError(w, 503, "upstream_unavailable", err.Error())
		return
	}
	if resp.StatusCode != 200 {
		upstreamFailure(w, resp)
		return
	}
	defer resp.Body.Close()
	var payload struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload); err != nil {
		writeError(w, 502, "invalid_upstream", "Invalid model catalog")
		return
	}
	items := make([]any, 0, len(payload.Models))
	for _, m := range payload.Models {
		if m.Visibility == "list" && m.Slug != "" {
			items = append(items, jsonMap{"id": m.Slug, "object": "model", "created": 0, "owned_by": "openai"})
		}
	}
	writeJSON(w, 200, jsonMap{"object": "list", "data": items})
}
func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	request, err := readJSON(r)
	if err != nil {
		writeError(w, 400, "invalid_request_error", err.Error())
		return
	}
	if err = validateResponses(request); err != nil {
		writeError(w, 400, "unsupported_capability", err.Error())
		return
	}
	stream, _ := request["stream"].(bool)
	request["store"] = false
	request["stream"] = true
	resp, err := s.upstream(r.Context(), "POST", "/responses", request)
	if err != nil {
		writeError(w, 503, "upstream_unavailable", err.Error())
		return
	}
	if resp.StatusCode != 200 {
		upstreamFailure(w, resp)
		return
	}
	defer resp.Body.Close()
	if stream {
		proxySSE(w, resp.Body)
		return
	}
	terminal, err := terminalResponse(resp.Body)
	if err != nil {
		writeError(w, 502, "incomplete_stream", err.Error())
		return
	}
	writeJSON(w, 200, terminal)
}
func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	req, err := readJSON(r)
	if err != nil {
		writeError(w, 400, "invalid_request_error", err.Error())
		return
	}
	upstreamReq, err := mapChatToResponses(req)
	if err != nil {
		writeError(w, 400, "unsupported_capability", err.Error())
		return
	}
	resp, err := s.upstream(r.Context(), "POST", "/responses", upstreamReq)
	if err != nil {
		writeError(w, 503, "upstream_unavailable", err.Error())
		return
	}
	if resp.StatusCode != 200 {
		upstreamFailure(w, resp)
		return
	}
	defer resp.Body.Close()
	model, _ := req["model"].(string)
	stream, _ := req["stream"].(bool)
	if stream {
		chatSSE(w, resp.Body, model)
		return
	}
	terminal, err := terminalResponse(resp.Body)
	if err != nil {
		writeError(w, 502, "incomplete_stream", err.Error())
		return
	}
	writeJSON(w, 200, responseToCompletion(terminal, model))
}

type SSEEvent struct {
	Event string
	Data  []byte
}

func eachSSE(reader io.Reader, fn func(SSEEvent) error) error {
	r := bufio.NewReaderSize(reader, 32<<10)
	var event SSEEvent
	for {
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(event.Data) > 0 {
				if e := fn(event); e != nil {
					return e
				}
			}
			event = SSEEvent{}
		} else if strings.HasPrefix(line, "event:") {
			event.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			if len(event.Data) > 10<<20 {
				return errors.New("SSE event too large")
			}
			if len(event.Data) > 0 {
				event.Data = append(event.Data, '\n')
			}
			event.Data = append(event.Data, strings.TrimPrefix(line, "data:")...)
		}
		if err == io.EOF {
			if len(event.Data) > 0 {
				return fn(event)
			}
			return nil
		}
	}
}

var errEventFailed = errors.New("inference failed")

func terminalResponse(body io.Reader) (jsonMap, error) {
	var final jsonMap
	err := eachSSE(body, func(ev SSEEvent) error {
		data, err := decodeJSON(ev.Data)
		if err != nil {
			if string(ev.Data) == "[DONE]" {
				return nil
			}
			return err
		}
		typ, _ := data["type"].(string)
		switch typ {
		case "response.completed":
			if value, ok := data["response"].(map[string]any); ok {
				final = value
			}
		case "response.failed":
			return fmt.Errorf("%w: upstream returned response.failed", errEventFailed)
		case "response.incomplete":
			return errors.New("inference incomplete")
		case "error":
			return errors.New("upstream stream error")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if final == nil {
		return nil, errors.New("upstream stream ended before response.completed")
	}
	return final, nil
}
func proxySSE(w http.ResponseWriter, body io.Reader) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming_not_supported", "ResponseWriter cannot flush")
		return
	}
	buf := make([]byte, 16<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, e := w.Write(buf[:n]); e != nil {
				return
			}
			flusher.Flush()
		}
		if err != nil {
			return
		}
	}
}
func writeSSE(w http.ResponseWriter, kind string, payload any) {
	data, _ := json.Marshal(payload)
	if kind != "" {
		fmt.Fprintf(w, "event: %s\n", kind)
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
func chatSSE(w http.ResponseWriter, body io.Reader, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	created := time.Now().Unix()
	id := "chatcmpl-local"
	nextToolIndex := 0
	indices := map[string]int{}
	completed := false
	finish := "stop"
	chunk := func(delta jsonMap, reason any, usage any) {
		val := jsonMap{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{jsonMap{"index": 0, "delta": delta, "finish_reason": reason}}}
		if usage != nil {
			val["usage"] = usage
		}
		writeSSE(w, "", val)
	}
	chunk(jsonMap{"role": "assistant"}, nil, nil)
	err := eachSSE(body, func(ev SSEEvent) error {
		data, e := decodeJSON(ev.Data)
		if e != nil {
			if string(ev.Data) == "[DONE]" {
				return nil
			}
			return e
		}
		typ, _ := data["type"].(string)
		switch typ {
		case "response.created":
			if resp, ok := data["response"].(map[string]any); ok {
				if v, ok := resp["id"].(string); ok {
					id = v
				}
			}
		case "response.output_text.delta":
			if t, ok := data["delta"].(string); ok {
				chunk(jsonMap{"content": t}, nil, nil)
			}
		case "response.output_item.added":
			item, ok := data["item"].(map[string]any)
			if !ok || item["type"] != "function_call" {
				break
			}
			callID, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			itemID, _ := item["id"].(string)
			idx := nextToolIndex
			nextToolIndex++
			if itemID != "" {
				indices[itemID] = idx
			}
			if callID != "" {
				indices[callID] = idx
			}
			finish = "tool_calls"
			chunk(jsonMap{"tool_calls": []any{jsonMap{"index": idx, "id": callID, "type": "function", "function": jsonMap{"name": name, "arguments": ""}}}}, nil, nil)
		case "response.function_call_arguments.delta":
			itemID, _ := data["item_id"].(string)
			idx, ok := indices[itemID]
			if !ok {
				break
			}
			arg, _ := data["delta"].(string)
			chunk(jsonMap{"tool_calls": []any{jsonMap{"index": idx, "function": jsonMap{"arguments": arg}}}}, nil, nil)
		case "response.completed":
			completed = true
			var usage any
			if resp, ok := data["response"].(map[string]any); ok {
				if u, ok := resp["usage"].(map[string]any); ok {
					usage = jsonMap{"prompt_tokens": u["input_tokens"], "completion_tokens": u["output_tokens"], "total_tokens": u["total_tokens"]}
				}
				if out, ok := resp["output"].([]any); ok {
					for _, raw := range out {
						if it, ok := raw.(map[string]any); ok && it["type"] == "function_call" {
							finish = "tool_calls"
						}
					}
				}
			}
			chunk(jsonMap{}, finish, usage)
		case "response.failed", "response.incomplete", "error":
			return errEventFailed
		}
		return nil
	})
	if err != nil || !completed {
		writeSSE(w, "", jsonMap{"error": jsonMap{"message": "Upstream response did not complete; discard partial result", "type": "incomplete_stream"}})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
