package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"genfity-ai-gateway-service/internal/service"
	"genfity-ai-gateway-service/internal/store"
)

type AdminHandler struct {
	models  *service.ModelService
	routers *service.RouterService
	usage   *service.UsageService
}

// patchHasField returns true if the PATCH body contained a key (even if null).
func patchHasField(fields map[string]json.RawMessage, key string) bool {
	_, ok := fields[key]
	return ok
}

// patchIsNull returns true if the PATCH body contained the key with explicit JSON null.
func patchIsNull(fields map[string]json.RawMessage, key string) bool {
	raw, ok := fields[key]
	if !ok {
		return false
	}
	return string(raw) == "null"
}

// patchDecodeOptionalString decodes a *string from the field; ok=false if absent or invalid.
func patchDecodeOptionalString(fields map[string]json.RawMessage, key string) (*string, bool) {
	raw, ok := fields[key]
	if !ok {
		return nil, false
	}
	if string(raw) == "null" {
		return nil, true
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	return &v, true
}

func NewAdminHandler(models *service.ModelService, routers *service.RouterService, usage *service.UsageService) *AdminHandler {
	return &AdminHandler{models: models, routers: routers, usage: usage}
}

func respondAdminWriteError(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503", "23505":
			respondError(w, http.StatusConflict, pgErr.ConstraintName)
		case "23502", "23514", "22P02":
			respondError(w, http.StatusBadRequest, pgErr.ConstraintName)
		default:
			respondError(w, http.StatusInternalServerError, "database_error")
		}
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		respondError(w, http.StatusNotFound, "not_found")
		return
	}
	respondError(w, http.StatusBadRequest, err.Error())
}

func (h *AdminHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"models": h.models.ListModels(r.Context())})
}

func (h *AdminHandler) CreateModel(w http.ResponseWriter, r *http.Request) {
	var payload store.AIModel
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	model, err := h.models.CreateModel(r.Context(), payload)
	if err != nil {
		respondAdminWriteError(w, err)
		return
	}
	respondJSON(w, http.StatusCreated, model)
}

