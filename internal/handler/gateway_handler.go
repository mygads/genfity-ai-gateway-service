package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"genfity-ai-gateway-service/internal/http/middleware"
	"genfity-ai-gateway-service/internal/router"
	"genfity-ai-gateway-service/internal/service"
	"genfity-ai-gateway-service/internal/store"
)

// --- entitlement helpers ---

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

// --- GatewayHandler ---

type GatewayHandler struct {
	models       *service.ModelService
	entitlements *service.EntitlementService
	usage        *service.UsageService
	rateLimit    *service.RateLimitService
	comboSvc     *service.ComboService
	cliProxy     *router.CLIProxyClient
}

func NewGatewayHandler(
	models *service.ModelService,
	entitlements *service.EntitlementService,
	usage *service.UsageService,
	rateLimit *service.RateLimitService,
	comboSvc *service.ComboService,
	cliProxy *router.CLIProxyClient,
) *GatewayHandler {
	return &GatewayHandler{
		models:       models,
		entitlements: entitlements,
		usage:        usage,
		rateLimit:    rateLimit,
		comboSvc:     comboSvc,
		cliProxy:     cliProxy,
	}
}

// --- helpers ---

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

func (h *GatewayHandler) writeUpstreamResponse(w http.ResponseWriter, resp *http.Response, body []byte) {
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// isRetriableStatus returns true when the upstream error code should trigger
// a combo fallback to the next entry.
func isRetriableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,   // 429 quota/rate limit
		http.StatusServiceUnavailable, // 503
		http.StatusBadGateway,         // 502
		http.StatusGatewayTimeout,     // 504
		http.StatusInternalServerError: // 500
		return true
	}
	return false
}

// isRetriableTrigger checks whether an error body contains one of the
// combo entry TriggerOn keywords (e.g. "quota_exceeded", "rate_limit", "error").
func isRetriableTrigger(body []byte, triggers []string) bool {
	if len(triggers) == 0 {
		return true
	}
	text := strings.ToLower(string(body))
	for _, t := range triggers {
		if strings.Contains(text, strings.ToLower(t)) {
			return true
		}
	}
	return false
}

// --- ListModels ---

func (h *GatewayHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	models := h.models.ListModels(r.Context())
	var list []map[string]any
	for _, m := range models {
		if m.Status == "active" {
			list = append(list, map[string]any{
				"id":       m.PublicModel,
				"object":   "model",
				"created": m.CreatedAt.Unix(),
				"owned_by": "genfity",
			})
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{"object": "list", "data": list})
}

// --- Embeddings ---

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

	resp, err := h.cliProxy.Embeddings(ctx, payload)
	if err != nil {
		respondError(w, http.StatusBadGateway, "upstream_error")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	h.recordUsage(ctx, apiKey, subscription, model, route, started, resp.StatusCode, body, publicModel)
	h.writeUpstreamResponse(w, resp, body)
}

// --- ChatCompletions with virtual combo fallback ---

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

	if h.rateLimit != nil && limits.HasRPM() {
		if err := h.rateLimit.CheckRPM(ctx, apiKey.ID.String(), limits.RPM); err != nil {
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
	}
	release := func() {}
	tenantIDStr := apiKey.GenfityUserID
	if apiKey.GenfityTenantID != nil {
		tenantIDStr = *apiKey.GenfityTenantID
	}
	if h.rateLimit != nil && limits.HasConcurrency() {
		var acquireErr error
		release, acquireErr = h.rateLimit.AcquireConcurrency(ctx, tenantIDStr, limits.ConcurrentLimit)
		if acquireErr != nil {
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
	}
	defer release()

	// Build the candidate list: primary route first, then combo fallbacks if any.
	type candidate struct {
		routerInstanceCode string
		routerModel        string
		triggerOn          []string
	}

	primaryCandidate := candidate{
		routerInstanceCode: route.RouterInstanceCode,
		routerModel:        route.RouterModel,
	}
	candidates := []candidate{primaryCandidate}

	if h.comboSvc != nil {
		if combo, comboErr := h.comboSvc.GetComboForModel(ctx, model.ID); comboErr == nil {
			entries := combo.Entries
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].Priority < entries[j].Priority
			})
			// Append combo entries as additional fallback candidates.
			for _, e := range entries {
				candidates = append(candidates, candidate{
					routerInstanceCode: e.RouterInstanceCode,
					routerModel:        e.RouterModel,
					triggerOn:          e.TriggerOn,
				})
			}
		}
	}

	var lastResp *http.Response
	var lastBody []byte
	var lastStatusCode int

	for _, cand := range candidates {
		p := clonePayload(payload)
		p["model"] = cand.routerModel

		resp, callErr := h.cliProxy.ChatCompletions(ctx, p)
		if callErr != nil {
			// Network-level error: try next candidate.
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		statusCode := resp.StatusCode

		if !isRetriableStatus(statusCode) || !isRetriableTrigger(body, cand.triggerOn) {
			// Success or non-retriable error: use this response.
			effectiveRoute := &store.AIModelRoute{
				ID:                 route.ID,
				ModelID:            route.ModelID,
				RouterInstanceCode: cand.routerInstanceCode,
				RouterModel:        cand.routerModel,
				Status:             route.Status,
				CreatedAt:          route.CreatedAt,
			}
			h.recordUsage(ctx, apiKey, subscription, model, effectiveRoute, started, statusCode, body, publicModel)
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(statusCode)
			_, _ = w.Write(body)
			return
		}

		// Retriable: save it and try next candidate.
		lastResp = resp
		lastBody = body
		lastStatusCode = statusCode
	}

	// All candidates exhausted, write the last known response.
	if lastResp != nil {
		effectiveRoute := route
		h.recordUsage(ctx, apiKey, subscription, model, effectiveRoute, started, lastStatusCode, lastBody, publicModel)
		for k, v := range lastResp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(lastStatusCode)
		_, _ = w.Write(lastBody)
		return
	}

	respondError(w, http.StatusBadGateway, "all_candidates_failed")
}

// clonePayload deep-copies a JSON payload map so that candidates
// can mutate their own copy independently.
func clonePayload(original map[string]any) map[string]any {
	b, _ := json.Marshal(original)
	var copy map[string]any
	_ = json.Unmarshal(b, &copy)
	return copy
}

// --- usage recording ---

func (h *GatewayHandler) processUsage(ctx context.Context, apiKey store.APIKey, subscription *service.ActiveSubscription, model *store.AIModel, route *store.AIModelRoute, started time.Time, statusCode int, body []byte, publicModel string) {
	h.recordUsage(ctx, apiKey, subscription, model, route, started, statusCode, body, publicModel)
}

func (h *GatewayHandler) recordUsage(ctx context.Context, apiKey store.APIKey, subscription *service.ActiveSubscription, model *store.AIModel, route *store.AIModelRoute, started time.Time, statusCode int, body []byte, publicModel string) {
	finished := time.Now().UTC()
	latencyMs := finished.Sub(started).Milliseconds()

	promptTokens, completionTokens, totalTokens := parseUsageFromBody(body)

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

	if statusCode < 400 && totalCostUsd > 0 && subscription.Entitlement != nil {
		var meta map[string]any
		if len(subscription.Entitlement.Metadata) > 0 {
			_ = json.Unmarshal(subscription.Entitlement.Metadata, &meta)
		}
		if pg, _ := meta["pricingGroup"].(string); pg == "credit_package" {
			if err := h.usage.DebitCreditBalance(ctx, apiKey.GenfityUserID, subscription.Entitlement.PlanCode, totalCostUsd); err != nil {
				_ = err
			}
		}
	}
}
