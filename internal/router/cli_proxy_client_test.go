package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpmiddleware "genfity-ai-gateway-service/internal/http/middleware"
)

func TestForwardJSONPropagatesRequestID(t *testing.T) {
	const requestID = "gateway-to-cliproxy-123"
	var upstreamRequestID string
	var upstreamBudget string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequestID = r.Header.Get("X-Request-ID")
		upstreamBudget = r.Header.Get(requestBudgetHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	client := NewCLIProxyClient(upstream.URL, "key", 5*time.Second)
	handler := httpmiddleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := client.ChatCompletions(r.Context(), map[string]any{"model": "test"})
		if err != nil {
			t.Errorf("forward: %v", err)
			return
		}
		_ = resp.Body.Close()
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Request-ID", requestID)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if upstreamRequestID != requestID {
		t.Fatalf("upstream request ID=%q want=%q", upstreamRequestID, requestID)
	}
	if upstreamBudget != "5000" {
		t.Fatalf("upstream request budget=%q want=5000", upstreamBudget)
	}
}
