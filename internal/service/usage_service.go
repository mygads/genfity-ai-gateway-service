package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
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
	now := time.Now().UTC()
	entries := s.store.ListUsageByUserSince(ctx, userID, now.Add(-30*24*time.Hour))
	summary := s.summaryForEntries(entries)
	// Dashboard "Request hari ini" needs a UTC-day count, not the 30d
	// total. Compute it from the same slice (already filtered to last
	// 30d, so the day boundary is always inside it). Also expose the
	// 30d count under both `request_count` (legacy) and `requests` so
	// frontends that read either field land on the right number.
	//
	// Free-model traffic (public_model suffixed `:free`) is excluded —
	// the plan-level "Request hari ini" bar must mirror the same gate
	// the rate limiter uses (free models don't burn the plan's RPD or
	// MaxRequestsPerPeriod). Counting them here would make the dashboard
	// disagree with the actual cap the gateway enforces.
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	requestsToday := 0
	requestsTodayFree := 0
	for _, item := range entries {
		if item.StartedAt.Before(startOfDay) {
			continue
		}
		if isFreeModelEntry(item) {
			requestsTodayFree++
			continue
		}
		requestsToday++
	}
	summary["requests_today"] = requestsToday
	summary["requests_today_free"] = requestsTodayFree
	summary["requests"] = summary["request_count"]
	return summary
}

// isFreeModelEntry returns true when a usage_ledger row was served by a
// free-tier model. The convention across the catalog is a `:free`
// suffix on public_model (e.g. `genfity/gpt-5.5:free`); the
// router_model carries the same marker for entries logged before the
// public_model column was populated.
func isFreeModelEntry(item store.UsageLedgerEntry) bool {
	if strings.HasSuffix(item.PublicModel, ":free") {
		return true
	}
	if item.RouterModel != nil && strings.HasSuffix(*item.RouterModel, ":free") {
		return true
	}
	return false
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

// PRD v3 Phase 2 — request-credit + PAYG USD balance service facades.
//
// These simply forward to the underlying Store; they exist so the
// gateway handler's 3-priority reservation chain has a single service
// object to talk to (`h.usage.ReserveRequestCredits(...)` reads cleaner
// than `h.store.ReserveRequestCredits(...)` and mirrors the existing
// quota/credit helpers).
//
// Caller contract:
//   - amount must be > 0 (zero/negative is a no-op)
//   - ErrInsufficientBalance bubbles up unchanged from the store so the
//     handler can map to HTTP 402
//   - Finalize is idempotent-safe: calling it twice with the same
//     reserved amount releases the reservation once and is a no-op the
//     second time (GREATEST(..., 0) guards)

func (s *UsageService) ReserveRequestCredits(ctx context.Context, userID string, amount float64) error {
	return s.store.ReserveRequestCredits(ctx, userID, amount)
}

func (s *UsageService) FinalizeRequestCredits(ctx context.Context, userID string, reservedAmount, actualAmount float64) error {
	return s.store.FinalizeRequestCredits(ctx, userID, reservedAmount, actualAmount)
}

func (s *UsageService) ReservePaygUsdBalance(ctx context.Context, userID string, amount float64) error {
	return s.store.ReservePaygUsdBalance(ctx, userID, amount)
}

func (s *UsageService) FinalizePaygUsdBalance(ctx context.Context, userID string, reservedAmount, actualAmount float64) error {
	return s.store.FinalizePaygUsdBalance(ctx, userID, reservedAmount, actualAmount)
}

func (s *UsageService) ListAll(ctx context.Context, limit int) []store.UsageLedgerEntry {
	return s.store.ListAllUsage(ctx, limit)
}

func (s *UsageService) SummaryGrouped(ctx context.Context, since time.Time) []store.UsageSummaryRow {
	return s.store.ListUsageSummaryGrouped(ctx, since)
}

// UsageByBillingModeSince rolls up one user's successful usage by
// billing_mode for [since, now). Powers the admin billing-detail modal.
func (s *UsageService) UsageByBillingModeSince(ctx context.Context, userID string, since time.Time) []store.BillingModeUsageRow {
	return s.store.ListUsageByBillingModeSince(ctx, userID, since)
}

func (s *UsageService) CreditBalances(ctx context.Context) []store.CreditBalanceRow {
	return s.store.ListCreditBalances(ctx)
}

// Analytics facades — power the admin /admin/usage/analytics endpoint.
// Each one is a thin pass-through; the heavy lifting is in the store.
func (s *UsageService) Timeseries(ctx context.Context, since time.Time, bucket string) []store.UsageTimeseriesPoint {
	return s.store.ListUsageTimeseries(ctx, since, bucket)
}

func (s *UsageService) TopModels(ctx context.Context, since time.Time, limit int) []store.TopModelRow {
	return s.store.ListTopModels(ctx, since, limit)
}

func (s *UsageService) BillingModeBreakdown(ctx context.Context, since time.Time) []store.BillingModeBreakdownRow {
	return s.store.ListBillingModeBreakdown(ctx, since)
}

func (s *UsageService) StatusBreakdown(ctx context.Context, since time.Time) []store.StatusBreakdownRow {
	return s.store.ListStatusBreakdown(ctx, since)
}

func (s *UsageService) ErrorCodeBreakdown(ctx context.Context, since time.Time, limit int) []store.StatusBreakdownRow {
	return s.store.ListErrorCodeBreakdown(ctx, since, limit)
}

func (s *UsageService) ProviderStats(ctx context.Context, since time.Time) []store.ProviderStatsRow {
	return s.store.ListProviderStats(ctx, since)
}

func (s *UsageService) LatencyStats(ctx context.Context, since time.Time) store.LatencyStats {
	return s.store.LatencyStats(ctx, since)
}

func (s *UsageService) ListByAPIKey(ctx context.Context, apiKeyID uuid.UUID, limit int) []store.UsageLedgerEntry {
	return s.store.ListUsageByAPIKey(ctx, apiKeyID, limit)
}

func (s *UsageService) ListLogs(ctx context.Context, filter store.UsageLogFilter) ([]store.UsageLedgerEntry, int, error) {
	return s.store.ListUsageLogs(ctx, filter)
}
