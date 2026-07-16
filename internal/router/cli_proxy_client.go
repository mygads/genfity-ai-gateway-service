package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	httpmiddleware "genfity-ai-gateway-service/internal/http/middleware"
	"golang.org/x/net/http2"
)

// CLIProxyClient talks to a CLIProxyAPI-compatible upstream.
// It covers only the OpenAI-compatible runtime endpoints that
// genfity-ai-gateway-service needs; all provider/auth configuration
// is handled directly inside the CLIProxyAPI web panel.
type CLIProxyClient struct {
	baseURL       string
	apiKey        string
	managementKey string
	httpClient    *http.Client
}

const requestBudgetHeader = "X-Genfity-Request-Budget-Ms"

// CLIProxy performs a graceful drain before Docker replaces the container.
// During a busy rollout the service name can reject new connections for
// roughly a minute even though in-flight requests are being protected. Keep
// safe pre-connect retries alive across that drain window; the request context
// still cancels immediately when the downstream client leaves.
const safePreconnectMaxAttempts = 80

func NewCLIProxyClient(baseURL, apiKey string, timeout time.Duration) *CLIProxyClient {
	return NewCLIProxyClientWithManagementKey(baseURL, apiKey, "", timeout)
}

func NewCLIProxyClientWithManagementKey(baseURL, apiKey, managementKey string, timeout time.Duration) *CLIProxyClient {
	// Pool tuned for high-concurrency forwarding to a single upstream.
	// Default Go transport caps MaxIdleConnsPerHost at 2 — under load
	// every request beyond that pays a fresh TCP+TLS handshake. We bump
	// per-host to 100 to keep a warm pool, and opt into HTTP/2 (with
	// automatic H1 fallback) for multiplexed streams to cli-proxy-api.
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	// ConfigureTransport upgrades the transport to support HTTP/2 over
	// TLS where the upstream advertises it. For plaintext H2 (h2c) we
	// rely on ForceAttemptHTTP2 + ALPN. Errors here are non-fatal — the
	// transport stays valid for HTTP/1.1.
	_ = http2.ConfigureTransport(transport)

	return &CLIProxyClient{
		baseURL:       strings.TrimRight(baseURL, "/"),
		apiKey:        apiKey,
		managementKey: managementKey,
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
	}
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

// Messages forwards an Anthropic-compatible messages request.
func (c *CLIProxyClient) Messages(ctx context.Context, payload map[string]any) (*http.Response, error) {
	return c.forwardJSON(ctx, "/v1/messages", payload)
}

// CountMessageTokens forwards an Anthropic-compatible token-count request.
func (c *CLIProxyClient) CountMessageTokens(ctx context.Context, payload map[string]any) (*http.Response, error) {
	return c.forwardJSON(ctx, "/v1/messages/count_tokens", payload)
}

// Embeddings forwards an embeddings request.
func (c *CLIProxyClient) Embeddings(ctx context.Context, payload map[string]any) (*http.Response, error) {
	return c.forwardJSON(ctx, "/v1/embeddings", payload)
}

// ImagesGenerations forwards an image generation request.
func (c *CLIProxyClient) ImagesGenerations(ctx context.Context, payload map[string]any) (*http.Response, error) {
	return c.forwardJSON(ctx, "/v1/images/generations", payload)
}

// ImagesEdits forwards an image edit request.
func (c *CLIProxyClient) ImagesEdits(ctx context.Context, payload map[string]any) (*http.Response, error) {
	return c.forwardJSON(ctx, "/v1/images/edits", payload)
}

// ListAuthFiles fetches /v0/management/auth-files using the management key.
// Returns the raw JSON map containing the "files" array.
func (c *CLIProxyClient) ListAuthFiles(ctx context.Context) (map[string]any, error) {
	return c.managementGet(ctx, "/v0/management/auth-files")
}

// GetGithubQuota fetches GitHub Copilot quota for a specific auth_index.
func (c *CLIProxyClient) GetGithubQuota(ctx context.Context, authIndex string) (map[string]any, error) {
	return c.managementGet(ctx, "/v0/management/github-quota?auth_index="+authIndex)
}

// GetKiroQuota fetches Kiro quota for a specific auth_index.
func (c *CLIProxyClient) GetKiroQuota(ctx context.Context, authIndex string) (map[string]any, error) {
	return c.managementGet(ctx, "/v0/management/kiro-quota?auth_index="+authIndex)
}

// managementGet calls a CLIProxyAPI management endpoint with the management key.
// Returns "management_key_not_configured" error when the key is missing.
func (c *CLIProxyClient) managementGet(ctx context.Context, path string) (map[string]any, error) {
	if c.managementKey == "" {
		return nil, fmt.Errorf("management_key_not_configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.managementKey)
	resp, err := c.doWithRetry(req, 1)
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

// forwardJSON marshals payload, sends the request, and returns the raw
// http.Response WITHOUT closing the body – callers are responsible for that.
func (c *CLIProxyClient) forwardJSON(ctx context.Context, path string, payload map[string]any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < safePreconnectMaxAttempts; attempt++ {
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
		if requestErr != nil {
			return nil, requestErr
		}
		req.Header.Set("Content-Type", "application/json")
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		if requestID := httpmiddleware.GetRequestID(ctx); requestID != "" {
			req.Header.Set("X-Request-ID", requestID)
		}
		if timeout := c.httpClient.Timeout; timeout > 0 {
			req.Header.Set(requestBudgetHeader, fmt.Sprintf("%d", timeout.Milliseconds()))
		}
		resp, callErr := c.httpClient.Do(req)
		if callErr == nil || !isSafePreconnectError(callErr) || attempt == safePreconnectMaxAttempts-1 {
			return resp, callErr
		}

		delay := safePreconnectRetryDelay(attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, fmt.Errorf("preconnect retry loop exhausted")
}

func safePreconnectRetryDelay(attempt int) time.Duration {
	delay := 250 * time.Millisecond
	for i := 0; i < attempt && delay < time.Second; i++ {
		delay *= 2
	}
	if delay > time.Second {
		return time.Second
	}
	return delay
}

// isSafePreconnectError is intentionally narrower than isRetriableError.
// These failures prove that no HTTP request reached CLIProxy, so retrying
// cannot double-bill a provider. EOF, reset, broken pipe, and response timeout
// are deliberately excluded because the upstream may already be processing.
func isSafePreconnectError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connection refused") ||
		strings.Contains(message, "no such host") ||
		strings.Contains(message, "server misbehaving") ||
		strings.Contains(message, "network is unreachable") ||
		strings.Contains(message, "temporary failure in name resolution")
}

func (c *CLIProxyClient) get(ctx context.Context, path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.doWithRetry(req, 2)
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

func (c *CLIProxyClient) doWithRetry(req *http.Request, maxRetries int) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(time.Duration(attempt*100) * time.Millisecond):
			}
			req.Body = io.NopCloser(bytes.NewReader(nil))
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if isRetriableError(err) {
				continue
			}
			return nil, err
		}
		if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout {
			resp.Body.Close()
			lastErr = fmt.Errorf("upstream returned %d", resp.StatusCode)
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

func isRetriableError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "no such host")
}
