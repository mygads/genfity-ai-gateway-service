package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"genfity-ai-gateway-service/internal/store"
)

var ErrNotFound = errors.New("not found")

type MemoryStore struct {
	mu           sync.RWMutex
	plans        map[string]store.SubscriptionPlanSnapshot
	apiKeys      map[uuid.UUID]store.APIKey
	models       map[uuid.UUID]store.AIModel
	prices       map[uuid.UUID]store.AIModelPrice
	routes       map[uuid.UUID]store.AIModelRoute
	entitlements map[uuid.UUID]store.CustomerEntitlement
	routers      map[string]store.RouterInstance
	usage        []store.UsageLedgerEntry
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		plans:        make(map[string]store.SubscriptionPlanSnapshot),
		apiKeys:      make(map[uuid.UUID]store.APIKey),
		models:       make(map[uuid.UUID]store.AIModel),
		prices:       make(map[uuid.UUID]store.AIModelPrice),
		routes:       make(map[uuid.UUID]store.AIModelRoute),
		entitlements: make(map[uuid.UUID]store.CustomerEntitlement),
		routers:      make(map[string]store.RouterInstance),
		usage:        make([]store.UsageLedgerEntry, 0),
	}
}

func (s *MemoryStore) UpsertPlan(_ context.Context, plan store.SubscriptionPlanSnapshot) store.SubscriptionPlanSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if plan.CreatedAt.IsZero() {
		plan.CreatedAt = now
	}
	plan.UpdatedAt = now
	if plan.SyncedFromGenfityAt.IsZero() {
		plan.SyncedFromGenfityAt = now
	}
	s.plans[plan.PlanCode] = plan
	return plan
}

func (s *MemoryStore) ListPlans(_ context.Context) []store.SubscriptionPlanSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]store.SubscriptionPlanSnapshot, 0, len(s.plans))
	for _, item := range s.plans {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].PlanCode < items[j].PlanCode })
	return items
}

func (s *MemoryStore) GetPlanByCode(_ context.Context, planCode string) (*store.SubscriptionPlanSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.plans[planCode]
	if !ok {
		return nil, ErrNotFound
	}
	copy := item
	return &copy, nil
}

func (s *MemoryStore) UpsertAPIKey(_ context.Context, key store.APIKey) store.APIKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key.CreatedAt.IsZero() {
		key.CreatedAt = time.Now().UTC()
	}
	s.apiKeys[key.ID] = key
	return key
}

func (s *MemoryStore) ListUsage(_ context.Context) []store.UsageLedgerEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]store.UsageLedgerEntry, 0, len(s.usage))
	items = append(items, s.usage...)
	sort.Slice(items, func(i, j int) bool { return items[i].StartedAt.After(items[j].StartedAt) })
	return items
}

func (s *MemoryStore) ListUsageByUserSince(_ context.Context, userID string, since time.Time) []store.UsageLedgerEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]store.UsageLedgerEntry, 0)
	for _, item := range s.usage {
		if item.GenfityUserID == userID && !item.StartedAt.Before(since) {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].StartedAt.After(items[j].StartedAt) })
	return items
}

func (s *MemoryStore) DebitCreditBalance(_ context.Context, userID string, planCode string, debitUsd float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, item := range s.entitlements {
		if item.GenfityUserID != userID || item.PlanCode != planCode {
			continue
		}
		if entitlementPricingGroup(item) != "credit_package" || item.BalanceSnapshot == nil {
			continue
		}
		current, err := strconv.ParseFloat(*item.BalanceSnapshot, 64)
		if err != nil {
			return err
		}
		next := current - debitUsd
		if next < 0 {
			next = 0
		}
		value := fmt.Sprintf("%.6f", next)
		item.BalanceSnapshot = &value
		s.entitlements[id] = item
		return nil
	}
	return ErrNotFound
}

func (s *MemoryStore) ListUsageByTenant(_ context.Context, tenantID string) []store.UsageLedgerEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]store.UsageLedgerEntry, 0)
	for _, item := range s.usage {
		if item.GenfityTenantID != nil && *item.GenfityTenantID == tenantID {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].StartedAt.After(items[j].StartedAt) })
	return items
}

