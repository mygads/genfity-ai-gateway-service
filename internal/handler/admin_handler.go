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
	var payload struct {
		ModelID             *uuid.UUID `json:"model_id"`
		InputPricePer1M     *string    `json:"input_price_per_1m"`
		OutputPricePer1M    *string    `json:"output_price_per_1m"`
		CachedPricePer1M    *string    `json:"cached_price_per_1m"`
		ReasoningPricePer1M *string    `json:"reasoning_price_per_1m"`
		Currency            *string    `json:"currency"`
		Active              *bool      `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if payload.ModelID != nil {
		current.ModelID = *payload.ModelID
	}
	if payload.InputPricePer1M != nil {
		current.InputPricePer1M = *payload.InputPricePer1M
	}
	if payload.OutputPricePer1M != nil {
		current.OutputPricePer1M = *payload.OutputPricePer1M
	}
	if payload.CachedPricePer1M != nil {
		current.CachedPricePer1M = payload.CachedPricePer1M
	}
	if payload.ReasoningPricePer1M != nil {
		current.ReasoningPricePer1M = payload.ReasoningPricePer1M
	}
	if payload.Currency != nil {
		current.Currency = *payload.Currency
	}
	if payload.Active != nil {
		current.Active = *payload.Active
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
	var payload struct {
		ModelID            *uuid.UUID       `json:"model_id"`
		RouterInstanceCode *string          `json:"router_instance_code"`
		RouterModel        *string          `json:"router_model"`
		Status             *string          `json:"status"`
		Metadata           *json.RawMessage `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if payload.ModelID != nil {
		current.ModelID = *payload.ModelID
	}
	if payload.RouterInstanceCode != nil {
		current.RouterInstanceCode = *payload.RouterInstanceCode
	}
	if payload.RouterModel != nil {
		current.RouterModel = *payload.RouterModel
	}
	if payload.Status != nil {
		current.Status = *payload.Status
	}
	if payload.Metadata != nil {
		current.Metadata = *payload.Metadata
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
	var payload struct {
		Code              *string          `json:"code"`
		PublicBaseURL     *string          `json:"public_base_url"`
		InternalBaseURL   *string          `json:"internal_base_url"`
		Status            *string          `json:"status"`
		EncryptedAPIKey   *string          `json:"encrypted_api_key"`
		HealthStatus      *string          `json:"health_status"`
		LastHealthCheckAt *time.Time       `json:"last_health_check_at"`
		Metadata          *json.RawMessage `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if payload.Code != nil {
		current.Code = *payload.Code
	}
	if payload.PublicBaseURL != nil {
		current.PublicBaseURL = payload.PublicBaseURL
	}
	if payload.InternalBaseURL != nil {
		current.InternalBaseURL = *payload.InternalBaseURL
	}
	if payload.Status != nil {
		current.Status = *payload.Status
	}
	if payload.EncryptedAPIKey != nil {
		current.EncryptedAPIKey = payload.EncryptedAPIKey
	}
	if payload.HealthStatus != nil {
		current.HealthStatus = payload.HealthStatus
	}
	if payload.LastHealthCheckAt != nil {
		current.LastHealthCheckAt = payload.LastHealthCheckAt
	}
	if payload.Metadata != nil {
		current.Metadata = *payload.Metadata
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
