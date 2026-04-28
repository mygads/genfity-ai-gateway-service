package handler

import (
	"encoding/json"
	"net/http"

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
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}

	created, err := h.apiKeys.Create(r.Context(), service.CreateAPIKeyInput{
		UserID:   user.ID,
		TenantID: user.TenantID,
		Name:     payload.Name,
		Status:   payload.Status,
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
	entitlement, err := h.entitlements.GetByUser(r.Context(), user.ID)
	if err != nil {
		respondError(w, http.StatusNotFound, "subscription_not_found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"subscription": entitlement})
}

func (h *CustomerHandler) UpdateAPIKeyStatus(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_api_key_id")
		return
	}
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if err := h.apiKeys.UpdateStatus(r.Context(), id, payload.Status); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
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
