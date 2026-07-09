package handler

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"genfity-ai-gateway-service/internal/service"
	"genfity-ai-gateway-service/internal/store"
)

type AdminHandler struct {
	models       *service.ModelService
	routers      *service.RouterService
	usage        *service.UsageService
	store        service.Store
	entitlements *service.EntitlementService
	rateLimit    *service.RateLimitService
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

func NewAdminHandler(models *service.ModelService, routers *service.RouterService, usage *service.UsageService, store service.Store, entitlements *service.EntitlementService, rateLimit *service.RateLimitService) *AdminHandler {
	return &AdminHandler{models: models, routers: routers, usage: usage, store: store, entitlements: entitlements, rateLimit: rateLimit}
}

func respondAdminWriteError(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			respondError(w, http.StatusConflict, "unique_violation")
		case "23503":
			respondError(w, http.StatusConflict, "fk_violation")
		case "23502":
			respondError(w, http.StatusBadRequest, "not_null_violation")
		case "23514":
			respondError(w, http.StatusBadRequest, "check_violation")
		case "22P02":
			respondError(w, http.StatusBadRequest, "invalid_text_representation")
		default:
			respondError(w, http.StatusInternalServerError, "database_error")
		}
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		respondError(w, http.StatusNotFound, "not_found")
		return
	}
	respondError(w, http.StatusBadRequest, "invalid_request")
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
		ModelType         *string `json:"model_type"`
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
	if payload.ModelType != nil {
		current.ModelType = *payload.ModelType
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

// ListUsageLogs is the per-request log feed for the admin "Logs" modal.
// Returns up to 1000 rows per page with offset pagination, enriched
// with the originating api key's prefix + name (so the UI can show
// "ai-xyz123 (My Personal Key)" without a second round trip).
func (h *AdminHandler) ListUsageLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}

	filter := store.UsageLogFilter{
		UserID:      q.Get("user_id"),
		Status:      q.Get("status"),
		BillingMode: q.Get("billing_mode"),
		PublicModel: q.Get("public_model"),
		Search:      q.Get("search"),
		Limit:       limit,
		Offset:      offset,
	}

	if v := q.Get("api_key_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			filter.APIKeyID = &id
		}
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.From = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.To = t
		}
	}

	rows, total, err := h.usage.ListLogs(r.Context(), filter)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "list_logs_failed")
		return
	}

	// Enrich with api key prefix + name. Gather unique IDs to avoid N
	// round trips for the same key.
	apiKeyMeta := map[uuid.UUID]map[string]any{}
	for _, row := range rows {
		if row.APIKeyID == nil {
			continue
		}
		id := *row.APIKeyID
		if _, ok := apiKeyMeta[id]; ok {
			continue
		}
		key, err := h.store.GetAPIKeyByID(r.Context(), id)
		if err != nil || key == nil {
			apiKeyMeta[id] = nil
			continue
		}
		apiKeyMeta[id] = map[string]any{
			"id":             key.ID.String(),
			"name":           key.Name,
			"key_prefix":     key.KeyPrefix,
			"billing_source": key.BillingSource,
		}
	}

	enriched := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		entry := map[string]any{
			"id":                          row.ID,
			"request_id":                  row.RequestID,
			"genfity_user_id":             row.GenfityUserID,
			"public_model":                row.PublicModel,
			"router_model":                row.RouterModel,
			"router_instance_code":        row.RouterInstanceCode,
			"prompt_tokens":               row.PromptTokens,
			"completion_tokens":           row.CompletionTokens,
			"total_tokens":                row.TotalTokens,
			"cached_tokens":               row.CachedTokens,
			"cache_read_input_tokens":     row.CacheReadInputTokens,
			"cache_creation_input_tokens": row.CacheCreationInputTokens,
			"reasoning_tokens":            row.ReasoningTokens,
			"input_cost":                  row.InputCost,
			"output_cost":                 row.OutputCost,
			"total_cost":                  row.TotalCost,
			"billing_mode":                row.BillingMode,
			"amount_credits":              row.AmountCredits,
			"balance_after_credits":       row.BalanceAfterCredits,
			"balance_after_usd":           row.BalanceAfterUsd,
			"status":                      row.Status,
			"error_code":                  row.ErrorCode,
			"latency_ms":                  row.LatencyMS,
			"started_at":                  row.StartedAt,
			"finished_at":                 row.FinishedAt,
		}
		if row.APIKeyID != nil {
			entry["api_key_id"] = row.APIKeyID.String()
			if meta := apiKeyMeta[*row.APIKeyID]; meta != nil {
				entry["api_key"] = meta
			}
		}
		// Surface pricing_group from metadata so the UI can label "subs"
		// vs "credit" vs "payg" without parsing the JSON blob.
		if len(row.Metadata) > 0 {
			var meta map[string]any
			if err := json.Unmarshal(row.Metadata, &meta); err == nil {
				if pg, ok := meta["pricing_group"].(string); ok {
					entry["pricing_group"] = pg
				}
				if planCode, ok := meta["plan_code"].(string); ok {
					entry["plan_code"] = planCode
				}
			}
		}
		enriched = append(enriched, entry)
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"data":   enriched,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func (h *AdminHandler) ListUsageDashboard(w http.ResponseWriter, r *http.Request) {
	rangeParam := r.URL.Query().Get("range")
	var since time.Time
	switch rangeParam {
	case "1d":
		since = time.Now().UTC().Add(-24 * time.Hour)
	case "7d":
		since = time.Now().UTC().Add(-7 * 24 * time.Hour)
	case "90d":
		since = time.Now().UTC().Add(-90 * 24 * time.Hour)
	case "all":
		// zero time = no filter
	default:
		since = time.Now().UTC().Add(-30 * 24 * time.Hour)
	}

	rows := h.usage.SummaryGrouped(r.Context(), since)

	type groupTotals struct {
		RequestCount int    `json:"request_count"`
		InputTokens  int64  `json:"input_tokens"`
		OutputTokens int64  `json:"output_tokens"`
		TotalTokens  int64  `json:"total_tokens"`
		TotalCost    string `json:"total_cost"`
	}
	type groupPayload struct {
		PricingGroup string                  `json:"pricing_group"`
		Label        string                  `json:"label"`
		Totals       groupTotals             `json:"totals"`
		Users        []store.UsageSummaryRow `json:"users"`
	}

	groupMap := map[string]*groupPayload{
		"unlimited":      {PricingGroup: "unlimited", Label: "Subscription", Users: []store.UsageSummaryRow{}},
		"unlimited_plan": {PricingGroup: "unlimited_plan", Label: "Subscription", Users: []store.UsageSummaryRow{}},
		"credit_package": {PricingGroup: "credit_package", Label: "Credit", Users: []store.UsageSummaryRow{}},
		"payg_topup":     {PricingGroup: "payg_topup", Label: "Pay-as-you-go", Users: []store.UsageSummaryRow{}},
		"unknown":        {PricingGroup: "unknown", Label: "Unknown", Users: []store.UsageSummaryRow{}},
	}

	var grandRequests int
	var grandInput, grandOutput, grandTotal int64
	grandCost := 0.0

	for _, row := range rows {
		g, ok := groupMap[row.PricingGroup]
		if !ok {
			g = groupMap["unknown"]
		}
		g.Users = append(g.Users, row)

		cost := 0.0
		if v, err := strconv.ParseFloat(row.TotalCost, 64); err == nil {
			cost = v
		}

		grandRequests += row.RequestCount
		grandInput += row.InputTokens
		grandOutput += row.OutputTokens
		grandTotal += row.TotalTokens
		grandCost += cost
	}

	// Merge unlimited + unlimited_plan into one group
	sub := groupMap["unlimited_plan"]
	if u := groupMap["unlimited"]; len(u.Users) > 0 {
		sub.Users = append(sub.Users, u.Users...)
	}

	// Compute per-group totals
	for _, g := range []*groupPayload{sub, groupMap["credit_package"], groupMap["payg_topup"], groupMap["unknown"]} {
		var rc int
		var it, ot, tt int64
		tc := 0.0
		for _, u := range g.Users {
			rc += u.RequestCount
			it += u.InputTokens
			ot += u.OutputTokens
			tt += u.TotalTokens
			if v, err := strconv.ParseFloat(u.TotalCost, 64); err == nil {
				tc += v
			}
		}
		g.Totals = groupTotals{
			RequestCount: rc,
			InputTokens:  it,
			OutputTokens: ot,
			TotalTokens:  tt,
			TotalCost:    strconv.FormatFloat(tc, 'f', 6, 64),
		}
	}

	// Fetch credit balances for credit_package users
	creditBalances := h.usage.CreditBalances(r.Context())
	type creditBalancePayload struct {
		GenfityUserID string `json:"genfity_user_id"`
		CreditBalance string `json:"credit_balance"`
		CreditUsed    string `json:"credit_used"`
	}
	cbList := make([]creditBalancePayload, 0, len(creditBalances))
	totalCreditBalance := 0.0
	totalCreditUsed := 0.0
	for _, cb := range creditBalances {
		cbList = append(cbList, creditBalancePayload{
			GenfityUserID: cb.GenfityUserID,
			CreditBalance: cb.CreditBalance,
			CreditUsed:    cb.CreditUsed,
		})
		if v, err := strconv.ParseFloat(cb.CreditBalance, 64); err == nil {
			totalCreditBalance += v
		}
		if v, err := strconv.ParseFloat(cb.CreditUsed, 64); err == nil {
			totalCreditUsed += v
		}
	}

	groups := []groupPayload{*sub, *groupMap["credit_package"], *groupMap["payg_topup"]}
	if unk := groupMap["unknown"]; len(unk.Users) > 0 {
		groups = append(groups, *unk)
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"grand_totals": groupTotals{
			RequestCount: grandRequests,
			InputTokens:  grandInput,
			OutputTokens: grandOutput,
			TotalTokens:  grandTotal,
			TotalCost:    strconv.FormatFloat(grandCost, 'f', 6, 64),
		},
		"groups": groups,
		"credit_summary": map[string]any{
			"total_balance":  strconv.FormatFloat(totalCreditBalance, 'f', 4, 64),
			"total_reserved": strconv.FormatFloat(totalCreditUsed, 'f', 4, 64),
			"user_balances":  cbList,
		},
	})
}

