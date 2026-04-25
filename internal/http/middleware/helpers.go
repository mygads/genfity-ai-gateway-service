package middleware

import (
	"encoding/json"
	"net/http"
)

func respondError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error":   "request_failed",
		"message": message,
	})
}
