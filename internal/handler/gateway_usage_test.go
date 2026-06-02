package handler

import (
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
