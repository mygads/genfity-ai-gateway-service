package middleware

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestLoggingAttachesRequestLogger(t *testing.T) {
	var output bytes.Buffer
	logger := zerolog.New(&output)
	var contextRequestID string

	handler := RequestID(Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextRequestID = GetRequestID(r.Context())
		zerolog.Ctx(r.Context()).Warn().Str("validation_code", "invalid_tool_arguments").Msg("rejected")
		w.WriteHeader(http.StatusBadRequest)
	})))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-Request-ID", "test-request-id")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if contextRequestID != "test-request-id" {
		t.Fatalf("context request ID=%q", contextRequestID)
	}
	logs := output.String()
	if !strings.Contains(logs, `"request_id":"test-request-id"`) {
		t.Fatalf("request ID missing from logs: %s", logs)
	}
	if !strings.Contains(logs, `"validation_code":"invalid_tool_arguments"`) {
		t.Fatalf("handler event missing from logs: %s", logs)
	}
}

func TestLoggingPreservesExistingContextValues(t *testing.T) {
	type contextKey struct{}
	logger := zerolog.Nop()
	handler := RequestID(Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Context().Value(contextKey{}); got != "kept" {
			t.Fatalf("context value=%v", got)
		}
	})))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req = req.WithContext(context.WithValue(req.Context(), contextKey{}, "kept"))
	handler.ServeHTTP(httptest.NewRecorder(), req)
}
