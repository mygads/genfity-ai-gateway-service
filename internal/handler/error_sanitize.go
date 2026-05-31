package handler

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// internalPatterns matches strings that reveal internal infrastructure details.
var internalPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)litellm\.\w+`),
	regexp.MustCompile(`(?i)MidStreamFallback\w*`),
	regexp.MustCompile(`(?i)All credentials for model\s+\S+`),
	regexp.MustCompile(`(?i)are cooling down`),
	regexp.MustCompile(`(?i)OpenAI?Exception`),
	regexp.MustCompile(`(?i)APIConnectionError:\s*\S+`),
	regexp.MustCompile(`(?i)providers?=\S+`),
	regexp.MustCompile(`(?i)model=\S+`),
	regexp.MustCompile(`(?i)/v0/management/\S+`),
	regexp.MustCompile(`(?i)mtr/\S+`),
	regexp.MustCompile(`(?i)via provider\s+\S+`),
	regexp.MustCompile(`(?i)openai_compatible`),
	regexp.MustCompile(`(?i)Original exception:`),
}

// internalKeywords are substrings that indicate an internal error leaked.
var internalKeywords = []string{
	"litellm",
	"MidStreamFallback",
	"All credentials for model",
	"cooling down",
	"OpenAIException",
	"OpenAlException",
	"APIConnectionError",
	"/v0/management/",
	"mtr/",
	"via provider",
	"openai_compatible",
	"Original exception",
	"cliproxy",
	"CLIProxy",
}

func containsInternalLeak(s string) bool {
	lower := strings.ToLower(s)
	for _, kw := range internalKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

func sanitizedMessageForStatus(statusCode int) string {
	switch {
	case statusCode == 429:
		return "Rate limit exceeded. Please retry after a short delay."
	case statusCode == 503:
		return "Service temporarily unavailable. Please retry shortly."
	case statusCode >= 500:
		return "An upstream error occurred. Please retry your request."
	case statusCode == 401 || statusCode == 403:
		return "Authentication or permission error."
	default:
		return "An error occurred while processing your request."
	}
}

// sanitizeErrorBody inspects a JSON error response body and replaces internal
// details with a safe customer-facing message. Non-error bodies (successful
// responses) are returned unchanged.
func sanitizeErrorBody(body []byte, statusCode int) []byte {
	if len(body) == 0 {
		return body
	}

	// Only sanitize error responses (4xx/5xx).
	if statusCode >= 200 && statusCode < 400 {
		return body
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		// Not JSON — check raw text for leaks.
		if containsInternalLeak(string(body)) {
			return buildSafeErrorJSON(statusCode, sanitizedMessageForStatus(statusCode))
		}
		return body
	}

	errField, hasError := payload["error"]
	if !hasError {
		return body
	}

	switch errObj := errField.(type) {
	case map[string]any:
		msg, _ := errObj["message"].(string)
		if containsInternalLeak(msg) {
			errObj["message"] = sanitizedMessageForStatus(statusCode)
			// Remove provider/model fields that leak internals.
			delete(errObj, "provider")
			delete(errObj, "model")
			payload["error"] = errObj
			sanitized, err := json.Marshal(payload)
			if err != nil {
				return buildSafeErrorJSON(statusCode, sanitizedMessageForStatus(statusCode))
			}
			return sanitized
		}
	case string:
		if containsInternalLeak(errObj) {
			payload["error"] = sanitizedMessageForStatus(statusCode)
			sanitized, err := json.Marshal(payload)
			if err != nil {
				return buildSafeErrorJSON(statusCode, sanitizedMessageForStatus(statusCode))
			}
			return sanitized
		}
	}

	return body
}

// sanitizeSSEChunk inspects a single SSE data line for internal error leaks.
// Returns the original chunk if safe, or a sanitized version if it leaks.
func sanitizeSSEChunk(chunk []byte) []byte {
	if !containsInternalLeak(string(chunk)) {
		return chunk
	}

	// Try to parse as SSE lines and sanitize data: payloads.
	lines := strings.Split(string(chunk), "\n")
	var result strings.Builder
	modified := false

	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if strings.HasPrefix(trimmed, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if data == "" || data == "[DONE]" {
				result.WriteString(line)
				result.WriteString("\n")
				continue
			}

			var payload map[string]any
			if err := json.Unmarshal([]byte(data), &payload); err == nil {
				if sanitizePayloadInPlace(payload) {
					modified = true
					sanitized, _ := json.Marshal(payload)
					result.WriteString("data: ")
					result.Write(sanitized)
					result.WriteString("\n")
					continue
				}
			}

			// Raw data line contains leak but isn't parseable JSON.
			if containsInternalLeak(data) {
				modified = true
				safePayload := map[string]any{
					"error": map[string]any{
						"message": "An upstream error occurred. Please retry your request.",
						"type":    "server_error",
						"code":    "upstream_error",
					},
				}
				sanitized, _ := json.Marshal(safePayload)
				result.WriteString("data: ")
				result.Write(sanitized)
				result.WriteString("\n")
				continue
			}
		}

		result.WriteString(line)
		result.WriteString("\n")
	}

	if !modified {
		return chunk
	}
	// Trim trailing extra newline added by loop.
	out := result.String()
	if len(out) > 0 && out[len(out)-1] == '\n' && (len(chunk) == 0 || chunk[len(chunk)-1] != '\n') {
		out = out[:len(out)-1]
	}
	return []byte(out)
}

// sanitizePayloadInPlace modifies a parsed JSON payload to remove internal leaks.
// Returns true if modifications were made.
func sanitizePayloadInPlace(payload map[string]any) bool {
	if payload == nil {
		return false
	}

	errField, hasError := payload["error"]
	if !hasError {
		return false
	}

	switch errObj := errField.(type) {
	case map[string]any:
		msg, _ := errObj["message"].(string)
		if containsInternalLeak(msg) {
			errObj["message"] = "An upstream error occurred. Please retry your request."
			delete(errObj, "provider")
			delete(errObj, "model")
			payload["error"] = errObj
			return true
		}
	case string:
		if containsInternalLeak(errObj) {
			payload["error"] = map[string]any{
				"message": "An upstream error occurred. Please retry your request.",
				"type":    "server_error",
				"code":    "upstream_error",
			}
			return true
		}
	}
	return false
}

// rewriteModelInPayload sets the "model" field of a parsed response payload
// to the public model name the customer requested, so the real router/
// upstream model (e.g. "minimax-m2.5", "kimi-k2.6") never leaks. Handles
// both the top-level "model" (OpenAI chat/completions + Anthropic non-stream
// message) and the nested "message.model" (Anthropic message_start SSE
// event). Returns true if anything changed.
func rewriteModelInPayload(payload map[string]any, publicModel string) bool {
	if payload == nil || publicModel == "" {
		return false
	}
	changed := false
	if cur, ok := payload["model"].(string); ok && cur != publicModel {
		payload["model"] = publicModel
		changed = true
	}
	if msg, ok := payload["message"].(map[string]any); ok {
		if cur, ok := msg["model"].(string); ok && cur != publicModel {
			msg["model"] = publicModel
			payload["message"] = msg
			changed = true
		}
	}
	return changed
}

// rewriteResponseModel rewrites the "model" field in a successful response
// body to the public model the customer asked for. Only touches 2xx/3xx
// JSON bodies; returns the body unchanged when publicModel is empty, the
// body has no "model" key, or it isn't parseable JSON. Error bodies are
// left to sanitizeErrorBody.
func rewriteResponseModel(body []byte, statusCode int, publicModel string) []byte {
	if publicModel == "" || len(body) == 0 || statusCode >= 400 {
		return body
	}
	if !bytes.Contains(body, []byte(`"model"`)) {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	if !rewriteModelInPayload(payload, publicModel) {
		return body
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

// rewriteSSEChunkModel rewrites the "model" field in each SSE data line of a
// streaming chunk to the public model name. Mirrors sanitizeSSEChunk's line
// handling exactly so SSE framing is preserved. Cheap fast-path: skips the
// whole chunk when it carries no "model" key.
func rewriteSSEChunkModel(chunk []byte, publicModel string) []byte {
	if publicModel == "" || !bytes.Contains(chunk, []byte(`"model"`)) {
		return chunk
	}
	lines := strings.Split(string(chunk), "\n")
	var result strings.Builder
	modified := false
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if strings.HasPrefix(trimmed, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if data != "" && data != "[DONE]" {
				var payload map[string]any
				if err := json.Unmarshal([]byte(data), &payload); err == nil {
					if rewriteModelInPayload(payload, publicModel) {
						modified = true
						rewritten, _ := json.Marshal(payload)
						result.WriteString("data: ")
						result.Write(rewritten)
						result.WriteString("\n")
						continue
					}
				}
			}
		}
		result.WriteString(line)
		result.WriteString("\n")
	}
	if !modified {
		return chunk
	}
	out := result.String()
	if len(out) > 0 && out[len(out)-1] == '\n' && (len(chunk) == 0 || chunk[len(chunk)-1] != '\n') {
		out = out[:len(out)-1]
	}
	return []byte(out)
}

func buildSafeErrorJSON(statusCode int, message string) []byte {	errType := "server_error"
	code := "upstream_error"
	switch {
	case statusCode == 429:
		errType = "rate_limit_error"
		code = "rate_limit_exceeded"
	case statusCode == 401 || statusCode == 403:
		errType = "authentication_error"
		code = "auth_error"
	}

	payload := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"code":    code,
		},
	}
	data, _ := json.Marshal(payload)
	return data
}
