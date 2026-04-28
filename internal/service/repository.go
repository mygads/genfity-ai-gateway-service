package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"genfity-ai-gateway-service/internal/store"
)

type Store interface {
	UpsertPlan(context.Context, store.SubscriptionPlanSnapshot) (store.SubscriptionPlanSnapshot, error)
	ListPlans(context.Context) []store.SubscriptionPlanSnapshot
	GetPlanByCode(context.Context, string) (*store.SubscriptionPlanSnapshot, error)

	UpsertAPIKey(context.Context, store.APIKey) (store.APIKey, error)
	ListAPIKeysByUser(context.Context, string) []store.APIKey
	FindAPIKeyByPrefix(context.Context, string) (*store.APIKey, error)
	RevokeAPIKey(context.Context, uuid.UUID, time.Time) error
	UpdateAPIKeyStatus(context.Context, uuid.UUID, string) error

	UpsertModel(context.Context, store.AIModel) (store.AIModel, error)
	UpdateModel(context.Context, store.AIModel) (store.AIModel, error)
	DeleteModel(context.Context, uuid.UUID) error
	ListModels(context.Context) []store.AIModel
	GetModelByID(context.Context, uuid.UUID) (*store.AIModel, error)
	GetModelByPublicName(context.Context, string) (*store.AIModel, error)

	UpsertPrice(context.Context, store.AIModelPrice) (store.AIModelPrice, error)
	GetPriceByID(context.Context, uuid.UUID) (*store.AIModelPrice, error)
	UpdatePrice(context.Context, store.AIModelPrice) (store.AIModelPrice, error)
	DeletePrice(context.Context, uuid.UUID) error
	ListPrices(context.Context) []store.AIModelPrice

	UpsertRoute(context.Context, store.AIModelRoute) (store.AIModelRoute, error)
	GetRouteByID(context.Context, uuid.UUID) (*store.AIModelRoute, error)
	UpdateRoute(context.Context, store.AIModelRoute) (store.AIModelRoute, error)
	DeleteRoute(context.Context, uuid.UUID) error
	ListRoutes(context.Context) []store.AIModelRoute
	GetRouteByModelID(context.Context, uuid.UUID) (*store.AIModelRoute, error)

	// VirtualCombo methods
	UpsertVirtualCombo(context.Context, store.VirtualCombo) store.VirtualCombo
	ListVirtualCombos(context.Context) []store.VirtualCombo
	GetVirtualComboByID(context.Context, uuid.UUID) (*store.VirtualCombo, error)
	DeleteVirtualCombo(context.Context, uuid.UUID) error
	GetVirtualComboByModelID(context.Context, uuid.UUID) (*store.VirtualCombo, error)

	UpsertEntitlement(context.Context, store.CustomerEntitlement) (store.CustomerEntitlement, error)
	UpsertEntitlementByUser(context.Context, store.CustomerEntitlement) (store.CustomerEntitlement, error)
	GetEntitlementByUser(context.Context, string) (*store.CustomerEntitlement, error)
	UpsertBalanceSnapshot(context.Context, string, string) (*store.CustomerEntitlement, error)

	UpsertRouterInstance(context.Context, store.RouterInstance) (store.RouterInstance, error)
	GetRouterInstanceByID(context.Context, uuid.UUID) (*store.RouterInstance, error)
	UpdateRouterInstance(context.Context, store.RouterInstance) (store.RouterInstance, error)
	DeleteRouterInstance(context.Context, uuid.UUID) error
	ListRouterInstances(context.Context) []store.RouterInstance
	GetRouterInstance(context.Context, string) (*store.RouterInstance, error)

	ReserveQuotaTokens(context.Context, string, *string, time.Time, time.Time, int64, int64) error
	FinalizeQuotaTokens(context.Context, string, time.Time, time.Time, int64, int64, bool) error
	ReserveCreditBalance(context.Context, string, string, float64) error
	FinalizeCreditBalance(context.Context, string, string, float64, float64) error

	AppendUsage(context.Context, store.UsageLedgerEntry) (store.UsageLedgerEntry, error)
	ListUsage(context.Context) []store.UsageLedgerEntry
	ListUsageByUser(context.Context, string) []store.UsageLedgerEntry
	ListUsageByUserSince(context.Context, string, time.Time) []store.UsageLedgerEntry
	ListUsageByTenant(context.Context, string) []store.UsageLedgerEntry
	ListAllUsage(ctx context.Context, limit int) []store.UsageLedgerEntry
	SumUsageTokensByUserSince(context.Context, string, time.Time) int64
	IncrementQuotaCounter(context.Context, string, *string, time.Time, time.Time, int64) error
	DebitCreditBalance(context.Context, string, string, float64) error
}
