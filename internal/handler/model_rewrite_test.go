package handler

import (
	"strings"
	"testing"
)

func TestRewriteResponseModel_OpenAINonStream(t *testing.T) {
	body := []byte(`{"id":"x","object":"chat.completion","model":"minimax-m2.5","choices":[{"message":{"content":"ok","reasoning_content":"hidden provider thoughts"}}]}`)
	out := rewriteResponseModel(body, 200, "genfity/claude-opus-4.7")
	if !strings.Contains(string(out), `"genfity/claude-opus-4.7"`) {
		t.Fatalf("expected public model in body, got %s", out)
	}
	if strings.Contains(string(out), "minimax") {
		t.Fatalf("upstream model leaked: %s", out)
	}
	if strings.Contains(string(out), "reasoning_content") || strings.Contains(string(out), "hidden provider thoughts") {
		t.Fatalf("reasoning_content leaked: %s", out)
	}
}

func TestRewriteResponseModel_StripsThinkingTags(t *testing.T) {
	body := []byte(`{"id":"x","object":"chat.completion","model":"kr/claude-haiku-4.5-thinking-agentic","choices":[{"message":{"role":"assistant","content":"<thinking>\ninternal reasoning\n</thinking>\n\nok"}}]}`)
	out := rewriteResponseModel(body, 200, "genfity/claude-haiku-4.5")
	text := string(out)
	if strings.Contains(strings.ToLower(text), "<thinking>") || strings.Contains(text, "internal reasoning") {
		t.Fatalf("thinking leaked: %s", out)
	}
	if !strings.Contains(text, `"content":"ok"`) {
		t.Fatalf("sanitized content missing: %s", out)
	}
}

func TestRewriteResponseModel_AnthropicNonStream(t *testing.T) {
	body := []byte(`{"id":"x","type":"message","role":"assistant","model":"kimi-k2.6","content":[{"type":"text","text":"ok"}]}`)
	out := rewriteResponseModel(body, 200, "genfity/claude-opus-4.8")
	if !strings.Contains(string(out), `"genfity/claude-opus-4.8"`) || strings.Contains(string(out), "kimi") {
		t.Fatalf("rewrite failed or leaked: %s", out)
	}
}

func TestRewriteResponseModel_NoOpCases(t *testing.T) {
	// empty publicModel -> unchanged
	body := []byte(`{"model":"kimi-k2.6"}`)
	if got := rewriteResponseModel(body, 200, ""); string(got) != string(body) {
		t.Fatalf("empty publicModel should be no-op, got %s", got)
	}
	// error status -> unchanged (left to sanitizeErrorBody)
	if got := rewriteResponseModel(body, 500, "genfity/x"); string(got) != string(body) {
		t.Fatalf("error status should be no-op, got %s", got)
	}
	// no model key -> unchanged
	nb := []byte(`{"id":"x","choices":[]}`)
	if got := rewriteResponseModel(nb, 200, "genfity/x"); string(got) != string(nb) {
		t.Fatalf("no model key should be no-op, got %s", got)
	}
	// non-JSON -> unchanged
	raw := []byte(`not json "model" here`)
	if got := rewriteResponseModel(raw, 200, "genfity/x"); string(got) != string(raw) {
		t.Fatalf("non-JSON should be no-op, got %s", got)
	}
}

func TestRewriteSSEChunkModel_OpenAIChunk(t *testing.T) {
	chunk := []byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"minimax-m2.5\",\"choices\":[{\"delta\":{\"content\":\"hi\",\"reasoning_content\":\"hidden\"}}]}\n\n")
	out := rewriteSSEChunkModel(chunk, "genfity/claude-opus-4.7")
	if !strings.Contains(string(out), "genfity/claude-opus-4.7") || strings.Contains(string(out), "minimax") {
		t.Fatalf("SSE rewrite failed or leaked: %q", out)
	}
	if strings.Contains(string(out), "reasoning_content") || strings.Contains(string(out), "hidden") {
		t.Fatalf("SSE reasoning_content leaked: %q", out)
	}
	if !strings.HasSuffix(string(out), "\n\n") {
		t.Fatalf("SSE framing (trailing newlines) not preserved: %q", out)
	}
}

func TestRewriteSSEChunkModel_StripsThinkingTagsAndNormalizesPrelude(t *testing.T) {
	chunk := []byte(": genflowaistreamopen\ndata: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"genflowai/claude-opus-4.8-thinking-agentic\",\"choices\":[{\"delta\":{\"content\":\"<thinking>internal reasoning</thinking>ok\",\"role\":\"assistant\"}}]}\n\n")
	out := rewriteSSEChunkModel(chunk, "genfity/claude-opus-4.8")
	text := string(out)
	if strings.Contains(text, "genflowai/claude-opus-4.8-thinking-agentic") || strings.Contains(text, "internal reasoning") || strings.Contains(strings.ToLower(text), "<thinking>") {
		t.Fatalf("stream thinking/model leaked: %q", out)
	}
	if !strings.Contains(text, ": keep-alive") {
		t.Fatalf("expected provider prelude normalization, got %q", out)
	}
	if !strings.Contains(text, `"content":"ok"`) {
		t.Fatalf("sanitized stream content missing: %q", out)
	}
}

