package store

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type SubscriptionPlanSnapshot struct {
	ID                   uuid.UUID       `json:"id"`
	PlanCode             string          `json:"plan_code"`
	DisplayName          string          `json:"display_name"`
	Status               string          `json:"status"`
	MonthlyPrice         string          `json:"monthly_price"`
	Currency             string          `json:"currency"`
	QuotaTokensMonthly   *int64          `json:"quota_tokens_monthly,omitempty"`
	RateLimitRPM         *int32          `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM         *int32          `json:"rate_limit_tpm,omitempty"`
	ConcurrentLimit      *int32          `json:"concurrent_limit,omitempty"`
	// MaxRequestsPerPeriod caps total requests in one entitlement period
	// (period_start..period_end). NULL/0 = unlimited.
	MaxRequestsPerPeriod *int32          `json:"max_requests_per_period,omitempty"`
	// RateLimitRPD caps requests per calendar day (UTC) per user on this
	// plan. Independent of MaxRequestsPerPeriod — admin may set either or
	// both. NULL/0 = no daily limit.
	RateLimitRPD         *int32          `json:"rate_limit_rpd,omitempty"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
	SyncedFromGenfityAt  time.Time       `json:"synced_from_genfity_at"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

type APIKey struct {
	ID              uuid.UUID  `json:"id"`
	GenfityUserID   string     `json:"genfity_user_id"`
	GenfityTenantID *string    `json:"genfity_tenant_id,omitempty"`
	Name            string     `json:"name"`
	KeyPrefix       string     `json:"key_prefix"`
	KeyHash         string     `json:"-"`
	Status          string     `json:"status"`
	// BillingSource constrains which billing schema the key may consume.
	// Values:
	//   "subscription" (default — unlimited plan only),
	//   "credit"       (credit_package balance only),
	//   "payg"         (payg_topup USD balance only).
	// The legacy "auto" value (3-priority cascade) was removed in 2026-05;
	// no rows still carry it. Empty source is treated as "subscription"
	// for safety, but the DB CHECK constraint should reject it.
	BillingSource   string     `json:"billing_source"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	RegeneratedAt   *time.Time `json:"regenerated_at,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
}

type AIModel struct {
	ID                uuid.UUID `json:"id"`
	PublicModel       string    `json:"public_model"`
	DisplayName       string    `json:"display_name"`
	Description       string    `json:"description,omitempty"`
	Status            string    `json:"status"`
	ModelType         string    `json:"model_type"`
	ContextWindow     *int32    `json:"context_window,omitempty"`
	SupportsStreaming bool      `json:"supports_streaming"`
	SupportsTools     bool      `json:"supports_tools"`
	SupportsVision    bool      `json:"supports_vision"`
	PaygExposed       bool      `json:"payg_exposed"`
	// IsFree marks the model as free-tier and activates the FreeLimit*
	// fields below (per-(user,model) limits). When false, those fields
	// are ignored. The user's billing balance must still be > 0 even
	// when the model is free.
	IsFree            bool      `json:"is_free"`
	FreeLimitRPD      *int32    `json:"free_limit_rpd,omitempty"`
	FreeLimitRPM      *int32    `json:"free_limit_rpm,omitempty"`
	FreeLimitTPD      *int64    `json:"free_limit_tpd,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type AIModelPrice struct {
	ID                  uuid.UUID `json:"id"`
	ModelID             uuid.UUID `json:"model_id"`
	InputPricePer1M     string    `json:"input_price_per_1m"`
	OutputPricePer1M    string    `json:"output_price_per_1m"`
	CachedPricePer1M    *string   `json:"cached_price_per_1m,omitempty"`
	ReasoningPricePer1M *string   `json:"reasoning_price_per_1m,omitempty"`
	Currency            string    `json:"currency"`
	Active              bool      `json:"active"`
	CreatedAt           time.Time `json:"created_at"`
}

