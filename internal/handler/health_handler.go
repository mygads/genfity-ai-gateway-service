package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"genfity-ai-gateway-service/internal/service"
)

type HealthHandler struct {
	sync   *service.SyncService
	dbPool *pgxpool.Pool
	redis  *redis.Client
}

func NewHealthHandler(sync *service.SyncService, dbPool *pgxpool.Pool, redisClient *redis.Client) *HealthHandler {
	return &HealthHandler{sync: sync, dbPool: dbPool, redis: redisClient}
}

func (h *HealthHandler) Check(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	detailed := r.URL.Query().Get("detailed") == "true"
	if !detailed {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"service": "genfity-ai-gateway",
		})
		return
	}

	result := map[string]any{
		"status":  "ok",
		"service": "genfity-ai-gateway",
	}

	if h.dbPool != nil {
		stat := h.dbPool.Stat()
		result["db"] = map[string]any{
			"total_conns":    stat.TotalConns(),
			"idle_conns":     stat.IdleConns(),
			"acquired_conns": stat.AcquiredConns(),
			"max_conns":      stat.MaxConns(),
		}
	}

	if h.redis != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.redis.Ping(ctx).Err(); err != nil {
			result["redis"] = map[string]any{"status": "down", "error": err.Error()}
		} else {
			poolStats := h.redis.PoolStats()
			result["redis"] = map[string]any{
				"status":     "up",
				"total_conns": poolStats.TotalConns,
				"idle_conns":  poolStats.IdleConns,
				"stale_conns": poolStats.StaleConns,
				"hits":        poolStats.Hits,
				"misses":      poolStats.Misses,
			}
		}
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(result)
}
