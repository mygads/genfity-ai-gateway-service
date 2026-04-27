package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CLIProxyClient talks to a CLIProxyAPI-compatible upstream.
// It covers only the OpenAI-compatible runtime endpoints that
// genfity-ai-gateway-service needs; all provider/auth configuration
// is handled directly inside the CLIProxyAPI web panel.
type CLIProxyClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NineRouterClient is a backward-compat alias kept so that nothing
// outside this package needs to change before the rename is complete.
// Deprecated: use CLIProxyClient directly.
type NineRouterClient = CLIProxyClient

func NewCLIProxyClient(baseURL, apiKey string, timeout time.Duration) *CLIProxyClient {
	return &CLIProxyClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: timeout},
	}
}

// NewNineRouterClient is a backward-compat constructor.
// Deprecated: use NewCLIProxyClient.
func NewNineRouterClient(baseURL, apiKey string, timeout time.Duration) *CLIProxyClient {
	return NewCLIProxyClient(baseURL, apiKey, timeout)
}

// ListModels fetches /v1/models from CLIProxyAPI.
func (c *CLIProxyClient) ListModels(ctx context.Context) (map[string]any, error) {
	return c.get(ctx, "/v1/models")
}

// RouterHealth returns a health summary based on the upstream model list.
func (c *CLIProxyClient) RouterHealth(ctx context.Context) (map[string]any, error) {
	models, err := c.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"status": "ok",
		"models": models,
	}, nil
}

// ChatCompletions forwards a chat-completion request and streams the response back.
func (c *CLIProxyClient) ChatCompletions(ctx context.Context, payload map[string]any) (*http.Response, error) {
	return c.forwardJSON(ctx, "/v1/chat/completions", payload)
}

// Embeddings forwards an embeddings request.
func (c *CLIProxyClient) Embeddings(ctx context.Context, payload map[string]any) (*http.Response, error) {
	return c.forwardJSON(ctx, "/v1/embeddings", payload)
}

// forwardJSON marshals payload, sends the request, and returns the raw
// http.Response WITHOUT closing the body – callers are responsible for that.
func (c *CLIProxyClient) forwardJSON(ctx context.Context, path string, payload map[string]any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return c.httpClient.Do(req)
}

func (c *CLIProxyClient) get(ctx context.Context, path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream_error status=%d body=%s", resp.StatusCode, string(b))
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
