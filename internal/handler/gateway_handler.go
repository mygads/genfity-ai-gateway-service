package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	models        *service.ModelService
	entitlements  *service.EntitlementService
	usage         *service.UsageService
	rateLimit     *service.RateLimitService
	comboSvc      *service.ComboService
	routers       *service.RouterService
	cliProxy      *router.CLIProxyClient
	routerAPIKey  string
	routerTimeout time.Duration
}

func NewGatewayHandler(
	models *service.ModelService,
	entitlements *service.EntitlementService,
	usage *service.UsageService,
	rateLimit *service.RateLimitService,
	comboSvc *service.ComboService,
	routers *service.RouterService,
	cliProxy *router.CLIProxyClient,
	routerAPIKey string,
	routerTimeout time.Duration,
) *GatewayHandler {
	return &GatewayHandler{
		models:        models,
		entitlements:  entitlements,
		usage:         usage,
		rateLimit:     rateLimit,
		comboSvc:      comboSvc,
		routers:       routers,
		cliProxy:      cliProxy,
		routerAPIKey:  routerAPIKey,
		routerTimeout: routerTimeout,
	}
}

// --- helpers ---

func parseUsageFromBody(body []byte) (prompt int64, completion int64, total int64) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		prompt, completion, total = parseUsageFromPayload(payload)
		if prompt != 0 || completion != 0 || total != 0 {
			return prompt, completion, total
		}
	}
	return parseUsageFromSSEBody(body)
}

func parseUsageFromSSEBody(body []byte) (prompt int64, completion int64, total int64) {
	var event strings.Builder
	flush := func() {
		data := strings.TrimSpace(event.String())
		event.Reset()
		if data == "" || data == "[DONE]" {
			return
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return
		}
		p, c, t := parseUsageFromPayload(payload)
		if p != 0 {
			prompt = p
		}
		if c != 0 {
			completion = c
		}
		if t != 0 {
			total = t
		}
	}

	for _, rawLine := range strings.Split(string(body), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if event.Len() > 0 {
				event.WriteByte('\n')
			}
			event.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	if total == 0 {
		total = prompt + completion
	}
	return prompt, completion, total
}

func parseUsageFromPayload(payload map[string]any) (prompt int64, completion int64, total int64) {
	usage, ok := payload["usage"].(map[string]any)
	if !ok {
		if message, ok := payload["message"].(map[string]any); ok {
			usage, _ = message["usage"].(map[string]any)
		}
	}
	if usage == nil {
		return 0, 0, 0
	}
	prompt = anyToInt64(usage["prompt_tokens"])
	completion = anyToInt64(usage["completion_tokens"])
	total = anyToInt64(usage["total_tokens"])
	if prompt == 0 {
		prompt = anyToInt64(usage["input_tokens"])
	}
	if completion == 0 {
		completion = anyToInt64(usage["output_tokens"])
	}
	if total == 0 {
		total = prompt + completion
	}
	return prompt, completion, total
}

func anyToInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		parsed, _ := t.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(t, 10, 64)
		return parsed
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

func isStreamingPayload(payload map[string]any) bool {
	stream, _ := payload["stream"].(bool)
	return stream
}

func ensureStreamUsageOption(payload map[string]any) {
	options, _ := payload["stream_options"].(map[string]any)
	if options == nil {
		options = map[string]any{}
	}
	options["include_usage"] = true
	payload["stream_options"] = options
}

func estimatePayloadTokens(value any) int64 {
	var chars int64
	var walk func(any)
	walk = func(v any) {
		switch typed := v.(type) {
		case string:
			chars += int64(len(typed))
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for key, item := range typed {
				if key == "model" || key == "stream" || key == "stream_options" {
					continue
				}
				walk(item)
			}
		}
	}
	walk(value)
	if chars == 0 {
		return 1
	}
	return (chars + 3) / 4
}

func activePeriod(entitlement *store.CustomerEntitlement) (time.Time, time.Time) {
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	if entitlement != nil {
		if entitlement.PeriodStart != nil {
			start = entitlement.PeriodStart.UTC()
		}
		if entitlement.PeriodEnd != nil {
			end = entitlement.PeriodEnd.UTC()
		}
	}
	return start, end
}

func pricingGroup(subscription *service.ActiveSubscription) string {
	if subscription == nil || subscription.Entitlement == nil || len(subscription.Entitlement.Metadata) == 0 {
		return ""
	}
	var meta map[string]any
	_ = json.Unmarshal(subscription.Entitlement.Metadata, &meta)
	value, _ := meta["pricingGroup"].(string)
	return value
}

func quotaLimit(subscription *service.ActiveSubscription) int64 {
	if subscription == nil {
		return 0
	}
	if subscription.Entitlement != nil && subscription.Entitlement.QuotaTokensMonthly != nil {
		return *subscription.Entitlement.QuotaTokensMonthly
	}
	if subscription.Plan != nil && subscription.Plan.QuotaTokensMonthly != nil {
		return *subscription.Plan.QuotaTokensMonthly
	}
	return 0
}

func balanceAmount(subscription *service.ActiveSubscription) float64 {
	if subscription == nil || subscription.Entitlement == nil || subscription.Entitlement.BalanceSnapshot == nil {
		return 0
	}
	value, _ := strconv.ParseFloat(*subscription.Entitlement.BalanceSnapshot, 64)
	return value
}

type tokenReservationEstimate struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	Bounded          bool
}

