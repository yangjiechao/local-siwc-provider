package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type jsonMap = map[string]any

var forbiddenResponseFields = map[string]bool{
	"background": true, "conversation": true, "max_tool_calls": true,
	"metadata": true, "moderation": true, "multi_agent": true, "prompt": true,
	"prompt_cache_retention": true, "safety_identifier": true, "temperature": true,
	"top_logprobs": true, "top_p": true, "truncation": true, "user": true,
	"previous_response_id": true,
}

// max_output_tokens is accepted from clients for compatibility but ignored on SIWC.
// It must never be sent to the SIWC subscription route.
func sanitizeResponsesForSIWC(m jsonMap) error {
	delete(m, "max_output_tokens")
	return validateResponses(m)
}

func validateResponses(m jsonMap) error {
	if name, ok := m["model"].(string); !ok || strings.TrimSpace(name) == "" {
		return errors.New("model is required")
	}
	if _, ok := m["input"]; !ok {
		return errors.New("input is required")
	}
	for field := range forbiddenResponseFields {
		if _, ok := m[field]; ok {
			return fmt.Errorf("%s is unavailable on SIWC Responses", field)
		}
	}
	if raw, ok := m["tools"]; ok {
		tools, ok := raw.([]any)
		if !ok {
			return errors.New("tools must be an array")
		}
		for _, tool := range tools {
			v, ok := tool.(map[string]any)
			if !ok || v["type"] != "namespace" {
				return errors.New("SIWC tools must use namespace groups; hosted tools are not supported")
			}
		}
	}
	return nil
}

func mapChatToResponses(req jsonMap) (jsonMap, error) {
	model, ok := req["model"].(string)
	if !ok || model == "" {
		return nil, errors.New("model is required")
	}
	messages, ok := req["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, errors.New("messages must be nonempty array")
	}
	// Fail closed rather than quietly ignoring parameters that change model behavior.
	allowed := map[string]bool{"model": true, "messages": true, "tools": true, "tool_choice": true, "stream": true, "stream_options": true, "parallel_tool_calls": true, "max_output_tokens": true}
	for key := range req {
		if !allowed[key] {
			return nil, fmt.Errorf("unsupported Chat Completions parameter: %s", key)
		}
	}
	input := make([]any, 0, len(messages))
	var system []string
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("each message must be object")
		}
		role, _ := msg["role"].(string)
		switch role {
		case "system":
			text, ok := msg["content"].(string)
			if !ok {
				return nil, errors.New("system content must be text")
			}
			system = append(system, text)
		case "developer", "user":
			content, err := mapInputContent(msg["content"])
			if err != nil {
				return nil, err
			}
			input = append(input, jsonMap{"role": role, "content": content})
		case "assistant":
			if c, ok := msg["content"].(string); ok && c != "" {
				input = append(input, jsonMap{"role": "assistant", "content": c})
			}
			if tools, ok := msg["tool_calls"].([]any); ok {
				for _, t := range tools {
					item, ok := t.(map[string]any)
					if !ok {
						return nil, errors.New("invalid tool call")
					}
					fn, ok := item["function"].(map[string]any)
					if !ok {
						return nil, errors.New("invalid function call")
					}
					id, _ := item["id"].(string)
					name, _ := fn["name"].(string)
					args, _ := fn["arguments"].(string)
					if id == "" || name == "" {
						return nil, errors.New("tool call missing id or name")
					}
					input = append(input, jsonMap{"type": "function_call", "namespace": "local", "call_id": id, "name": name, "arguments": args})
				}
			}
		case "tool":
			id, _ := msg["tool_call_id"].(string)
			if id == "" {
				return nil, errors.New("tool result missing tool_call_id")
			}
			content, ok := msg["content"].(string)
			if !ok {
				return nil, errors.New("tool result must be text")
			}
			input = append(input, jsonMap{"type": "function_call_output", "namespace": "local", "call_id": id, "output": content})
		default:
			return nil, fmt.Errorf("unsupported message role %q", role)
		}
	}
	out := jsonMap{"model": model, "input": input, "store": false, "stream": true}
	if len(system) > 0 {
		out["instructions"] = strings.Join(system, "\n\n")
	}
	if toolsRaw, ok := req["tools"]; ok {
		rawTools, ok := toolsRaw.([]any)
		if !ok {
			return nil, errors.New("tools must be array")
		}
		if len(rawTools) > 0 {
			grouped := make([]any, 0, len(rawTools))
			for _, raw := range rawTools {
				t, ok := raw.(map[string]any)
				if !ok || t["type"] != "function" {
					return nil, errors.New("only function tools supported")
				}
				fn, ok := t["function"].(map[string]any)
				if !ok {
					return nil, errors.New("missing function definition")
				}
				name, _ := fn["name"].(string)
				if name == "" {
					return nil, errors.New("missing tool name")
				}
				f := jsonMap{"type": "function", "name": name}
				for _, field := range []string{"description", "parameters", "strict"} {
					if val, ok := fn[field]; ok {
						f[field] = val
					}
				}
				grouped = append(grouped, f)
			}
			out["tools"] = []any{jsonMap{"type": "namespace", "name": "local", "description": "Functions executed locally by the client", "tools": grouped}}
		}
	}
	if choice, ok := req["tool_choice"]; ok {
		switch v := choice.(type) {
		case string:
			if v != "auto" && v != "none" && v != "required" {
				return nil, errors.New("unsupported tool_choice")
			}
			out["tool_choice"] = v
		case map[string]any:
			fn, ok := v["function"].(map[string]any)
			if !ok {
				return nil, errors.New("unsupported tool_choice object")
			}
			name, _ := fn["name"].(string)
			if name == "" {
				return nil, errors.New("missing function choice")
			}
			out["tool_choice"] = jsonMap{"type": "function", "namespace": "local", "name": name}
		default:
			return nil, errors.New("unsupported tool_choice")
		}
	}
	if parallel, ok := req["parallel_tool_calls"]; ok {
		out["parallel_tool_calls"] = parallel
	}
	return out, nil
}