// entitlementRowPricingGroup resolves a raw entitlement row's pricing
// group the same way the persistence layer does — column first, then the
// metadata.pricingGroup fallback for legacy rows whose pricing_group
// column is NULL (the store queries use COALESCE for exactly this). Keeps
// the billing-detail credit/PAYG matching consistent with the SQL ordering
// and with billingDetailIsSubscription.
func entitlementRowPricingGroup(row store.CustomerEntitlement) string {
	if row.PricingGroup != nil && *row.PricingGroup != "" {
		return *row.PricingGroup
	}
	if len(row.Metadata) > 0 {
		var meta map[string]any
		if err := json.Unmarshal(row.Metadata, &meta); err == nil {
			if s, ok := meta["pricingGroup"].(string); ok {
				return s
			}
		}
	}
	return ""
}

// billingDetailIsSubscription reports whether the active entitlement is a
// real subscription plan (period-based / unlimited) rather than a
// credit_package or payg_topup row. CheckActiveSubscription returns the
// user's highest-priority active entitlement, which for a credit- or
// PAYG-only user is the credit/PAYG row itself. Rendering that as an
// "active subscription" in the admin modal is wrong — it shows a credit
// package (e.g. "Admin Grant Credit") with empty period and 0/∞ limits.
// Credit and PAYG have their own dedicated sections below.
func billingDetailIsSubscription(sub *service.ActiveSubscription) bool {
	group := resolveSubscriptionPricingGroup(sub)
	if group == "" {
		group = pricingGroup(sub)
	}
	switch group {
	case "credit_package", "payg_topup":
		return false
	default:
		return true
	}
}

