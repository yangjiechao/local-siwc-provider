package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newImageTestServer(t *testing.T, roundTrip mockRoundTrip) *Server {
	t.Helper()
	store, err := newStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.upsertProvider(ExternalProvider{
		ID: "openai-api", Name: "Paid OpenAI", Kind: "openai-compatible",
		BaseURL: "https://api.example.org/v1", APIKey: "paid-upstream-only", Enabled: true,
		ManualModels: []string{"gpt-image-2.5-sunburst", "image-model"},
	}); err != nil {
		t.Fatal(err)
	}
	server := newServer(newAuth(store))
	server.router.client = &http.Client{Transport: roundTrip}
	return server
}

func callImageAPI(t *testing.T, server *Server, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8087"+path, body)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Host = "127.0.0.1:8087"
	req.Header.Set("Authorization", "Bearer "+server.auth.store.get().LocalKey)
	req.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(response, req)
	return response
}

func TestImagesGenerationsJSONForwardAndModelRewrite(t *testing.T) {
	calls := 0
	server := newImageTestServer(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != "https://api.example.org/v1/images/generations" || req.Method != http.MethodPost {
			t.Errorf("wrong endpoint: %s %s", req.Method, req.URL.String())
		}
		if req.Header.Get("Authorization") != "Bearer paid-upstream-only" {
			t.Error("wrong upstream auth")
		}
		if req.Header.Get("Content-Type") != "application/json" {
			t.Error("wrong JSON content type")
		}
		var data map[string]any
		if err := json.NewDecoder(req.Body).Decode(&data); err != nil {
			t.Fatal(err)
		}
		if data["model"] != "gpt-image-2.5-sunburst" || data["prompt"] != "a playful cat" || data["size"] != "1024x1024" {
			t.Errorf("JSON fields not preserved / model not rewritten: %v", data)
		}
		return testResponse(200, "application/json", `{"created":123,"data":[{"b64_json":"aGVsbG8="}]}`), nil
	})
	body := `{"model":"openai-api/gpt-image-2.5-sunburst","prompt":"a playful cat","size":"1024x1024","n":1}`
	out := callImageAPI(t, server, "/v1/images/generations", "application/json", strings.NewReader(body))
	if calls != 1 || out.Code != 200 || !strings.Contains(out.Body.String(), `"b64_json":"aGVsbG8="`) {
		t.Fatalf("wrong generation response %d %s (calls=%d)", out.Code, out.Body.String(), calls)
	}
}

func TestImagesEditsJSONReferences(t *testing.T) {
	server := newImageTestServer(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/images/edits" {
			t.Errorf("wrong path: %q", req.URL.Path)
		}
		var data map[string]any
		if err := json.NewDecoder(req.Body).Decode(&data); err != nil {
			t.Fatal(err)
		}
		if data["model"] != "image-model" || data["prompt"] != "add snow" || data["images"] == nil {
			t.Errorf("JSON image edits fields lost: %#v", data)
		}
		return testResponse(200, "application/json", `{"data":[{"b64_json":"YWJj"}]}`), nil
	})
	body := `{"model":"openai-api/image-model","prompt":"add snow","images":[{"image_url":"https://assets.example.org/image.png"}],"mask":{"image_url":"https://assets.example.org/mask.png"}}`
	got := callImageAPI(t, server, "/v1/images/edits", "application/json; charset=utf-8", strings.NewReader(body))
	if got.Code != 200 {
		t.Fatalf("JSON edits returned %d %s", got.Code, got.Body.String())
	}
}

func TestImagesEditsMultipartImageMaskAndLateModel(t *testing.T) {
	image := []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 255, 0, 0x89}
	mask := []byte{0x89, 'P', 'N', 'G', 0, 9, 8, 7}
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	field, err := form.CreateFormFile("image[]", "source one.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := field.Write(image); err != nil {
		t.Fatal(err)
	}
	field, err = form.CreateFormFile("image[]", "source two.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := field.Write([]byte("second image")); err != nil {
		t.Fatal(err)
	}
	field, err = form.CreateFormFile("mask", "mask.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := field.Write(mask); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("prompt", "Replace only the masked area"); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("model", "openai-api/gpt-image-2.5-sunburst"); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("quality", "high"); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}

	server := newImageTestServer(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/images/edits" {
			t.Errorf("wrong path: %q", req.URL.Path)
		}
		media, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
		if err != nil || media != "multipart/form-data" {
			t.Fatalf("broken outbound multipart: %v %s", err, media)
		}
		reader, err := req.MultipartReader()
		if err != nil {
			t.Fatal(err)
		}
		got := map[string][]byte{}
		var images [][]byte
		var names []string
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(part)
			if err != nil {
				t.Fatal(err)
			}
			got[part.FormName()] = b
			if part.FormName() == "image[]" {
				images = append(images, b)
				names = append(names, part.FileName())
			}
		}
		if len(images) != 2 || !bytes.Equal(images[0], image) || !bytes.Equal(images[1], []byte("second image")) || names[0] != "source one.png" || names[1] != "source two.png" || !bytes.Equal(got["mask"], mask) || string(got["model"]) != "gpt-image-2.5-sunburst" || string(got["prompt"]) != "Replace only the masked area" || string(got["quality"]) != "high" {
			t.Errorf("multipart content corrupted: %#v", got)
		}
		return testResponse(200, "application/json", `{"data":[{"b64_json":"aW1hZ2U="}]}`), nil
	})
	got := callImageAPI(t, server, "/v1/images/edits", form.FormDataContentType(), bytes.NewReader(upload.Bytes()))
	if got.Code != 200 || !strings.Contains(got.Body.String(), "aW1hZ2U=") {
		t.Fatalf("edits returned %d %s", got.Code, got.Body.String())
	}
}

