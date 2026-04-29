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

var (
	ErrNotFound            = errors.New("not found")
	ErrQuotaExceeded       = errors.New("quota exceeded")
	ErrInsufficientBalance = errors.New("insufficient balance")
)

type MemoryStore struct {
	mu            sync.RWMutex
	plans         map[string]store.SubscriptionPlanSnapshot
	apiKeys       map[uuid.UUID]store.APIKey
	models        map[uuid.UUID]store.AIModel
	prices        map[uuid.UUID]store.AIModelPrice
	routes        map[uuid.UUID]store.AIModelRoute
	combos        map[uuid.UUID]store.VirtualCombo
	entitlements  map[uuid.UUID]store.CustomerEntitlement
	routers       map[string]store.RouterInstance
	quotaReserved map[string]int64
	quotaUsed     map[string]int64
	quotaRequests map[string]int64
	usage         []store.UsageLedgerEntry
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		plans:         make(map[string]store.SubscriptionPlanSnapshot),
		apiKeys:       make(map[uuid.UUID]store.APIKey),
		models:        make(map[uuid.UUID]store.AIModel),
		prices:        make(map[uuid.UUID]store.AIModelPrice),
		routes:        make(map[uuid.UUID]store.AIModelRoute),
		combos:        make(map[uuid.UUID]store.VirtualCombo),
		entitlements:  make(map[uuid.UUID]store.CustomerEntitlement),
		routers:       make(map[string]store.RouterInstance),
		quotaReserved: make(map[string]int64),
		quotaUsed:     make(map[string]int64),
		quotaRequests: make(map[string]int64),
		usage:         make([]store.UsageLedgerEntry, 0),
	}
}

func (s *MemoryStore) UpsertPlan(_ context.Context, plan store.SubscriptionPlanSnapshot) (store.SubscriptionPlanSnapshot, error) {
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
	return plan, nil
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

func (s *MemoryStore) UpsertAPIKey(_ context.Context, key store.APIKey) (store.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key.CreatedAt.IsZero() {
		key.CreatedAt = time.Now().UTC()
	}
	s.apiKeys[key.ID] = key
	return key, nil
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
		if item.KeyPrefix == prefix && item.Status == "active" {
			copy := item
			return &copy, nil
		}
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) RevokeAPIKey(_ context.Context, id uuid.UUID, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.apiKeys[id]
	if !ok {
		return ErrNotFound
	}
	item.Status = "revoked"
	item.RevokedAt = &at
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
	s.apiKeys[id] = item
	return nil
}

func (s *MemoryStore) UpdateAPIKeyLastUsedAt(_ context.Context, id uuid.UUID, ts time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.apiKeys[id]
	if !ok {
		return ErrNotFound
	}
	item.LastUsedAt = &ts
	s.apiKeys[id] = item
	return nil
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
			return ErrInsufficientBalance
		}
		value := fmt.Sprintf("%.6f", next)
		item.BalanceSnapshot = &value
		s.entitlements[id] = item
		return nil
	}
	return ErrNotFound
}

func memoryQuotaKey(userID string, periodStart time.Time, periodEnd time.Time) string {
	return userID + ":" + periodStart.UTC().Format(time.RFC3339Nano) + ":" + periodEnd.UTC().Format(time.RFC3339Nano)
}

func (s *MemoryStore) SumUsageTokensByUserSince(_ context.Context, userID string, since time.Time) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total int64
	for _, item := range s.usage {
		if item.GenfityUserID == userID && item.Status == "success" && !item.StartedAt.Before(since) {
			total += item.TotalTokens
		}
	}
	return total
}

func (s *MemoryStore) IncrementQuotaCounter(_ context.Context, userID string, _ *string, periodStart time.Time, periodEnd time.Time, tokens int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := memoryQuotaKey(userID, periodStart, periodEnd)
	if tokens > 0 {
		s.quotaUsed[key] += tokens
	}
	s.quotaRequests[key]++
	return nil
}

func (s *MemoryStore) ReserveQuotaTokens(_ context.Context, userID string, _ *string, periodStart time.Time, periodEnd time.Time, tokens int64, limit int64) error {
	if tokens <= 0 || limit <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := memoryQuotaKey(userID, periodStart, periodEnd)
	used := s.quotaUsed[key]
	if used == 0 {
		// Fall back to scanning ledger if counter has not yet been initialized.
		for _, item := range s.usage {
			if item.GenfityUserID == userID && item.Status == "success" && !item.StartedAt.Before(periodStart) {
				used += item.TotalTokens
			}
		}
	}
	if used+s.quotaReserved[key]+tokens > limit {
		return ErrQuotaExceeded
	}
	s.quotaReserved[key] += tokens
	return nil
}

