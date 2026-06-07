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
	// Billing / entitlement errors — tell the customer what's actually wrong.
	case "insufficient_credit_balance", "insufficient_balance",
		"credit_reservation_failed", "payg_reservation_failed":
		return "Insufficient balance. Please top up your credits or upgrade your plan."
	case "no_active_subscription", "subscription_inactive":
		return "No active subscription. Please subscribe or renew your plan."
	case "quota_exceeded", "quota_reservation_failed":
		return "Your usage quota has been exceeded. Please upgrade or wait for the next billing period."
	case "model_not_in_unlimited_plan", "subscription_not_covering_model":
		return "Your current plan does not include this model. Please upgrade your plan."
	case "rate_limit_exceeded":
		return "Rate limit exceeded. Please slow down and try again shortly."
	case "credit_cost_not_configured", "payg_price_not_configured":
		return "This model is not available for your billing type. Please contact support."
	// Model/request validation — actionable but generic.
	case "missing_model", "model_not_allowed", "max_tokens_required":
		return "Invalid request. Please check your model name and parameters."
	// Infrastructure / upstream — genuinely opaque to the customer.
	case "router_unavailable", "upstream_error", "all_candidates_failed",
		"settlement_failed", "internal_error", "service_unavailable":
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
