package handler

import (
	"context"
	"testing"

	"genfity-ai-gateway-service/internal/service"
	"genfity-ai-gateway-service/internal/store"
)

func TestParseUsageFromBodySeparatesCacheUsage(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":70,"cache_creation_input_tokens":11},"completion_tokens_details":{"reasoning_tokens":9}}}`)

	metrics := parseUsageFromBody(body)

	if metrics.PromptTokens != 120 || metrics.CompletionTokens != 30 || metrics.TotalTokens != 150 {
		t.Fatalf("unexpected base usage: %+v", metrics)
	}
	if metrics.CachedTokens != 81 {
		t.Fatalf("cached tokens = %d, want 81", metrics.CachedTokens)
	}
	if metrics.CacheReadInputTokens != 70 {
		t.Fatalf("cache read tokens = %d, want 70", metrics.CacheReadInputTokens)
	}
	if metrics.CacheCreationInputTokens != 11 {
		t.Fatalf("cache creation tokens = %d, want 11", metrics.CacheCreationInputTokens)
	}
	if metrics.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want 9", metrics.ReasoningTokens)
	}
}

func TestQuotaLimitPrefersLivePlanSnapshot(t *testing.T) {
	entitlementQuota := int64(1000)
	planQuota := int64(6000)
	subscription := &service.ActiveSubscription{
		Entitlement: &store.CustomerEntitlement{QuotaTokensMonthly: &entitlementQuota},
		Plan:        &store.SubscriptionPlanSnapshot{QuotaTokensMonthly: &planQuota},
	}

	if got := quotaLimit(subscription); got != planQuota {
		t.Fatalf("quotaLimit() = %d, want %d", got, planQuota)
	}
}

func TestCalculateActualRequestCredits_Uses20kBuckets(t *testing.T) {
	if got := calculateActualRequestCredits(3, 39_000); got != 6 {
		t.Fatalf("calculateActualRequestCredits() = %v, want 6", got)
	}
	if got := calculateActualRequestCredits(3, 20_000); got != 3 {
		t.Fatalf("calculateActualRequestCredits() 20k = %v, want 3", got)
	}
	if got := calculateActualRequestCredits(3, 0); got != 0 {
		t.Fatalf("calculateActualRequestCredits() zero = %v, want 0", got)
	}
	if got := calculateActualRequestCredits(5.0/3.0, 60_000); got != 5 {
		t.Fatalf("calculateActualRequestCredits() exact = %v, want 5", got)
	}
	if got := calculateActualRequestCredits(4.0/3.0, 60_000); got != 4 {
		t.Fatalf("calculateActualRequestCredits() exact for 4 = %v, want 4", got)
	}
}

func TestEstimateReservedRequestCredits_UsesEstimatedBuckets(t *testing.T) {
	estimate := tokenReservationEstimate{TotalTokens: 30_000}
	if got := estimateReservedRequestCredits(3, estimate); got != 6 {
		t.Fatalf("estimateReservedRequestCredits() = %v, want 6", got)
	}
}

func TestClassifyCompletionBodyIgnoresHeartbeatOnlyStreams(t *testing.T) {
	tests := []struct {
		name string
		body string
		want completionBodyState
	}{
		{
			name: "openai heartbeat only",
			body: "data: {\"choices\":[{\"delta\":{},\"finish_reason\":null,\"index\":0}]}\n\ndata: [DONE]\n\n",
			want: completionBodyEmpty,
		},
		{
			name: "anthropic ping only",
			body: "event: ping\ndata: {\"type\":\"ping\"}\n\n",
			want: completionBodyEmpty,
		},
		{
			name: "done only",
			body: "data: [DONE]\n\n",
			want: completionBodyEmpty,
		},
		{
			name: "openai output without terminal",
			body: "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null,\"index\":0}]}\n\n",
			want: completionBodyPartial,
		},
		{
			name: "openai completed output",
			body: "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null,\"index\":0}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}],\"usage\":{\"completion_tokens\":1}}\n\ndata: [DONE]\n\n",
			want: completionBodyComplete,
		},
		{
			name: "anthropic completed tool use",
			body: "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"read_file\",\"input\":{}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			want: completionBodyComplete,
		},
		{
			name: "non streaming json remains complete",
			body: `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
			want: completionBodyComplete,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyCompletionBody([]byte(tc.body)); got != tc.want {
				t.Fatalf("classifyCompletionBody() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShouldCountAsSuccessRequiresCompletedStream(t *testing.T) {
	heartbeat := []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":null}]}\n\n")
	partial := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
	complete := append(append([]byte{}, partial...), []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")...)

	if shouldCountAsSuccess(200, heartbeat) {
		t.Fatal("heartbeat-only stream must not count as success")
	}
	if shouldCountAsSuccess(200, partial) {
		t.Fatal("unterminated partial stream must not count as success")
	}
	if !shouldCountAsSuccess(200, complete) {
		t.Fatal("completed stream should count as success")
	}
}

func TestSettlementStatusMarksCanceledIncompleteRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	heartbeat := []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":null}]}\n\n")
	partial := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
	complete := append(append([]byte{}, partial...), []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")...)

	if got := settlementStatus(ctx, 200, heartbeat); got != statusClientClosedRequest {
		t.Fatalf("heartbeat-only canceled request status = %d, want %d", got, statusClientClosedRequest)
	}
	if got := settlementStatus(ctx, 200, partial); got != statusClientClosedRequest {
		t.Fatalf("partial canceled request status = %d, want %d", got, statusClientClosedRequest)
	}
	if got := settlementStatus(ctx, 200, complete); got != 200 {
		t.Fatalf("completed response must retain upstream status, got %d", got)
	}
	if got := settlementStatus(context.Background(), 200, heartbeat); got != 200 {
		t.Fatalf("live request status changed unexpectedly: %d", got)
	}
}

func TestDetectProviderErrorPreservesIncompatiblePayloadRoutingOutcome(t *testing.T) {
	body := []byte(`{"error":{"message":"incompatible_payload: candidate skipped (request_too_large)","type":"invalid_request_error"}}`)
	code := detectProviderErrorFromBody(body)
	if code != "incompatible_payload" {
		t.Fatalf("detectProviderErrorFromBody() = %q, want incompatible_payload", code)
	}
	if namespaced := namespaceUpstreamErrorCode(code); namespaced != "incompatible_payload" {
		t.Fatalf("namespaceUpstreamErrorCode() = %q, want incompatible_payload", namespaced)
	}
}