func (s *MemoryStore) FinalizeQuotaTokens(_ context.Context, userID string, periodStart time.Time, periodEnd time.Time, reservedTokens int64, _ int64, _ bool) error {
	if reservedTokens <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := memoryQuotaKey(userID, periodStart, periodEnd)
	next := s.quotaReserved[key] - reservedTokens
	if next <= 0 {
		delete(s.quotaReserved, key)
		return nil
	}
	s.quotaReserved[key] = next
	return nil
}

func (s *MemoryStore) ReserveCreditBalance(_ context.Context, userID string, planCode string, amountUsd float64) error {
	if amountUsd <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := false
	for id, item := range s.entitlements {
		if item.GenfityUserID != userID || item.PlanCode != planCode {
			continue
		}
		if entitlementPricingGroup(item) != "credit_package" || item.BalanceSnapshot == nil {
			continue
		}
		matched = true
		snapshot, err := strconv.ParseFloat(*item.BalanceSnapshot, 64)
		if err != nil {
			return err
		}
		var reserved float64
		if item.BalanceReserved != nil {
			reserved, err = strconv.ParseFloat(*item.BalanceReserved, 64)
			if err != nil {
				return err
			}
		}
		if snapshot-reserved < amountUsd {
			return ErrInsufficientBalance
		}
		nextReserved := reserved + amountUsd
		value := fmt.Sprintf("%.6f", nextReserved)
		item.BalanceReserved = &value
		s.entitlements[id] = item
		return nil
	}
	if matched {
		return ErrInsufficientBalance
	}
	return ErrNotFound
}