func (h *AdminHandler) UpdateModel(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_model_id")
		return
	}
	current, err := h.models.GetModel(r.Context(), id)
	if err != nil {
		respondError(w, http.StatusNotFound, "model_not_found")
		return
	}
	var payload struct {
		PublicModel       *string `json:"public_model"`
		DisplayName       *string `json:"display_name"`
		Description       *string `json:"description"`
		Status            *string `json:"status"`
		ContextWindow     *int32  `json:"context_window"`
		SupportsStreaming *bool   `json:"supports_streaming"`
		SupportsTools     *bool   `json:"supports_tools"`
		SupportsVision    *bool   `json:"supports_vision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if payload.PublicModel != nil {
		current.PublicModel = *payload.PublicModel
	}
	if payload.DisplayName != nil {
		current.DisplayName = *payload.DisplayName
	}
	if payload.Description != nil {
		current.Description = *payload.Description
	}
	if payload.Status != nil {
		current.Status = *payload.Status
	}
	if payload.ContextWindow != nil {
		current.ContextWindow = payload.ContextWindow
	}
	if payload.SupportsStreaming != nil {
		current.SupportsStreaming = *payload.SupportsStreaming
	}
	if payload.SupportsTools != nil {
		current.SupportsTools = *payload.SupportsTools
	}
	if payload.SupportsVision != nil {
		current.SupportsVision = *payload.SupportsVision
	}
	model, err := h.models.UpdateModel(r.Context(), *current)
	if err != nil {
		respondAdminWriteError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, model)
}

func (h *AdminHandler) ListModelPrices(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"prices": h.models.ListPrices(r.Context())})
}

func (h *AdminHandler) UpsertModelPrice(w http.ResponseWriter, r *http.Request) {
	var payload store.AIModelPrice
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	out, err := h.models.UpsertPrice(r.Context(), payload)
	if err != nil {
		respondAdminWriteError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *AdminHandler) UpdateModelPrice(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_model_price_id")
		return
	}
	current, err := h.models.GetPrice(r.Context(), id)
	if err != nil {
		respondAdminWriteError(w, err)
		return
	}
	var fields map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if raw, ok := fields["model_id"]; ok {
		var v uuid.UUID
		if err := json.Unmarshal(raw, &v); err != nil {
			respondError(w, http.StatusBadRequest, "invalid_model_id")
			return
		}
		current.ModelID = v
	}
	if v, ok := patchDecodeOptionalString(fields, "input_price_per_1m"); ok {
		if v == nil {
			respondError(w, http.StatusBadRequest, "input_price_per_1m_required")
			return
		}
		current.InputPricePer1M = *v
	}
	if v, ok := patchDecodeOptionalString(fields, "output_price_per_1m"); ok {
		if v == nil {
			respondError(w, http.StatusBadRequest, "output_price_per_1m_required")
			return
		}
		current.OutputPricePer1M = *v
	}
	if patchHasField(fields, "cached_price_per_1m") {
		v, _ := patchDecodeOptionalString(fields, "cached_price_per_1m")
		current.CachedPricePer1M = v
	}
	if patchHasField(fields, "reasoning_price_per_1m") {
		v, _ := patchDecodeOptionalString(fields, "reasoning_price_per_1m")
		current.ReasoningPricePer1M = v
	}
	if v, ok := patchDecodeOptionalString(fields, "currency"); ok {
		if v == nil {
			respondError(w, http.StatusBadRequest, "currency_required")
			return
		}
		current.Currency = *v
	}
	if raw, ok := fields["active"]; ok {
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			respondError(w, http.StatusBadRequest, "invalid_active")
			return
		}
		current.Active = v
	}
	out, err := h.models.UpdatePrice(r.Context(), *current)
	if err != nil {
		respondAdminWriteError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *AdminHandler) DeleteModelPrice(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_model_price_id")
		return
	}
	if err := h.models.DeletePrice(r.Context(), id); err != nil {
		respondAdminWriteError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *AdminHandler) ListModelRoutes(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"routes": h.models.ListRoutes(r.Context())})
}

func (h *AdminHandler) UpsertModelRoute(w http.ResponseWriter, r *http.Request) {
	var payload store.AIModelRoute
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	out, err := h.models.UpsertRoute(r.Context(), payload)
	if err != nil {
		respondAdminWriteError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *AdminHandler) UpdateModelRoute(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_model_route_id")
		return
	}
	current, err := h.models.GetRoute(r.Context(), id)
	if err != nil {
		respondAdminWriteError(w, err)
		return
	}
	var fields map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if raw, ok := fields["model_id"]; ok {
		var v uuid.UUID
		if err := json.Unmarshal(raw, &v); err != nil {
			respondError(w, http.StatusBadRequest, "invalid_model_id")
			return
		}
		current.ModelID = v
	}
	if v, ok := patchDecodeOptionalString(fields, "router_instance_code"); ok {
		if v == nil {
			respondError(w, http.StatusBadRequest, "router_instance_code_required")
			return
		}
		current.RouterInstanceCode = *v
	}
	if v, ok := patchDecodeOptionalString(fields, "router_model"); ok {
		if v == nil {
			respondError(w, http.StatusBadRequest, "router_model_required")
			return
		}
		current.RouterModel = *v
	}
	if v, ok := patchDecodeOptionalString(fields, "status"); ok {
		if v == nil {
			respondError(w, http.StatusBadRequest, "status_required")
			return
		}
		current.Status = *v
	}
	if patchHasField(fields, "metadata") {
		raw := fields["metadata"]
		if string(raw) == "null" {
			current.Metadata = nil
		} else {
			current.Metadata = json.RawMessage(raw)
		}
	}
	out, err := h.models.UpdateRoute(r.Context(), *current)
	if err != nil {
		respondAdminWriteError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *AdminHandler) DeleteModelRoute(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_model_route_id")
		return
	}
	if err := h.models.DeleteRoute(r.Context(), id); err != nil {
		respondAdminWriteError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *AdminHandler) ListRouterInstances(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"router_instances": h.routers.ListInstances(r.Context())})
}

func (h *AdminHandler) UpsertRouterInstance(w http.ResponseWriter, r *http.Request) {
	var payload store.RouterInstance
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	instance, err := h.routers.UpsertInstance(r.Context(), payload)
	if err != nil {
		respondAdminWriteError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, instance)
}

func (h *AdminHandler) UpdateRouterInstance(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_router_instance_id")
		return
	}
	current, err := h.routers.GetInstanceByID(r.Context(), id)
	if err != nil {
		respondAdminWriteError(w, err)
		return
	}
	var fields map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if v, ok := patchDecodeOptionalString(fields, "code"); ok {
		if v == nil {
			respondError(w, http.StatusBadRequest, "code_required")
			return
		}
		current.Code = *v
	}
	if patchHasField(fields, "public_base_url") {
		v, _ := patchDecodeOptionalString(fields, "public_base_url")
		current.PublicBaseURL = v
	}
	if v, ok := patchDecodeOptionalString(fields, "internal_base_url"); ok {
		if v == nil {
			respondError(w, http.StatusBadRequest, "internal_base_url_required")
			return
		}
		current.InternalBaseURL = *v
	}
	if v, ok := patchDecodeOptionalString(fields, "status"); ok {
		if v == nil {
			respondError(w, http.StatusBadRequest, "status_required")
			return
		}
		current.Status = *v
	}
	if patchHasField(fields, "encrypted_api_key") {
		v, _ := patchDecodeOptionalString(fields, "encrypted_api_key")
		current.EncryptedAPIKey = v
	}
	if patchHasField(fields, "health_status") {
		v, _ := patchDecodeOptionalString(fields, "health_status")
		current.HealthStatus = v
	}
	if patchHasField(fields, "last_health_check_at") {
		raw := fields["last_health_check_at"]
		if string(raw) == "null" {
			current.LastHealthCheckAt = nil
		} else {
			var v time.Time
			if err := json.Unmarshal(raw, &v); err != nil {
				respondError(w, http.StatusBadRequest, "invalid_last_health_check_at")
				return
			}
			current.LastHealthCheckAt = &v
		}
	}
	if patchHasField(fields, "metadata") {
		raw := fields["metadata"]
		if string(raw) == "null" {
			current.Metadata = nil
		} else {
			current.Metadata = json.RawMessage(raw)
		}
	}
	instance, err := h.routers.UpdateInstance(r.Context(), *current)
	if err != nil {
		respondAdminWriteError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, instance)
}

func (h *AdminHandler) DeleteRouterInstance(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_router_instance_id")
		return
	}
	if err := h.routers.DeleteInstance(r.Context(), id); err != nil {
		respondAdminWriteError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *AdminHandler) ListAllUsage(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"usage": h.usage.ListAll(r.Context(), 200)})
}

func (h *AdminHandler) DeleteModel(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_model_id")
		return
	}
	if err := h.models.DeleteModel(r.Context(), id); err != nil {
		respondAdminWriteError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}