type runtimeReservation struct {
	PeriodStart    time.Time
	PeriodEnd      time.Time
	QuotaTokens    int64
	CreditUSD      float64
	CreditPlanCode string
}

type usageSettlement struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	TotalCostUSD     float64
}

func estimateRequestTokens(payload map[string]any, model *store.AIModel) tokenReservationEstimate {
	prompt := estimatePayloadTokens(payload)
	completion := anyToInt64(payload["max_tokens"])
	if completion == 0 {
		completion = anyToInt64(payload["max_completion_tokens"])
	}
	bounded := completion > 0
	if completion == 0 && model != nil && model.ContextWindow != nil {
		remaining := int64(*model.ContextWindow) - prompt
		if remaining > 0 {
			completion = remaining
			bounded = true
		}
	}
	total := prompt + completion
	if total <= 0 {
		total = 1
	}
	return tokenReservationEstimate{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total, Bounded: bounded}
}

func (h *GatewayHandler) reserveRuntimeLimits(ctx context.Context, apiKey store.APIKey, subscription *service.ActiveSubscription, limits service.PlanLimits, payload map[string]any, model *store.AIModel) (runtimeReservation, int, string) {
	estimate := estimateRequestTokens(payload, model)
	if h.rateLimit != nil && limits.HasTPM() {
		if err := h.rateLimit.CheckTPM(ctx, apiKey.GenfityUserID, limits.TPMAllowance(estimate.TotalTokens), limits.TPM); err != nil {
			return runtimeReservation{}, http.StatusTooManyRequests, "rate_limit_exceeded"
		}
	}
	var reservation runtimeReservation
	limit := quotaLimit(subscription)
	if limit > 0 && subscription != nil && subscription.Entitlement != nil {
		if !estimate.Bounded {
			return runtimeReservation{}, http.StatusBadRequest, "max_tokens_required"
		}
		periodStart, periodEnd := activePeriod(subscription.Entitlement)
		if err := h.usage.ReserveQuotaTokens(ctx, apiKey.GenfityUserID, apiKey.GenfityTenantID, periodStart, periodEnd, estimate.TotalTokens, limit); err != nil {
			if errors.Is(err, service.ErrQuotaExceeded) {
				return runtimeReservation{}, http.StatusTooManyRequests, "quota_exceeded"
			}
			return runtimeReservation{}, http.StatusInternalServerError, "quota_reservation_failed"
		}
		reservation.PeriodStart = periodStart
		reservation.PeriodEnd = periodEnd
		reservation.QuotaTokens = estimate.TotalTokens
	}
	if pricingGroup(subscription) == "credit_package" {
		if balanceAmount(subscription) <= 0 {
			if reservation.QuotaTokens > 0 {
				_ = h.usage.FinalizeQuotaTokens(ctx, apiKey.GenfityUserID, reservation.PeriodStart, reservation.PeriodEnd, reservation.QuotaTokens, 0, false)
			}
			return runtimeReservation{}, http.StatusPaymentRequired, "insufficient_balance"
		}
		prices := h.models.ListPrices(ctx)
		price := modelPrice(prices, model.ID)
		if price != nil {
			if !estimate.Bounded && parseFloatPtr(&price.OutputPricePer1M) > 0 {
				if reservation.QuotaTokens > 0 {
					_ = h.usage.FinalizeQuotaTokens(ctx, apiKey.GenfityUserID, reservation.PeriodStart, reservation.PeriodEnd, reservation.QuotaTokens, 0, false)
				}
				return runtimeReservation{}, http.StatusBadRequest, "max_tokens_required"
			}
			reserveUSD := float64(estimate.PromptTokens)/1_000_000*parseFloatPtr(&price.InputPricePer1M) + float64(estimate.CompletionTokens)/1_000_000*parseFloatPtr(&price.OutputPricePer1M)
			if reserveUSD > 0 && subscription != nil && subscription.Entitlement != nil {
				if err := h.usage.ReserveCreditBalance(ctx, apiKey.GenfityUserID, subscription.Entitlement.PlanCode, reserveUSD); err != nil {
					if reservation.QuotaTokens > 0 {
						_ = h.usage.FinalizeQuotaTokens(ctx, apiKey.GenfityUserID, reservation.PeriodStart, reservation.PeriodEnd, reservation.QuotaTokens, 0, false)
					}
					if errors.Is(err, service.ErrInsufficientBalance) {
						return runtimeReservation{}, http.StatusPaymentRequired, "insufficient_balance"
					}
					return runtimeReservation{}, http.StatusInternalServerError, "credit_reservation_failed"
				}
				reservation.CreditUSD = reserveUSD
				reservation.CreditPlanCode = subscription.Entitlement.PlanCode
			}
		}
	}
	return reservation, 0, ""
}

