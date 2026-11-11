package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ProviderPreset is an immutable, verified endpoint choice shown in the UI.
// Selecting "custom" is the only way to enter an arbitrary cloud/local URL.
// A preset does not prove the account supports any particular model.
type ProviderPreset struct {
	ID string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	BaseURL string `json:"base_url"`
	Logo string `json:"logo,omitempty"`
	Category string `json:"category"`
}

var providerPresets = []ProviderPreset{
	{ID:"openai", Name:"OpenAI API", Kind:"openai-compatible", BaseURL:"https://api.openai.com/v1", Logo:"openai", Category:"cloud"},
	{ID:"deepseek", Name:"DeepSeek", Kind:"openai-compatible", BaseURL:"https://api.deepseek.com", Logo:"deepseek", Category:"cloud"},
	{ID:"openrouter", Name:"OpenRouter", Kind:"openai-compatible", BaseURL:"https://openrouter.ai/api/v1", Logo:"openrouter", Category:"cloud"},
	{ID:"groq", Name:"Groq", Kind:"openai-compatible", BaseURL:"https://api.groq.com/openai/v1", Logo:"groq", Category:"cloud"},
	{ID:"xai", Name:"xAI / Grok", Kind:"openai-compatible", BaseURL:"https://api.x.ai/v1", Logo:"xai", Category:"cloud"},
	{ID:"qwen-cn", Name:"阿里云百炼 / Qwen（北京）", Kind:"openai-compatible", BaseURL:"https://dashscope.aliyuncs.com/compatible-mode/v1", Logo:"qwen", Category:"cloud"},
	{ID:"qwen-intl", Name:"阿里云百炼 / Qwen（新加坡）", Kind:"openai-compatible", BaseURL:"https://dashscope-intl.aliyuncs.com/compatible-mode/v1", Logo:"qwen", Category:"cloud"},
	{ID:"volcengine-video", Name:"火山方舟 / Seedance 视频", Kind:"ark-video", BaseURL:"https://ark.cn-beijing.volces.com/api/v3", Logo:"volcengine", Category:"cloud"},
	{ID:"ollama", Name:"Ollama", Kind:"local-openai-compatible", BaseURL:"http://127.0.0.1:11434/v1", Logo:"ollama", Category:"local"},
	{ID:"lmstudio", Name:"LM Studio", Kind:"local-openai-compatible", BaseURL:"http://127.0.0.1:1234/v1", Logo:"lmstudio", Category:"local"},
	{ID:"vllm", Name:"vLLM", Kind:"local-openai-compatible", BaseURL:"http://127.0.0.1:8000/v1", Logo:"vllm", Category:"local"},
	{ID:"llamacpp", Name:"llama.cpp server", Kind:"local-openai-compatible", BaseURL:"http://127.0.0.1:8080/v1", Category:"local"},
}

func findProviderPreset(id string) (ProviderPreset, bool) {
	for _, p := range providerPresets { if p.ID == id { return p, true } }
	return ProviderPreset{}, false
}

// validateLocalProviderURL only allows explicit local/private addresses and
// Bonjour .local hosts; never allow a URL to declare a public HTTPS service
// as a "local" backend to bypass cloud security. Dynamic DNS is checked again
// by dialLocalProvider for every new TCP connection.
func validateLocalProviderURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("local provider URL must be http(s)://localhost-or-private-IP:port/v1 without credentials, query or fragment")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1024 || port > 65535 || port == defaultAdminPort || port == defaultAPIPort {
		return errors.New("local provider requires an explicit port 1024–65535 (the Provider dashboard/API ports are reserved)")
	}
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() && !ip.IsPrivate() {
			return errors.New("local provider IP must be a loopback or private LAN address")
		}
	} else if host != "localhost" && !strings.HasSuffix(host, ".local") {
		return errors.New("local provider hostname must be localhost, *.local or an explicit private IP")
	}
	if strings.Contains(u.EscapedPath(), "%") || strings.Contains(u.Path, "..") || strings.Contains(u.Path, "//") {
		return errors.New("invalid local provider base URL path")
	}
	return nil
}

// Every connection performs its own DNS resolution and pins it to private IPs.
// This avoids redirects, publicly resolving domains and DNS rebinding bypasses.
func dialLocalProvider(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil { return nil, err }
	host = strings.ToLower(host)
	if ip := net.ParseIP(host); ip == nil && host != "localhost" && !strings.HasSuffix(host, ".local") {
		return nil, errors.New("local inference hostname is not explicitly trusted")
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil { return nil, err }
	if len(ips)==0 { return nil, errors.New("local hostname has no IP addresses") }
	for _, ip := range ips {
		if !ip.IsLoopback() && !ip.IsPrivate() {
			return nil, errors.New("local inference hostname resolves outside loopback/private networks")
		}
	}
	var lastErr error
	d := &net.Dialer{}
	for _, ip := range ips {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil { return conn, nil }
		lastErr = err
	}
	return nil, fmt.Errorf("local model server cannot be reached: %w", lastErr)
}
