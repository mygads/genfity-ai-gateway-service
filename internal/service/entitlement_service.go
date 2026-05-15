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
	cache *Cache
	log   zerolog.Logger
}

const (
	entitlementCacheNamespace = "ent"
	entitlementCacheTTL       = 30 * time.Second
)

type ActiveSubscription struct {
	Entitlement *store.CustomerEntitlement
	Plan        *store.SubscriptionPlanSnapshot
}

func NewEntitlementService(store Store, logger zerolog.Logger) *EntitlementService {
	return &EntitlementService{store: store, log: logger.With().Str("component", "entitlement_service").Logger()}
}

// WithCache attaches a Redis cache to the service. Read-through is used
// only for the "active subscription" hot path; reserve/finalize must
// always hit DB to keep balances consistent. Mutators bust the entry.
func (s *EntitlementService) WithCache(c *Cache) *EntitlementService {
	s.cache = c
	return s
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
	saved, err := s.store.UpsertEntitlementByUser(ctx, item)
	if err == nil {
		s.cache.Del(ctx, entitlementCacheNamespace, saved.GenfityUserID)
	}
	return saved, err
}

// InvalidateUser drops the cached entitlement for a user. Call after any
// out-of-band balance change (sync from genfity-app, finalize debit) so
// the next request reads the freshest snapshot.
func (s *EntitlementService) InvalidateUser(ctx context.Context, userID string) {
	s.cache.Del(ctx, entitlementCacheNamespace, userID)
}

func (s *EntitlementService) GetByUser(ctx context.Context, userID string) (*store.CustomerEntitlement, error) {
	if s.cache.Enabled() {
		var cached store.CustomerEntitlement
		if err := s.cache.Get(ctx, entitlementCacheNamespace, userID, &cached); err == nil {
			c := cached
			return &c, nil
		}
	}
	fresh, err := s.store.GetEntitlementByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	s.cache.Set(ctx, entitlementCacheNamespace, userID, fresh, entitlementCacheTTL)
	return fresh, nil
}

func (s *EntitlementService) CheckActive(ctx context.Context, userID string) (*store.CustomerEntitlement, error) {
	subscription, err := s.CheckActiveSubscription(ctx, userID)
	if err != nil {
		return nil, err
	}
	return subscription.Entitlement, nil
}

func (s *EntitlementService) CheckActiveSubscription(ctx context.Context, userID string) (*ActiveSubscription, error) {
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
	plan, err := s.store.GetPlanByCode(ctx, entitlement.PlanCode)
	if err != nil {
		return nil, err
	}
	if plan.Status != "active" {
		return nil, fmt.Errorf("subscription_inactive")
	}
	return &ActiveSubscription{Entitlement: entitlement, Plan: plan}, nil
}
