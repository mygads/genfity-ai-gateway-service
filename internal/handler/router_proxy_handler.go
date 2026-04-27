package handler

import (
	"net/http"
	"strings"

	"genfity-ai-gateway-service/internal/router"
)

type RouterProxyHandler struct {
	cliProxyClient *router.CLIProxyClient
}

func NewRouterProxyHandler(cliProxyClient *router.CLIProxyClient) *RouterProxyHandler {
	return &RouterProxyHandler{cliProxyClient: cliProxyClient}
}

func (h *RouterProxyHandler) Health(w http.ResponseWriter, r *http.Request) {
	routerCode := strings.TrimSpace(r.PathValue("code"))
	if routerCode == "" {
		respondError(w, http.StatusBadRequest, "invalid_router_code")
		return
	}
	out, err := h.cliProxyClient.RouterHealth(r.Context())
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
	out, err := h.cliProxyClient.ListModels(r.Context())
	if err != nil {
		respondError(w, http.StatusBadGateway, "router_error")
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *RouterProxyHandler) Providers(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"data": []any{}})
}

func (h *RouterProxyHandler) ProviderModels(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"data": []any{}})
}
