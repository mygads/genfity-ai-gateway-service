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

	UpsertAPIKey(context.Context, store.APIKey) store.APIKey
	ListAPIKeysByUser(context.Context, string) []store.APIKey
	FindAPIKeyByPrefix(context.Context, string) (*store.APIKey, error)
	RevokeAPIKey(context.Context, uuid.UUID, time.Time) error

	UpsertModel(context.Context, store.AIModel) store.AIModel
	ListModels(context.Context) []store.AIModel
	GetModelByPublicName(context.Context, string) (*store.AIModel, error)

	UpsertPrice(context.Context, store.AIModelPrice) store.AIModelPrice
	ListPrices(context.Context) []store.AIModelPrice

	UpsertRoute(context.Context, store.AIModelRoute) store.AIModelRoute
	ListRoutes(context.Context) []store.AIModelRoute
	GetRouteByModelID(context.Context, uuid.UUID) (*store.AIModelRoute, error)

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
	ListUsageByTenant(context.Context, string) []store.UsageLedgerEntry
}
