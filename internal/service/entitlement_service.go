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

// ListActiveByUser returns every active entitlement for the user. Use
// when you need a specific pricing_group row (e.g. credit_package row
// for credit balance) and the legacy GetByUser would return the wrong
// one because it picks the highest-priority entitlement only. Bypasses
// the GetByUser cache; the underlying query is still indexed and runs
// once per request, so the impact is small.
func (s *EntitlementService) ListActiveByUser(ctx context.Context, userID string) ([]store.CustomerEntitlement, error) {
	return s.store.ListActiveEntitlementsByUser(ctx, userID)
}

// GetCreditEntitlementByUser returns the credit_package entitlement
// for the user (if any). credit-pinned API keys must read
// CreditBalance from this row, not from the unlimited row whose
// CreditBalance is always NULL. Returns ErrNotFound when the user
// has no credit_package entitlement.
func (s *EntitlementService) GetCreditEntitlementByUser(ctx context.Context, userID string) (*store.CustomerEntitlement, error) {
	rows, err := s.ListActiveByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if entitlementPricingGroup(rows[i]) == "credit_package" {
			row := rows[i]
			return &row, nil
		}
	}
	return nil, ErrNotFound
}

// GetPaygEntitlementByUser returns the payg_topup entitlement for the
// user (if any). PAYG-pinned API keys must read PaygUsdBalance from
// this row.
func (s *EntitlementService) GetPaygEntitlementByUser(ctx context.Context, userID string) (*store.CustomerEntitlement, error) {
	rows, err := s.ListActiveByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if entitlementPricingGroup(rows[i]) == "payg_topup" {
			row := rows[i]
			return &row, nil
		}
	}
	return nil, ErrNotFound
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
	// We intentionally DO NOT block on plan.Status != "active" here.
	// "inactive" in genfity-app means "hidden from the marketplace + new
	// checkout disabled" — existing subscribers keep their entitlement
	// alive until period_end. Blocking at the runtime layer would cancel
	// paid users mid-period whenever an admin retires a plan, which is
	// the opposite of what admins expect from "inactive". The marketplace
	// and checkout endpoints are responsible for filtering inactive
	// plans away from new buyers.
	return &ActiveSubscription{Entitlement: entitlement, Plan: plan}, nil
}
