package middleware

import (
	"net/http"
	"strings"

	"genfity-ai-gateway-service/internal/config"
)

type InternalMiddleware struct {
	cfg *config.Config
}

func NewInternalMiddleware(cfg *config.Config) *InternalMiddleware {
	return &InternalMiddleware{cfg: cfg}
}

func (m *InternalMiddleware) RequireInternalSecret(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimSpace(r.Header.Get("X-Internal-Secret"))
		if provided == "" || provided != m.cfg.GenfityInternalSecret {
			respondError(w, http.StatusUnauthorized, "invalid_internal_secret")
			return
		}
		next.ServeHTTP(w, r)
	})
}
