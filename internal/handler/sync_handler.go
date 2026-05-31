package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"genfity-ai-gateway-service/internal/service"
	"genfity-ai-gateway-service/internal/store"
)

type SyncHandler struct {
	sync               *service.SyncService
	callback           *service.GenfityCallback
	cliproxyInternalURL string
	cliproxyAPIKey      string
}

type balanceSyncPayload struct {
	UserID          string  `json:"user_id"`
	Balance         string  `json:"balance"`
	PaygBalance     *string `json:"payg_balance,omitempty"`
	CreditExpiresAt *string `json:"credit_expires_at,omitempty"`
}

func NewSyncHandler(sync *service.SyncService, callback *service.GenfityCallback, cliproxyInternalURL, cliproxyAPIKey string) *SyncHandler {
	return &SyncHandler{sync: sync, callback: callback, cliproxyInternalURL: cliproxyInternalURL, cliproxyAPIKey: cliproxyAPIKey}
}

func (h *SyncHandler) SyncSubscriptionPlans(w http.ResponseWriter, r *http.Request) {
	var payload []store.SubscriptionPlanSnapshot
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	count, err := h.sync.SyncSubscriptionPlans(r.Context(), payload)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"synced": count})
}

func (h *SyncHandler) SyncCustomerEntitlements(w http.ResponseWriter, r *http.Request) {
	var payload []store.CustomerEntitlement
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	count, failures, err := h.sync.SyncCustomerEntitlements(r.Context(), payload)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Per-row resilience: report how many succeeded and which rows failed,
	// instead of aborting the whole batch on the first error. The app
	// treats synced==len(payload) as full success and uses `failures` to
	// mark ONLY the offending rows, so one bad row no longer tars the
	// other 499 in a sync.
	respondJSON(w, http.StatusOK, map[string]any{"synced": count, "failures": failures})
}

func (h *SyncHandler) SyncCustomerBalance(w http.ResponseWriter, r *http.Request) {
	var payload balanceSyncPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if payload.UserID == "" {
		respondError(w, http.StatusBadRequest, "invalid_user_id")
		return
	}
	var expiresAt *time.Time
	if payload.CreditExpiresAt != nil && *payload.CreditExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, *payload.CreditExpiresAt)
		if err != nil {
			respondError(w, http.StatusBadRequest, "invalid_credit_expires_at")
			return
		}
		expiresAt = &parsed
	}

	if err := h.sync.SyncCustomerBalance(r.Context(), payload.UserID, payload.Balance, payload.PaygBalance, expiresAt); err != nil {
		respondError(w, http.StatusNotFound, "subscription_not_found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *SyncHandler) ReplayUsageDebits(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var since time.Time
	if raw := query.Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondError(w, http.StatusBadRequest, "invalid_since")
			return
		}
		since = parsed
	}
	limit := 500
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			respondError(w, http.StatusBadRequest, "invalid_limit")
			return
		}
		limit = parsed
	}

	result, err := h.sync.ReplayUsageDebits(r.Context(), h.callback, service.ReplayUsageDebitsInput{
		UserID: query.Get("user_id"),
		Since:  since,
		Limit:  limit,
		DryRun: query.Get("dry_run") == "true",
	})
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, result)
}

// RollupUsage rolls usage_ledger days older than the retention window
// into usage_daily_rollup and prunes the raw rows. Defaults: retention
// 7 days, deletes after rollup. Query params:
//   - retention_days: int (default 7)
//   - dry_run: "true" rolls up but keeps raw rows (verification)
// Pure analytics maintenance — never touches credit/quota state.
func (h *SyncHandler) RollupUsage(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	retentionDays := 7
	if raw := query.Get("retention_days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			respondError(w, http.StatusBadRequest, "invalid_retention_days")
			return
		}
		retentionDays = parsed
	}
	dryRun := query.Get("dry_run") == "true"

	result, err := h.sync.RollupAndPruneUsage(r.Context(), retentionDays, dryRun)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *SyncHandler) SyncModelCreditCosts(w http.ResponseWriter, r *http.Request) {
	var payload []store.ModelCreditCost
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	count, err := h.sync.SyncModelCreditCosts(r.Context(), payload)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"synced": count})
}

func (h *SyncHandler) SyncModels(w http.ResponseWriter, r *http.Request) {
	var payload []service.ModelSyncItem
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	count, err := h.sync.SyncModels(r.Context(), payload)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"synced": count})
}

func (h *SyncHandler) SyncPaygTopupRates(w http.ResponseWriter, r *http.Request) {
	var payload []store.PaygTopupRate
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	count, err := h.sync.SyncPaygTopupRates(r.Context(), payload)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"synced": count})
}

func (h *SyncHandler) ExportPlans(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"plans": h.sync.ExportPlans(r.Context())})
}

func (h *SyncHandler) ExportModels(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"models": h.sync.ExportModels(r.Context())})
}

func (h *SyncHandler) ExportModelPrices(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"prices": h.sync.ExportModelPrices(r.Context())})
}

func (h *SyncHandler) ExportUsageSummary(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		respondError(w, http.StatusBadRequest, "invalid_user_id")
		return
	}
	respondJSON(w, http.StatusOK, h.sync.ExportUsageSummary(r.Context(), userID))
}

func (h *SyncHandler) ExportCliproxyModels(w http.ResponseWriter, r *http.Request) {
	if h.cliproxyInternalURL == "" {
		respondJSON(w, http.StatusOK, map[string]any{"models": []any{}})
		return
	}
	url := fmt.Sprintf("%s/v1/models", h.cliproxyInternalURL)
	req, err := http.NewRequestWithContext(r.Context(), "GET", url, nil)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "cliproxy_request_failed")
		return
	}
	if h.cliproxyAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.cliproxyAPIKey)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		respondError(w, http.StatusBadGateway, "cliproxy_unreachable")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		respondError(w, http.StatusBadGateway, "cliproxy_error")
		return
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		respondError(w, http.StatusBadGateway, "cliproxy_invalid_response")
		return
	}
	models := make([]map[string]string, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, map[string]string{"id": m.ID})
	}
	respondJSON(w, http.StatusOK, map[string]any{"models": models})
}
