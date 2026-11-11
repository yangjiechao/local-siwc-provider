package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
)

// Image generation is deliberately routed ONLY to an explicitly selected,
// independently billed external API provider. The SIWC OAuth token must never
// be used for /images/*, and a failed image request must never trigger a
// fallback to another provider or billing route.
//
// Uploaded image parts are staged in private temporary files to bound memory
// usage for multi-image requests. They are deleted as soon as the request ends.
const maxImageRequestBytes int64 = 128 << 20 // 128 MiB, including multipart overhead
const maxImageModelBytes = 1024

func (s *Server) routeImages(w http.ResponseWriter, req *http.Request) {
	if s.router == nil {
		writeError(w, http.StatusNotImplemented, "images_unavailable", "Images requires a configured external API provider")
		return
	}
	var endpointPath string
	switch req.URL.Path {
	case "/v1/images/generations":
		endpointPath = "/images/generations"
	case "/v1/images/edits":
		endpointPath = "/images/edits"
	default:
		writeError(w, http.StatusNotFound, "not_found", "unsupported image endpoint")
		return
	}

	// Reject declared oversized uploads before allocating memory or staging files.
	// MaxBytesReader below also enforces the limit for chunked/unknown lengths.
	if req.ContentLength > maxImageRequestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "Image request exceeds the 128 MiB limit")
		return
	}
	mediaType, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json or multipart/form-data")
		return
	}
	var model, contentType string
	var payload io.Reader
	var closeBody func()
	switch mediaType {
	case "application/json":
		payloadBytes, modelName, readErr := readImageJSON(w, req)
		if readErr != nil {
			writeImageInputError(w, readErr)
			return
		}
		model, contentType = modelName, "application/json"
		// JSON is rewritten after the provider has been resolved.
		payload = bytes.NewReader(payloadBytes)
	case "multipart/form-data":
		if endpointPath != "/images/edits" {
			writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Image generation uses application/json")
			return
		}
		boundary := params["boundary"]
		if boundary == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "multipart boundary missing")
			return
		}
		source, modelName, readErr := stageImageMultipart(w, req, boundary)
		if readErr != nil {
			writeImageInputError(w, readErr)
			return
		}
		model = modelName
		closeBody = func() { source.Close(); os.Remove(source.Name()) }
		defer closeBody()
		payload = source
	default:
		writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Image generation needs JSON; edits accept JSON or multipart/form-data")
		return
	}

	// Requiring a qualified model prevents accidental charges when an unprefixed
	// name collides with an SIWC model or another external model vendor.
	providerID, _, qualified := strings.Cut(model, "/")
	if !qualified || providerID == "siwc" || providerID == "" {
		writeError(w, http.StatusBadRequest, "provider_required", "Images require an external provider/model ID, for example openai-api/gpt-image-2.5-sunburst; SIWC images are unsupported")
		return
	}
	provider, upstreamModel, err := s.router.Resolve(model)
	if err != nil {
		writeError(w, http.StatusNotFound, "model_not_found", err.Error())
		return
	}
	if provider == nil || provider.Kind != "openai-compatible" || provider.ID != providerID {
		writeError(w, http.StatusBadRequest, "unsupported_capability", "Images require an explicitly selected OpenAI-compatible, independently billed provider")
		return
	}

	if mediaType == "application/json" {
		var body map[string]json.RawMessage
		if err = json.NewDecoder(payload).Decode(&body); err != nil || body == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "JSON object required")
			return
		}
		body["model"], _ = json.Marshal(upstreamModel)
		converted, e := json.Marshal(body)
		if e != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "could not encode image request")
			return
		}
		payload = bytes.NewReader(converted)
	} else {
		source := payload.(*os.File)
		output, rewrittenType, rewriteErr := rewriteImageMultipart(source, params["boundary"], upstreamModel)
		if rewriteErr != nil {
			writeImageInputError(w, rewriteErr)
			return
		}
		defer func() { output.Close(); os.Remove(output.Name()) }()
		payload, contentType = output, rewrittenType
	}

	s.router.proxyImageRequest(w, req, *provider, endpointPath, contentType, payload)
}

func imageRequestLimit(w http.ResponseWriter, req *http.Request) io.Reader {
	return http.MaxBytesReader(w, req.Body, maxImageRequestBytes)
}

