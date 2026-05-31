package handler

import (
	"strings"
	"testing"
)

func TestRewriteResponseModel_OpenAINonStream(t *testing.T) {
	body := []byte(`{"id":"x","object":"chat.completion","model":"minimax-m2.5","choices":[{"message":{"content":"ok"}}]}`)
	out := rewriteResponseModel(body, 200, "genfity/claude-opus-4.7")
	if !strings.Contains(string(out), `"genfity/claude-opus-4.7"`) {
		t.Fatalf("expected public model in body, got %s", out)
	}
	if strings.Contains(string(out), "minimax") {
		t.Fatalf("upstream model leaked: %s", out)
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
	chunk := []byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"minimax-m2.5\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	out := rewriteSSEChunkModel(chunk, "genfity/claude-opus-4.7")
	if !strings.Contains(string(out), "genfity/claude-opus-4.7") || strings.Contains(string(out), "minimax") {
		t.Fatalf("SSE rewrite failed or leaked: %q", out)
	}
	if !strings.HasSuffix(string(out), "\n\n") {
		t.Fatalf("SSE framing (trailing newlines) not preserved: %q", out)
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