func mapInputContent(v any) (any, error) {
	switch parts := v.(type) {
	case string:
		return parts, nil
	case []any:
		out := make([]any, 0, len(parts))
		for _, raw := range parts {
			p, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("invalid content part")
			}
			typ, _ := p["type"].(string)
			switch typ {
			case "text":
				t, ok := p["text"].(string)
				if !ok {
					return nil, errors.New("missing text")
				}
				out = append(out, jsonMap{"type": "input_text", "text": t})
			case "image_url":
				u, ok := p["image_url"].(map[string]any)
				if !ok {
					return nil, errors.New("missing image_url")
				}
				uri, ok := u["url"].(string)
				if !ok || uri == "" {
					return nil, errors.New("missing image URL")
				}
				out = append(out, jsonMap{"type": "input_image", "image_url": uri})
			default:
				return nil, fmt.Errorf("unsupported content type %q", typ)
			}
		}
		return out, nil
	default:
		return nil, errors.New("content must be a string or supported parts array")
	}
}

func responseToCompletion(resp jsonMap, model string) jsonMap {
	content := ""
	tools := make([]any, 0)
	if output, ok := resp["output"].([]any); ok {
		for _, raw := range output {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch item["type"] {
			case "message":
				if parts, ok := item["content"].([]any); ok {
					for _, p := range parts {
						if obj, ok := p.(map[string]any); ok && obj["type"] == "output_text" {
							if text, ok := obj["text"].(string); ok {
								content += text
							}
						}
					}
				}
			case "function_call":
				id, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				args, _ := item["arguments"].(string)
				tools = append(tools, jsonMap{"id": id, "type": "function", "function": jsonMap{"name": name, "arguments": args}})
			}
		}
	}
	finish := "stop"
	if len(tools) > 0 {
		finish = "tool_calls"
	}
	if resp["status"] == "incomplete" {
		finish = "length"
	}
	msg := jsonMap{"role": "assistant", "content": content}
	if len(tools) > 0 {
		msg["tool_calls"] = tools
	}
	id, _ := resp["id"].(string)
	created := time.Now().Unix()
	if c, ok := resp["created_at"].(float64); ok {
		created = int64(c)
	}
	result := jsonMap{"id": id, "object": "chat.completion", "created": created, "model": model, "choices": []any{jsonMap{"index": 0, "message": msg, "finish_reason": finish}}}
	if usage, ok := resp["usage"].(map[string]any); ok {
		result["usage"] = jsonMap{"prompt_tokens": usage["input_tokens"], "completion_tokens": usage["output_tokens"], "total_tokens": usage["total_tokens"]}
	}
	return result
}

func decodeJSON(b []byte) (jsonMap, error) {
	var m jsonMap
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("JSON object required")
	}
	return m, nil
}
