package handler

import (
	"encoding/json"
	"net/http"

	"genfity-ai-gateway-service/internal/service"
)

type HealthHandler struct {
	sync *service.SyncService
}

func NewHealthHandler(sync *service.SyncService) *HealthHandler {
	return &HealthHandler{sync: sync}
}

func (h *HealthHandler) Check(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"service": "genfity-ai-gateway",
	})
}
