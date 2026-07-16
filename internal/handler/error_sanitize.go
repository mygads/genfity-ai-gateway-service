package handler

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

const customerGatewayBusyMessage = "The requested model/provider is currently experiencing high traffic. Please try again later."

var thinkingTagPattern = regexp.MustCompile(`(?is)<thinking>\s*.*?\s*</thinking>\s*`)

func sanitizedMessageForStatus(statusCode int) string {
	return customerGatewayBusyMessage
}

// sanitizeErrorBody replaces ALL error response bodies (4xx/5xx) with a
// generic customer-facing message. By the time we reach writeUpstreamResponse
// the request has already passed through gateway-level validation (respondError
// handles gateway errors before contacting upstreams). Every 4xx/5xx body that
// arrives here came from a third-party provider/router and its message must
// never be shown to the customer — it could leak provider names, internal
// routing, or infrastructure details.
//
// Prior approach was a blacklist (block litellm/CLIProxy/etc.) but new provider
// names constantly leak through. This whitelist approach is exhaustive:
// nothing survives unless explicitly marked safe.
func sanitizeErrorBody(body []byte, statusCode int) []byte {
	if len(body) == 0 {
		if statusCode < 200 || statusCode >= 400 {
			return buildSafeErrorJSON(statusCode, sanitizedMessageForStatus(statusCode))
		}
		return body
	}

	// Only sanitize error responses.
	if statusCode >= 200 && statusCode < 400 {
		return body
	}

	// Parse the body. If it looks like JSON with an error key, rewrite it
	// entirely. If we can't parse it, replace it with a safe error envelope.
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return buildSafeErrorJSON(statusCode, sanitizedMessageForStatus(statusCode))
	}

	// If the body has an error field, blind-replace it. No content-based
	// filtering — all provider error text is unsafe by default.
	if _, hasError := payload["error"]; hasError {
		payload["error"] = map[string]any{
			"message": sanitizedMessageForStatus(statusCode),
			"type":    errorTypeForStatus(statusCode),
			"code":    errorCodeForStatus(statusCode),
		}
		// Drop any other keys that might leak internals.
		for k := range payload {
			if k != "error" {
				delete(payload, k)
			}
		}
		sanitized, err := json.Marshal(payload)
		if err != nil {
			return buildSafeErrorJSON(statusCode, sanitizedMessageForStatus(statusCode))
		}
		return sanitized
	}

	// Non-standard body: replace entirely.
	return buildSafeErrorJSON(statusCode, sanitizedMessageForStatus(statusCode))
}

func errorTypeForStatus(statusCode int) string {
	switch {
	case statusCode == 429:
		return "rate_limit_error"
	case statusCode == 401 || statusCode == 403:
		return "authentication_error"
	default:
		return "server_error"
	}
}

func errorCodeForStatus(statusCode int) string {
	switch {
	case statusCode == 429:
		return "rate_limit_exceeded"
	case statusCode == 401 || statusCode == 403:
		return "auth_error"
	default:
		return "upstream_error"
	}
}

