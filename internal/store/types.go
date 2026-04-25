package store

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type SubscriptionPlanSnapshot struct {
	ID                  uuid.UUID       `json:"id"`
	PlanCode            string          `json:"plan_code"`
	DisplayName         string          `json:"display_name"`
	Status              string          `json:"status"`
	MonthlyPrice        string          `json:"monthly_price"`
	Currency            string          `json:"currency"`
	QuotaTokensMonthly  *int64          `json:"quota_tokens_monthly,omitempty"`
	RateLimitRPM        *int32          `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM        *int32          `json:"rate_limit_tpm,omitempty"`
	ConcurrentLimit     *int32          `json:"concurrent_limit,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
	SyncedFromGenfityAt time.Time       `json:"synced_from_genfity_at"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type APIKey struct {
	ID              uuid.UUID  `json:"id"`
	GenfityUserID   uuid.UUID  `json:"genfity_user_id"`
	GenfityTenantID *uuid.UUID `json:"genfity_tenant_id,omitempty"`
	Name            string     `json:"name"`
	KeyPrefix       string     `json:"key_prefix"`
	KeyHash         string     `json:"-"`
	Status          string     `json:"status"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
}

type AIModel struct {
	ID                uuid.UUID `json:"id"`
	PublicModel       string    `json:"public_model"`
	DisplayName       string    `json:"display_name"`
	Description       string    `json:"description,omitempty"`
	Status            string    `json:"status"`
	ContextWindow     *int32    `json:"context_window,omitempty"`
	SupportsStreaming bool      `json:"supports_streaming"`
	SupportsTools     bool      `json:"supports_tools"`
	SupportsVision    bool      `json:"supports_vision"`
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

type CustomerEntitlement struct {
	ID                   uuid.UUID  `json:"id"`
	GenfityUserID        uuid.UUID  `json:"genfity_user_id"`
	GenfityTenantID      *uuid.UUID `json:"genfity_tenant_id,omitempty"`
	PlanCode             string     `json:"plan_code"`
	Status               string     `json:"status"`
	PeriodStart          *time.Time `json:"period_start,omitempty"`
	PeriodEnd            *time.Time `json:"period_end,omitempty"`
	QuotaTokensMonthly   *int64     `json:"quota_tokens_monthly,omitempty"`
	BalanceSnapshot      *string    `json:"balance_snapshot,omitempty"`
	UpdatedFromGenfityAt time.Time  `json:"updated_from_genfity_at"`
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
	ID                 uuid.UUID       `json:"id"`
	RequestID          string          `json:"request_id"`
	GenfityUserID      uuid.UUID       `json:"genfity_user_id"`
	GenfityTenantID    *uuid.UUID      `json:"genfity_tenant_id,omitempty"`
	APIKeyID           *uuid.UUID      `json:"api_key_id,omitempty"`
	PublicModel        string          `json:"public_model"`
	RouterModel        *string         `json:"router_model,omitempty"`
	RouterInstanceCode *string         `json:"router_instance_code,omitempty"`
	PromptTokens       int64           `json:"prompt_tokens"`
	CompletionTokens   int64           `json:"completion_tokens"`
	TotalTokens        int64           `json:"total_tokens"`
	CachedTokens       int64           `json:"cached_tokens"`
	ReasoningTokens    int64           `json:"reasoning_tokens"`
	InputCost          string          `json:"input_cost"`
	OutputCost         string          `json:"output_cost"`
	TotalCost          string          `json:"total_cost"`
	Status             string          `json:"status"`
	ErrorCode          *string         `json:"error_code,omitempty"`
	LatencyMS          *int32          `json:"latency_ms,omitempty"`
	StartedAt          time.Time       `json:"started_at"`
	FinishedAt         *time.Time      `json:"finished_at,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
}

type AuthUser struct {
	ID        uuid.UUID  `json:"id"`
	Email     string     `json:"email"`
	Role      string     `json:"role"`
	TenantID  *uuid.UUID `json:"tenant_id,omitempty"`
	SessionID string     `json:"session_id,omitempty"`
}
