package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"genfity-ai-gateway-service/internal/router"
	"genfity-ai-gateway-service/internal/service"
	"genfity-ai-gateway-service/internal/store"
)

// MonitoringHandler exposes admin endpoints that proxy CLIProxyAPI
// management API for OAuth account introspection, plus aggregated
// provider request stats from usage_ledger.
type MonitoringHandler struct {
	cliProxy *router.CLIProxyClient
	usage    *service.UsageService
	repo     service.Store
}

func NewMonitoringHandler(cliProxy *router.CLIProxyClient, usage *service.UsageService, repo service.Store) *MonitoringHandler {
	return &MonitoringHandler{cliProxy: cliProxy, usage: usage, repo: repo}
}

// ListOAuthAccounts proxies CLIProxyAPI /v0/management/auth-files.
// Returns {"files":[{ ... }]} as-is so the UI can read fields like
// status, email, account_type, last_refresh, etc.
func (h *MonitoringHandler) ListOAuthAccounts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	data, err := h.cliProxy.ListAuthFiles(ctx)
	if err != nil {
		if err.Error() == "management_key_not_configured" {
			respondError(w, http.StatusServiceUnavailable, "AI_ROUTER_CORE2_MANAGEMENT_KEY not configured")
			return
		}
		respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, data)
}

// GetOAuthAccountQuota dispatches to the right CLIProxyAPI quota endpoint
// based on the provider query param. Currently supports github and kiro.
// Other providers return 501 (not_implemented) so the UI can degrade.
func (h *MonitoringHandler) GetOAuthAccountQuota(w http.ResponseWriter, r *http.Request) {
	authIndex := chi.URLParam(r, "authIndex")
	if authIndex == "" {
		respondError(w, http.StatusBadRequest, "missing auth_index")
		return
	}
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		respondError(w, http.StatusBadRequest, "missing provider")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var (
		data map[string]any
		err  error
	)
	switch provider {
	case "github", "github-copilot", "copilot":
		data, err = h.cliProxy.GetGithubQuota(ctx, authIndex)
	case "kiro":
		data, err = h.cliProxy.GetKiroQuota(ctx, authIndex)
	default:
		respondJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "quota_not_supported",
			"message":  "Quota endpoint not available for provider",
			"provider": provider,
		})
		return
	}
	if err != nil {
		if err.Error() == "management_key_not_configured" {
			respondError(w, http.StatusServiceUnavailable, "AI_ROUTER_CORE2_MANAGEMENT_KEY not configured")
			return
		}
		respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, data)
}

// GetProviderStats returns request counts grouped by router_model prefix
// for the given time window. Window is "1h" or "1d" (defaults to 1d).
func (h *MonitoringHandler) GetProviderStats(w http.ResponseWriter, r *http.Request) {
	window := r.URL.Query().Get("window")
	since := time.Now().UTC().Add(-24 * time.Hour)
	switch window {
	case "1h":
		since = time.Now().UTC().Add(-1 * time.Hour)
	case "1d", "":
		since = time.Now().UTC().Add(-24 * time.Hour)
	}

	rows := h.repo.ListProviderStats(r.Context(), since)

	var totalReq, totalSuccess, totalError int64
	for _, row := range rows {
		totalReq += row.TotalCount
		totalSuccess += row.SuccessCount
		totalError += row.ErrorCount
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"window":         window,
		"since":          since,
		"total_requests": totalReq,
		"total_success":  totalSuccess,
		"total_errors":   totalError,
		"by_prefix":      rows,
		"by_provider":    mapPrefixesToProviders(rows),
	})
}

// mapPrefixesToProviders aggregates prefix-level stats into upstream-provider
// buckets. Mapping is best-effort and reflects CLIProxyAPI router conventions.
//
//	mtr  → mid-tier (OpenAI/Anthropic/Google direct)
//	cc   → claude-code (Anthropic OAuth)
//	kr   → kiro (AWS Bedrock OAuth)
//	xm   → xmind/extra-mtr
//	tc   → token-cycle/test-channel
//	free → free tier
//	gen  → generic/test
//	genfity → genfity combo (multi-provider fallback)
func mapPrefixesToProviders(rows []store.ProviderStatsRow) []store.ProviderStatsRow {
	mapping := map[string]string{
		"mtr":     "OpenAI/Anthropic/Google (direct)",
		"cc":      "Claude Code (Anthropic OAuth)",
		"kr":      "Kiro (AWS Bedrock)",
		"xm":      "Extra Router",
		"tc":      "Token Cycle",
		"free":    "Free Tier",
		"gen":     "Test/Generic",
		"genfity": "Genfity Combo",
	}
	bucket := map[string]*store.ProviderStatsRow{}
	for _, row := range rows {
		name, ok := mapping[row.Prefix]
		if !ok {
			name = row.Prefix
		}
		if existing, found := bucket[name]; found {
			existing.TotalCount += row.TotalCount
			existing.SuccessCount += row.SuccessCount
			existing.ErrorCount += row.ErrorCount
		} else {
			bucket[name] = &store.ProviderStatsRow{
				Prefix:       name,
				TotalCount:   row.TotalCount,
				SuccessCount: row.SuccessCount,
				ErrorCount:   row.ErrorCount,
			}
		}
	}
	out := make([]store.ProviderStatsRow, 0, len(bucket))
	for _, v := range bucket {
		out = append(out, *v)
	}
	return out
}
