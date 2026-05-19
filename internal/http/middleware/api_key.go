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

type ctxKey int

const apiKeyCtxKey ctxKey = iota

func NewAPIKeyMiddleware(cfg *config.Config, apiKeys *service.APIKeyService) *APIKeyMiddleware {
	return &APIKeyMiddleware{cfg: cfg, apiKeys: apiKeys}
}

func (m *APIKeyMiddleware) RequireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Accept either:
		//   1. Authorization: Bearer <key>   (OpenAI-style, our native scheme)
		//   2. x-api-key: <key>              (Anthropic SDK convention)
		//   3. api-key: <key>                (Azure OpenAI convention; harmless fallback)
		// We support multiple headers so an Anthropic-native client (Claude
		// Code, anthropic-sdk-python, anthropic-sdk-typescript) can hit our
		// /v1/messages endpoint without rewriting auth headers.
		var rawKey string
		if h := r.Header.Get("Authorization"); h != "" {
			parts := strings.SplitN(h, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
				rawKey = strings.TrimSpace(parts[1])
			} else if len(parts) == 1 {
				// Some clients send the raw key without the Bearer prefix.
				rawKey = strings.TrimSpace(parts[0])
			} else {
				respondError(w, http.StatusUnauthorized, "invalid_api_key_format")
				return
			}
		}
		if rawKey == "" {
			rawKey = strings.TrimSpace(r.Header.Get("x-api-key"))
		}
		if rawKey == "" {
			rawKey = strings.TrimSpace(r.Header.Get("X-Api-Key"))
		}
		if rawKey == "" {
			rawKey = strings.TrimSpace(r.Header.Get("api-key"))
		}
		if rawKey == "" {
			respondError(w, http.StatusUnauthorized, "missing_api_key")
			return
		}

		keyRecord, err := m.apiKeys.Validate(r.Context(), rawKey)
		if err != nil {
			if err == service.ErrNotFound {
				respondError(w, http.StatusUnauthorized, "invalid_api_key")
			} else {
				respondError(w, http.StatusInternalServerError, "internal_error")
			}
			return
		}

		ctx := context.WithValue(r.Context(), apiKeyCtxKey, *keyRecord)

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
	val := ctx.Value(apiKeyCtxKey)
	if val == nil {
		return store.APIKey{}
	}
	return val.(store.APIKey)
}