// sanitizeSSEChunk rewrites any SSE data line that carries an error payload
// so provider details never leak in streaming responses. Non-error lines and
// non-JSON data lines pass through unchanged.
func sanitizeSSEChunk(chunk []byte) []byte {
	// Fast-path: skip chunks that don't carry an error key at all.
	if !bytes.Contains(chunk, []byte(`"error"`)) {
		return chunk
	}

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

// sanitizePayloadInPlace rewrites any error payload to a safe customer-facing
// message. All upstream/provider error text is unsafe by default.
// Returns true if modifications were made.
func sanitizePayloadInPlace(payload map[string]any) bool {
	if payload == nil {
		return false
	}

	_, hasError := payload["error"]
	if !hasError {
		return false
	}

	// Blind-replace: any error in a streaming chunk came from a provider
	// and must be masked. No content-based filtering.
	payload["error"] = map[string]any{
		"message": "An upstream error occurred. Please retry your request.",
		"type":    "server_error",
		"code":    "upstream_error",
	}
	return true
}

func stripReasoningContentInPlace(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		for key, nested := range typed {
			if strings.EqualFold(key, "reasoning_content") {
				delete(typed, key)
				changed = true
				continue
			}
			if stripReasoningContentInPlace(nested) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, nested := range typed {
			if stripReasoningContentInPlace(nested) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

func stripThinkingTags(text string) (string, bool) {
	if text == "" {
		return text, false
	}
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "<thinking>") || !strings.Contains(lower, "</thinking>") {
		return text, false
	}
	cleaned := thinkingTagPattern.ReplaceAllString(text, "")
	cleaned = strings.TrimSpace(cleaned)
	return cleaned, cleaned != text
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

// rewriteResponseModel rewrites successful response bodies so customer-facing
// payloads never expose combo internals. It forces model fields back to the
// public model name and strips provider-specific reasoning_content fields.
// Error bodies are left to sanitizeErrorBody.
func rewriteResponseModel(body []byte, statusCode int, publicModel string) []byte {
	if len(body) == 0 || statusCode >= 400 {
		return body
	}
	if !bytes.Contains(body, []byte(`"model"`)) &&
		!bytes.Contains(body, []byte(`"reasoning_content"`)) &&
		!bytes.Contains(bytes.ToLower(body), []byte("<thinking>")) {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	changed := rewriteModelInPayload(payload, publicModel)
	if stripReasoningContentInPlace(payload) {
		changed = true
	}
	if stripThinkingTagsInPlace(payload) {
		changed = true
	}
	if !changed {
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
// handling exactly so SSE framing is preserved. It also strips
// reasoning_content from any JSON SSE data event.
func rewriteSSEChunkModel(chunk []byte, publicModel string) []byte {
	if !bytes.Contains(chunk, []byte(`"model"`)) &&
		!bytes.Contains(chunk, []byte(`"reasoning_content"`)) &&
		!bytes.Contains(bytes.ToLower(chunk), []byte("<thinking>")) {
		return chunk
	}
	lines := strings.Split(string(chunk), "\n")
	var result strings.Builder
	modified := false
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if strings.HasPrefix(trimmed, ":") && strings.TrimSpace(strings.TrimPrefix(trimmed, ":")) != "" {
			result.WriteString(": keep-alive\n")
			modified = true
			continue
		}
		if strings.HasPrefix(trimmed, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if data != "" && data != "[DONE]" {
				var payload map[string]any
				if err := json.Unmarshal([]byte(data), &payload); err == nil {
					changed := rewriteModelInPayload(payload, publicModel)
					if stripReasoningContentInPlace(payload) {
						changed = true
					}
					if stripThinkingTagsInPlace(payload) {
						changed = true
					}
					if changed {
						modified = true
						rewritten, _ := json.Marshal(payload)
						result.WriteString("data: ")
						result.Write(rewritten)
						result.WriteString("\n")
						continue
					}
				} else if cleaned, changed := stripThinkingTags(data); changed {
					modified = true
					result.WriteString("data: ")
					result.WriteString(cleaned)
					result.WriteString("\n")
					continue
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

func stripThinkingTagsInPlace(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		for key, nested := range typed {
			lowerKey := strings.ToLower(key)
			if (lowerKey == "content" || lowerKey == "text") && nested != nil {
				if current, ok := nested.(string); ok {
					if cleaned, modified := stripThinkingTags(current); modified {
						typed[key] = cleaned
						changed = true
						continue
					}
				}
			}
			if stripThinkingTagsInPlace(nested) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, nested := range typed {
			if stripThinkingTagsInPlace(nested) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

type sseRewriteBuffer struct {
	pending         []byte
	streamProtocol  string
	providerStarted bool
}

const (
	providerStartedOpenAISSEMarker        = "data: {\"type\":\"genfity.provider_started\",\"genfity_internal\":true}\n\n"
	providerStartedClaudeSSEMarker        = "event: genfity.provider_started\ndata: {\"type\":\"genfity.provider_started\",\"genfity_internal\":true}\n\n"
	providerStartedLegacySSEMarker        = ": genfity-provider-started\n\n"
	providerStartedWrappedLegacySSEMarker = "data: : genfity-provider-started\n\n"
)

func (b *sseRewriteBuffer) append(chunk []byte, publicModel string) []byte {
	if len(chunk) == 0 {
		return nil
	}
	b.pending = append(b.pending, chunk...)
	completeEnd, delimLen := findLastSSEEventBoundary(b.pending)
	if completeEnd < 0 {
		return nil
	}
	complete := append([]byte(nil), b.pending[:completeEnd+delimLen]...)
	b.pending = append(b.pending[:0], b.pending[completeEnd+delimLen:]...)
	complete = b.stripProviderStartedMarker(complete)
	return rewriteAndSanitizeSSEPayload(complete, publicModel, b.streamProtocol)
}

func (b *sseRewriteBuffer) flush(publicModel string) []byte {
	if len(b.pending) == 0 {
		return nil
	}
	out := rewriteAndSanitizeSSEPayload(b.stripProviderStartedMarker(b.pending), publicModel, b.streamProtocol)
	b.pending = nil
	return out
}

func (b *sseRewriteBuffer) stripProviderStartedMarker(chunk []byte) []byte {
	if !bytes.Contains(chunk, []byte("genfity-provider-started")) && !bytes.Contains(chunk, []byte("genfity.provider_started")) {
		return chunk
	}
	b.providerStarted = true
	for _, marker := range []string{
		providerStartedOpenAISSEMarker,
		providerStartedClaudeSSEMarker,
		providerStartedLegacySSEMarker,
		providerStartedWrappedLegacySSEMarker,
	} {
		chunk = bytes.ReplaceAll(chunk, []byte(marker), nil)
	}
	return chunk
}

func rewriteAndSanitizeSSEPayload(chunk []byte, publicModel, streamProtocol string) []byte {
	safe := sanitizeSSEChunk(chunk)
	rewritten := rewriteSSEChunkModel(safe, publicModel)
	return replaceSSECommentHeartbeats(rewritten, publicModel, streamProtocol)
}

// replaceSSECommentHeartbeats prevents compatibility failures in clients that
// correctly parse `data:` frames but incorrectly attempt JSON.parse on SSE
// comment heartbeats such as `: keep-alive`. We retain the liveness signal by
// converting comment-only events into protocol-native no-op events:
// OpenAI receives an empty chat chunk; Anthropic receives a standard ping.
// Comments attached to a real data event are simply removed.
func replaceSSECommentHeartbeats(chunk []byte, publicModel, streamProtocol string) []byte {
	if !bytes.Contains(chunk, []byte(":")) {
		return chunk
	}
	normalized := strings.ReplaceAll(string(chunk), "\r\n", "\n")
	events := strings.Split(normalized, "\n\n")
	changed := false
	var out strings.Builder
	for _, event := range events {
		if event == "" {
			continue
		}
		lines := strings.Split(event, "\n")
		kept := make([]string, 0, len(lines))
		hadComment := false
		hasPayload := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, ":") {
				hadComment = true
				changed = true
				continue
			}
			if trimmed != "" {
				hasPayload = true
				kept = append(kept, line)
			}
		}
		if hadComment && !hasPayload {
			switch streamProtocol {
			case "anthropic":
				out.WriteString("event: ping\ndata: {\"type\":\"ping\"}\n\n")
			default:
				modelJSON, _ := json.Marshal(publicModel)
				out.WriteString("data: {\"id\":\"chatcmpl-keepalive\",\"object\":\"chat.completion.chunk\",\"model\":")
				out.Write(modelJSON)
				out.WriteString(",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":null}]}\n\n")
			}
			continue
		}
		if len(kept) > 0 {
			out.WriteString(strings.Join(kept, "\n"))
			out.WriteString("\n\n")
		}
	}
	if !changed {
		return chunk
	}
	return []byte(out.String())
}

func findLastSSEEventBoundary(chunk []byte) (int, int) {
	lastLF := bytes.LastIndex(chunk, []byte("\n\n"))
	lastCRLF := bytes.LastIndex(chunk, []byte("\r\n\r\n"))
	switch {
	case lastLF < 0 && lastCRLF < 0:
		return -1, 0
	case lastLF > lastCRLF:
		return lastLF, 2
	default:
		return lastCRLF, 4
	}
}

func buildSafeErrorJSON(statusCode int, message string) []byte {
	errType := "server_error"
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
