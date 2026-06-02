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
	case "router_unavailable",
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
