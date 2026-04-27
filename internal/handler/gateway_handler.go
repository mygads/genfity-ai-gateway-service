package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"genfity-ai-gateway-service/internal/http/middleware"
	"genfity-ai-gateway-service/internal/router"
	"genfity-ai-gateway-service/internal/service"
	"genfity-ai-gateway-service/internal/store"
)

func entitlementAllowsModel(entitlement any, publicModel string) bool {
	typed, ok := entitlement.(*service.ActiveSubscription)
	if !ok || typed == nil || typed.Entitlement == nil {
		return false
	}
	metadata := typed.Entitlement.Metadata
	if len(metadata) == 0 {
		return true
	}
	var payload map[string]any
	if err := json.Unmarshal(metadata, &payload); err != nil {
		return true
	}
	pricingGroup, _ := payload["pricingGroup"].(string)
	if pricingGroup != "unlimited_plan" {
		return true
	}
	allowedRaw, exists := payload["allowedModels"]
	if !exists {
		allowedRaw = payload["allowed_models"]
	}
	if allowedRaw == nil {
		return true
	}
	allowed, ok := allowedRaw.([]any)
	if !ok {
		return true
	}
	for _, item := range allowed {
		if modelName, ok := item.(string); ok && strings.EqualFold(modelName, publicModel) {
			return true
		}
	}
	return false
}

type GatewayHandler struct {
	models       *service.ModelService
	entitlements *service.EntitlementService
	usage        *service.UsageService
	rateLimit    *service.RateLimitService
	nineClient   *router.NineRouterClient
}

func NewGatewayHandler(models *service.ModelService, entitlements *service.EntitlementService, usage *service.UsageService, rateLimit *service.RateLimitService, nineClient *router.NineRouterClient) *GatewayHandler {
	return &GatewayHandler{models: models, entitlements: entitlements, usage: usage, rateLimit: rateLimit, nineClient: nineClient}
}

func parseUsageFromBody(body []byte) (prompt int64, completion int64, total int64) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, 0, 0
	}
	usage, ok := payload["usage"].(map[string]any)
	if !ok {
		return 0, 0, 0
	}
	prompt = anyToInt64(usage["prompt_tokens"])
	completion = anyToInt64(usage["completion_tokens"])
	total = anyToInt64(usage["total_tokens"])
	if total == 0 {
		total = prompt + completion
	}
	return
}

func anyToInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	default:
		return 0
	}
}

func modelPrice(prices []store.AIModelPrice, modelID uuid.UUID) *store.AIModelPrice {
	for _, item := range prices {
		if item.ModelID == modelID && item.Active {
			return &item
		}
	}
	return nil
}

func parseFloatPtr(value *string) float64 {
	if value == nil {
		return 0
	}
	v, err := strconv.ParseFloat(*value, 64)
	if err != nil {
		return 0
	}
	return v
}

func formatAmount(value float64) string {
	return fmt.Sprintf("%.6f", value)
}

func recordUsageAndMaybeDebit(ctx any, h *GatewayHandler, apiKey *store.APIKey, subscription *service.ActiveSubscription, model *store.AIModel, route *store.AIModelRoute, started time.Time, statusCode int, responseBody []byte) {
	contextValue, ok := ctx.(interface{ Done() <-chan struct{} })
	_ = contextValue
	_ = ok
}

