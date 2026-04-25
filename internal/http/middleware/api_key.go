package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"

	"genfity-ai-gateway-service/internal/config"
	"genfity-ai-gateway-service/internal/service"
	"genfity-ai-gateway-service/internal/store"
)

type APIKeyMiddleware struct {
	cfg     *config.Config
	apiKeys *service.APIKeyService
}

func NewAPIKeyMiddleware(cfg *config.Config, apiKeys *service.APIKeyService) *APIKeyMiddleware {
	return &APIKeyMiddleware{cfg: cfg, apiKeys: apiKeys}
}

func (m *APIKeyMiddleware) RequireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			respondError(w, http.StatusUnauthorized, "missing_api_key")
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			respondError(w, http.StatusUnauthorized, "invalid_api_key_format")
			return
		}

		rawKey := parts[1]

		keyRecord, err := m.apiKeys.Validate(r.Context(), rawKey)
		if err != nil {
			if err == service.ErrNotFound {
				respondError(w, http.StatusUnauthorized, "invalid_api_key")
			} else {
				respondError(w, http.StatusInternalServerError, "internal_error")
			}
			return
		}

		ctx := context.WithValue(r.Context(), "api_key", *keyRecord)

		logger := hlog.FromRequest(r)
		logger.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.Str("api_key_id", keyRecord.ID.String()).Str("tenant_id", tenantString(keyRecord.GenfityTenantID))
		})

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func tenantString(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

func GetAPIKey(ctx context.Context) store.APIKey {
	val := ctx.Value("api_key")
	if val == nil {
		return store.APIKey{}
	}
	return val.(store.APIKey)
}
