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

type NineRouterClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewNineRouterClient(baseURL, apiKey string, timeout time.Duration) *NineRouterClient {
	return &NineRouterClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (c *NineRouterClient) ListModels(ctx context.Context) (map[string]any, error) {
	return c.get(ctx, "/v1/models")
}

func (c *NineRouterClient) ListCombos(ctx context.Context) (map[string]any, error) {
	return c.get(ctx, "/api/combos")
}

func (c *NineRouterClient) CreateCombo(ctx context.Context, payload map[string]any) (map[string]any, error) {
	return c.sendJSON(ctx, http.MethodPost, "/api/combos", payload)
}

func (c *NineRouterClient) UpdateCombo(ctx context.Context, id string, payload map[string]any) (map[string]any, error) {
	return c.sendJSON(ctx, http.MethodPut, fmt.Sprintf("/api/combos/%s", id), payload)
}

func (c *NineRouterClient) DeleteCombo(ctx context.Context, id string) error {
	_, err := c.sendJSON(ctx, http.MethodDelete, fmt.Sprintf("/api/combos/%s", id), nil)
	return err
}

func (c *NineRouterClient) ListProviders(ctx context.Context) (map[string]any, error) {
	return c.get(ctx, "/api/providers")
}

func (c *NineRouterClient) RouterHealth(ctx context.Context) (map[string]any, error) {
	models, err := c.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"status": "ok",
		"models": models,
	}, nil
}

func (c *NineRouterClient) ListProviderModels(ctx context.Context, id string) (map[string]any, error) {
	return c.get(ctx, fmt.Sprintf("/api/providers/%s/models", id))
}

func (c *NineRouterClient) RawCombo(ctx context.Context, id string) (map[string]any, error) {
	return c.get(ctx, fmt.Sprintf("/api/combos/%s", id))
}

func (c *NineRouterClient) UpdateComboPartial(ctx context.Context, id string, payload map[string]any) (map[string]any, error) {
	return c.sendJSON(ctx, http.MethodPut, fmt.Sprintf("/api/combos/%s", id), payload)
}

func (c *NineRouterClient) RawGet(ctx context.Context, path string) (map[string]any, error) {
	return c.get(ctx, path)
}

func (c *NineRouterClient) RawSendJSON(ctx context.Context, method, path string, payload any) (map[string]any, error) {
	return c.sendJSON(ctx, method, path, payload)
}

func (c *NineRouterClient) RawDelete(ctx context.Context, path string) error {
	_, err := c.sendJSON(ctx, http.MethodDelete, path, nil)
	return err
}

func (c *NineRouterClient) Embeddings(ctx context.Context, payload map[string]any) (*http.Response, error) {
	return c.forwardJSON(ctx, "/v1/embeddings", payload)
}

func (c *NineRouterClient) ChatCompletions(ctx context.Context, payload map[string]any) (*http.Response, error) {
	return c.forwardJSON(ctx, "/v1/chat/completions", payload)
}

func (c *NineRouterClient) forwardJSON(ctx context.Context, path string, payload map[string]any) (*http.Response, error) {
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
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("router_error status=%d body=%s", resp.StatusCode, string(b))
	}
	return resp, nil
}

func (c *NineRouterClient) get(ctx context.Context, path string) (map[string]any, error) {
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
		return nil, fmt.Errorf("router_error status=%d body=%s", resp.StatusCode, string(b))
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *NineRouterClient) sendJSON(ctx context.Context, method, path string, payload any) (map[string]any, error) {
	var reader io.Reader
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
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
		return nil, fmt.Errorf("router_error status=%d body=%s", resp.StatusCode, string(b))
	}
	if resp.StatusCode == http.StatusNoContent {
		return map[string]any{"success": true}, nil
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