func (h *GatewayHandler) writeUpstreamResponse(w http.ResponseWriter, resp *http.Response, body []byte) {
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func (h *GatewayHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	models := h.models.ListModels(r.Context())
	var list []map[string]any
	for _, m := range models {
		if m.Status == "active" {
			list = append(list, map[string]any{"id": m.PublicModel, "object": "model", "created": m.CreatedAt.Unix(), "owned_by": "genfity"})
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{"object": "list", "data": list})
}

func (h *GatewayHandler) Embeddings(w http.ResponseWriter, r *http.Request) {
	apiKey := middleware.GetAPIKey(r.Context())
	ctx := r.Context()
	started := time.Now().UTC()

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

	subscription, err := h.entitlements.CheckActiveSubscription(ctx, apiKey.GenfityUserID)
	if err != nil {
		respondError(w, http.StatusPaymentRequired, err.Error())
		return
	}
	if !entitlementAllowsModel(subscription, publicModel) {
		respondError(w, http.StatusPaymentRequired, "model_not_in_unlimited_plan")
		return
	}
	route, model, err := h.models.ResolveRouteByPublicModel(ctx, publicModel)
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
	body, _ := io.ReadAll(resp.Body)
	h.processUsage(ctx, apiKey, subscription, model, route, started, resp.StatusCode, body, publicModel)
	h.writeUpstreamResponse(w, resp, body)
}

func (h *GatewayHandler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	apiKey := middleware.GetAPIKey(r.Context())
	ctx := r.Context()
	started := time.Now().UTC()

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

	subscription, err := h.entitlements.CheckActiveSubscription(ctx, apiKey.GenfityUserID)
	if err != nil {
		respondError(w, http.StatusPaymentRequired, err.Error())
		return
	}
	if !entitlementAllowsModel(subscription, publicModel) {
		respondError(w, http.StatusPaymentRequired, "model_not_in_unlimited_plan")
		return
	}
	limits := service.PlanLimitsFromSnapshot(subscription.Plan)
	route, model, err := h.models.ResolveRouteByPublicModel(ctx, publicModel)
	if err != nil {
		respondError(w, http.StatusBadRequest, "model_not_allowed")
		return
	}
	payload["model"] = route.RouterModel
	tenantIDStr := apiKey.GenfityUserID
	if apiKey.GenfityTenantID != nil {
		tenantIDStr = *apiKey.GenfityTenantID
	}
	if h.rateLimit != nil && limits.HasRPM() {
		if err := h.rateLimit.CheckRPM(ctx, apiKey.ID.String(), limits.RPM); err != nil {
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
	}
	release := func() {}
	if h.rateLimit != nil && limits.HasConcurrency() {
		var acquireErr error
		release, acquireErr = h.rateLimit.AcquireConcurrency(ctx, tenantIDStr, limits.ConcurrentLimit)
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
	body, _ := io.ReadAll(resp.Body)
	h.processUsage(ctx, apiKey, subscription, model, route, started, resp.StatusCode, body, publicModel)
	h.writeUpstreamResponse(w, resp, body)
}

func (h *GatewayHandler) processUsage(ctx context.Context, apiKey store.APIKey, subscription *service.ActiveSubscription, model *store.AIModel, route *store.AIModelRoute, started time.Time, statusCode int, body []byte, publicModel string) {
	h.recordUsage(ctx, apiKey, subscription, model, route, started, statusCode, body, publicModel)
}

func (h *GatewayHandler) recordUsage(ctx context.Context, apiKey store.APIKey, subscription *service.ActiveSubscription, model *store.AIModel, route *store.AIModelRoute, started time.Time, statusCode int, body []byte, publicModel string) {
	finished := time.Now().UTC()
	latencyMs := finished.Sub(started).Milliseconds()

	promptTokens, completionTokens, totalTokens := parseUsageFromBody(body)

	// Compute cost from model price
	prices := h.models.ListPrices(ctx)
	price := modelPrice(prices, model.ID)
	inputCostUsd := 0.0
	outputCostUsd := 0.0
	totalCostUsd := 0.0
	if price != nil {
		inputCostUsd = float64(promptTokens) / 1_000_000 * parseFloatPtr(&price.InputPricePer1M)
		outputCostUsd = float64(completionTokens) / 1_000_000 * parseFloatPtr(&price.OutputPricePer1M)
		totalCostUsd = inputCostUsd + outputCostUsd
	}

	statusStr := "success"
	if statusCode >= 400 {
		statusStr = "failed"
	}

	inCost := formatAmount(inputCostUsd)
	outCost := formatAmount(outputCostUsd)
	totCost := formatAmount(totalCostUsd)
	routerModel := route.RouterModel
	routerCode := route.RouterInstanceCode
	apiKeyID := apiKey.ID
	latencyMs32 := int32(latencyMs)

	var entryMeta map[string]any
	if subscription != nil && subscription.Entitlement != nil {
		var subMeta map[string]any
		if len(subscription.Entitlement.Metadata) > 0 {
			_ = json.Unmarshal(subscription.Entitlement.Metadata, &subMeta)
		}
		pg, _ := subMeta["pricingGroup"].(string)
		isUnlim := pg == "unlimited_plan"
		entryMeta = map[string]any{
			"pricing_group": pg,
			"is_unlimited":  isUnlim,
			"plan_code":     subscription.Entitlement.PlanCode,
		}
	}
	var entryMetaJSON []byte
	if entryMeta != nil {
		entryMetaJSON, _ = json.Marshal(entryMeta)
	}

	entry := store.UsageLedgerEntry{
		ID:                 uuid.New(),
		RequestID:          uuid.New().String(),
		GenfityUserID:      apiKey.GenfityUserID,
		GenfityTenantID:    apiKey.GenfityTenantID,
		APIKeyID:           &apiKeyID,
		PublicModel:        publicModel,
		RouterModel:        &routerModel,
		RouterInstanceCode: &routerCode,
		PromptTokens:       promptTokens,
		CompletionTokens:   completionTokens,
		TotalTokens:        totalTokens,
		InputCost:          inCost,
		OutputCost:         outCost,
		TotalCost:          totCost,
		Status:             statusStr,
		LatencyMS:          &latencyMs32,
		StartedAt:          started,
		FinishedAt:         &finished,
		Metadata:           entryMetaJSON,
	}
	h.usage.Record(ctx, entry)

	// Debit credit balance only if active plan is credit_package
	if statusCode < 400 && totalCostUsd > 0 && subscription.Entitlement != nil {
		var meta map[string]any
		if len(subscription.Entitlement.Metadata) > 0 {
			_ = json.Unmarshal(subscription.Entitlement.Metadata, &meta)
		}
		if pg, _ := meta["pricingGroup"].(string); pg == "credit_package" {
			if err := h.usage.DebitCreditBalance(ctx, apiKey.GenfityUserID, subscription.Entitlement.PlanCode, totalCostUsd); err != nil {
				// log but don't block
				_ = err
			}
		}
	}
}