func TestSanitizeErrorBodyMasksProviderCatalogError(t *testing.T) {
	body := []byte(`{"error":{"code":"invalid_request_error","message":"Model \"deepseek-v4-pro\" is not available in current public model catalog.","type":"invalid_request_error","upstream_status":400}}`)
	out := sanitizeErrorBody(body, 400)
	if strings.Contains(string(out), "deepseek") || strings.Contains(string(out), "model catalog") {
		t.Fatalf("provider error leaked: %s", out)
	}
	if !strings.Contains(string(out), customerGatewayBusyMessage) {
		t.Fatalf("safe message missing: %s", out)
	}
}

func TestRewriteSSEChunkModel_AnthropicMessageStart(t *testing.T) {
	chunk := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"x\",\"model\":\"kimi-k2.6\",\"role\":\"assistant\"}}\n\n")
	out := rewriteSSEChunkModel(chunk, "genfity/claude-opus-4.8")
	if !strings.Contains(string(out), "genfity/claude-opus-4.8") || strings.Contains(string(out), "kimi") {
		t.Fatalf("message_start rewrite failed or leaked: %q", out)
	}
	if !strings.Contains(string(out), "event: message_start") {
		t.Fatalf("event line not preserved: %q", out)
	}
}

func TestRewriteSSEChunkModel_NoOpCases(t *testing.T) {
	// [DONE] sentinel + no model -> unchanged
	done := []byte("data: [DONE]\n\n")
	if got := rewriteSSEChunkModel(done, "genfity/x"); string(got) != string(done) {
		t.Fatalf("[DONE] should be no-op, got %q", got)
	}
	// empty publicModel -> unchanged
	c := []byte("data: {\"model\":\"kimi\"}\n\n")
	if got := rewriteSSEChunkModel(c, ""); string(got) != string(c) {
		t.Fatalf("empty publicModel should be no-op, got %q", got)
	}
	// chunk without model key -> unchanged (fast path)
	nm := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	if got := rewriteSSEChunkModel(nm, "genfity/x"); string(got) != string(nm) {
		t.Fatalf("no-model chunk should be no-op, got %q", got)
	}
}

func TestSSERewriteBuffer_ReassemblesSplitEvents(t *testing.T) {
	buffer := sseRewriteBuffer{}
	part1 := []byte(": genflowaistreamopen\n")
	part2 := []byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"claude-opus-4.8-thinking-agentic\",\"choices\":[{\"delta\":{\"reasoning_content\":\"hidden\",\"role\":\"assistant\"}}]}\n\n")

	if got := buffer.append(part1, "genfity/claude-opus-4.8"); len(got) != 0 {
		t.Fatalf("expected no flushed output for partial SSE event, got %q", got)
	}

	got := buffer.append(part2, "genfity/claude-opus-4.8")
	out := string(got)
	if strings.Contains(out, ": keep-alive") || strings.Contains(out, ": genflow") {
		t.Fatalf("SSE comment leaked to JSON-oriented client, got %q", out)
	}
	if !strings.Contains(out, "\"model\":\"genfity/claude-opus-4.8\"") {
		t.Fatalf("expected public model rewrite, got %q", out)
	}
	if strings.Contains(out, "thinking-agentic") {
		t.Fatalf("upstream model leaked after split reassembly: %q", out)
	}
	if strings.Contains(out, "reasoning_content") || strings.Contains(out, "hidden") {
		t.Fatalf("reasoning_content leaked after split reassembly: %q", out)
	}
}

func TestSSERewriteBuffer_ReplacesCommentOnlyHeartbeatWithOpenAIChunk(t *testing.T) {
	buffer := sseRewriteBuffer{streamProtocol: "openai"}
	out := string(buffer.append([]byte(": keep-alive\n\n"), "genfity/claude-opus-4.8"))
	if strings.Contains(out, ": keep-alive") || !strings.Contains(out, `"object":"chat.completion.chunk"`) || !strings.Contains(out, `"delta":{}`) {
		t.Fatalf("unexpected heartbeat rewrite: %q", out)
	}
}

func TestSSERewriteBuffer_ReplacesCommentOnlyHeartbeatWithAnthropicPing(t *testing.T) {
	buffer := sseRewriteBuffer{streamProtocol: "anthropic"}
	out := string(buffer.append([]byte(": ka\n\n"), "genfity/claude-opus-4.8"))
	if strings.Contains(out, ": ka") || !strings.Contains(out, "event: ping") || !strings.Contains(out, `{"type":"ping"}`) {
		t.Fatalf("unexpected heartbeat rewrite: %q", out)
	}
}
