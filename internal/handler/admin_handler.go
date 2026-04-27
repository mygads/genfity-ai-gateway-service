package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

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
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, model)
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
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, out)
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
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, out)
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
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, instance)
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
	_ = id
	respondJSON(w, http.StatusNotImplemented, map[string]any{"message": "delete model is not implemented yet"})
}
