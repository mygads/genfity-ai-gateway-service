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
	GetAPIKeyByID(context.Context, uuid.UUID) (*store.APIKey, error)
	RevokeAPIKey(context.Context, uuid.UUID, time.Time) error
	UpdateAPIKeyStatus(context.Context, uuid.UUID, string) error
	UpdateAPIKeyBillingSource(context.Context, uuid.UUID, string) error
	UpdateAPIKeyLastUsedAt(context.Context, uuid.UUID, time.Time) error

	UpsertModel(context.Context, store.AIModel) (store.AIModel, error)
	UpdateModel(context.Context, store.AIModel) (store.AIModel, error)
	DeleteModel(context.Context, uuid.UUID) error
	ListModels(context.Context) []store.AIModel
	ListAllModels(context.Context) []store.AIModel
	UpdateModelStatus(context.Context, uuid.UUID, string) error
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

	UpsertEntitlement(context.Context, store.CustomerEntitlement) (store.CustomerEntitlement, error)
	UpsertEntitlementByUser(context.Context, store.CustomerEntitlement) (store.CustomerEntitlement, error)
	GetEntitlementByUser(context.Context, string) (*store.CustomerEntitlement, error)
	// ListActiveEntitlementsByUser returns every active, non-expired
	// entitlement for the user. A user can hold multiple entitlements at
	// once (e.g. unlimited trial + credit_package + payg_topup), and
	// callers that need a specific pricing_group (credit balance for a
	// credit-pinned key, PAYG balance for a PAYG-pinned key) must pick
	// from this list rather than rely on GetEntitlementByUser, which
	// only returns the highest-priority row.
	ListActiveEntitlementsByUser(context.Context, string) ([]store.CustomerEntitlement, error)
	UpsertBalanceSnapshot(context.Context, string, string, *string, *time.Time) (*store.CustomerEntitlement, error)

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

	// PRD v3 Phase 2: request-credit balance (integer/fractional credits
	// per request) and PAYG USD balance (actual-cost debit on per-1M
	// token pricing). Both use the reservation pattern:
	//   - Reserve on request start, subject to the `pricing_group` of
	//     the active entitlement and the user's cached balance.
	//   - Finalize on request completion with the actual amount spent;
	//     over-reservation releases back to the balance.
	// Idempotency belongs to the caller — these helpers assume each
	// call is unique per requestID.
	ReserveRequestCredits(ctx context.Context, userID string, amount float64) error
	FinalizeRequestCredits(ctx context.Context, userID string, reservedAmount, actualAmount float64) error
	ReservePaygUsdBalance(ctx context.Context, userID string, amount float64) error
	FinalizePaygUsdBalance(ctx context.Context, userID string, reservedAmount, actualAmount float64) error

	// ReleaseStaleReservations zeroes out credit_balance_reserved /
	// payg_usd_balance_reserved on rows whose updated_at is older than
	// the supplied threshold. Called by the background sweeper to undo
	// orphan reservations left behind by panics, crashes, or in-flight
	// requests killed by client disconnect before the deferred rollback
	// could run. Returns the number of rows touched. Safe to call with
	// an aggressive interval — only rows that haven't been touched
	// recently are released, so live in-flight reservations are not
	// disturbed.
	ReleaseStaleReservations(ctx context.Context, olderThan time.Duration) (int64, error)

	// Pending callback queue: durable retry for usage-debit callbacks
	// to genfity-app that fail at the network layer. genfity-app's
	// handler is idempotent on (request_id, kind), so a row added
	// here can be safely replayed by the background worker until it
	// succeeds. EnqueuePendingCallback is idempotent on
	// (request_id, billing_mode) — the gateway can call it from any
	// retry path without worrying about duplicating queue entries.
	EnqueuePendingCallback(ctx context.Context, item store.PendingCallback) error
	ListDuePendingCallbacks(ctx context.Context, limit int) ([]store.PendingCallback, error)
	MarkCallbackSucceeded(ctx context.Context, id uuid.UUID) error
	MarkCallbackRetry(ctx context.Context, id uuid.UUID, lastError string, nextAttemptAt time.Time) error
	MarkCallbackAbandoned(ctx context.Context, id uuid.UUID, status string, lastError string) error

	// UpsertModelCreditCost installs/refreshes the per-model credit
	// cost row. Called by the sync worker when genfity-app publishes a
	// new pricing decision.
	UpsertModelCreditCost(ctx context.Context, cost store.ModelCreditCost) (store.ModelCreditCost, error)
	GetModelCreditCost(ctx context.Context, fullModelID string) (*store.ModelCreditCost, error)
	ListModelCreditCosts(ctx context.Context) []store.ModelCreditCost

	// PAYG top-up rate catalog, synced from genfity-app's
	// AiGatewayPaygTopupRate table. Used at checkout to validate the
	// offered package and record the locked rate.
	UpsertPaygTopupRate(ctx context.Context, rate store.PaygTopupRate) (store.PaygTopupRate, error)
	GetPaygTopupRate(ctx context.Context, code string) (*store.PaygTopupRate, error)
	ListPaygTopupRates(ctx context.Context) []store.PaygTopupRate

	AppendUsage(context.Context, store.UsageLedgerEntry) (store.UsageLedgerEntry, error)
	ListUsage(context.Context) []store.UsageLedgerEntry
	ListUsageByUser(context.Context, string) []store.UsageLedgerEntry
	ListUsageByUserSince(context.Context, string, time.Time) []store.UsageLedgerEntry
	ListUsageByTenant(context.Context, string) []store.UsageLedgerEntry
	ListAllUsage(ctx context.Context, limit int) []store.UsageLedgerEntry
	// ListUsageLogs is a paginated, filtered view of usage_ledger for the
	// admin "Logs" modal. Filters are optional; passing zero values
	// disables that predicate.
	ListUsageLogs(ctx context.Context, filter store.UsageLogFilter) ([]store.UsageLedgerEntry, int, error)
	ListUsageSummaryGrouped(ctx context.Context, since time.Time) []store.UsageSummaryRow
	// ListUsageByBillingModeSince rolls up a single user's successful
	// usage by billing_mode for the window [since, now). Used by the
	// admin billing-detail modal for per-mode "today" requests + tokens.
	ListUsageByBillingModeSince(ctx context.Context, userID string, since time.Time) []store.BillingModeUsageRow
	// RollupAndPruneUsage rolls every UTC day older than retentionDays
	// that still has raw usage_ledger rows into usage_daily_rollup
	// (idempotent UPSERT), then deletes those raw rows. Each day runs in
	// its own transaction. When dryRun is true the rollup runs but the
	// raw rows are NOT deleted. Pure analytics maintenance — never touches
	// credit/quota tables.
	RollupAndPruneUsage(ctx context.Context, retentionDays int, dryRun bool) (store.UsageRollupResult, error)
	// ListProviderStats aggregates usage_ledger rows since `since` by
	// router_model prefix. Used by the admin Provider Stats page.
	ListProviderStats(ctx context.Context, since time.Time) []store.ProviderStatsRow
	// ListUsageTimeseries returns usage_ledger aggregated into time
	// buckets (caller-controlled bucket width) for the admin usage
	// charts. `since` is inclusive; passing the zero time disables the
	// time filter. `bucket` accepts "hour" or "day" — anything else
	// falls back to "day".
	ListUsageTimeseries(ctx context.Context, since time.Time, bucket string) []store.UsageTimeseriesPoint
	// ListTopModels returns the highest-cost public_model entries since
	// `since`, ordered by total_cost desc. `limit` caps the result; 0
	// applies a sane default (10).
	ListTopModels(ctx context.Context, since time.Time, limit int) []store.TopModelRow
	// ListBillingModeBreakdown groups usage_ledger by the billing_mode
	// column. NULL is reported as "subscription_unmetered" so the admin
	// chart can label rows that bypassed the priority-billing chain.
	ListBillingModeBreakdown(ctx context.Context, since time.Time) []store.BillingModeBreakdownRow
	// ListStatusBreakdown reports request counts by status (success vs
	// error) along with the top error_code values for the same window.
	ListStatusBreakdown(ctx context.Context, since time.Time) []store.StatusBreakdownRow
	// ListErrorCodeBreakdown reports the top error_code values so the
	// admin can spot misbehaving providers without scrolling the logs.
	ListErrorCodeBreakdown(ctx context.Context, since time.Time, limit int) []store.StatusBreakdownRow
	// LatencyStats computes aggregate latency_ms statistics over the
	// requested window. Skips rows where latency_ms is NULL.
	LatencyStats(ctx context.Context, since time.Time) store.LatencyStats
	// ListPrefixHourlyStats buckets one prefix's usage_ledger rows into
	// hourly windows. Used for the OAuth quota dashboard chart and the
	// provider-stats drill-down. `prefix == ""` matches NULL/empty
	// router_model rows (the "unknown" bucket — pre-upstream failures).
	ListPrefixHourlyStats(ctx context.Context, prefix string, since time.Time) []store.PrefixHourlyPoint
	// ListPrefixModelStats returns per-model success/error counts inside
	// a single prefix. Reveals which combo children carry traffic for
	// "genfity" or which model failed for "unknown" rows.
	ListPrefixModelStats(ctx context.Context, prefix string, since time.Time, limit int) []store.PrefixModelRow
	// ListPrefixErrorCodes returns the top error_code values for a given
	// prefix (or NULL/empty for the unknown bucket). Helps admins
	// understand why a prefix has a high error rate.
	ListPrefixErrorCodes(ctx context.Context, prefix string, since time.Time, limit int) []store.StatusBreakdownRow
	ListCreditBalances(ctx context.Context) []store.CreditBalanceRow
	ListUsageByAPIKey(ctx context.Context, apiKeyID uuid.UUID, limit int) []store.UsageLedgerEntry
	SumUsageTokensByUserSince(context.Context, string, time.Time) int64
	IncrementQuotaCounter(context.Context, string, *string, time.Time, time.Time, int64) error
	DebitCreditBalance(context.Context, string, string, float64) error
}