// UserBillingDetail returns a single user's current billing posture for
// the admin "Detail" modal: active subscription (plan + RPM/RPD/RPP/TPM/
// concurrent/quota limits and live RPD/RPP usage), credit balance +
// expiry + today's credit usage, and PAYG balance + today's PAYG usage.
// Read-only: the Redis counter reads use GET, never INCR, so opening the
// modal cannot consume a user's quota.
func (h *AdminHandler) UserBillingDetail(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userId")
	if userID == "" {
		respondError(w, http.StatusBadRequest, "invalid_user_id")
		return
	}

	ctx := r.Context()

	// --- Subscription section ---
	subSection := map[string]any{"active": false}
	if sub, err := h.entitlements.CheckActiveSubscription(ctx, userID); err == nil && sub != nil && sub.Entitlement != nil && billingDetailIsSubscription(sub) {
		ent := sub.Entitlement
		s := map[string]any{
			"active":       true,
			"plan_code":    ent.PlanCode,
			"status":       ent.Status,
			"period_start": ent.PeriodStart,
			"period_end":   ent.PeriodEnd,
		}
		if ent.PeriodEnd != nil {
			days := int(time.Until(*ent.PeriodEnd).Hours() / 24)
			if rem := time.Until(*ent.PeriodEnd); rem > 0 && rem%(24*time.Hour) != 0 {
				days++ // ceil partial day so "expires in <24h" shows 1, not 0
			}
			if days < 0 {
				days = 0
			}
			s["days_remaining"] = days
		}
		limits := service.PlanLimitsFromSnapshot(sub.Plan)
		quotaTokensMonthly := quotaLimitPtr(sub)
		// Report the EFFECTIVE RPP cap (base × stacked-window multiplier),
		// matching runtime enforcement in applyPreRequestLimits and the
		// customer /subscription endpoint. base_rpp exposes the unscaled
		// plan value for reference.
		baseRPP := limits.MaxRequestsPerPeriod
		effectiveRPP := baseRPP * periodRPPMultiplier(sub)
		// Anti-abuse debt reserve mirrors applyPreRequestLimits: 75% of the
		// scaled base is held back to repay flagged overage, so usable_rpp is
		// what the user can actually spend this window.
		flaggedDebt, debtRemaining, _ := resolveAbuseDebt(sub)
		usableRPP := effectiveRPP
		if flaggedDebt && debtRemaining > 0 {
			debtReserve := int(math.Round(float64(baseRPP)*0.75)) * periodRPPMultiplier(sub)
			reserve := debtRemaining
			if reserve > debtReserve {
				reserve = debtReserve
			}
			usableRPP = effectiveRPP - reserve
			if usableRPP < 0 {
				usableRPP = 0
			}
		}
		s["limits"] = map[string]any{
			"rpm":                     limits.RPM,
			"tpm":                     limits.TPM,
			"rpd":                     limits.RPD,
			"rpp":                     effectiveRPP,
			"base_rpp":                baseRPP,
			"usable_rpp":              usableRPP,
			"credit_limit_per_day":    limits.CreditLimitPerDay,
			"credit_limit_per_period": limits.CreditLimitPerPeriod,
			"concurrent":              limits.ConcurrentLimit,
			"quota_tokens_monthly":    quotaTokensMonthly,
		}
		if sub.Plan != nil {
			s["plan_name"] = sub.Plan.DisplayName
		}
		usageSnapshot := collectSubscriptionUsageSnapshot(h.store.ListUsageByUser(ctx, userID), sub, h.rateLimit, h.usage, userID)
		s["usage"] = map[string]any{
			"rpd_used":           usageSnapshot.RPDUsed,
			"rpp_used":           usageSnapshot.RPPUsed,
			"rpm_used":           usageSnapshot.RPMUsed,
			"concurrent_used":    usageSnapshot.ConcurrentUsed,
			"period_tokens_used": usageSnapshot.PeriodTokensUsed,
			"credit_used_today":  usageSnapshot.CreditUsedToday,
			"credit_used_period": usageSnapshot.CreditUsedPeriod,
			"debt_flagged":       usageSnapshot.DebtFlagged,
			"debt_remaining":     usageSnapshot.DebtRemaining,
		}
		subSection = s
	}

	// --- Credit + PAYG balances (each lives on its own pricing_group row) ---
	creditSection := map[string]any{"balance": "0", "expires_at": nil}
	paygSection := map[string]any{"balance_usd": "0"}
	if rows, err := h.entitlements.ListActiveByUser(ctx, userID); err == nil {
		for i := range rows {
			switch entitlementRowPricingGroup(rows[i]) {
			case "credit_package":
				if rows[i].CreditBalance != nil {
					creditSection["balance"] = *rows[i].CreditBalance
				}
				creditSection["expires_at"] = rows[i].CreditExpiresAt
			case "payg_topup":
				if rows[i].PaygUsdBalance != nil {
					paygSection["balance_usd"] = *rows[i].PaygUsdBalance
				}
			}
		}
	}

	// --- Today usage by billing mode (UTC day, matches RPD reset window) ---
	now := time.Now().UTC()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for _, row := range h.usage.UsageByBillingModeSince(ctx, userID, startOfDay) {
		switch row.BillingMode {
		case "credit_package":
			creditSection["requests_today"] = row.RequestCount
			creditSection["credits_used_today"] = row.CreditsUsed
			creditSection["input_tokens_today"] = row.InputTokens
			creditSection["output_tokens_today"] = row.OutputTokens
		case "payg_topup":
			paygSection["requests_today"] = row.RequestCount
			paygSection["cost_today"] = row.TotalCost
			paygSection["input_tokens_today"] = row.InputTokens
			paygSection["output_tokens_today"] = row.OutputTokens
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"user_id":      userID,
		"subscription": subSection,
		"credit":       creditSection,
		"payg":         paygSection,
	})
}