func TestImagesNeverUseSIWCOrSwitchProviders(t *testing.T) {
	calls := 0
	server := newImageTestServer(t, func(req *http.Request) (*http.Response, error) {
		calls++
		return testResponse(429, "application/json", `{"error":{"code":"rate_limit_exceeded"}}`), nil
	})
	for _, input := range []string{
		`{"model":"gpt-image-2.5-sunburst","prompt":"x"}`,
		`{"model":"siwc/gpt-image-2.5-sunburst","prompt":"x"}`,
		`{"model":"missing/gpt-image-2.5-sunburst","prompt":"x"}`,
		`{"model":"openai-api/unknown-model","prompt":"x"}`,
	} {
		got := callImageAPI(t, server, "/v1/images/generations", "application/json", strings.NewReader(input))
		if got.Code < 400 || got.Code >= 500 {
			t.Errorf("unsafe route accepted %d: %s", got.Code, input)
		}
	}
	if calls != 0 {
		t.Fatalf("unexpected charged requests: %d", calls)
	}
	got := callImageAPI(t, server, "/v1/images/generations", "application/json", strings.NewReader(`{"model":"openai-api/image-model","prompt":"x"}`))
	if got.Code != http.StatusTooManyRequests || calls != 1 || !strings.Contains(got.Body.String(), "rate_limit_exceeded") {
		t.Fatalf("rate limit not preserved: %d %s calls=%d", got.Code, got.Body.String(), calls)
	}
}

func TestImageInputValidationAndAuth(t *testing.T) {
	calls := 0
	server := newImageTestServer(t, func(req *http.Request) (*http.Response, error) {
		calls++
		return testResponse(200, "application/json", `{}`), nil
	})
	cases := []struct {
		path, contentType, body string
		want                    int
	}{
		{"/v1/images/edits", "text/plain", "hi", 415},
		{"/v1/images/generations", "multipart/form-data; boundary=test", "", 415},
		{"/v1/images/generations", "application/json", `{"prompt":"hi"}`, 400},
		{"/v1/images/edits", "application/json", `{"model":2}`, 400},
	}
	for _, tc := range cases {
		got := callImageAPI(t, server, tc.path, tc.contentType, strings.NewReader(tc.body))
		if got.Code != tc.want {
			t.Errorf("%s: status %d want %d (%s)", tc.path, got.Code, tc.want, got.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("untrusted input triggered upstream calls: %d", calls)
	}

	var buf bytes.Buffer
	m := multipart.NewWriter(&buf)
	if err := m.WriteField("model", "openai-api/image-model"); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteField("model", "openai-api/image-model"); err != nil {
		t.Fatal(err)
	}
	m.Close()
	got := callImageAPI(t, server, "/v1/images/edits", m.FormDataContentType(), &buf)
	if got.Code != 400 || calls != 0 {
		t.Fatalf("duplicate model should be rejected: %d", got.Code)
	}

	unauth := httptest.NewRequest("POST", "http://127.0.0.1:8087/v1/images/generations", strings.NewReader(`{"model":"openai-api/image-model"}`))
	unauth.Host = "127.0.0.1:8087"
	unauth.RemoteAddr = "127.0.0.1:12345"
	unauth.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(w, unauth)
	if w.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("API auth bypass: %d", w.Code)
	}
}

func TestImageResponseHeadersAndStreaming(t *testing.T) {
	server := newImageTestServer(t, func(req *http.Request) (*http.Response, error) {
		r := testResponse(429, "application/json", `{"error":{"message":"provider rate limit"}}`)
		r.Header.Set("Retry-After", "15")
		r.Header.Set("X-Request-Id", "req_image_test")
		return r, nil
	})
	got := callImageAPI(t, server, "/v1/images/generations", "application/json", strings.NewReader(`{"model":"openai-api/image-model","prompt":"x"}`))
	if got.Code != 429 || got.Header().Get("Retry-After") != "15" || got.Header().Get("X-Request-Id") != "req_image_test" {
		t.Fatalf("error response malformed: %d headers=%v", got.Code, got.Header())
	}
	server.router.client = &http.Client{Transport: mockRoundTrip(func(req *http.Request) (*http.Response, error) {
		return testResponse(200, "text/event-stream", "data: {\"type\":\"image_generation.completed\"}\n\n"), nil
	})}
	got = callImageAPI(t, server, "/v1/images/generations", "application/json", strings.NewReader(`{"model":"openai-api/image-model","prompt":"x","stream":true}`))
	if got.Code != 200 || !strings.Contains(got.Body.String(), "image_generation.completed") || got.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream malformed: %d %s", got.Code, got.Body.String())
	}
}

func TestImagesRequestMaxBodyLimit(t *testing.T) {
	server := newImageTestServer(t, func(req *http.Request) (*http.Response, error) {
		t.Fatal("oversized request should never reach upstream")
		return testResponse(200, "application/json", "{}"), nil
	})
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8087/v1/images/generations", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:5555"
	req.Host = "127.0.0.1:8087"
	req.ContentLength = maxImageRequestBytes + 1
	req.Header.Set("Authorization", "Bearer "+server.auth.store.get().LocalKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body was not rejected: %d %s", w.Code, w.Body.String())
	}
}