func (s *MemoryStore) FinalizeCreditBalance(_ context.Context, userID string, planCode string, reservedUsd float64, actualUsd float64) error {
	if reservedUsd <= 0 && actualUsd <= 0 {
		return nil
	}
	if reservedUsd < 0 {
		reservedUsd = 0
	}
	if actualUsd < 0 {
		actualUsd = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, item := range s.entitlements {
		if item.GenfityUserID != userID || item.PlanCode != planCode {
			continue
		}
		if entitlementPricingGroup(item) != "credit_package" || item.BalanceSnapshot == nil {
			continue
		}
		snapshot, err := strconv.ParseFloat(*item.BalanceSnapshot, 64)
		if err != nil {
			return err
		}
		var reserved float64
		if item.BalanceReserved != nil {
			reserved, err = strconv.ParseFloat(*item.BalanceReserved, 64)
			if err != nil {
				return err
			}
		}
		nextReserved := reserved - reservedUsd
		if nextReserved < 0 {
			nextReserved = 0
		}
		debit := actualUsd
		if debit > snapshot {
			debit = snapshot
		}
		nextSnapshot := snapshot - debit
		if nextSnapshot < 0 {
			nextSnapshot = 0
		}
		snapshotValue := fmt.Sprintf("%.6f", nextSnapshot)
		reservedValue := fmt.Sprintf("%.6f", nextReserved)
		item.BalanceSnapshot = &snapshotValue
		item.BalanceReserved = &reservedValue
		s.entitlements[id] = item
		return nil
	}
	return nil
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

func (s *MemoryStore) UpsertEntitlement(_ context.Context, item store.CustomerEntitlement) (store.CustomerEntitlement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.ID == uuid.Nil {
		item.ID = uuid.New()
	}
	if item.UpdatedFromGenfityAt.IsZero() {
		item.UpdatedFromGenfityAt = time.Now().UTC()
	}
	s.entitlements[item.ID] = item
	return item, nil
}

func (s *MemoryStore) UpsertEntitlementByUser(_ context.Context, item store.CustomerEntitlement) (store.CustomerEntitlement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item.UpdatedFromGenfityAt = time.Now().UTC()
	for id, existing := range s.entitlements {
		if existing.GenfityUserID == item.GenfityUserID && existing.PlanCode == item.PlanCode {
			if item.ID == uuid.Nil {
				item.ID = existing.ID
			}
			s.entitlements[id] = item
			return item, nil
		}
	}
	if item.ID == uuid.Nil {
		item.ID = uuid.New()
	}
	s.entitlements[item.ID] = item
	return item, nil
}

func (s *MemoryStore) UpsertModel(_ context.Context, model store.AIModel) (store.AIModel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if model.CreatedAt.IsZero() {
		model.CreatedAt = now
	}
	model.UpdatedAt = now
	s.models[model.ID] = model
	return model, nil
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

func (s *MemoryStore) GetModelByID(_ context.Context, id uuid.UUID) (*store.AIModel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.models[id]
	if !ok {
		return nil, ErrNotFound
	}
	copy := item
	return &copy, nil
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

func (s *MemoryStore) UpdateModel(_ context.Context, model store.AIModel) (store.AIModel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.models[model.ID]
	if !ok {
		return store.AIModel{}, ErrNotFound
	}
	if model.CreatedAt.IsZero() {
		model.CreatedAt = current.CreatedAt
	}
	model.UpdatedAt = time.Now().UTC()
	s.models[model.ID] = model
	return model, nil
}

func (s *MemoryStore) DeleteModel(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.models[id]; !ok {
		return ErrNotFound
	}
	delete(s.models, id)
	return nil
}

func (s *MemoryStore) UpsertPrice(_ context.Context, price store.AIModelPrice) (store.AIModelPrice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if price.CreatedAt.IsZero() {
		price.CreatedAt = time.Now().UTC()
	}
	s.prices[price.ID] = price
	return price, nil
}

func (s *MemoryStore) GetPriceByID(_ context.Context, id uuid.UUID) (*store.AIModelPrice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.prices[id]
	if !ok {
		return nil, ErrNotFound
	}
	copy := item
	return &copy, nil
}

func (s *MemoryStore) UpdatePrice(_ context.Context, price store.AIModelPrice) (store.AIModelPrice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.prices[price.ID]
	if !ok {
		return store.AIModelPrice{}, ErrNotFound
	}
	if price.CreatedAt.IsZero() {
		price.CreatedAt = current.CreatedAt
	}
	s.prices[price.ID] = price
	return price, nil
}

func (s *MemoryStore) DeletePrice(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.prices[id]; !ok {
		return ErrNotFound
	}
	delete(s.prices, id)
	return nil
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

func (s *MemoryStore) UpsertRoute(_ context.Context, route store.AIModelRoute) (store.AIModelRoute, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if route.CreatedAt.IsZero() {
		route.CreatedAt = time.Now().UTC()
	}
	s.routes[route.ID] = route
	return route, nil
}

func (s *MemoryStore) GetRouteByID(_ context.Context, id uuid.UUID) (*store.AIModelRoute, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.routes[id]
	if !ok {
		return nil, ErrNotFound
	}
	copy := item
	return &copy, nil
}

func (s *MemoryStore) UpdateRoute(_ context.Context, route store.AIModelRoute) (store.AIModelRoute, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.routes[route.ID]
	if !ok {
		return store.AIModelRoute{}, ErrNotFound
	}
	if route.CreatedAt.IsZero() {
		route.CreatedAt = current.CreatedAt
	}
	s.routes[route.ID] = route
	return route, nil
}

func (s *MemoryStore) DeleteRoute(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.routes[id]; !ok {
		return ErrNotFound
	}
	delete(s.routes, id)
	return nil
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

// VirtualCombo methods

func (s *MemoryStore) UpsertVirtualCombo(_ context.Context, combo store.VirtualCombo) store.VirtualCombo {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if combo.CreatedAt.IsZero() {
		combo.CreatedAt = now
	}
	combo.UpdatedAt = now
	s.combos[combo.ID] = combo
	return combo
}

func (s *MemoryStore) ListVirtualCombos(_ context.Context) []store.VirtualCombo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]store.VirtualCombo, 0, len(s.combos))
	for _, item := range s.combos {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

func (s *MemoryStore) GetVirtualComboByID(_ context.Context, id uuid.UUID) (*store.VirtualCombo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.combos[id]
	if !ok {
		return nil, ErrNotFound
	}
	copy := item
	return &copy, nil
}

func (s *MemoryStore) DeleteVirtualCombo(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.combos[id]; !ok {
		return ErrNotFound
	}
	delete(s.combos, id)
	return nil
}

func (s *MemoryStore) GetVirtualComboByModelID(_ context.Context, modelID uuid.UUID) (*store.VirtualCombo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.combos {
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

func (s *MemoryStore) UpsertRouterInstance(_ context.Context, item store.RouterInstance) (store.RouterInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	s.routers[item.Code] = item
	return item, nil
}

func (s *MemoryStore) GetRouterInstanceByID(_ context.Context, id uuid.UUID) (*store.RouterInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.routers {
		if item.ID == id {
			copy := item
			return &copy, nil
		}
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) UpdateRouterInstance(_ context.Context, item store.RouterInstance) (store.RouterInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for code, current := range s.routers {
		if current.ID == item.ID {
			if item.CreatedAt.IsZero() {
				item.CreatedAt = current.CreatedAt
			}
			if code != item.Code {
				delete(s.routers, code)
			}
			s.routers[item.Code] = item
			return item, nil
		}
	}
	return store.RouterInstance{}, ErrNotFound
}

func (s *MemoryStore) DeleteRouterInstance(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for code, item := range s.routers {
		if item.ID == id {
			delete(s.routers, code)
			return nil
		}
	}
	return ErrNotFound
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

func (s *MemoryStore) AppendUsage(_ context.Context, item store.UsageLedgerEntry) (store.UsageLedgerEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = append(s.usage, item)
	return item, nil
}

func (s *MemoryStore) ListAllUsage(_ context.Context, limit int) []store.UsageLedgerEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]store.UsageLedgerEntry, 0, len(s.usage))
	items = append(items, s.usage...)
	if limit > 0 && len(items) > limit {
		return items[len(items)-limit:]
	}
	return items
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
