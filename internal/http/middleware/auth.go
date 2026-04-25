package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"

	"genfity-ai-gateway-service/internal/config"
	"genfity-ai-gateway-service/internal/store"
)

func claimString(claims jwt.MapClaims, keys ...string) string {
	for _, k := range keys {
		if v, ok := claims[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

type AuthMiddleware struct {
	cfg *config.Config
}

func NewAuthMiddleware(cfg *config.Config) *AuthMiddleware {
	return &AuthMiddleware{cfg: cfg}
}

func (m *AuthMiddleware) RequireRoles(allowedRoles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				respondError(w, http.StatusUnauthorized, "missing_token")
				return
			}

			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
				respondError(w, http.StatusUnauthorized, "invalid_token_format")
				return
			}

			tokenString := parts[1]

			// Parse JWT
			token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
				}
				return []byte(m.cfg.GenfityJWTSecret), nil
			})

			if err != nil || !token.Valid {
				respondError(w, http.StatusUnauthorized, "invalid_token")
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				respondError(w, http.StatusUnauthorized, "invalid_claims")
				return
			}

			userIDStr := claimString(claims, "id", "userId", "sub")
			email := claimString(claims, "email")
			role := claimString(claims, "role")
			sessionID := claimString(claims, "sessionId", "session_id")
			tenantIDStr := claimString(claims, "tenantId", "tenant_id")
			if userIDStr == "" {
				respondError(w, http.StatusUnauthorized, "invalid_user_id")
				return
			}

			var tenantID *string
			if tenantIDStr != "" {
				tenantID = &tenantIDStr
			}

			// Check Roles
			roleAllowed := false
			for _, allowed := range allowedRoles {
				if role == allowed {
					roleAllowed = true
					break
				}
			}

			if !roleAllowed {
				respondError(w, http.StatusForbidden, "forbidden")
				return
			}

			user := store.AuthUser{
				ID:        userIDStr,
				Email:     email,
				Role:      role,
				TenantID:  tenantID,
				SessionID: sessionID,
			}

			ctx := context.WithValue(r.Context(), "auth_user", user)

			// Add to logger
			logger := hlog.FromRequest(r)
			logger.UpdateContext(func(c zerolog.Context) zerolog.Context {
				return c.Str("user_id", userIDStr).Str("role", role)
			})

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func GetAuthUser(ctx context.Context) store.AuthUser {
	val := ctx.Value("auth_user")
	if val == nil {
		return store.AuthUser{}
	}
	return val.(store.AuthUser)
}
