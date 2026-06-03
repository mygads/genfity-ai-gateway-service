package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"genfity-ai-gateway-service/internal/http/middleware"
	"genfity-ai-gateway-service/internal/service"
	"genfity-ai-gateway-service/internal/store"
)

type CustomerHandler struct {
	apiKeys      *service.APIKeyService
	models       *service.ModelService
	usage        *service.UsageService
	entitlements *service.EntitlementService
}

func NewCustomerHandler(apiKeys *service.APIKeyService, models *service.ModelService, usage *service.UsageService, entitlements *service.EntitlementService) *CustomerHandler {
	return &CustomerHandler{apiKeys: apiKeys, models: models, usage: usage, entitlements: entitlements}
}

func activeModels(models []store.AIModel) []store.AIModel {
	items := make([]store.AIModel, 0, len(models))
	for _, model := range models {
		if model.Status == "active" {
			items = append(items, model)
		}
	}
	return items
}

func subscriptionCreditUsage(entries []store.UsageLedgerEntry, subscription *service.ActiveSubscription) (float64, float64) {
	if subscription == nil || subscription.Entitlement == nil {
		return 0, 0
	}
	now := time.Now().UTC()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	periodStart, periodEnd := activePeriod(subscription.Entitlement)
	var todayUsed float64
	var periodUsed float64
	for _, row := range entries {
		if row.Status != "success" || row.BillingMode == nil || *row.BillingMode != "unlimited" || row.AmountCredits == nil {
			continue
		}
		credits := parseFloatPtr(row.AmountCredits)
		if credits <= 0 {
			continue
		}
		if !row.StartedAt.Before(startOfDay) {
			todayUsed += credits
		}
		if (row.StartedAt.Equal(periodStart) || row.StartedAt.After(periodStart)) && row.StartedAt.Before(periodEnd) {
			periodUsed += credits
		}
	}
	return todayUsed, periodUsed
}

func (h *CustomerHandler) Overview(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetAuthUser(r.Context())
	respondJSON(w, http.StatusOK, map[string]any{
		"user_id":       user.ID,
		"role":          user.Role,
		"models":        activeModels(h.models.ListModels(r.Context())),
		"usage_summary": h.usage.SummaryByUser(r.Context(), user.ID),
	})
}

func (h *CustomerHandler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetAuthUser(r.Context())
	respondJSON(w, http.StatusOK, map[string]any{
		"api_keys": h.apiKeys.ListByUser(r.Context(), user.ID),
	})
}

