package middleware

import (
	"crypto/subtle"
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
		configured := strings.TrimSpace(m.cfg.GenfityInternalSecret)
		if configured == "" || provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(configured)) != 1 {
			respondError(w, http.StatusUnauthorized, "invalid_internal_secret")
			return
		}
		next.ServeHTTP(w, r)
	})
}
