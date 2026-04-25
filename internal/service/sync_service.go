package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"genfity-ai-gateway-service/internal/store"
)

type SyncService struct {
	store        Store
	entitlements *EntitlementService
	models       *ModelService
	usage        *UsageService
	log          zerolog.Logger
}

func NewSyncService(store Store, entitlements *EntitlementService, models *ModelService, usage *UsageService, logger zerolog.Logger) *SyncService {
	return &SyncService{
		store:        store,
		entitlements: entitlements,
		models:       models,
		usage:        usage,
		log:          logger.With().Str("component", "sync_service").Logger(),
	}
}

func (s *SyncService) SyncSubscriptionPlans(ctx context.Context, payload []store.SubscriptionPlanSnapshot) (int, error) {
	count := 0
	for _, item := range payload {
		if item.ID == uuid.Nil {
			item.ID = uuid.New()
		}
		s.store.UpsertPlan(ctx, item)
		count++
	}
	return count, nil
}

func (s *SyncService) SyncCustomerEntitlements(ctx context.Context, payload []store.CustomerEntitlement) (int, error) {
	count := 0
	for _, item := range payload {
		if item.ID == uuid.Nil {
			item.ID = uuid.New()
		}
		if _, err := s.entitlements.Upsert(ctx, item); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *SyncService) SyncCustomerBalance(ctx context.Context, userID string, balance string) error {
	_, err := s.store.UpsertBalanceSnapshot(ctx, userID, balance)
	return err
}

func (s *SyncService) ExportModels(ctx context.Context) []store.AIModel {
	return s.models.ListModels(ctx)
}

func (s *SyncService) ExportPlans(ctx context.Context) []store.SubscriptionPlanSnapshot {
	return s.store.ListPlans(ctx)
}

func (s *SyncService) ExportModelPrices(ctx context.Context) []store.AIModelPrice {
	return s.models.ListPrices(ctx)
}

func (s *SyncService) ExportUsageSummary(ctx context.Context, userID string) map[string]any {
	return s.usage.SummaryByUser(ctx, userID)
}