func (h *GatewayHandler) finalizeRuntimeReservation(ctx context.Context, apiKey store.APIKey, reservation runtimeReservation, settlement usageSettlement, success bool) error {
	usedTokens := int64(0)
	actualUSD := 0.0
	if success {
		usedTokens = settlement.TotalTokens
		actualUSD = settlement.TotalCostUSD
	}
	if reservation.QuotaTokens > 0 {
		if err := h.usage.FinalizeQuotaTokens(ctx, apiKey.GenfityUserID, reservation.PeriodStart, reservation.PeriodEnd, reservation.QuotaTokens, usedTokens, success); err != nil {
			return err
		}
	}
	if reservation.CreditUSD > 0 {
		if err := h.usage.FinalizeCreditBalance(ctx, apiKey.GenfityUserID, reservation.CreditPlanCode, reservation.CreditUSD, actualUSD); err != nil {
			return err
		}
	}
	return nil
}

func (h *GatewayHandler) recordAndFinalizeRuntime(ctx context.Context, apiKey store.APIKey, subscription *service.ActiveSubscription, model *store.AIModel, route *store.AIModelRoute, reservation runtimeReservation, started time.Time, statusCode int, body []byte, publicModel string) error {
	settlement, err := h.recordUsage(ctx, apiKey, subscription, model, route, started, statusCode, body, publicModel)
	if err != nil {
		_ = h.finalizeRuntimeReservation(ctx, apiKey, reservation, usageSettlement{}, false)
		return err
	}
	return h.finalizeRuntimeReservation(ctx, apiKey, reservation, settlement, statusCode < 400)
}

func (h *GatewayHandler) streamUpstreamResponse(w http.ResponseWriter, resp *http.Response) []byte {
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.Header().Del("Content-Length")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)

	var captured bytes.Buffer
	writer := io.MultiWriter(w, &captured)
	buf := make([]byte, 32*1024)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			_, _ = writer.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	return captured.Bytes()
}

