package handler

import (
	"encoding/json"
	"net/http"
)

func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func publicErrorMessage(message string) string {
	switch message {
	case "missing_model",
		"model_not_allowed",
		"model_not_in_unlimited_plan",
		"subscription_not_covering_model",
		"subscription_inactive",
		"no_active_subscription",
		"quota_exceeded",
		"max_tokens_required",
		"credit_cost_not_configured",
		"payg_price_not_configured",
		"insufficient_credit_balance",
		"insufficient_balance",
		"quota_reservation_failed",
		"credit_reservation_failed",
		"payg_reservation_failed",
		"rate_limit_exceeded",
		"router_unavailable",
		"upstream_error",
		"all_candidates_failed",
		"settlement_failed",
		"internal_error",
		"service_unavailable":
		return customerGatewayBusyMessage
	default:
		return message
	}
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{
		"error":   "request_failed",
		"message": publicErrorMessage(message),
	})
}
