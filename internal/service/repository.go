package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"genfity-ai-gateway-service/internal/store"
)

type Store interface {
	UpsertPlan(context.Context, store.SubscriptionPlanSnapshot) store.SubscriptionPlanSnapshot
	ListPlans(context.Context) []store.SubscriptionPlanSnapshot
	GetPlanByCode(context.Context, string) (*store.SubscriptionPlanSnapshot, error)

	UpsertAPIKey(context.Context, store.APIKey) store.APIKey
	ListAPIKeysByUser(context.Context, string) []store.APIKey
	FindAPIKeyByPrefix(context.Context, string) (*store.APIKey, error)
	RevokeAPIKey(context.Context, uuid.UUID, time.Time) error
	UpdateAPIKeyStatus(context.Context, uuid.UUID, string) error

	UpsertModel(context.Context, store.AIModel) store.AIModel
	ListModels(context.Context) []store.AIModel
	GetModelByPublicName(context.Context, string) (*store.AIModel, error)

	UpsertPrice(context.Context, store.AIModelPrice) store.AIModelPrice
	ListPrices(context.Context) []store.AIModelPrice

	UpsertRoute(context.Context, store.AIModelRoute) store.AIModelRoute
	ListRoutes(context.Context) []store.AIModelRoute
	GetRouteByModelID(context.Context, uuid.UUID) (*store.AIModelRoute, error)

	// VirtualCombo methods
	UpsertVirtualCombo(context.Context, store.VirtualCombo) store.VirtualCombo
	ListVirtualCombos(context.Context) []store.VirtualCombo
	GetVirtualComboByID(context.Context, uuid.UUID) (*store.VirtualCombo, error)
	DeleteVirtualCombo(context.Context, uuid.UUID) error
	GetVirtualComboByModelID(context.Context, uuid.UUID) (*store.VirtualCombo, error)

	UpsertEntitlement(context.Context, store.CustomerEntitlement) store.CustomerEntitlement
	UpsertEntitlementByUser(context.Context, store.CustomerEntitlement) store.CustomerEntitlement
	GetEntitlementByUser(context.Context, string) (*store.CustomerEntitlement, error)
	UpsertBalanceSnapshot(context.Context, string, string) (*store.CustomerEntitlement, error)

	UpsertRouterInstance(context.Context, store.RouterInstance) store.RouterInstance
	ListRouterInstances(context.Context) []store.RouterInstance
	GetRouterInstance(context.Context, string) (*store.RouterInstance, error)

	AppendUsage(context.Context, store.UsageLedgerEntry) store.UsageLedgerEntry
	ListUsage(context.Context) []store.UsageLedgerEntry
	ListUsageByUser(context.Context, string) []store.UsageLedgerEntry
	ListUsageByUserSince(context.Context, string, time.Time) []store.UsageLedgerEntry
	ListUsageByTenant(context.Context, string) []store.UsageLedgerEntry
	ListAllUsage(ctx context.Context, limit int) []store.UsageLedgerEntry
	DebitCreditBalance(context.Context, string, string, float64) error
}