func readImageJSON(w http.ResponseWriter, req *http.Request) ([]byte, string, error) {
	b, err := io.ReadAll(imageRequestLimit(w, req))
	if err != nil {
		return nil, "", err
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(b, &body); err != nil || body == nil {
		return nil, "", errors.New("JSON object required")
	}
	var model string
	if err := json.Unmarshal(body["model"], &model); err != nil || model == "" {
		return nil, "", errors.New("explicit model string required")
	}
	if len(model) > maxImageModelBytes {
		return nil, "", errors.New("model ID too long")
	}
	return b, model, nil
}

func stageImageMultipart(w http.ResponseWriter, req *http.Request, boundary string) (*os.File, string, error) {
	source, err := os.CreateTemp("", "siwc-images-input-*")
	if err != nil {
		return nil, "", errors.New("unable to stage image upload")
	}
	clean := func() { source.Close(); os.Remove(source.Name()) }
	if _, err = io.Copy(source, imageRequestLimit(w, req)); err != nil {
		clean()
		return nil, "", err
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		clean()
		return nil, "", err
	}
	reader := multipart.NewReader(source, boundary)
	var model string
	seen := 0
	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			clean()
			return nil, "", errors.New("invalid multipart form")
		}
		if part.FormName() == "model" {
			seen++
			if seen > 1 || part.FileName() != "" {
				part.Close()
				clean()
				return nil, "", errors.New("provide exactly one text model field")
			}
			b, e := io.ReadAll(io.LimitReader(part, maxImageModelBytes+1))
			if e != nil || len(b) > maxImageModelBytes {
				part.Close()
				clean()
				return nil, "", errors.New("invalid multipart model field")
			}
			model = string(b)
		}
		part.Close()
	}
	if seen != 1 || model == "" {
		clean()
		return nil, "", errors.New("multipart form requires exactly one nonempty model field")
	}
	return source, model, nil
}

// rewriteImageMultipart preserves all file names, fields and MIME types while
// modifying only the multipart 'model' text value. The model may appear after
// image[] and mask parts, so resolution must happen in a separate first pass.
func rewriteImageMultipart(source *os.File, boundary, actualModel string) (*os.File, string, error) {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	output, err := os.CreateTemp("", "siwc-images-out-*")
	if err != nil {
		return nil, "", errors.New("unable to stage outbound upload")
	}
	clean := func() { output.Close(); os.Remove(output.Name()) }
	writer := multipart.NewWriter(output)
	reader := multipart.NewReader(source, boundary)
	for {
		part, e := reader.NextRawPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			clean()
			return nil, "", errors.New("invalid multipart form")
		}
		newPart, e := writer.CreatePart(part.Header)
		if e == nil {
			if part.FormName() == "model" && part.FileName() == "" {
				_, e = io.WriteString(newPart, actualModel)
			} else {
				_, e = io.Copy(newPart, part)
			}
		}
		part.Close()
		if e != nil {
			clean()
			return nil, "", errors.New("unable to rewrite image form")
		}
	}
	if err := writer.Close(); err != nil {
		clean()
		return nil, "", errors.New("unable to complete image form")
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		clean()
		return nil, "", err
	}
	return output, writer.FormDataContentType(), nil
}

func writeImageInputError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "Image request exceeds the 128 MiB limit")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
}

func (r *Router) proxyImageRequest(w http.ResponseWriter, req *http.Request, provider ExternalProvider, path, contentType string, payload io.Reader) {
	endpoint := strings.TrimRight(provider.BaseURL, "/") + path
	outbound, err := http.NewRequestWithContext(req.Context(), http.MethodPost, endpoint, payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not construct image request")
		return
	}
	outbound.Header.Set("Authorization", "Bearer "+provider.APIKey)
	outbound.Header.Set("Content-Type", contentType)
	outbound.Header.Set("Accept", "application/json, text/event-stream")
	// Reuse the externally configured client's DNS SSRF guard and TLS settings,
	// but image generation can take substantially longer than text completion.
	client := r.client
	if transport, ok := r.client.Transport.(*http.Transport); ok {
		clone := transport.Clone()
		clone.ResponseHeaderTimeout = 4 * time.Minute
		defer clone.CloseIdleConnections()
		client = &http.Client{Transport: clone, CheckRedirect: r.client.CheckRedirect}
	}
	response, err := client.Do(outbound)
	if err != nil {
		writeError(w, http.StatusBadGateway, "provider_unavailable", "External image provider request failed")
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		writeError(w, http.StatusBadGateway, "provider_redirect", "External image provider redirect refused")
		return
	}
	for _, header := range []string{"Content-Type", "Retry-After", "X-Request-Id"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(response.StatusCode)
	// Stream JSON/base64 and SSE bodies. Do not buffer large image responses.
	if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		buf := make([]byte, 16<<10)
		for {
			n, e := response.Body.Read(buf)
			if n > 0 {
				if _, writeErr := w.Write(buf[:n]); writeErr != nil {
					return
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
			if e != nil {
				return
			}
		}
	}
	_, _ = io.Copy(w, response.Body)
}
