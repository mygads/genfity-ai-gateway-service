package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
	rateLimit    *service.RateLimitService
}

func NewCustomerHandler(apiKeys *service.APIKeyService, models *service.ModelService, usage *service.UsageService, entitlements *service.EntitlementService, rateLimit *service.RateLimitService) *CustomerHandler {
	return &CustomerHandler{apiKeys: apiKeys, models: models, usage: usage, entitlements: entitlements, rateLimit: rateLimit}
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

type subscriptionUsageSnapshot struct {
	RPDUsed          int
	RPPUsed          int
	RPMUsed          int
	ConcurrentUsed   int
	PeriodTokensUsed int64
	CreditUsedToday  float64
	CreditUsedPeriod float64
	DebtFlagged      bool
	DebtRemaining    int
}

func isFreeUsageLedgerEntry(row store.UsageLedgerEntry) bool {
	if strings.HasSuffix(row.PublicModel, ":free") {
		return true
	}
	return row.RouterModel != nil && strings.HasSuffix(*row.RouterModel, ":free")
}

func collectSubscriptionUsageSnapshot(entries []store.UsageLedgerEntry, subscription *service.ActiveSubscription, rateLimit *service.RateLimitService, usage *service.UsageService, userID string) subscriptionUsageSnapshot {
	if subscription == nil || subscription.Entitlement == nil {
		return subscriptionUsageSnapshot{}
	}

	now := time.Now().UTC()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	periodStart, periodEnd := activePeriod(subscription.Entitlement)

	snapshot := subscriptionUsageSnapshot{}
	ledgerRPDUsed := 0
	ledgerRPPUsed := 0
	ledgerCreditUsedToday := 0.0
	ledgerCreditUsedPeriod := 0.0
	redisAvailable := false
	if rateLimit != nil && userID != "" {
		pk := periodKey(subscription.Entitlement)
		snapshot.RPDUsed = rateLimit.GetPlanRPDCount(context.Background(), userID, pk)
		snapshot.RPPUsed = rateLimit.GetRequestsPerPeriodCount(context.Background(), userID, pk)
		snapshot.RPMUsed = rateLimit.GetRPMCount(context.Background(), userID)
		snapshot.ConcurrentUsed = rateLimit.GetConcurrencyCount(context.Background(), userID)
		snapshot.CreditUsedToday = rateLimit.GetPlanCreditRPDCount(context.Background(), userID, pk)
		snapshot.CreditUsedPeriod = rateLimit.GetPlanCreditsPerPeriodCount(context.Background(), userID, pk)
		redisAvailable = true
	}

	for _, row := range entries {
		if row.Status != "success" || row.BillingMode == nil || *row.BillingMode != "unlimited" || isFreeUsageLedgerEntry(row) {
			continue
		}
		if !row.StartedAt.Before(startOfDay) {
			ledgerRPDUsed++
			if row.AmountCredits != nil {
				ledgerCreditUsedToday += parseFloatPtr(row.AmountCredits)
			}
		}
		if row.StartedAt.Before(periodStart) || !row.StartedAt.Before(periodEnd) {
			continue
		}
		snapshot.PeriodTokensUsed += row.TotalTokens
		ledgerRPPUsed++
		if row.AmountCredits != nil {
			ledgerCreditUsedPeriod += parseFloatPtr(row.AmountCredits)
		}
	}
	// Durable quota_counters is the authoritative per-period tally for
	// token-quota (unlimited-limit) plans: unlike usage_ledger it is never
	// retention-pruned, so on a long (e.g. 30-day) plan it still reflects
	// requests older than the ledger's ~7-day raw window. Prefer it for
	// period_tokens_used and the unlimited-plan RPP fallback below; fall
	// back to the ledger-derived counts when the row hasn't been created.
	var quotaCounter *store.QuotaCounter
	if usage != nil && userID != "" {
		quotaCounter, _ = usage.GetQuotaCounter(context.Background(), userID, periodStart, periodEnd)
	}
	if quotaCounter != nil {
		snapshot.PeriodTokensUsed = quotaCounter.TokensUsed
	}

	// Redis is the source of truth — it's exactly what the request path
	// enforces against (CheckPlanRPD / CheckRequestsPerPeriod). The ledger is
	// only a fallback for when Redis is unavailable. We must NOT max() the two:
	// the ledger counts ALL unlimited requests today across every plan/period,
	// so after a plan switch (new plan's counter resets to a fresh low value)
	// or an admin "Atur" reset (Redis lowered), the stale higher ledger count
	// would mask the real enforced value and the change would look unsaved.
	if !redisAvailable {
		snapshot.RPDUsed = ledgerRPDUsed
		snapshot.RPPUsed = ledgerRPPUsed
		snapshot.CreditUsedToday = ledgerCreditUsedToday
		snapshot.CreditUsedPeriod = ledgerCreditUsedPeriod
	} else {
		// When a limit is unlimited (≤0), the request path never increments
		// its Redis enforcement counter (CheckPlanRPD/CheckRequestsPerPeriod
		// both no-op on limit<=0), so the Redis read is a permanent 0 even
		// though the user is making requests (e.g. a token-quota plan with
		// RPD=∞/RPP=∞). Show the actual usage in that case so the dashboard
		// reflects reality. The "Redis is truth" concern only applies to
		// enforced (capped) limits, where the reset/plan-switch masking
		// described above matters.
		limits := service.PlanLimitsFromSnapshot(subscription.Plan)
		if !limits.HasRPD() {
			snapshot.RPDUsed = ledgerRPDUsed
		}
		if !limits.HasMaxRequestsPerPeriod() {
			// Prefer the durable request_count (survives ledger pruning on
			// long plans); fall back to the ledger tally when absent.
			if quotaCounter != nil {
				snapshot.RPPUsed = int(quotaCounter.RequestCount)
			} else {
				snapshot.RPPUsed = ledgerRPPUsed
			}
		}
		if !limits.HasCreditPerDay() {
			snapshot.CreditUsedToday = ledgerCreditUsedToday
		}
		if !limits.HasCreditPerPeriod() {
			snapshot.CreditUsedPeriod = ledgerCreditUsedPeriod
		}
	}

	flagged, debtRemaining, _ := resolveAbuseDebt(subscription)
	snapshot.DebtFlagged = flagged
	snapshot.DebtRemaining = debtRemaining

	return snapshot
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
		usageSnapshot := collectSubscriptionUsageSnapshot(h.usage.ListByUser(r.Context(), user.ID), subscription, h.rateLimit, h.usage, user.ID)
		resp := map[string]any{
			"balance_snapshot":   subscription.Entitlement.BalanceSnapshot,
			"period_start":       subscription.Entitlement.PeriodStart,
			"period_end":         subscription.Entitlement.PeriodEnd,
			"credit_used_today":  usageSnapshot.CreditUsedToday,
			"credit_used_period": usageSnapshot.CreditUsedPeriod,
			"rpd_used":           usageSnapshot.RPDUsed,
			"rpp_used":           usageSnapshot.RPPUsed,
			"rpm_used":           usageSnapshot.RPMUsed,
			"concurrent_used":    usageSnapshot.ConcurrentUsed,
			"period_tokens_used": usageSnapshot.PeriodTokensUsed,
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
	usageSnapshot := collectSubscriptionUsageSnapshot(h.usage.ListByUser(r.Context(), user.ID), subscription, h.rateLimit, h.usage, user.ID)
	resp := map[string]any{
		"subscription":       subscription.Entitlement,
		"plan":               subscription.Plan,
		"rpd_used":           usageSnapshot.RPDUsed,
		"rpp_used":           usageSnapshot.RPPUsed,
		"rpm_used":           usageSnapshot.RPMUsed,
		"concurrent_used":    usageSnapshot.ConcurrentUsed,
		"period_tokens_used": usageSnapshot.PeriodTokensUsed,
		"credit_used_today":  usageSnapshot.CreditUsedToday,
		"credit_used_period": usageSnapshot.CreditUsedPeriod,
		"debt_flagged":       usageSnapshot.DebtFlagged,
		"debt_remaining":     usageSnapshot.DebtRemaining,
	}
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
		resp["concurrent_limit"] = subscription.Plan.ConcurrentLimit
		// Report the EFFECTIVE RPP cap (base × stacked-window multiplier),
		// so the dashboard shows the quota the user actually has after
		// stacking/extending the plan, matching enforcement in
		// applyPreRequestLimits. base_max_requests_per_period exposes the
		// unscaled plan value for reference.
		if subscription.Plan.MaxRequestsPerPeriod != nil {
			base := int(*subscription.Plan.MaxRequestsPerPeriod)
			resp["base_max_requests_per_period"] = base
			resp["max_requests_per_period"] = base * periodRPPMultiplier(subscription)
		} else {
			resp["max_requests_per_period"] = subscription.Plan.MaxRequestsPerPeriod
		}
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
