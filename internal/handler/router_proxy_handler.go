package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"genfity-ai-gateway-service/internal/router"
)

type RouterProxyHandler struct {
	nineClient *router.NineRouterClient
}

func NewRouterProxyHandler(nineClient *router.NineRouterClient) *RouterProxyHandler {
	return &RouterProxyHandler{nineClient: nineClient}
}

func (h *RouterProxyHandler) Health(w http.ResponseWriter, r *http.Request) {
	routerCode := strings.TrimSpace(r.PathValue("code"))
	if routerCode == "" {
		respondError(w, http.StatusBadRequest, "invalid_router_code")
		return
	}
	out, err := h.nineClient.RouterHealth(r.Context())
	if err == nil {
		out["router_code"] = routerCode
	}
	if err != nil {
		respondError(w, http.StatusBadGateway, "router_error")
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *RouterProxyHandler) Models(w http.ResponseWriter, r *http.Request) {
	out, err := h.nineClient.ListModels(r.Context())
	if err != nil {
		respondError(w, http.StatusBadGateway, "router_error")
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *RouterProxyHandler) Providers(w http.ResponseWriter, r *http.Request) {
	out, err := h.nineClient.ListProviders(r.Context())
	if err != nil {
		respondError(w, http.StatusBadGateway, "router_error")
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *RouterProxyHandler) ProviderModels(w http.ResponseWriter, r *http.Request) {
	providerID := strings.TrimSpace(r.PathValue("providerID"))
	if providerID == "" {
		respondError(w, http.StatusBadRequest, "invalid_provider_id")
		return
	}
	out, err := h.nineClient.ListProviderModels(r.Context(), providerID)
	if err != nil {
		respondError(w, http.StatusBadGateway, "router_error")
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *RouterProxyHandler) Combos(w http.ResponseWriter, r *http.Request) {
	out, err := h.nineClient.ListCombos(r.Context())
	if err != nil {
		respondError(w, http.StatusBadGateway, "router_error")
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *RouterProxyHandler) CreateCombo(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	out, err := h.nineClient.CreateCombo(r.Context(), payload)
	if err != nil {
		respondError(w, http.StatusBadGateway, "router_error")
		return
	}
	respondJSON(w, http.StatusCreated, out)
}

func (h *RouterProxyHandler) UpdateCombo(w http.ResponseWriter, r *http.Request) {
	comboID := strings.TrimSpace(r.PathValue("comboID"))
	if comboID == "" {
		respondError(w, http.StatusBadRequest, "invalid_combo_id")
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	out, err := h.nineClient.UpdateCombo(r.Context(), comboID, payload)
	if err != nil {
		respondError(w, http.StatusBadGateway, "router_error")
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *RouterProxyHandler) DeleteCombo(w http.ResponseWriter, r *http.Request) {
	comboID := strings.TrimSpace(r.PathValue("comboID"))
	if comboID == "" {
		respondError(w, http.StatusBadRequest, "invalid_combo_id")
		return
	}
	if err := h.nineClient.DeleteCombo(r.Context(), comboID); err != nil {
		respondError(w, http.StatusBadGateway, "router_error")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}