func (h *CustomerHandler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetAuthUser(r.Context())

	var payload struct {
		Name          string `json:"name"`
		Status        string `json:"status"`
		BillingSource string `json:"billing_source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}

	created, err := h.apiKeys.Create(r.Context(), service.CreateAPIKeyInput{
		UserID:        user.ID,
		TenantID:      user.TenantID,
		Name:          payload.Name,
		Status:        payload.Status,
		BillingSource: payload.BillingSource,
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed_to_create_api_key")
		return
	}

	respondJSON(w, http.StatusCreated, map[string]any{
		"api_key": created.Record,
		"raw_key": created.RawKey,
	})
}

func (h *CustomerHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{
		"models": activeModels(h.models.ListModels(r.Context())),
	})
}

func (h *CustomerHandler) Usage(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetAuthUser(r.Context())
	respondJSON(w, http.StatusOK, map[string]any{
		"summary": h.usage.SummaryByUser30d(r.Context(), user.ID),
		"entries": h.usage.ListByUser(r.Context(), user.ID),
	})
}

func (h *CustomerHandler) UsageSummary(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetAuthUser(r.Context())
	respondJSON(w, http.StatusOK, h.usage.SummaryByUser30d(r.Context(), user.ID))
}

func (h *CustomerHandler) Quota(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetAuthUser(r.Context())
	if subscription, err := h.entitlements.CheckActiveSubscription(r.Context(), user.ID); err == nil && subscription != nil && subscription.Entitlement != nil {
		creditUsedToday, creditUsedPeriod := subscriptionCreditUsage(h.usage.ListByUser(r.Context(), user.ID), subscription)
		resp := map[string]any{
			"balance_snapshot":   subscription.Entitlement.BalanceSnapshot,
			"period_start":       subscription.Entitlement.PeriodStart,
			"period_end":         subscription.Entitlement.PeriodEnd,
			"credit_used_today":  creditUsedToday,
			"credit_used_period": creditUsedPeriod,
		}
		if quotaTokensMonthly := quotaLimitPtr(subscription); quotaTokensMonthly != nil {
			resp["quota_tokens_monthly"] = *quotaTokensMonthly
		}
		if subscription.Plan != nil {
			resp["credit_limit_per_day"] = subscription.Plan.CreditLimitPerDay
			resp["credit_limit_per_period"] = subscription.Plan.CreditLimitPerPeriod
		}
		respondJSON(w, http.StatusOK, resp)
		return
	}
	entitlement, err := h.entitlements.GetByUser(r.Context(), user.ID)
	if err != nil {
		respondError(w, http.StatusNotFound, "subscription_not_found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"quota_tokens_monthly": entitlement.QuotaTokensMonthly,
		"balance_snapshot":     entitlement.BalanceSnapshot,
		"period_start":         entitlement.PeriodStart,
		"period_end":           entitlement.PeriodEnd,
	})
}

func (h *CustomerHandler) Subscription(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetAuthUser(r.Context())
	subscription, err := h.entitlements.CheckActiveSubscription(r.Context(), user.ID)
	if err != nil {
		// Fallback: maybe entitlement exists but no plan snapshot.
		// Don't fail outright — return entitlement-only response so the
		// dashboard can still render saldo/expiry cards.
		entitlement, gerr := h.entitlements.GetByUser(r.Context(), user.ID)
		if gerr != nil {
			respondError(w, http.StatusNotFound, "subscription_not_found")
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"subscription": entitlement,
			"plan":         nil,
		})
		return
	}
	// Flatten plan limits onto the response so the customer dashboard
	// can display RPD/RPM/concurrent/period/quota caps without making a
	// second admin call. The frontend reads these fields directly.
	resp := map[string]any{
		"subscription": subscription.Entitlement,
		"plan":         subscription.Plan,
	}
	creditUsedToday, creditUsedPeriod := subscriptionCreditUsage(h.usage.ListByUser(r.Context(), user.ID), subscription)
	if quotaTokensMonthly := quotaLimitPtr(subscription); quotaTokensMonthly != nil {
		resp["quota_tokens_monthly"] = *quotaTokensMonthly
	}
	if subscription.Plan != nil {
		resp["plan_code"] = subscription.Entitlement.PlanCode
		resp["plan_name"] = subscription.Plan.DisplayName
		resp["status"] = subscription.Entitlement.Status
		resp["period_start"] = subscription.Entitlement.PeriodStart
		resp["period_end"] = subscription.Entitlement.PeriodEnd
		resp["rate_limit_rpm"] = subscription.Plan.RateLimitRPM
		resp["rate_limit_tpm"] = subscription.Plan.RateLimitTPM
		resp["rate_limit_rpd"] = subscription.Plan.RateLimitRPD
		resp["credit_limit_per_day"] = subscription.Plan.CreditLimitPerDay
		resp["credit_limit_per_period"] = subscription.Plan.CreditLimitPerPeriod
		resp["credit_used_today"] = creditUsedToday
		resp["credit_used_period"] = creditUsedPeriod
		resp["concurrent_limit"] = subscription.Plan.ConcurrentLimit
		resp["max_requests_per_period"] = subscription.Plan.MaxRequestsPerPeriod
		if subscription.Entitlement.PricingGroup != nil {
			resp["pricing_group"] = *subscription.Entitlement.PricingGroup
		}
	}
	respondJSON(w, http.StatusOK, resp)
}

func (h *CustomerHandler) UpdateAPIKeyStatus(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_api_key_id")
		return
	}
	var payload struct {
		Status        *string `json:"status"`
		BillingSource *string `json:"billing_source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if payload.Status != nil {
		if err := h.apiKeys.UpdateStatus(r.Context(), id, *payload.Status); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if payload.BillingSource != nil {
		if err := h.apiKeys.UpdateBillingSource(r.Context(), id, *payload.BillingSource); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *CustomerHandler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_api_key_id")
		return
	}
	if err := h.apiKeys.Revoke(r.Context(), id); err != nil {
		respondError(w, http.StatusNotFound, "api_key_not_found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

// RegenerateAPIKey rotates the secret of an existing key. Same id,
// same name/billing_source/status — but a new raw key is returned
// once. Caller can show the new raw key to the user; the old raw
// key stops working immediately.
func (h *CustomerHandler) RegenerateAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_api_key_id")
		return
	}
	user := middleware.GetAuthUser(r.Context())
	created, err := h.apiKeys.Regenerate(r.Context(), id, user.ID)
	if err != nil {
		respondError(w, http.StatusNotFound, "api_key_not_found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"api_key": created.Record,
		"raw_key": created.RawKey,
	})
}

func (h *CustomerHandler) APIKeyLogs(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_api_key_id")
		return
	}
	user := middleware.GetAuthUser(r.Context())
	keys := h.apiKeys.ListByUser(r.Context(), user.ID)
	owned := false
	for _, k := range keys {
		if k.ID == id {
			owned = true
			break
		}
	}
	if !owned {
		respondError(w, http.StatusNotFound, "api_key_not_found")
		return
	}
	logs := h.usage.ListByAPIKey(r.Context(), id, 100)
	respondJSON(w, http.StatusOK, map[string]any{"logs": logs})
}