// AdjustUserUsage lets an admin set/add/subtract a user's live RPD or RPP
// usage counter (Redis), without touching the plan's configured caps. Use
// cases: reset a user blocked by plan_rpd_exceeded/plan_period_limit_exceeded,
// or compensate by lowering their consumed count. The customer dashboard
// and the admin billing-detail modal both read these counters live, so an
// adjustment is reflected everywhere on the next read.
//
// POST /admin/users/{userId}/usage-adjust
//
//	{ "counter": "rpd"|"rpp", "mode": "set"|"add"|"subtract", "value": <int >= 0> }
func (h *AdminHandler) AdjustUserUsage(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userId")
	if userID == "" {
		respondError(w, http.StatusBadRequest, "invalid_user_id")
		return
	}
	if h.rateLimit == nil {
		respondError(w, http.StatusServiceUnavailable, "rate_limit_unavailable")
		return
	}

	var body struct {
		Counter string `json:"counter"`
		Mode    string `json:"mode"`
		Value   int    `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if body.Counter != "rpd" && body.Counter != "rpp" && body.Counter != "tokens" {
		respondError(w, http.StatusBadRequest, "counter must be 'rpd', 'rpp', or 'tokens'")
		return
	}
	if body.Mode != "set" && body.Mode != "add" && body.Mode != "subtract" {
		respondError(w, http.StatusBadRequest, "mode must be 'set', 'add', or 'subtract'")
		return
	}
	if body.Value < 0 {
		respondError(w, http.StatusBadRequest, "value must be >= 0")
		return
	}

	ctx := r.Context()
	sub, err := h.entitlements.CheckActiveSubscription(ctx, userID)
	if err != nil || sub == nil || sub.Entitlement == nil || sub.Plan == nil || !billingDetailIsSubscription(sub) {
		respondError(w, http.StatusNotFound, "no_active_subscription")
		return
	}

	// target computes the new counter value from the current one per mode.
	target := func(current int) int {
		switch body.Mode {
		case "set":
			return body.Value
		case "add":
			return current + body.Value
		case "subtract":
			if current-body.Value < 0 {
				return 0
			}
			return current - body.Value
		}
		return current
	}

	var previous, newValue int
	switch body.Counter {
	case "rpd":
		planCode := sub.Plan.PlanCode
		previous = h.rateLimit.GetPlanRPDCount(ctx, userID, planCode)
		newValue = target(previous)
		if err := h.rateLimit.SetPlanRPD(ctx, userID, planCode, newValue); err != nil {
			respondError(w, http.StatusInternalServerError, "failed_to_set_rpd")
			return
		}
	case "rpp":
		pk := periodKey(sub.Entitlement)
		_, end := activePeriod(sub.Entitlement)
		ttl := time.Until(end)
		if ttl <= 0 {
			ttl = 24 * time.Hour
		}
		previous = h.rateLimit.GetRequestsPerPeriodCount(ctx, userID, pk)
		newValue = target(previous)
		if err := h.rateLimit.SetRequestsPerPeriod(ctx, userID, pk, ttl, newValue); err != nil {
			respondError(w, http.StatusInternalServerError, "failed_to_set_rpp")
			return
		}
	case "tokens":
		// Adjust the durable per-period token usage counter
		// (quota_counters.tokens_used). Enforced against the plan's token
		// quota on the next request. Same (period_start, period_end) key the
		// reserve/finalize path uses, so the change is picked up live.
		periodStart, periodEnd := activePeriod(sub.Entitlement)
		var prevTokens int64
		if qc, qErr := h.usage.GetQuotaCounter(ctx, userID, periodStart, periodEnd); qErr == nil && qc != nil {
			prevTokens = qc.TokensUsed
		}
		newTokens := prevTokens
		switch body.Mode {
		case "set":
			newTokens = int64(body.Value)
		case "add":
			newTokens = prevTokens + int64(body.Value)
		case "subtract":
			newTokens = prevTokens - int64(body.Value)
			if newTokens < 0 {
				newTokens = 0
			}
		}
		if err := h.usage.SetQuotaTokensUsed(ctx, userID, periodStart, periodEnd, newTokens); err != nil {
			respondError(w, http.StatusInternalServerError, "failed_to_set_tokens")
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"user_id":   userID,
			"counter":   body.Counter,
			"mode":      body.Mode,
			"previous":  prevTokens,
			"new_value": newTokens,
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"user_id":   userID,
		"counter":   body.Counter,
		"mode":      body.Mode,
		"previous":  previous,
		"new_value": newValue,
	})
}


// ListUsageAnalytics returns aggregated time-series and breakdowns for
// the admin usage page charts. One round-trip surfaces:
//
//	timeseries          — request/token/cost time buckets (hour/day)
//	top_models          — public_model leaderboard sorted by total_cost
//	billing_modes       — billing_mode pie/donut data
//	status_breakdown    — success vs error counts (donut)
//	error_codes         — top error_code values (bar)
//	providers           — router_model prefix volume + error rate
//	latency             — avg + p50/p95/p99 over the same window
//
// Rationale for the single endpoint: each individual aggregation is
// cheap (uses the (started_at) index) and the admin page renders them
// together, so issuing one request keeps the UI loading cleaner and
// avoids 7× the round-trips on a slow connection.
func (h *AdminHandler) ListUsageAnalytics(w http.ResponseWriter, r *http.Request) {
	rangeParam := r.URL.Query().Get("range")
	var since time.Time
	bucket := "day"
	switch rangeParam {
	case "1d":
		since = time.Now().UTC().Add(-24 * time.Hour)
		bucket = "hour"
	case "7d":
		since = time.Now().UTC().Add(-7 * 24 * time.Hour)
	case "90d":
		since = time.Now().UTC().Add(-90 * 24 * time.Hour)
	case "all":
		// zero time = no filter, default daily bucket
	default:
		// 30d default
		since = time.Now().UTC().Add(-30 * 24 * time.Hour)
	}

	limit := 10
	if v := r.URL.Query().Get("top_limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"range":            rangeParam,
		"since":            since,
		"bucket":           bucket,
		"timeseries":       h.usage.Timeseries(r.Context(), since, bucket),
		"top_models":       h.usage.TopModels(r.Context(), since, limit),
		"billing_modes":    h.usage.BillingModeBreakdown(r.Context(), since),
		"status_breakdown": h.usage.StatusBreakdown(r.Context(), since),
		"error_codes":      h.usage.ErrorCodeBreakdown(r.Context(), since, limit),
		"providers":        h.usage.ProviderStats(r.Context(), since),
		"latency":          h.usage.LatencyStats(r.Context(), since),
	})
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