func (h *GatewayHandler) clientForRouter(ctx context.Context, routerInstanceCode string) (*router.CLIProxyClient, bool) {
	code := strings.TrimSpace(routerInstanceCode)
	if h.routers == nil || code == "" {
		return h.cliProxy, true
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

// isRetriableStatus returns true when the upstream error code should trigger
// a combo fallback to the next entry.
func isRetriableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429 quota/rate limit
		http.StatusServiceUnavailable,  // 503
		http.StatusBadGateway,          // 502
		http.StatusGatewayTimeout,      // 504
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
				"created":  m.CreatedAt.Unix(),
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

	client, ok := h.clientForRouter(ctx, route.RouterInstanceCode)
	if !ok {
		respondError(w, http.StatusBadGateway, "router_unavailable")
		return
	}
	resp, err := client.Embeddings(ctx, payload)
	if err != nil {
		respondError(w, http.StatusBadGateway, "upstream_error")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	h.recordUsageWithLegacyBilling(ctx, apiKey, subscription, model, route, started, resp.StatusCode, body, publicModel)
	h.writeUpstreamResponse(w, resp, body)
}

// --- Anthropic-compatible Messages with virtual combo fallback ---

func (h *GatewayHandler) Messages(w http.ResponseWriter, r *http.Request) {
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
	accountID := apiKey.GenfityUserID
	if h.rateLimit != nil && limits.HasConcurrency() {
		var acquireErr error
		release, acquireErr = h.rateLimit.AcquireConcurrency(ctx, accountID, limits.ConcurrentLimit)
		if acquireErr != nil {
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
	}
	defer release()

	reservation, statusCode, errorCode := h.reserveRuntimeLimits(ctx, apiKey, subscription, limits, payload, model)
	if statusCode != 0 {
		respondError(w, statusCode, errorCode)
		return
	}

	type candidate struct {
		routerInstanceCode string
		routerModel        string
		triggerOn          []string
	}

	candidates := []candidate{{routerInstanceCode: route.RouterInstanceCode, routerModel: route.RouterModel}}

	if h.comboSvc != nil {
		if combo, comboErr := h.comboSvc.GetComboForModel(ctx, model.ID); comboErr == nil {
			entries := combo.Entries
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].Priority < entries[j].Priority
			})
			for _, e := range entries {
				candidates = append(candidates, candidate{
					routerInstanceCode: e.RouterInstanceCode,
					routerModel:        e.RouterModel,
					triggerOn:          e.TriggerOn,
				})
			}
		}
	}

	streamRequested := isStreamingPayload(payload)
	var lastResp *http.Response
	var lastBody []byte
	var lastStatusCode int

	for _, cand := range candidates {
		p := clonePayload(payload)
		p["model"] = cand.routerModel

		client, ok := h.clientForRouter(ctx, cand.routerInstanceCode)
		if !ok {
			continue
		}
		resp, callErr := client.Messages(ctx, p)
		if callErr != nil {
			continue
		}

		statusCode := resp.StatusCode
		if streamRequested && statusCode < 400 {
			effectiveRoute := &store.AIModelRoute{
				ID:                 route.ID,
				ModelID:            route.ModelID,
				RouterInstanceCode: cand.routerInstanceCode,
				RouterModel:        cand.routerModel,
				Status:             route.Status,
				CreatedAt:          route.CreatedAt,
			}
			body := h.streamUpstreamResponse(w, resp)
			resp.Body.Close()
			_ = h.recordAndFinalizeRuntime(ctx, apiKey, subscription, model, effectiveRoute, reservation, started, statusCode, body, publicModel)
			return
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if !isRetriableStatus(statusCode) || !isRetriableTrigger(body, cand.triggerOn) {
			effectiveRoute := &store.AIModelRoute{
				ID:                 route.ID,
				ModelID:            route.ModelID,
				RouterInstanceCode: cand.routerInstanceCode,
				RouterModel:        cand.routerModel,
				Status:             route.Status,
				CreatedAt:          route.CreatedAt,
			}
			if err := h.recordAndFinalizeRuntime(ctx, apiKey, subscription, model, effectiveRoute, reservation, started, statusCode, body, publicModel); err != nil {
				respondError(w, http.StatusInternalServerError, "settlement_failed")
				return
			}
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(statusCode)
			_, _ = w.Write(body)
			return
		}

		lastResp = resp
		lastBody = body
		lastStatusCode = statusCode
	}

	if lastResp != nil {
		if err := h.recordAndFinalizeRuntime(ctx, apiKey, subscription, model, route, reservation, started, lastStatusCode, lastBody, publicModel); err != nil {
			respondError(w, http.StatusInternalServerError, "settlement_failed")
			return
		}
		for k, v := range lastResp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(lastStatusCode)
		_, _ = w.Write(lastBody)
		return
	}

	if err := h.finalizeRuntimeReservation(ctx, apiKey, reservation, usageSettlement{}, false); err != nil {
		respondError(w, http.StatusInternalServerError, "settlement_failed")
		return
	}
	respondError(w, http.StatusBadGateway, "all_candidates_failed")
}

func (h *GatewayHandler) CountMessageTokens(w http.ResponseWriter, r *http.Request) {
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

	subscription, err := h.entitlements.CheckActiveSubscription(ctx, apiKey.GenfityUserID)
	if err != nil {
		respondError(w, http.StatusPaymentRequired, err.Error())
		return
	}
	if !entitlementAllowsModel(subscription, publicModel) {
		respondError(w, http.StatusPaymentRequired, "model_not_in_unlimited_plan")
		return
	}

	route, _, err := h.models.ResolveRouteByPublicModel(ctx, publicModel)
	if err != nil {
		respondError(w, http.StatusBadRequest, "model_not_allowed")
		return
	}
	payload["model"] = route.RouterModel

	client, ok := h.clientForRouter(ctx, route.RouterInstanceCode)
	if !ok {
		respondError(w, http.StatusBadGateway, "router_unavailable")
		return
	}
	resp, err := client.CountMessageTokens(ctx, payload)
	if err != nil {
		respondError(w, http.StatusBadGateway, "upstream_error")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
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
	accountID := apiKey.GenfityUserID
	if h.rateLimit != nil && limits.HasConcurrency() {
		var acquireErr error
		release, acquireErr = h.rateLimit.AcquireConcurrency(ctx, accountID, limits.ConcurrentLimit)
		if acquireErr != nil {
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
	}
	defer release()

	reservation, statusCode, errorCode := h.reserveRuntimeLimits(ctx, apiKey, subscription, limits, payload, model)
	if statusCode != 0 {
		respondError(w, statusCode, errorCode)
		return
	}

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

	streamRequested := isStreamingPayload(payload)
	if streamRequested {
		ensureStreamUsageOption(payload)
	}
	var lastResp *http.Response
	var lastBody []byte
	var lastStatusCode int

	for _, cand := range candidates {
		p := clonePayload(payload)
		p["model"] = cand.routerModel

		client, ok := h.clientForRouter(ctx, cand.routerInstanceCode)
		if !ok {
			continue
		}
		resp, callErr := client.ChatCompletions(ctx, p)
		if callErr != nil {
			continue
		}

		statusCode := resp.StatusCode
		if streamRequested && statusCode < 400 {
			effectiveRoute := &store.AIModelRoute{
				ID:                 route.ID,
				ModelID:            route.ModelID,
				RouterInstanceCode: cand.routerInstanceCode,
				RouterModel:        cand.routerModel,
				Status:             route.Status,
				CreatedAt:          route.CreatedAt,
			}
			body := h.streamUpstreamResponse(w, resp)
			resp.Body.Close()
			_ = h.recordAndFinalizeRuntime(ctx, apiKey, subscription, model, effectiveRoute, reservation, started, statusCode, body, publicModel)
			return
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

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
			if err := h.recordAndFinalizeRuntime(ctx, apiKey, subscription, model, effectiveRoute, reservation, started, statusCode, body, publicModel); err != nil {
				respondError(w, http.StatusInternalServerError, "settlement_failed")
				return
			}
			h.writeUpstreamResponse(w, resp, body)
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
		if err := h.recordAndFinalizeRuntime(ctx, apiKey, subscription, model, effectiveRoute, reservation, started, lastStatusCode, lastBody, publicModel); err != nil {
			respondError(w, http.StatusInternalServerError, "settlement_failed")
			return
		}
		for k, v := range lastResp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(lastStatusCode)
		_, _ = w.Write(lastBody)
		return
	}

	if err := h.finalizeRuntimeReservation(ctx, apiKey, reservation, usageSettlement{}, false); err != nil {
		respondError(w, http.StatusInternalServerError, "settlement_failed")
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
	h.recordUsageWithLegacyBilling(ctx, apiKey, subscription, model, route, started, statusCode, body, publicModel)
}

func (h *GatewayHandler) recordUsage(ctx context.Context, apiKey store.APIKey, subscription *service.ActiveSubscription, model *store.AIModel, route *store.AIModelRoute, started time.Time, statusCode int, body []byte, publicModel string) (usageSettlement, error) {
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
	if _, err := h.usage.Record(ctx, entry); err != nil {
		return usageSettlement{}, err
	}

	return usageSettlement{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
		TotalCostUSD:     totalCostUsd,
	}, nil
}

func (h *GatewayHandler) recordUsageWithLegacyBilling(ctx context.Context, apiKey store.APIKey, subscription *service.ActiveSubscription, model *store.AIModel, route *store.AIModelRoute, started time.Time, statusCode int, body []byte, publicModel string) {
	settlement, err := h.recordUsage(ctx, apiKey, subscription, model, route, started, statusCode, body, publicModel)
	if err != nil {
		return
	}
	if statusCode < 400 && settlement.TotalTokens > 0 && subscription != nil && subscription.Entitlement != nil {
		periodStart, periodEnd := activePeriod(subscription.Entitlement)
		_ = h.usage.IncrementQuotaCounter(ctx, apiKey.GenfityUserID, apiKey.GenfityTenantID, periodStart, periodEnd, settlement.TotalTokens)
	}
	if statusCode < 400 && settlement.TotalCostUSD > 0 && pricingGroup(subscription) == "credit_package" && subscription != nil && subscription.Entitlement != nil {
		_ = h.usage.DebitCreditBalance(ctx, apiKey.GenfityUserID, subscription.Entitlement.PlanCode, settlement.TotalCostUSD)
	}
}