type AIModelRoute struct {
	ID                 uuid.UUID       `json:"id"`
	ModelID            uuid.UUID       `json:"model_id"`
	RouterInstanceCode string          `json:"router_instance_code"`
	RouterModel        string          `json:"router_model"`
	Status             string          `json:"status"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
}

// VirtualCombo / VirtualComboEntry were removed in 2026-05 (PRD §3.3) when
// combo routing moved to CLIProxyAPI. If you need to read old combo data for
// a one-shot migration, check out this file at tag pre-combo-removal.

type CustomerEntitlement struct {
	ID                   uuid.UUID       `json:"id"`
	GenfityUserID        string          `json:"genfity_user_id"`
	GenfityTenantID      *string         `json:"genfity_tenant_id,omitempty"`
	PlanCode             string          `json:"plan_code"`
	Status               string          `json:"status"`
	PeriodStart          *time.Time      `json:"period_start,omitempty"`
	PeriodEnd            *time.Time      `json:"period_end,omitempty"`
	QuotaTokensMonthly   *int64          `json:"quota_tokens_monthly,omitempty"`
	BalanceSnapshot      *string         `json:"balance_snapshot,omitempty"`
	BalanceReserved      *string         `json:"balance_reserved,omitempty"`
	// PRD v3 Phase 2: per-user credit + PAYG USD balances. Mirrors
	// genfity-app's User.aiGatewayCreditBalance / aiGatewayPaygUsdBalance
	// so the gateway can enforce the 3-priority chain without a round
	// trip. Updated via the sync worker on every entitlement sync and
	// mutated by reserve/finalize paths during request handling.
	//
	// PricingGroup carries the billing schema type for the active
	// entitlement: "unlimited", "credit_package", or "payg_topup". For
	// users who hold multiple entitlements simultaneously, the gateway
	// queries credit + PAYG balances regardless; PricingGroup is only
	// used to select the "current" unlimited entitlement when one is
	// active.
	CreditBalance         *string         `json:"credit_balance,omitempty"`
	CreditBalanceReserved *string         `json:"credit_balance_reserved,omitempty"`
	CreditExpiresAt       *time.Time      `json:"credit_expires_at,omitempty"`
	PaygUsdBalance        *string         `json:"payg_usd_balance,omitempty"`
	PaygUsdBalanceReserved *string        `json:"payg_usd_balance_reserved,omitempty"`
	PricingGroup          *string         `json:"pricing_group,omitempty"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
	UpdatedFromGenfityAt time.Time       `json:"updated_from_genfity_at"`
}

