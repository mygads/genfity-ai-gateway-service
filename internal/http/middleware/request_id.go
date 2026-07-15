package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type requestIDContextKey struct{}

// GetRequestID returns the stable edge request ID attached by RequestID.
// The same value is used by the usage ledger and forwarded to CLIProxyAPI so
// operators can correlate a public request without retaining its prompt.
func GetRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return strings.TrimSpace(requestID)
}

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" || len(requestID) > 128 {
			requestID = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", requestID)
		r.Header.Set("X-Request-ID", requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
