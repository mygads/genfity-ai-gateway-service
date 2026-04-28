package service

import (
	"context"
	"strconv"
	"time"

	"github.com/rs/zerolog"

	"genfity-ai-gateway-service/internal/store"
)

type UsageService struct {
	store Store
	log   zerolog.Logger
}

func NewUsageService(store Store, logger zerolog.Logger) *UsageService {
	return &UsageService{store: store, log: logger.With().Str("component", "usage_service").Logger()}
}

func (s *UsageService) Record(ctx context.Context, entry store.UsageLedgerEntry) (store.UsageLedgerEntry, error) {
	return s.store.AppendUsage(ctx, entry)
}

func (s *UsageService) ListByUser(ctx context.Context, userID string) []store.UsageLedgerEntry {
	return s.store.ListUsageByUser(ctx, userID)
}

func (s *UsageService) SummaryByUser(ctx context.Context, userID string) map[string]any {
	return s.summaryForEntries(s.ListByUser(ctx, userID))
}

func (s *UsageService) SummaryByUser30d(ctx context.Context, userID string) map[string]any {
	entries := s.store.ListUsageByUserSince(ctx, userID, time.Now().UTC().Add(-30*24*time.Hour))
	return s.summaryForEntries(entries)
}

func (s *UsageService) summaryForEntries(entries []store.UsageLedgerEntry) map[string]any {
	var promptTokens int64
	var completionTokens int64
	var totalTokens int64
	var cachedTokens int64
	var reasoningTokens int64
	totalCostUsd := 0.0
	for _, item := range entries {
		promptTokens += item.PromptTokens
		completionTokens += item.CompletionTokens
		totalTokens += item.TotalTokens
		cachedTokens += item.CachedTokens
		reasoningTokens += item.ReasoningTokens
		if v, err := strconv.ParseFloat(item.TotalCost, 64); err == nil {
			totalCostUsd += v
		}
	}
	return map[string]any{
		"request_count":     len(entries),
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      totalTokens,
		"cached_tokens":     cachedTokens,
		"reasoning_tokens":  reasoningTokens,
		"input_tokens":      promptTokens,
		"output_tokens":     completionTokens,
		"total_cost_usd":    totalCostUsd,
		"period":            "30d",
	}
}

func (s *UsageService) TokensUsedSince(ctx context.Context, userID string, since time.Time) int64 {
	return s.store.SumUsageTokensByUserSince(ctx, userID, since)
}

func (s *UsageService) IncrementQuotaCounter(ctx context.Context, userID string, tenantID *string, periodStart time.Time, periodEnd time.Time, tokens int64) error {
	return s.store.IncrementQuotaCounter(ctx, userID, tenantID, periodStart, periodEnd, tokens)
}

func (s *UsageService) ReserveQuotaTokens(ctx context.Context, userID string, tenantID *string, periodStart time.Time, periodEnd time.Time, tokens int64, limit int64) error {
	return s.store.ReserveQuotaTokens(ctx, userID, tenantID, periodStart, periodEnd, tokens, limit)
}

func (s *UsageService) FinalizeQuotaTokens(ctx context.Context, userID string, periodStart time.Time, periodEnd time.Time, reservedTokens int64, usedTokens int64, countRequest bool) error {
	return s.store.FinalizeQuotaTokens(ctx, userID, periodStart, periodEnd, reservedTokens, usedTokens, countRequest)
}

func (s *UsageService) DebitCreditBalance(ctx context.Context, userID string, planCode string, debitUsd float64) error {
	return s.store.DebitCreditBalance(ctx, userID, planCode, debitUsd)
}

func (s *UsageService) ReserveCreditBalance(ctx context.Context, userID string, planCode string, amountUsd float64) error {
	return s.store.ReserveCreditBalance(ctx, userID, planCode, amountUsd)
}

func (s *UsageService) FinalizeCreditBalance(ctx context.Context, userID string, planCode string, reservedUsd float64, actualUsd float64) error {
	return s.store.FinalizeCreditBalance(ctx, userID, planCode, reservedUsd, actualUsd)
}

func (s *UsageService) ListAll(ctx context.Context, limit int) []store.UsageLedgerEntry {
	return s.store.ListAllUsage(ctx, limit)
}
