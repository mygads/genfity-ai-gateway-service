package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"genfity-ai-gateway-service/internal/router"
	"genfity-ai-gateway-service/internal/service"
)

type RouterProxyHandler struct {
	cliProxyClient *router.CLIProxyClient
	routers        *service.RouterService
	routerAPIKey   string
	routerTimeout  time.Duration
}

func NewRouterProxyHandler(cliProxyClient *router.CLIProxyClient, routers *service.RouterService, routerAPIKey string, routerTimeout time.Duration) *RouterProxyHandler {
	return &RouterProxyHandler{
		cliProxyClient: cliProxyClient,
		routers:        routers,
		routerAPIKey:   routerAPIKey,
		routerTimeout:  routerTimeout,
	}
}

func (h *RouterProxyHandler) clientForRouter(ctx context.Context, routerCode string) (*router.CLIProxyClient, bool) {
	code := strings.TrimSpace(routerCode)
	if h.routers == nil || code == "" {
		return h.cliProxyClient, code != ""
	}
	instance, err := h.routers.GetInstance(ctx, code)
	if err != nil || instance == nil || strings.TrimSpace(instance.InternalBaseURL) == "" || instance.Status != "active" {
		return nil, false
	}
	timeout := h.routerTimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return router.NewCLIProxyClient(instance.InternalBaseURL, h.routerAPIKey, timeout), true
}

func (h *RouterProxyHandler) Health(w http.ResponseWriter, r *http.Request) {
	routerCode := strings.TrimSpace(r.PathValue("code"))
	client, ok := h.clientForRouter(r.Context(), routerCode)
	if !ok {
		respondError(w, http.StatusBadRequest, "invalid_router_code")
		return
	}
	out, err := client.RouterHealth(r.Context())
	if err != nil {
		respondError(w, http.StatusBadGateway, "router_error")
		return
	}
	out["router_code"] = routerCode
	respondJSON(w, http.StatusOK, out)
}

func (h *RouterProxyHandler) Models(w http.ResponseWriter, r *http.Request) {
	routerCode := strings.TrimSpace(r.PathValue("code"))
	client, ok := h.clientForRouter(r.Context(), routerCode)
	if !ok {
		respondError(w, http.StatusBadRequest, "invalid_router_code")
		return
	}
	out, err := client.ListModels(r.Context())
	if err != nil {
		respondError(w, http.StatusBadGateway, "router_error")
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *RouterProxyHandler) Providers(w http.ResponseWriter, r *http.Request) {
	respondError(w, http.StatusNotImplemented, "provider_management_not_supported_by_cliproxy")
}

func (h *RouterProxyHandler) ProviderModels(w http.ResponseWriter, r *http.Request) {
	respondError(w, http.StatusNotImplemented, "provider_management_not_supported_by_cliproxy")
}
