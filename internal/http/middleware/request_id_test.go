package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestIDPreservesHeaderAndContext(t *testing.T) {
	const requestID = "edge-request-123"
	var contextID string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextID = GetRequestID(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", requestID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if contextID != requestID || rec.Header().Get("X-Request-ID") != requestID {
		t.Fatalf("context=%q response=%q", contextID, rec.Header().Get("X-Request-ID"))
	}
}