// ModelCreditCost is the per-model request credit cost for the
// credit_package billing schema (PRD v3 Phase 2). Synced from
// genfity-app's AiGatewayModelCreditCost table. Each model request
// debits CreditsPerReq from the caller's balance; IsFree bypasses the
// balance check entirely.
type ModelCreditCost struct {
	ID            uuid.UUID       `json:"id"`
	FullModelID   string          `json:"full_model_id"`
	CreditsPerReq string          `json:"credits_per_req"` // stored as numeric(10,4)
	IsFree        bool            `json:"is_free"`
	IsActive      bool            `json:"is_active"`
	Notes         *string         `json:"notes,omitempty"`
	SyncedAt      time.Time       `json:"synced_at"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// PaygTopupRate mirrors the PAYG USD→IDR catalog row synced from
// genfity-app's AiGatewayPaygTopupRate table. Used at checkout to
// validate + lock the rate that was applied at purchase time.
type PaygTopupRate struct {
	ID          uuid.UUID       `json:"id"`
	Code        string          `json:"code"`
	DisplayName string          `json:"display_name"`
	UsdAmount   string          `json:"usd_amount"`   // numeric(18,2) as string
	PriceIdr    string          `json:"price_idr"`    // numeric(18,2)
	RateUsdIdr  string          `json:"rate_usd_idr"` // numeric(18,4)
	Status      string          `json:"status"`
	SortOrder   int             `json:"sort_order"`
	ValidFrom   *time.Time      `json:"valid_from,omitempty"`
	ValidUntil  *time.Time      `json:"valid_until,omitempty"`
	IsPromo     bool            `json:"is_promo"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	SyncedAt    time.Time       `json:"synced_at"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type RouterInstance struct {
	ID                uuid.UUID       `json:"id"`
	Code              string          `json:"code"`
	PublicBaseURL     *string         `json:"public_base_url,omitempty"`
	InternalBaseURL   string          `json:"internal_base_url"`
	Status            string          `json:"status"`
	EncryptedAPIKey   *string         `json:"encrypted_api_key,omitempty"`
	HealthStatus      *string         `json:"health_status,omitempty"`
	LastHealthCheckAt *time.Time      `json:"last_health_check_at,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}

type UsageLedgerEntry struct {
	ID                  uuid.UUID       `json:"id"`
	RequestID           string          `json:"request_id"`
	GenfityUserID       string          `json:"genfity_user_id"`
	GenfityTenantID     *string         `json:"genfity_tenant_id,omitempty"`
	APIKeyID            *uuid.UUID      `json:"api_key_id,omitempty"`
	PublicModel         string          `json:"public_model"`
	RouterModel         *string         `json:"router_model,omitempty"`
	RouterInstanceCode  *string         `json:"router_instance_code,omitempty"`
	PromptTokens        int64           `json:"prompt_tokens"`
	CompletionTokens    int64           `json:"completion_tokens"`
	TotalTokens         int64           `json:"total_tokens"`
	CachedTokens        int64           `json:"cached_tokens"`
	ReasoningTokens    int64           `json:"reasoning_tokens"`
	InputCost           string          `json:"input_cost"`
	OutputCost          string          `json:"output_cost"`
	TotalCost           string          `json:"total_cost"`
	BillingMode         *string         `json:"billing_mode,omitempty"`
	AmountCredits       *string         `json:"amount_credits,omitempty"`
	BalanceAfterCredits *string         `json:"balance_after_credits,omitempty"`
	BalanceAfterUsd     *string         `json:"balance_after_usd,omitempty"`
	Status              string          `json:"status"`
	ErrorCode           *string         `json:"error_code,omitempty"`
	LatencyMS           *int32          `json:"latency_ms,omitempty"`
	StartedAt           time.Time       `json:"started_at"`
	FinishedAt          *time.Time      `json:"finished_at,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
}

type UsageSummaryRow struct {
	PricingGroup  string    `json:"pricing_group"`
	GenfityUserID string    `json:"genfity_user_id"`
	RequestCount  int       `json:"request_count"`
	InputTokens   int64     `json:"input_tokens"`
	OutputTokens  int64     `json:"output_tokens"`
	TotalTokens   int64     `json:"total_tokens"`
	TotalCost     string    `json:"total_cost"`
	LastActive    time.Time `json:"last_active"`
}

// ProviderStatsRow aggregates usage_ledger by router_model prefix
// (segment before the first "/"). Used by admin Provider Stats page.
type ProviderStatsRow struct {
	Prefix       string `json:"prefix"`
	TotalCount   int64  `json:"total_count"`
	SuccessCount int64  `json:"success_count"`
	ErrorCount   int64  `json:"error_count"`
}

// UsageTimeseriesPoint is a single time bucket of aggregated usage.
// Bucket size is decided by the handler based on the requested range
// (hourly for 1d, daily for longer windows). Cost is rendered as a
// string so JSON consumers don't lose precision on the numeric(18,6).
type UsageTimeseriesPoint struct {
	Bucket       time.Time `json:"bucket"`
	RequestCount int64     `json:"request_count"`
	SuccessCount int64     `json:"success_count"`
	ErrorCount   int64     `json:"error_count"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	TotalTokens  int64     `json:"total_tokens"`
	TotalCost    string    `json:"total_cost"`
}

// TopModelRow is one row of the "Top Models" leaderboard the admin
// usage page renders. Surfaces both volume (requests, tokens) and
// economics (total cost) so admins can spot the most expensive models
// even when their request count is small (e.g. claude-opus calls).
type TopModelRow struct {
	PublicModel  string `json:"public_model"`
	RequestCount int64  `json:"request_count"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
	TotalCost    string `json:"total_cost"`
	SuccessCount int64  `json:"success_count"`
	ErrorCount   int64  `json:"error_count"`
}

// BillingModeBreakdownRow groups usage_ledger by the billing_mode
// column (subscription / credit_package / payg_topup / null). Used by
// the admin "Billing Mode" pie/donut chart so admins can see what
// share of traffic each pricing scheme handles.
type BillingModeBreakdownRow struct {
	BillingMode  string `json:"billing_mode"`
	RequestCount int64  `json:"request_count"`
	TotalTokens  int64  `json:"total_tokens"`
	TotalCost    string `json:"total_cost"`
}

// BillingModeUsageRow is a per-(user, billing_mode) usage rollup for a
// single time window (e.g. "today"). Split input/output tokens so the
// admin billing-detail modal can show, for a credit/PAYG user, how many
// requests + how many tokens in each direction they consumed today.
type BillingModeUsageRow struct {
	BillingMode  string `json:"billing_mode"`
	RequestCount int64  `json:"request_count"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
	TotalCost    string `json:"total_cost"`
	CreditsUsed  string `json:"credits_used"`
}

// StatusBreakdownRow surfaces success/error counts plus an explicit
// error_code histogram so admins can scan for misbehaving providers
// or auth/quota issues without paging through the logs modal.
type StatusBreakdownRow struct {
	Bucket       string `json:"bucket"`
	RequestCount int64  `json:"request_count"`
}

// LatencyStats reports aggregate latency over the requested window.
// p50/p95/p99 are computed via Postgres percentile_cont so admins can
// flag regressions in router performance.
type LatencyStats struct {
	SampleSize int64   `json:"sample_size"`
	AvgMS      float64 `json:"avg_ms"`
	P50MS      float64 `json:"p50_ms"`
	P95MS      float64 `json:"p95_ms"`
	P99MS      float64 `json:"p99_ms"`
	MaxMS      float64 `json:"max_ms"`
}

// PrefixHourlyPoint is one hourly bucket for a given router_model prefix.
// Used to render the "today by hour" stacked bar on the OAuth quota
// dashboard and the per-prefix drill-down on provider-stats.
type PrefixHourlyPoint struct {
	Bucket       time.Time `json:"bucket"`
	SuccessCount int64     `json:"success_count"`
	ErrorCount   int64     `json:"error_count"`
}

// PrefixModelRow shows a model breakdown within a single prefix
// (e.g. for the "genfity" prefix this tells the admin which combo
// children received traffic — claude-opus, gemini, etc.).
type PrefixModelRow struct {
	RouterModel  string `json:"router_model"`
	PublicModel  string `json:"public_model"`
	TotalCount   int64  `json:"total_count"`
	SuccessCount int64  `json:"success_count"`
	ErrorCount   int64  `json:"error_count"`
}

// UsageLogFilter scopes the admin "Logs" modal query.
//
// Limit/Offset drive offset-based pagination so the modal can deep-link
// into a page (1000-row admin scroll). UserID/APIKeyID/Status/BillingMode/
// PublicModel narrow the result set; empty values disable that predicate.
type UsageLogFilter struct {
	UserID      string
	APIKeyID    *uuid.UUID
	Status      string
	BillingMode string
	PublicModel string
	Search      string // matches user_id / public_model / request_id (ILIKE)
	From        time.Time
	To          time.Time
	Limit       int
	Offset      int
}

type CreditBalanceRow struct {
	GenfityUserID string  `json:"genfity_user_id"`
	CreditBalance string  `json:"credit_balance"`
	CreditUsed    string  `json:"credit_used"`
}

type AuthUser struct {
	ID        string  `json:"id"`
	Email     string  `json:"email"`
	Role      string  `json:"role"`
	TenantID  *string `json:"tenant_id,omitempty"`
	SessionID string  `json:"session_id,omitempty"`
}

// PendingCallback is the durable retry queue row for a usage-debit
// callback to genfity-app that the in-process retry path could not
// deliver. Mirror of ai_gateway.pending_callbacks (migration 00019).
//
// Idempotency: enqueue is keyed on (RequestID, BillingMode) — the
// queue tolerates the same callback being submitted from multiple
// retry sites (e.g., the in-process attempt and a future re-attempt
// from the same finalizer) without creating duplicates.
type PendingCallback struct {
	ID             uuid.UUID `json:"id"`
	RequestID      string    `json:"request_id"`
	UserID         string    `json:"user_id"`
	BillingMode    string    `json:"billing_mode"`
	AmountCredits  *string   `json:"amount_credits,omitempty"`
	AmountUSD      *string   `json:"amount_usd,omitempty"`
	Model          *string   `json:"model,omitempty"`
	Notes          *string   `json:"notes,omitempty"`
	Attempts       int       `json:"attempts"`
	LastError      *string   `json:"last_error,omitempty"`
	LastAttemptAt  *time.Time `json:"last_attempt_at,omitempty"`
	NextAttemptAt  time.Time `json:"next_attempt_at"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
}
