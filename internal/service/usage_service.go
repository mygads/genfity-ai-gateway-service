package service

import (
	"context"
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

func (s *UsageService) Record(ctx context.Context, entry store.UsageLedgerEntry) store.UsageLedgerEntry {
	return s.store.AppendUsage(ctx, entry)
}

func (s *UsageService) ListByUser(ctx context.Context, userID uuid.UUID) []store.UsageLedgerEntry {
	return s.store.ListUsageByUser(ctx, userID)
}

func (s *UsageService) SummaryByUser(ctx context.Context, userID uuid.UUID) map[string]any {
	entries := s.ListByUser(ctx, userID)
	var promptTokens int64
	var completionTokens int64
	var totalTokens int64
	for _, item := range entries {
		promptTokens += item.PromptTokens
		completionTokens += item.CompletionTokens
		totalTokens += item.TotalTokens
	}
	return map[string]any{
		"request_count":     len(entries),
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      totalTokens,
	}
}
