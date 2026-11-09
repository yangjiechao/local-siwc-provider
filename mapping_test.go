package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMapChatToolsRoundTrip(t *testing.T) {
	request := jsonMap{
		"model": "test-model", "stream": true,
		"messages": []any{
			jsonMap{"role": "system", "content": "Be concise"},
			jsonMap{"role": "user", "content": "Use calculate"},
			jsonMap{"role": "assistant", "content": nil, "tool_calls": []any{jsonMap{"id": "call_1", "type": "function", "function": jsonMap{"name": "calculate", "arguments": "{\"n\":2}"}}}},
			jsonMap{"role": "tool", "tool_call_id": "call_1", "content": "4"},
		},
		"tools": []any{jsonMap{"type": "function", "function": jsonMap{"name": "calculate", "description": "Compute value", "parameters": jsonMap{"type": "object", "properties": jsonMap{"n": jsonMap{"type": "integer"}}}}}},
	}
	got, err := mapChatToResponses(request)
	if err != nil {
		t.Fatal(err)
	}
	if got["store"] != false || got["stream"] != true {
		t.Fatal("must enforce store:false stream:true")
	}
	tools := got["tools"].([]any)
	if tools[0].(map[string]any)["type"] != "namespace" {
		t.Fatal("expected namespace")
	}
	input := got["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("want three items, got %d", len(input))
	}
	if input[1].(map[string]any)["type"] != "function_call" {
		t.Fatal("function call lost")
	}
	if input[2].(map[string]any)["type"] != "function_call_output" {
		t.Fatal("tool output lost")
	}
}
func TestRejectUnsupportedParameters(t *testing.T) {
	for _, key := range []string{"temperature", "max_completion_tokens", "response_format"} {
		_, err := mapChatToResponses(jsonMap{"model": "m", "messages": []any{jsonMap{"role": "user", "content": "hi"}}, key: 1})
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("missing rejection of %s: %v", key, err)
		}
	}
}
func TestResponsesValidation(t *testing.T) {
	good := jsonMap{"model": "m", "input": []any{jsonMap{"role": "user", "content": "hello"}}, "tools": []any{jsonMap{"type": "namespace", "name": "local", "tools": []any{}}}}
	if err := validateResponses(good); err != nil {
		t.Fatal(err)
	}
	good["temperature"] = 0.5
	if err := validateResponses(good); err == nil {
		t.Fatal("must reject unsupported temperature")
	}
	delete(good, "temperature")
	good["tools"] = []any{jsonMap{"type": "code_interpreter"}}
	if err := validateResponses(good); err == nil {
		t.Fatal("must reject hosted tool")
	}
}
func TestResponseToCompletion(t *testing.T) {
	upstream := jsonMap{"id": "resp_1", "created_at": float64(20), "status": "completed", "output": []any{
		jsonMap{"type": "message", "content": []any{jsonMap{"type": "output_text", "text": "Hi"}}},
		jsonMap{"type": "function_call", "call_id": "call_2", "name": "work", "arguments": "{}"},
	}, "usage": jsonMap{"input_tokens": float64(10), "output_tokens": float64(5), "total_tokens": float64(15)}}
	got := responseToCompletion(upstream, "model")
	choice := got["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatal(choice)
	}
	msg := choice["message"].(map[string]any)
	if msg["content"] != "Hi" {
		t.Fatal(msg)
	}
	if len(msg["tool_calls"].([]any)) != 1 {
		t.Fatal(msg)
	}
}
func TestSSECompletesOrFails(t *testing.T) {
	data := `event: response.created
data: {"type":"response.created","response":{"id":"resp_test"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_test","output":[],"status":"completed"}}

`
	got, err := terminalResponse(strings.NewReader(data))
	if err != nil || got["id"] != "resp_test" {
		t.Fatalf("%v: %v", got, err)
	}
	_, err = terminalResponse(strings.NewReader(`data: {"type":"response.created"}

`))
	if err == nil {
		t.Fatal("must not accept truncated SSE")
	}
	_, err = terminalResponse(strings.NewReader(`data: {"type":"response.failed"}

`))
	if err == nil {
		t.Fatal("must not accept failed SSE")
	}
}
func TestChatSSE(t *testing.T) {
	input := `data: {"type":"response.output_text.delta","delta":"hel"}

data: {"type":"response.output_text.delta","delta":"lo"}

data: {"type":"response.completed","response":{"output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}

`
	// An httptest recorder is sufficient for verifying serialized chunks.
	w := httptest.NewRecorder()
	chatSSE(w, strings.NewReader(input), "m")
	if !strings.Contains(w.Body.String(), `"content":"hel"`) || !strings.Contains(w.Body.String(), `"content":"lo"`) || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatal(w.Body.String())
	}
}

func TestStorePermissionsAndUUID(t *testing.T) {
	file := filepath.Join(t.TempDir(), "secret", "state.json")
	s, err := newStore(file)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Mode().Perm() != 0600 {
		t.Fatalf("permissions=%v", meta.Mode().Perm())
	}
	if !strings.HasPrefix(s.get().HostID, "urn:uuid:") {
		t.Fatal("bad host ID")
	}
	if _, err := newStore(file); err != nil {
		t.Fatal(err)
	}
	if err := s.saveCredential(&Credential{ClientID: "oaiapp_test", ExpiresAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var decoded State
	if err := json.Unmarshal(b, &decoded); err != nil || decoded.Credential == nil {
		t.Fatalf("store invalid: %v", err)
	}
}
