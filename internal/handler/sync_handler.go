package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"genfity-ai-gateway-service/internal/service"
	"genfity-ai-gateway-service/internal/store"
)

type SyncHandler struct {
	sync *service.SyncService
}

type balanceSyncPayload struct {
	UserID  uuid.UUID `json:"user_id"`
	Balance string    `json:"balance"`
}

func NewSyncHandler(sync *service.SyncService) *SyncHandler {
	return &SyncHandler{sync: sync}
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
	count, err := h.sync.SyncCustomerEntitlements(r.Context(), payload)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"synced": count})
}

func (h *SyncHandler) SyncCustomerBalance(w http.ResponseWriter, r *http.Request) {
	var payload balanceSyncPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if payload.UserID == uuid.Nil {
		respondError(w, http.StatusBadRequest, "invalid_user_id")
		return
	}
	if err := h.sync.SyncCustomerBalance(r.Context(), payload.UserID, payload.Balance); err != nil {
		respondError(w, http.StatusNotFound, "subscription_not_found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
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
	userID, err := uuid.Parse(r.URL.Query().Get("user_id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_user_id")
		return
	}
	respondJSON(w, http.StatusOK, h.sync.ExportUsageSummary(r.Context(), userID))
}