func (s *MemoryStore) UpsertBalanceSnapshot(_ context.Context, userID string, balance string) (*store.CustomerEntitlement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, item := range s.entitlements {
		if item.GenfityUserID == userID {
			item.BalanceSnapshot = &balance
			item.UpdatedFromGenfityAt = time.Now().UTC()
			s.entitlements[id] = item
			copy := item
			return &copy, nil
		}
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) UpsertEntitlement(_ context.Context, item store.CustomerEntitlement) store.CustomerEntitlement {
	s.mu.Lock()
	defer s.mu.Unlock()
	item.UpdatedFromGenfityAt = time.Now().UTC()
	s.entitlements[item.ID] = item
	return item
}

func (s *MemoryStore) UpsertEntitlementByUser(_ context.Context, item store.CustomerEntitlement) store.CustomerEntitlement {
	s.mu.Lock()
	defer s.mu.Unlock()
	item.UpdatedFromGenfityAt = time.Now().UTC()
	for id, existing := range s.entitlements {
		if existing.GenfityUserID == item.GenfityUserID {
			item.ID = id
			s.entitlements[id] = item
			return item
		}
	}
	s.entitlements[item.ID] = item
	return item
}

func (s *MemoryStore) ListAPIKeysByUser(_ context.Context, userID string) []store.APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]store.APIKey, 0)
	for _, item := range s.apiKeys {
		if item.GenfityUserID == userID {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items
}

func (s *MemoryStore) FindAPIKeyByPrefix(_ context.Context, prefix string) (*store.APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.apiKeys {
		if item.KeyPrefix == prefix {
			copy := item
			return &copy, nil
		}
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) RevokeAPIKey(_ context.Context, id uuid.UUID, revokedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.apiKeys[id]
	if !ok {
		return ErrNotFound
	}
	item.Status = "revoked"
	item.RevokedAt = &revokedAt
	s.apiKeys[id] = item
	return nil
}

func (s *MemoryStore) UpdateAPIKeyStatus(_ context.Context, id uuid.UUID, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.apiKeys[id]
	if !ok {
		return ErrNotFound
	}
	item.Status = status
	if status == "revoked" {
		now := time.Now().UTC()
		item.RevokedAt = &now
	} else {
		item.RevokedAt = nil
	}
	s.apiKeys[id] = item
	return nil
}

func (s *MemoryStore) UpsertModel(_ context.Context, model store.AIModel) store.AIModel {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if model.CreatedAt.IsZero() {
		model.CreatedAt = now
	}
	model.UpdatedAt = now
	s.models[model.ID] = model
	return model
}

func (s *MemoryStore) ListModels(_ context.Context) []store.AIModel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]store.AIModel, 0, len(s.models))
	for _, item := range s.models {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DisplayName < items[j].DisplayName })
	return items
}

func (s *MemoryStore) GetModelByPublicName(_ context.Context, publicModel string) (*store.AIModel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.models {
		if item.PublicModel == publicModel {
			copy := item
			return &copy, nil
		}
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) UpsertPrice(_ context.Context, price store.AIModelPrice) store.AIModelPrice {
	s.mu.Lock()
	defer s.mu.Unlock()
	if price.CreatedAt.IsZero() {
		price.CreatedAt = time.Now().UTC()
	}
	s.prices[price.ID] = price
	return price
}

func (s *MemoryStore) ListPrices(_ context.Context) []store.AIModelPrice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]store.AIModelPrice, 0, len(s.prices))
	for _, item := range s.prices {
		items = append(items, item)
	}
	return items
}

func (s *MemoryStore) UpsertRoute(_ context.Context, route store.AIModelRoute) store.AIModelRoute {
	s.mu.Lock()
	defer s.mu.Unlock()
	if route.CreatedAt.IsZero() {
		route.CreatedAt = time.Now().UTC()
	}
	s.routes[route.ID] = route
	return route
}

func (s *MemoryStore) ListRoutes(_ context.Context) []store.AIModelRoute {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]store.AIModelRoute, 0, len(s.routes))
	for _, item := range s.routes {
		items = append(items, item)
	}
	return items
}

func (s *MemoryStore) GetRouteByModelID(_ context.Context, modelID uuid.UUID) (*store.AIModelRoute, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.routes {
		if item.ModelID == modelID && item.Status == "active" {
			copy := item
			return &copy, nil
		}
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) GetEntitlementByUser(_ context.Context, userID string) (*store.CustomerEntitlement, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	var credit *store.CustomerEntitlement
	for _, item := range s.entitlements {
		if item.GenfityUserID != userID || item.Status != "active" {
			continue
		}
		if item.PeriodEnd != nil && item.PeriodEnd.Before(now) {
			continue
		}
		copy := item
		if entitlementPricingGroup(item) == "unlimited_plan" {
			return &copy, nil
		}
		if credit == nil {
			credit = &copy
		}
	}
	if credit != nil {
		return credit, nil
	}
	return nil, ErrNotFound
}

func entitlementPricingGroup(item store.CustomerEntitlement) string {
	if len(item.Metadata) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(item.Metadata, &payload); err != nil {
		return ""
	}
	if value, ok := payload["pricingGroup"].(string); ok {
		return value
	}
	return ""
}

func (s *MemoryStore) UpsertRouterInstance(_ context.Context, item store.RouterInstance) store.RouterInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	s.routers[item.Code] = item
	return item
}

func (s *MemoryStore) ListRouterInstances(_ context.Context) []store.RouterInstance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]store.RouterInstance, 0, len(s.routers))
	for _, item := range s.routers {
		items = append(items, item)
	}
	return items
}

func (s *MemoryStore) GetRouterInstance(_ context.Context, code string) (*store.RouterInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.routers[code]
	if !ok {
		return nil, ErrNotFound
	}
	copy := item
	return &copy, nil
}

func (s *MemoryStore) AppendUsage(_ context.Context, item store.UsageLedgerEntry) store.UsageLedgerEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = append(s.usage, item)
	return item
}

func (s *MemoryStore) ListUsageByUser(_ context.Context, userID string) []store.UsageLedgerEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]store.UsageLedgerEntry, 0)
	for _, item := range s.usage {
		if item.GenfityUserID == userID {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].StartedAt.After(items[j].StartedAt) })
	return items
}
