package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"genfity-ai-gateway-service/internal/http/middleware"
	"genfity-ai-gateway-service/internal/router"
	"genfity-ai-gateway-service/internal/service"
)

type GatewayHandler struct {
	models       *service.ModelService
	entitlements *service.EntitlementService
	rateLimit    *service.RateLimitService
	nineClient   *router.NineRouterClient
}

func NewGatewayHandler(models *service.ModelService, entitlements *service.EntitlementService, rateLimit *service.RateLimitService, nineClient *router.NineRouterClient) *GatewayHandler {
	return &GatewayHandler{models: models, entitlements: entitlements, rateLimit: rateLimit, nineClient: nineClient}
}

func (h *GatewayHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	models := h.models.ListModels(r.Context())

	var list []map[string]any
	for _, m := range models {
		if m.Status == "active" {
			list = append(list, map[string]any{
				"id":       m.PublicModel,
				"object":   "model",
				"created":  m.CreatedAt.Unix(),
				"owned_by": "genfity",
			})
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   list,
	})
}

func (h *GatewayHandler) Embeddings(w http.ResponseWriter, r *http.Request) {
	apiKey := middleware.GetAPIKey(r.Context())
	ctx := r.Context()

	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}

	publicModel, ok := payload["model"].(string)
	if !ok || publicModel == "" {
		respondError(w, http.StatusBadRequest, "missing_model")
		return
	}

	_, err := h.entitlements.CheckActive(ctx, apiKey.GenfityUserID)
	if err != nil {
		respondError(w, http.StatusPaymentRequired, err.Error())
		return
	}

	route, _, err := h.models.ResolveRouteByPublicModel(ctx, publicModel)
	if err != nil {
		respondError(w, http.StatusBadRequest, "model_not_allowed")
		return
	}

	payload["model"] = route.RouterModel

	resp, err := h.nineClient.Embeddings(ctx, payload)
	if err != nil {
		respondError(w, http.StatusBadGateway, "upstream_error")
		return
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (h *GatewayHandler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	apiKey := middleware.GetAPIKey(r.Context())
	ctx := r.Context()

	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}

	publicModel, ok := payload["model"].(string)
	if !ok || publicModel == "" {
		respondError(w, http.StatusBadRequest, "missing_model")
		return
	}

	_, err := h.entitlements.CheckActive(ctx, apiKey.GenfityUserID)
	if err != nil {
		respondError(w, http.StatusPaymentRequired, err.Error())
		return
	}

	route, _, err := h.models.ResolveRouteByPublicModel(ctx, publicModel)
	if err != nil {
		respondError(w, http.StatusBadRequest, "model_not_allowed")
		return
	}

	payload["model"] = route.RouterModel

	tenantIDStr := apiKey.GenfityUserID.String()
	if apiKey.GenfityTenantID != nil {
		tenantIDStr = apiKey.GenfityTenantID.String()
	}

	if h.rateLimit != nil {
		if err := h.rateLimit.CheckRPM(ctx, apiKey.ID.String()); err != nil {
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
	}

	release := func() {}
	if h.rateLimit != nil {
		var acquireErr error
		release, acquireErr = h.rateLimit.AcquireConcurrency(ctx, tenantIDStr)
		if acquireErr != nil {
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
	}
	defer release()

	resp, err := h.nineClient.ChatCompletions(ctx, payload)
	if err != nil {
		respondError(w, http.StatusBadGateway, "upstream_error")
		return
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)

	_, _ = io.Copy(w, resp.Body)
}
