package router

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpmiddleware "genfity-ai-gateway-service/internal/http/middleware"
)

type preconnectRetryTransport struct {
	calls      int
	failures   int
	lastHeader http.Header
}

func (t *preconnectRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls++
	t.lastHeader = req.Header.Clone()
	if t.calls <= t.failures {
		return nil, errors.New("dial tcp 127.0.0.1:8317: connect: connection refused")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Request:    req,
	}, nil
}

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

func TestForwardJSONRetriesOnlySafePreconnectFailures(t *testing.T) {
	transport := &preconnectRetryTransport{failures: 2}
	client := NewCLIProxyClient("http://cli-proxy-api:8317", "key", 5*time.Second)
	client.httpClient.Transport = transport

	resp, err := client.ChatCompletions(context.Background(), map[string]any{"model": "test"})
	if err != nil {
		t.Fatalf("forward after preconnect recovery: %v", err)
	}
	_ = resp.Body.Close()
	if transport.calls != 3 {
		t.Fatalf("round trips=%d want=3", transport.calls)
	}
	if transport.lastHeader.Get(requestBudgetHeader) != "5000" {
		t.Fatalf("request budget was not retained across retry")
	}

	unsafe := &preconnectRetryTransport{failures: 0}
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		unsafe.calls++
		return nil, io.ErrUnexpectedEOF
	})
	if _, err := client.ChatCompletions(context.Background(), map[string]any{"model": "test"}); err == nil {
		t.Fatal("expected unsafe transport failure")
	}
	if unsafe.calls != 1 {
		t.Fatalf("unsafe post-connect failure retried %d times", unsafe.calls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
