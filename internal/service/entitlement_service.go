package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"genfity-ai-gateway-service/internal/store"
)

type EntitlementService struct {
	store Store
	log   zerolog.Logger
}

func NewEntitlementService(store Store, logger zerolog.Logger) *EntitlementService {
	return &EntitlementService{store: store, log: logger.With().Str("component", "entitlement_service").Logger()}
}

func (s *EntitlementService) Upsert(ctx context.Context, item store.CustomerEntitlement) (store.CustomerEntitlement, error) {
	if item.ID == uuid.Nil {
		item.ID = uuid.New()
	}
	if item.GenfityUserID == "" {
		return store.CustomerEntitlement{}, fmt.Errorf("genfity_user_id is required")
	}
	if item.PlanCode == "" {
		return store.CustomerEntitlement{}, fmt.Errorf("plan_code is required")
	}
	if item.Status == "" {
		item.Status = "active"
	}
	item.UpdatedFromGenfityAt = time.Now().UTC()
	return s.store.UpsertEntitlement(ctx, item), nil
}

func (s *EntitlementService) GetByUser(ctx context.Context, userID string) (*store.CustomerEntitlement, error) {
	return s.store.GetEntitlementByUser(ctx, userID)
}

func (s *EntitlementService) CheckActive(ctx context.Context, userID string) (*store.CustomerEntitlement, error) {
	entitlement, err := s.GetByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if entitlement.Status != "active" {
		return nil, fmt.Errorf("subscription_inactive")
	}
	if entitlement.PeriodEnd != nil && entitlement.PeriodEnd.Before(time.Now().UTC()) {
		return nil, fmt.Errorf("subscription_inactive")
	}
	return entitlement, nil
}
