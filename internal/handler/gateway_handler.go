package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"genfity-ai-gateway-service/internal/http/middleware"
	"genfity-ai-gateway-service/internal/router"
	"genfity-ai-gateway-service/internal/service"
	"genfity-ai-gateway-service/internal/store"
)

// --- entitlement helpers ---

// resolveSubscription returns an active subscription if one exists, or
// nil when the user only has credit/payg entitlements. Callers must not
// treat a nil subscription as an error — credit-package and payg_topup
// users intentionally have no unlimited subscription. The priority
// billing chain in reserveRuntimeLimits handles credit/payg paths even
// when subscription is nil.
//
// Real errors (DB failure etc.) still bubble up. ErrNotFound is
// swallowed because that's the legitimate "no subscription, but maybe
// they have credits" case.
func (h *GatewayHandler) resolveSubscription(ctx context.Context, userID string) (*service.ActiveSubscription, error) {
	sub, err := h.entitlements.CheckActiveSubscription(ctx, userID)
	if err == nil {
		return sub, nil
	}
	if errors.Is(err, service.ErrNotFound) {
		return nil, nil
	}
	if msg := err.Error(); msg == "subscription_inactive" {
		return nil, nil
	}
	return nil, err
}

func subscriptionPlan(sub *service.ActiveSubscription) *store.SubscriptionPlanSnapshot {
	if sub == nil {
		return nil
	}
	return sub.Plan
}

func shouldEnforceUnlimitedAllowlist(apiKey store.APIKey) bool {
	source := apiKey.BillingSource
	if source == "" {
		source = "subscription"
	}
	return source == "subscription"
}

// isUnlimitedSubscription returns true when the active subscription is on
// an unlimited plan, using the live-policy chain: live plan snapshot first,
// entitlement column / metadata as fallback. Admin flipping a plan's
// pricingGroup propagates to existing subscribers next request.
func isUnlimitedSubscription(subscription *service.ActiveSubscription) bool {
	if subscription == nil || subscription.Entitlement == nil {
		return false
	}
	group := resolveSubscriptionPricingGroup(subscription)
	if group == "" {
		group = pricingGroup(subscription)
	}
	return group == "unlimited" || group == "unlimited_plan"
}

func entitlementAllowsModel(entitlement any, publicModel string) bool {
	typed, ok := entitlement.(*service.ActiveSubscription)
	if !ok || typed == nil || typed.Entitlement == nil {
		return false
	}
	// Use the live-policy chain so plan edits propagate without
	// re-syncing every user's entitlement. Falls back to entitlement
	// metadata only when plan snapshot doesn't define allowedModels.
	group := resolveSubscriptionPricingGroup(typed)
	if group != "unlimited_plan" && group != "unlimited" {
		// Non-unlimited plans don't enforce an allowlist at this gate.
		return true
	}
	allowed := resolveAllowedModels(typed)
	if len(allowed) == 0 {
		// Permissive default for legacy unlimited plans without an
		// explicit allowlist (matches modelCoveredByUnlimited).
		return true
	}
	for _, modelName := range allowed {
		if strings.EqualFold(modelName, publicModel) {
			return true
		}
		if !strings.Contains(publicModel, "/") && strings.EqualFold(modelName, "genfity/"+publicModel) {
			return true
		}
	}
	return false
}

// mapSubscriptionError converts an entitlement/subscription check error to a
// stable client-facing code. Internal details are never leaked to the caller.
func mapSubscriptionError(ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, service.ErrNotFound) {
		return "subscription_inactive"
	}
	msg := err.Error()
	switch msg {
	case "subscription_inactive", "quota_exceeded", "insufficient_balance":
		return msg
	}
	zerolog.Ctx(ctx).Warn().Err(err).Msg("subscription check failed")
	return "subscription_inactive"
}

// --- GatewayHandler ---

type GatewayHandler struct {
	models        *service.ModelService
	entitlements  *service.EntitlementService
	usage         *service.UsageService
	rateLimit     *service.RateLimitService
	routers       *service.RouterService
	cliProxy      *router.CLIProxyClient
	callback      *service.GenfityCallback
	routerAPIKey  string
	routerTimeout time.Duration
}

// NewGatewayHandler builds the gateway handler.
//
// NOTE (2026-05, PRD §3.3): virtual combos used to live here. They now live
// in CLIProxyAPI so the gateway can focus on API keys, quota, and usage.
// If fallback behaviour is required, configure a combo upstream.
func NewGatewayHandler(
	models *service.ModelService,
	entitlements *service.EntitlementService,
	usage *service.UsageService,
	rateLimit *service.RateLimitService,
	routers *service.RouterService,
	cliProxy *router.CLIProxyClient,
	callback *service.GenfityCallback,
	routerAPIKey string,
	routerTimeout time.Duration,
) *GatewayHandler {
	return &GatewayHandler{
		models:        models,
		entitlements:  entitlements,
		usage:         usage,
		rateLimit:     rateLimit,
		routers:       routers,
		cliProxy:      cliProxy,
		callback:      callback,
		routerAPIKey:  routerAPIKey,
		routerTimeout: routerTimeout,
	}
}

// --- helpers ---

const (
	maxRuntimeRequestBodyBytes int64 = 8 << 20
	maxStreamCaptureBytes            = 1 << 20
)

func decodeRuntimePayload(w http.ResponseWriter, r *http.Request, payload *map[string]any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRuntimeRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(payload)
}

type tailCaptureBuffer struct {
	limit int
	buf   bytes.Buffer
}

func (b *tailCaptureBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	if len(p) >= b.limit {
		b.buf.Reset()
		_, _ = b.buf.Write(p[len(p)-b.limit:])
		return len(p), nil
	}
	if overflow := b.buf.Len() + len(p) - b.limit; overflow > 0 {
		kept := append([]byte(nil), b.buf.Bytes()[overflow:]...)
		b.buf.Reset()
		_, _ = b.buf.Write(kept)
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *tailCaptureBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

// recordFailedRequest writes a usage_ledger entry for requests that fail
// before the upstream call (model_not_allowed, billing failures, rate
// limits, etc.). Without this, the api-keys logs panel would only show
// successful + upstream-failed requests, hiding configuration / billing
// issues that customers need to debug. Tokens stay 0 because no upstream
// work happened.
func (h *GatewayHandler) recordFailedRequest(ctx context.Context, apiKey store.APIKey, publicModel, errorCode string, statusCode int, started time.Time) {
	finished := time.Now().UTC()
	latencyMs := int32(finished.Sub(started).Milliseconds())
	apiKeyID := apiKey.ID
	ec := errorCode

	entry := store.UsageLedgerEntry{
		ID:              uuid.New(),
		RequestID:       uuid.New().String(),
		GenfityUserID:   apiKey.GenfityUserID,
		GenfityTenantID: apiKey.GenfityTenantID,
		APIKeyID:        &apiKeyID,
		PublicModel:     publicModel,
		Status:          "failed",
		ErrorCode:       &ec,
		LatencyMS:       &latencyMs,
		StartedAt:       started,
		FinishedAt:      &finished,
		InputCost:       "0",
		OutputCost:      "0",
		TotalCost:       "0",
	}
	if _, err := h.usage.Record(ctx, entry); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("error_code", errorCode).Msg("failed to record failed-request usage entry")
	}
	_ = statusCode
}

type usageMetrics struct {
	PromptTokens             int64
	CompletionTokens         int64
	TotalTokens              int64
	CachedTokens             int64
	CacheReadInputTokens     int64
	CacheCreationInputTokens int64
	ReasoningTokens          int64
}

func parseUsageFromBody(body []byte) usageMetrics {
	dec := json.NewDecoder(bytes.NewReader(body))
	var payload map[string]any
	if err := dec.Decode(&payload); err == nil {
		metrics := parseUsageFromPayload(payload)
		// If completion is 0 but the response has content (text or tool_use),
		// estimate from body. Anthropic upstream sometimes reports
		// output_tokens=0 for tool_use responses; without this fallback,
		// credit/PAYG users get charged $0 for tool calls.
		if metrics.CompletionTokens == 0 {
			if est := estimateOutputTokensFromPayload(payload); est > 0 {
				metrics.CompletionTokens = est
				if metrics.TotalTokens < metrics.PromptTokens+metrics.CompletionTokens {
					metrics.TotalTokens = metrics.PromptTokens + metrics.CompletionTokens
				}
			}
		}
		if metrics.PromptTokens != 0 || metrics.CompletionTokens != 0 || metrics.TotalTokens != 0 {
			return metrics
		}
	}
	p, c, t := parseUsageFromSSEBody(body)
	return usageMetrics{
		PromptTokens:     p,
		CompletionTokens: c,
		TotalTokens:      t,
	}
}

// detectProviderErrorFromBody inspects an upstream response body (JSON or SSE)
// and returns a non-empty error code if the body conveys an in-body provider error.
// This is necessary because some providers return HTTP 200 with `{"error": ...}`.
func detectProviderErrorFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if code := detectErrorFromJSONBytes(body); code != "" {
		return code
	}
	// SSE fallback: scan each data: line.
	for _, rawLine := range strings.Split(string(body), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		if code := detectErrorFromJSONBytes([]byte(data)); code != "" {
			return code
		}
	}
	return ""
}

func detectErrorFromJSONBytes(b []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(b, &payload); err != nil {
		return ""
	}
	return detectErrorFromPayload(payload)
}

func detectErrorFromPayload(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if errVal, ok := payload["error"]; ok && errVal != nil {
		switch v := errVal.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return "provider_error"
			}
		case map[string]any:
			if code, ok := v["code"].(string); ok && code != "" {
				return code
			}
			if msg, ok := v["message"].(string); ok && strings.TrimSpace(msg) != "" {
				return "provider_error"
			}
			if t, ok := v["type"].(string); ok && t != "" {
				return t
			}
			return "provider_error"
		}
	}
	return ""
}

// namespaceUpstreamErrorCode prefixes a provider-body error code with
// "upstream_" so the ledger can distinguish a provider-side failure from a
// gateway-side limit. Without this, a leaf returning {"error":{"code":
// "rate_limit_exceeded"}} is recorded identically to a gateway RPM rejection,
// making "model busy" indistinguishable from "account hit its quota". An empty
// code stays empty; already-namespaced codes are left untouched.
func namespaceUpstreamErrorCode(code string) string {
	if code == "" || strings.HasPrefix(code, "upstream_") {
		return code
	}
	return "upstream_" + code
}


// expected shape of a real completion (OpenAI choices[], Anthropic
// content[], or an SSE stream that started emitting). Used as the
// "is this really a success" gate for tick-on-success counters —
// stricter than detectProviderErrorFromBody, which only catches the
// known error envelopes. Some providers return HTTP 200 with shapes
// like `{"message":"Improperly formed request.","reason":null}` that
// neither match the success schema nor any error envelope; treating
// those as success would charge users for failed requests.
func looksLikeSuccessfulCompletion(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	// SSE: presence of any `data:` line means the stream started — at
	// that point the request reached upstream and produced output. We
	// trust the streaming body's own error detection elsewhere.
	if bytes.Contains(body, []byte("data:")) {
		return true
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	// OpenAI-shape: choices is a non-empty array.
	if choices, ok := payload["choices"].([]any); ok && len(choices) > 0 {
		return true
	}
	// Anthropic-shape: content is a non-empty array AND type=="message".
	if content, ok := payload["content"].([]any); ok && len(content) > 0 {
		if t, _ := payload["type"].(string); t == "message" {
			return true
		}
		return true
	}
	// Anthropic count_tokens / embeddings success: presence of
	// input_tokens (count) or data array (embeddings).
	if _, ok := payload["input_tokens"]; ok {
		return true
	}
	if data, ok := payload["data"].([]any); ok && len(data) > 0 {
		return true
	}
	return false
}

// shouldCountAsSuccess combines HTTP status, in-body error detection,
// and positive shape detection. Counters tick only when ALL three
// agree the request really succeeded.
func shouldCountAsSuccess(statusCode int, body []byte) bool {
	if statusCode < 200 || statusCode >= 300 {
		return false
	}
	if detectProviderErrorFromBody(body) != "" {
		return false
	}
	return looksLikeSuccessfulCompletion(body)
}

func parseUsageFromSSEBody(body []byte) (prompt int64, completion int64, total int64) {
	var event strings.Builder
	// Track whether we've seen a final usage report (message_delta with
	// stop_reason set, or chat.completion final chunk). When we have, we
	// stop overwriting from earlier partials. SSE upstreams sometimes
	// emit per-event running counters AND a final cumulative — the parser
	// previously did "last non-zero wins" which can pick a partial.
	var contentChars int64
	finalSeen := false
	flush := func() {
		data := strings.TrimSpace(event.String())
		event.Reset()
		if data == "" || data == "[DONE]" {
			return
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return
		}
		// Track text/tool_use content so we can fall back to char-based
		// estimation if no usage block ever arrives.
		contentChars += extractContentCharsFromSSEEvent(payload)

		isFinal := isFinalSSEEvent(payload)
		// Only overwrite if we haven't seen a final yet, OR this event is
		// also a final (e.g. multiple message_delta arriving).
		if finalSeen && !isFinal {
			return
		}
		metrics := parseUsageFromPayload(payload)
		if metrics.PromptTokens != 0 {
			prompt = metrics.PromptTokens
		}
		if metrics.CompletionTokens != 0 {
			completion = metrics.CompletionTokens
		}
		if metrics.TotalTokens != 0 {
			total = metrics.TotalTokens
		}
		if isFinal {
			finalSeen = true
		}
	}

	for _, rawLine := range strings.Split(string(body), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if event.Len() > 0 {
				event.WriteByte('\n')
			}
			event.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()

	// Fallback: if upstream never reported a meaningful completion count
	// (tool_use streams, providers without include_usage), estimate from
	// the content we observed. ~4 chars per token is the standard rough
	// approximation that matches OpenAI's tiktoken at ±10%.
	if completion == 0 && contentChars > 0 {
		completion = (contentChars + 3) / 4
		if completion < 1 {
			completion = 1
		}
	}
	if total == 0 {
		total = prompt + completion
	}
	return prompt, completion, total
}

// extractContentCharsFromSSEEvent counts chars in any text/tool_use payload
// inside one SSE chunk, regardless of provider format. We handle the
// shapes we see in production:
//   - OpenAI: choices[].delta.content (string)
//   - Anthropic: delta.text, content_block.text, content_block.input (tool_use)
//   - Anthropic: content[].text, content[].input
func extractContentCharsFromSSEEvent(payload map[string]any) int64 {
	var chars int64
	// OpenAI delta.content
	if choices, ok := payload["choices"].([]any); ok {
		for _, ch := range choices {
			chMap, _ := ch.(map[string]any)
			if chMap == nil {
				continue
			}
			if delta, ok := chMap["delta"].(map[string]any); ok {
				if s, ok := delta["content"].(string); ok {
					chars += int64(len(s))
				}
				if calls, ok := delta["tool_calls"].([]any); ok {
					for _, c := range calls {
						chars += charsInToolCall(c)
					}
				}
			}
			if msg, ok := chMap["message"].(map[string]any); ok {
				if s, ok := msg["content"].(string); ok {
					chars += int64(len(s))
				}
				if calls, ok := msg["tool_calls"].([]any); ok {
					for _, c := range calls {
						chars += charsInToolCall(c)
					}
				}
			}
		}
	}
	// Anthropic delta on content_block_delta
	if delta, ok := payload["delta"].(map[string]any); ok {
		if s, ok := delta["text"].(string); ok {
			chars += int64(len(s))
		}
		if s, ok := delta["partial_json"].(string); ok {
			chars += int64(len(s))
		}
	}
	// Anthropic content_block_start with a tool_use block
	if cb, ok := payload["content_block"].(map[string]any); ok {
		chars += charsInAnthropicContentBlock(cb)
	}
	// Anthropic message.content array (message_start gives empty, but
	// some providers ship a flat final event with content[])
	if msg, ok := payload["message"].(map[string]any); ok {
		if blocks, ok := msg["content"].([]any); ok {
			for _, b := range blocks {
				if cb, ok := b.(map[string]any); ok {
					chars += charsInAnthropicContentBlock(cb)
				}
			}
		}
	}
	// Top-level content (Anthropic non-streaming response)
	if blocks, ok := payload["content"].([]any); ok {
		for _, b := range blocks {
			if cb, ok := b.(map[string]any); ok {
				chars += charsInAnthropicContentBlock(cb)
			}
		}
	}
	return chars
}

func charsInAnthropicContentBlock(cb map[string]any) int64 {
	var chars int64
	if s, ok := cb["text"].(string); ok {
		chars += int64(len(s))
	}
	if input, ok := cb["input"].(map[string]any); ok {
		if b, err := json.Marshal(input); err == nil {
			chars += int64(len(b))
		}
	}
	if input, ok := cb["input"].([]any); ok {
		if b, err := json.Marshal(input); err == nil {
			chars += int64(len(b))
		}
	}
	if name, ok := cb["name"].(string); ok {
		chars += int64(len(name))
	}
	return chars
}

func charsInToolCall(c any) int64 {
	cMap, ok := c.(map[string]any)
	if !ok {
		return 0
	}
	var chars int64
	if fn, ok := cMap["function"].(map[string]any); ok {
		if s, ok := fn["name"].(string); ok {
			chars += int64(len(s))
		}
		if s, ok := fn["arguments"].(string); ok {
			chars += int64(len(s))
		}
	}
	return chars
}

// isFinalSSEEvent reports whether the SSE chunk is a terminal usage
// event we should trust over earlier partials. Anthropic emits
// `message_delta` with `stop_reason` (and the final `usage`) before
// `message_stop`; OpenAI emits a chunk with `finish_reason` set.
func isFinalSSEEvent(payload map[string]any) bool {
	if t, ok := payload["type"].(string); ok {
		if t == "message_delta" {
			if d, ok := payload["delta"].(map[string]any); ok {
				if sr, ok := d["stop_reason"].(string); ok && sr != "" {
					return true
				}
			}
		}
	}
	if choices, ok := payload["choices"].([]any); ok {
		for _, ch := range choices {
			chMap, _ := ch.(map[string]any)
			if chMap == nil {
				continue
			}
			if fr, ok := chMap["finish_reason"].(string); ok && fr != "" {
				return true
			}
		}
	}
	// OpenAI always sends a tail chunk where usage is populated when
	// stream_options.include_usage=true; treat any chunk that already
	// carries a usage block as final.
	if _, ok := payload["usage"].(map[string]any); ok {
		return true
	}
	return false
}

// estimateOutputTokensFromPayload counts text + tool_use content in a
// non-streaming response payload and returns ~chars/4 as token estimate.
// Returns 0 when the body has no usable content (in which case the
// upstream's reported zero is presumably correct).
func estimateOutputTokensFromPayload(payload map[string]any) int64 {
	var chars int64
	// Anthropic-shape: top-level content array
	if blocks, ok := payload["content"].([]any); ok {
		for _, b := range blocks {
			if cb, ok := b.(map[string]any); ok {
				chars += charsInAnthropicContentBlock(cb)
			}
		}
	}
	// OpenAI-shape: choices[].message
	if choices, ok := payload["choices"].([]any); ok {
		for _, ch := range choices {
			chMap, _ := ch.(map[string]any)
			if chMap == nil {
				continue
			}
			if msg, ok := chMap["message"].(map[string]any); ok {
				if s, ok := msg["content"].(string); ok {
					chars += int64(len(s))
				}
				if calls, ok := msg["tool_calls"].([]any); ok {
					for _, c := range calls {
						chars += charsInToolCall(c)
					}
				}
			}
		}
	}
	if chars == 0 {
		return 0
	}
	tokens := (chars + 3) / 4
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

func parseUsageFromPayload(payload map[string]any) usageMetrics {
	usage, ok := payload["usage"].(map[string]any)
	if !ok {
		if message, ok := payload["message"].(map[string]any); ok {
			usage, _ = message["usage"].(map[string]any)
		}
	}
	if usage == nil {
		return usageMetrics{}
	}

	prompt := anyToInt64(usage["prompt_tokens"])
	completion := anyToInt64(usage["completion_tokens"])
	total := anyToInt64(usage["total_tokens"])
	if prompt == 0 {
		prompt = anyToInt64(usage["input_tokens"])
	}
	if completion == 0 {
		completion = anyToInt64(usage["output_tokens"])
	}
	if total == 0 {
		total = prompt + completion
	}

	reasoning := anyToInt64(usage["reasoning_tokens"])
	if reasoning == 0 {
		if details, ok := usage["completion_tokens_details"].(map[string]any); ok {
			reasoning = anyToInt64(details["reasoning_tokens"])
		}
	}
	if reasoning == 0 {
		if details, ok := usage["output_tokens_details"].(map[string]any); ok {
			reasoning = anyToInt64(details["reasoning_tokens"])
		}
	}

	cached := anyToInt64(usage["cached_tokens"])
	cacheRead := anyToInt64(usage["cache_read_tokens"])
	cacheCreation := anyToInt64(usage["cache_creation_tokens"])
	if details, ok := usage["prompt_tokens_details"].(map[string]any); ok {
		if cacheRead == 0 {
			cacheRead = anyToInt64(details["cached_tokens"])
		}
		if cacheRead == 0 {
			cacheRead = anyToInt64(details["cache_read_tokens"])
		}
		if cacheRead == 0 {
			cacheRead = anyToInt64(details["cache_read_input_tokens"])
		}
		if cacheCreation == 0 {
			cacheCreation = anyToInt64(details["cache_creation_tokens"])
		}
		if cacheCreation == 0 {
			cacheCreation = anyToInt64(details["cache_creation_input_tokens"])
		}
	}
	if cached == 0 {
		cached = cacheRead + cacheCreation
	}

	return usageMetrics{
		PromptTokens:             prompt,
		CompletionTokens:         completion,
		TotalTokens:              total,
		CachedTokens:             cached,
		CacheReadInputTokens:     cacheRead,
		CacheCreationInputTokens: cacheCreation,
		ReasoningTokens:          reasoning,
	}
}

func anyToInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		parsed, _ := t.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(t, 10, 64)
		return parsed
	default:
		return 0
	}
}

func modelPrice(prices []store.AIModelPrice, modelID uuid.UUID) *store.AIModelPrice {
	for _, item := range prices {
		if item.ModelID == modelID && item.Active {
			return &item
		}
	}
	return nil
}

func parseFloatPtr(value *string) float64 {
	if value == nil {
		return 0
	}
	v, err := strconv.ParseFloat(*value, 64)
	if err != nil {
		return 0
	}
	return v
}

func formatAmount(value float64) string {
	return fmt.Sprintf("%.6f", value)
}

func (h *GatewayHandler) writeUpstreamResponse(w http.ResponseWriter, resp *http.Response, body []byte, publicModel string) {
	// Sanitize error bodies so internal provider/router details (litellm,
	// "All credentials for model X are cooling down", "via provider Y", etc.)
	// never reach the customer. Successful 2xx/3xx responses pass through.
	sanitized := sanitizeErrorBody(body, resp.StatusCode)
	// Rewrite the response "model" field to the public model the customer
	// requested so the real router/upstream model (e.g. "minimax-m2.5",
	// "kimi-k2.6" for a disguised combo) never leaks. No-op on error bodies
	// and when the name already matches.
	sanitized = rewriteResponseModel(sanitized, resp.StatusCode, publicModel)

	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	if len(sanitized) != len(body) {
		w.Header().Set("Content-Length", strconv.Itoa(len(sanitized)))
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(sanitized)
}

func isStreamingPayload(payload map[string]any) bool {
	stream, _ := payload["stream"].(bool)
	return stream
}

// ensureNonStreaming forces stream:false on the upstream payload. Per the
// OpenAI spec, `stream` defaults to false when omitted, but some upstreams
// (notably Claude/Anthropic models via CLIProxyAPI) default to SSE when the
// field is absent. The CRM's non-streaming OpenAI SDK omits `stream`, so
// without this the client receives `data:` SSE chunks and fails to parse the
// JSON body ("invalid character 'd'"). Setting it explicitly keeps the
// non-streaming contract intact for every upstream/model.
func ensureNonStreaming(payload map[string]any) {
	payload["stream"] = false
	delete(payload, "stream_options")
}

func ensureStreamUsageOption(payload map[string]any) {
	options, _ := payload["stream_options"].(map[string]any)
	if options == nil {
		options = map[string]any{}
	}
	options["include_usage"] = true
	payload["stream_options"] = options
}

func estimatePayloadTokens(value any) int64 {
	var chars int64
	var walk func(any)
	walk = func(v any) {
		switch typed := v.(type) {
		case string:
			chars += int64(len(typed))
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for key, item := range typed {
				if key == "model" || key == "stream" || key == "stream_options" {
					continue
				}
				walk(item)
			}
		}
	}
	walk(value)
	if chars == 0 {
		return 1
	}
	return (chars + 3) / 4
}

// preRequestCounters tracks which counters were incremented during
// applyPreRequestLimits so the caller can roll them back if a later
// step fails (concurrency acquire, billing reservation, payload clone,
// upstream call). Without rollback, every later-stage failure burned a
// period/RPD slot and banned users well below the configured cap.
type preRequestCounters struct {
	rateLimit       *service.RateLimitService
	userID          string
	periodKey       string
	planCode        string
	publicModel     string
	periodConsumed  bool
	rpdConsumed     bool
	freeRPMConsumed bool
	freeRPDConsumed bool
	committed       bool
}

// rollback releases every counter that was incremented in
// applyPreRequestLimits, unless commit() has already been called.
// Pass a fresh background context if r.Context() is already canceled
// (e.g. client disconnect mid-request) so the decrement still goes
// through.
func (p *preRequestCounters) rollback(ctx context.Context) {
	if p == nil || p.rateLimit == nil || p.committed {
		return
	}
	if p.periodConsumed {
		p.rateLimit.RollbackRequestsPerPeriod(ctx, p.userID, p.periodKey)
		p.periodConsumed = false
	}
	if p.rpdConsumed {
		p.rateLimit.RollbackPlanRPD(ctx, p.userID, p.planCode)
		p.rpdConsumed = false
	}
	if p.freeRPMConsumed {
		p.rateLimit.RollbackFreeModelRPM(ctx, p.userID, p.publicModel)
		p.freeRPMConsumed = false
	}
	if p.freeRPDConsumed {
		p.rateLimit.RollbackFreeModelRPD(ctx, p.userID, p.publicModel)
		p.freeRPDConsumed = false
	}
}

// commit marks the request as having genuinely consumed the slot.
// After commit, rollback is a no-op. Call once the upstream returns
// (success or non-retriable failure) and the usage_ledger entry has
// been recorded — at that point the request "exists" and should count
// against the period cap.
//
// If the client has already disconnected (RTO/cancel) by the time we
// settle, commit is skipped: the request never reached the customer, so
// it must not burn their rate-limit slot (RPD/RPP/free-RPM). The deferred
// rollback (which runs on a background context) then releases the slot.
// Token billing is handled separately in recordAndFinalizeRuntime and is
// NOT governed by this — the provider already processed the tokens, so
// that cost stands regardless of client disconnect.
func (p *preRequestCounters) commit(ctx context.Context) {
	if p == nil {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	p.committed = true
}

// applyPreRequestLimits enforces the limits that we can check before the
// upstream call: plan-period total requests + per-(user,model) free model
// RPM/RPD. Returns an errorCode + statusCode when the request must be
// rejected; ("", 0) means continue. Plan RPM/concurrency are kept inline
// at the call sites because the concurrency release is owned there.
//
// Counters are incremented in the order period → RPD → free-RPM →
// free-RPD. If a later check trips, every earlier increment is rolled
// back before the function returns — this keeps the period counter
// honest. The returned tracker also lets the caller roll back later
// failures (concurrency, billing reservation, payload clone, upstream
// error) so those don't quietly eat period slots either.
func (h *GatewayHandler) applyPreRequestLimits(
	ctx context.Context,
	apiKey store.APIKey,
	subscription *service.ActiveSubscription,
	limits service.PlanLimits,
	model *store.AIModel,
) (errorCode string, statusCode int, tracker *preRequestCounters) {
	if h.rateLimit == nil {
		return "", 0, nil
	}
	tracker = &preRequestCounters{
		rateLimit:   h.rateLimit,
		userID:      apiKey.GenfityUserID,
		publicModel: "",
	}
	if model != nil {
		tracker.publicModel = model.PublicModel
	}
	if subscription != nil && subscription.Plan != nil {
		tracker.planCode = subscription.Plan.PlanCode
	}
	// Only count this request against the unlimited plan's per-period and
	// per-day caps when it will actually be billed via the unlimited
	// subscription. A user who has both an unlimited trial AND a credit
	// package was previously getting their credit-billed requests for
	// paid models (e.g. claude-opus-4.7) counted against the trial's
	// MaxRequestsPerPeriod, which banned them well below their real
	// budget. tryPriorityBilling owns the same routing logic; we mirror
	// the priority-1 gate here (billing_source allows subscription AND
	// the model is covered by allowedModels) so the counter only ticks
	// for requests the unlimited plan is actually paying for.
	willUseUnlimited := requestWillUseUnlimited(apiKey, subscription, model)
	// Free models are governed by their per-(user, model) free_model
	// limits below — they must NOT consume the plan's MaxRequestsPerPeriod
	// or RPD slot. Letting a free :free model burn the unlimited plan's
	// daily/period quota would mean the same plan that advertises
	// "unlimited paid models" also caps free-model usage at the same
	// number, which surprises users and burns slots they paid for.
	willConsumePlanCounters := willUseUnlimited && (model == nil || !model.IsFree)
	if willConsumePlanCounters && limits.HasMaxRequestsPerPeriod() && subscription != nil && subscription.Entitlement != nil {
		pk := periodKey(subscription.Entitlement)
		tracker.periodKey = pk
		_, end := activePeriod(subscription.Entitlement)
		ttl := time.Until(end)
		if ttl <= 0 {
			ttl = 24 * time.Hour
		}
		// When the app cleared an abuse debt on a purchase it marks
		// needsRppReset so the user starts the fresh window at 0. Honor it
		// exactly once (sentinel-guarded) BEFORE the baseline ensure so the
		// ledger reseed does not immediately refill it from the period's prior
		// (debt-era) usage. Without the once-guard this would re-fire on every
		// request (the app clears needsRppReset only on the next sync) and
		// hand the user unlimited requests. After a successful reset we skip
		// the baseline reseed for this request.
		didReset := false
		if _, _, needsRppReset := resolveAbuseDebt(subscription); needsRppReset {
			didReset, _ = h.rateLimit.ResetRequestsPerPeriodOnce(ctx, apiKey.GenfityUserID, pk, ttl)
		}
		if !didReset {
			h.ensurePeriodCounterBaseline(ctx, apiKey.GenfityUserID, subscription, ttl, true, false)
		}
		// Scale the RPP cap by how many base purchase windows this period
		// spans. Stacking a 1-day plan 4× extends the window to 4 days, so
		// the user should get 4× the single-purchase RPP instead of being
		// capped at one day's worth across the whole window. Multiplier is
		// 1 when the base duration is unknown (fail safe). RPD still bounds
		// daily burst independently.
		mult := periodRPPMultiplier(subscription)
		effectiveRPP := limits.MaxRequestsPerPeriod * mult
		// Anti-abuse debt: when an admin has flagged this user for bypassing
		// the RPP cap (historical leak from the period-key bug), reserve part
		// of each purchase window's RPP to repay the overage. 75% of the base
		// per-purchase RPP (scaled by the stacked-window multiplier) is held
		// back for repayment; only the remaining 25% is usable. The reserve is
		// capped at the remaining debt so the user is never penalised beyond
		// what they owe. debtRepayment marks that the reserve is the binding
		// constraint, so we can surface a clearer error than a generic cap hit.
		flagged, debtRemaining, _ := resolveAbuseDebt(subscription)
		debtRepayment := false
		if flagged && debtRemaining > 0 {
			debtReserve := int(math.Round(float64(limits.MaxRequestsPerPeriod)*0.75)) * mult
			reserve := debtRemaining
			if reserve > debtReserve {
				reserve = debtReserve
			}
			effectiveRPP -= reserve
			if effectiveRPP < 0 {
				effectiveRPP = 0
			}
			debtRepayment = true
		}
		if err := h.rateLimit.CheckRequestsPerPeriod(ctx, apiKey.GenfityUserID, pk, ttl, effectiveRPP); err != nil {
			if debtRepayment {
				return "plan_debt_repayment", http.StatusTooManyRequests, tracker
			}
			return "plan_period_limit_exceeded", http.StatusTooManyRequests, tracker
		}
		tracker.periodConsumed = true
	}
	if willConsumePlanCounters && limits.HasRPD() && subscription != nil && subscription.Plan != nil {
		if err := h.rateLimit.CheckPlanRPD(ctx, apiKey.GenfityUserID, subscription.Plan.PlanCode, limits.RPD); err != nil {
			tracker.rollback(ctx)
			return "plan_rpd_exceeded", http.StatusTooManyRequests, tracker
		}
		tracker.rpdConsumed = true
	}
	if model != nil && model.IsFree {
		if model.FreeLimitRPM != nil && *model.FreeLimitRPM > 0 {
			if err := h.rateLimit.CheckFreeModelRPM(ctx, apiKey.GenfityUserID, model.PublicModel, int(*model.FreeLimitRPM)); err != nil {
				tracker.rollback(ctx)
				return "free_model_rpm_exceeded", http.StatusTooManyRequests, tracker
			}
			tracker.freeRPMConsumed = true
		}
		if model.FreeLimitRPD != nil && *model.FreeLimitRPD > 0 {
			if err := h.rateLimit.CheckFreeModelRPD(ctx, apiKey.GenfityUserID, model.PublicModel, int(*model.FreeLimitRPD)); err != nil {
				tracker.rollback(ctx)
				return "free_model_rpd_exceeded", http.StatusTooManyRequests, tracker
			}
			tracker.freeRPDConsumed = true
		}
	}
	return "", 0, tracker
}

func activePeriod(entitlement *store.CustomerEntitlement) (time.Time, time.Time) {
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	if entitlement != nil {
		if entitlement.PeriodStart != nil {
			start = entitlement.PeriodStart.UTC()
		}
		if entitlement.PeriodEnd != nil {
			end = entitlement.PeriodEnd.UTC()
		}
	}
	return start, end
}

// periodKey returns a stable string identifying the current entitlement
// period for use as part of a Redis counter key. We anchor it on
// period_start only so extending the same entitlement keeps the already-
// consumed RPP / credit-per-period counters instead of silently starting
// from zero mid-cycle.
func periodKey(entitlement *store.CustomerEntitlement) string {
	if entitlement == nil {
		return ""
	}
	start, _ := activePeriod(entitlement)
	return fmt.Sprintf("%d", start.Unix())
}

func (h *GatewayHandler) ensurePeriodCounterBaseline(
	ctx context.Context,
	userID string,
	subscription *service.ActiveSubscription,
	ttl time.Duration,
	ensureRequests bool,
	ensureCredits bool,
) {
	if h == nil || h.rateLimit == nil || h.usage == nil || userID == "" || subscription == nil || subscription.Entitlement == nil {
		return
	}
	pk := periodKey(subscription.Entitlement)
	if pk == "" {
		return
	}
	needRequests := ensureRequests && h.rateLimit.GetRequestsPerPeriodCount(ctx, userID, pk) == 0
	needCredits := ensureCredits && h.rateLimit.GetPlanCreditsPerPeriodCount(ctx, userID, pk) == 0
	if !needRequests && !needCredits {
		return
	}

	start, end := activePeriod(subscription.Entitlement)
	rows := h.usage.ListByUserSince(ctx, userID, start)
	requests := 0
	credits := 0.0
	for _, row := range rows {
		if row.Status != "success" || row.BillingMode == nil || *row.BillingMode != "unlimited" || isFreeUsageLedgerEntry(row) {
			continue
		}
		if row.StartedAt.Before(start) || !row.StartedAt.Before(end) {
			continue
		}
		requests++
		if row.AmountCredits != nil {
			credits += parseFloatPtr(row.AmountCredits)
		}
	}
	if needRequests && requests > 0 {
		_ = h.rateLimit.SetRequestsPerPeriod(ctx, userID, pk, ttl, requests)
	}
	if needCredits && credits > 0 {
		_ = h.rateLimit.SetPlanCreditsPerPeriod(ctx, userID, pk, ttl, credits)
	}
}

// planBaseDurationDays reads the plan's base purchase duration (in days)
// from the live plan snapshot metadata. Returns 0 when unknown — callers
// must treat 0 as "no scaling" (multiplier 1) so a missing field can never
// shrink or inflate a user's quota. The field is injected by genfity-app's
// buildPlanSnapshotPayload from AiGatewayPlan.durationDays.
func planBaseDurationDays(subscription *service.ActiveSubscription) int {
	if subscription == nil || subscription.Plan == nil || len(subscription.Plan.Metadata) == 0 {
		return 0
	}
	var meta map[string]any
	if err := json.Unmarshal(subscription.Plan.Metadata, &meta); err != nil {
		return 0
	}
	switch v := meta["durationDays"].(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 0 {
			return int(n)
		}
	}
	return 0
}

// periodRPPMultiplier returns how many base purchase windows the active
// entitlement period spans, so the RPP (requests-per-period) cap scales
// with stacked/extended purchases of the same plan.
//
// Why: a user who buys a 1-day plan 4× has the entitlement window extended
// to 4 days (one row, period_start..period_end = 4 days), but the plan's
// configured RPP is a single-purchase value (e.g. 750). Without scaling,
// the 4-day window is capped at 750 total — the user paid for 4 days of
// quota but gets 1 day's worth. We multiply RPP by round(windowDays /
// baseDurationDays) so the effective cap matches what they purchased
// (750 → 3000). RPD (per UTC-day) still bounds daily burst independently.
//
// Returns 1 (no scaling) when the base duration is unknown, the window is
// shorter than one base period, or inputs are degenerate — failing safe to
// the configured cap rather than guessing.
func periodRPPMultiplier(subscription *service.ActiveSubscription) int {
	if subscription == nil || subscription.Entitlement == nil {
		return 1
	}
	baseDays := planBaseDurationDays(subscription)
	if baseDays <= 0 {
		return 1
	}
	start, end := activePeriod(subscription.Entitlement)
	windowSeconds := end.Sub(start).Seconds()
	baseSeconds := float64(baseDays) * 24 * 3600
	if windowSeconds <= 0 || baseSeconds <= 0 {
		return 1
	}
	multiplier := int(math.Round(windowSeconds / baseSeconds))
	if multiplier < 1 {
		return 1
	}
	return multiplier
}

func pricingGroup(subscription *service.ActiveSubscription) string {
	if subscription == nil || subscription.Entitlement == nil {
		return ""
	}
	if subscription.Entitlement.PricingGroup != nil && *subscription.Entitlement.PricingGroup != "" {
		return *subscription.Entitlement.PricingGroup
	}
	if len(subscription.Entitlement.Metadata) == 0 {
		return ""
	}
	var meta map[string]any
	_ = json.Unmarshal(subscription.Entitlement.Metadata, &meta)
	value, _ := meta["pricingGroup"].(string)
	return value
}

// resolveAbuseDebt reads the anti-abuse debt block from the entitlement
// metadata (written by genfity-app when an admin flags a user for bypassing
// the RPP cap). Returns whether the user is flagged, the remaining debt in
// requests, and whether a one-time RPP counter reset is pending (set by the
// app when the debt reaches zero on a purchase). All values default to the
// no-debt case on any parse failure so a malformed blob can never penalise a
// user. Mirrors the json.Unmarshal pattern in pricingGroup().
func resolveAbuseDebt(subscription *service.ActiveSubscription) (flagged bool, debtRemaining int, needsRppReset bool) {
	if subscription == nil || subscription.Entitlement == nil || len(subscription.Entitlement.Metadata) == 0 {
		return false, 0, false
	}
	var meta map[string]any
	if err := json.Unmarshal(subscription.Entitlement.Metadata, &meta); err != nil {
		return false, 0, false
	}
	debt, ok := meta["abuseDebt"].(map[string]any)
	if !ok {
		return false, 0, false
	}
	flagged, _ = debt["flagged"].(bool)
	needsRppReset, _ = debt["needsRppReset"].(bool)
	switch v := debt["debtRemaining"].(type) {
	case float64:
		if v > 0 {
			debtRemaining = int(v)
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 0 {
			debtRemaining = int(n)
		}
	}
	return flagged, debtRemaining, needsRppReset
}

func quotaLimit(subscription *service.ActiveSubscription) int64 {
	limit := quotaLimitPtr(subscription)
	if limit == nil {
		return 0
	}
	return *limit
}

func quotaLimitPtr(subscription *service.ActiveSubscription) *int64 {
	if subscription == nil {
		return nil
	}
	var planQuota, entQuota *int64
	if subscription.Plan != nil {
		planQuota = subscription.Plan.QuotaTokensMonthly
	}
	if subscription.Entitlement != nil {
		entQuota = subscription.Entitlement.QuotaTokensMonthly
	}
	// Prefer the larger value: re-buy stacking accumulates on the entitlement
	// row, but the plan snapshot may carry the original single-purchase value.
	if planQuota != nil && entQuota != nil {
		if *entQuota > *planQuota {
			return entQuota
		}
		return planQuota
	}
	if planQuota != nil {
		return planQuota
	}
	return entQuota
}

func balanceAmount(subscription *service.ActiveSubscription) float64 {
	if subscription == nil || subscription.Entitlement == nil {
		return 0
	}
	// Prefer CreditBalance (authoritative for credit_package entitlements).
	if subscription.Entitlement.CreditBalance != nil {
		value, _ := strconv.ParseFloat(*subscription.Entitlement.CreditBalance, 64)
		if value > 0 {
			return value
		}
	}
	if subscription.Entitlement.BalanceSnapshot == nil {
		return 0
	}
	value, _ := strconv.ParseFloat(*subscription.Entitlement.BalanceSnapshot, 64)
	return value
}

func estimatedPromptReservationPrice(price *store.AIModelPrice) float64 {
	if price == nil {
		return 0
	}
	inputPrice := parseFloatPtr(&price.InputPricePer1M)
	if inputPrice > 0 {
		return inputPrice
	}
	if price.CachedPricePer1M != nil {
		return parseFloatPtr(price.CachedPricePer1M)
	}
	return 0
}

type tokenReservationEstimate struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	Bounded          bool
}

type runtimeReservation struct {
	PeriodStart    time.Time
	PeriodEnd      time.Time
	QuotaTokens    int64
	PlanCode       string
	CreditUSD      float64
	CreditPlanCode string

	// PRD v3 Phase 2: 3-priority chain reservation bookkeeping.
	//
	// BillingMode records which schema paid for the request so the
	// finalize path knows which Finalize* helper to call. Empty when
	// the request is covered by an unlimited entitlement (no debit).
	//
	// RequestCredits is the RESERVED credit amount for the
	// credit_package schema. The configured model price is stored in
	// CreditPricePer20k (price per 20k total tokens) and runtime
	// billing charges in 20k-token buckets.
	// PaygUSD is the reserved USD amount for the payg_topup schema
	// (actual-cost per-1M pricing). Exactly one of these is non-zero
	// when BillingMode != "unlimited".
	BillingMode           string // "unlimited" | "credit_package" | "payg_topup" | ""
	RequestCredits        float64
	CreditPricePer20k     float64
	PaygUSD               float64
	PlanCreditPricePer20k float64
	PlanCreditsPerDay     float64
	PlanCreditsPerPeriod  float64
	PlanCreditPeriodKey   string
}

type usageSettlement struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	TotalCostUSD     float64
	ActualCredits    float64
	Success          bool
	ErrorCode        string
	RequestID        string
}

func reservationNeedsFinalize(reservation runtimeReservation) bool {
	return reservation.BillingMode != "" ||
		reservation.QuotaTokens > 0 ||
		reservation.CreditUSD > 0 ||
		reservation.RequestCredits > 0 ||
		reservation.PaygUSD > 0 ||
		reservation.PlanCreditsPerDay > 0 ||
		reservation.PlanCreditsPerPeriod > 0
}

const (
	creditBillingBucketTokens = 20_000
)

func roundCredits(value float64) float64 {
	return math.Round(value*10_000) / 10_000
}

func creditBillingBuckets(totalTokens int64) int64 {
	if totalTokens <= 0 {
		return 0
	}
	return int64(math.Ceil(float64(totalTokens) / float64(creditBillingBucketTokens)))
}

func calculateActualRequestCredits(creditsPer20k float64, totalTokens int64) float64 {
	buckets := creditBillingBuckets(totalTokens)
	if buckets <= 0 || creditsPer20k <= 0 {
		return 0
	}
	return roundCredits(float64(buckets) * creditsPer20k)
}

func estimateReservedRequestCredits(creditsPer20k float64, estimate tokenReservationEstimate) float64 {
	if creditsPer20k <= 0 {
		return 0
	}
	totalTokens := estimate.TotalTokens
	if totalTokens <= 0 {
		totalTokens = creditBillingBucketTokens
	}
	return calculateActualRequestCredits(creditsPer20k, totalTokens)
}

func estimateRequestTokens(payload map[string]any, model *store.AIModel) tokenReservationEstimate {
	prompt := estimatePayloadTokens(payload)
	completion := anyToInt64(payload["max_tokens"])
	if completion == 0 {
		completion = anyToInt64(payload["max_completion_tokens"])
	}
	if completion == 0 {
		// When the client doesn't bound the response, use a sane default
		// rather than the full context window. Reserving context_window
		// (e.g. 1M for some free models) burns the entire per-day TPD
		// budget on the very first request, even though real responses
		// are typically <2k tokens. The reservation is reconciled with
		// the actual usage on finalize, so a slight over-reserve is
		// safe; an over-reserve of 1M tokens is not.
		completion = 4096
		// Don't exceed remaining context window if the model declares one
		// — the upstream would reject a request that asks for more.
		if model != nil && model.ContextWindow != nil {
			remaining := int64(*model.ContextWindow) - prompt
			if remaining > 0 && remaining < completion {
				completion = remaining
			}
		}
	}
	total := prompt + completion
	if total <= 0 {
		total = 1
	}
	// Bounded: true when either the client or our default provides a token
	// cap. This allows the PAYG billing path to reserve a sensible amount
	// (4096 by default) without rejecting every unbounded request with
	// max_tokens_required. The actual cost is reconciled on finalize.
	return tokenReservationEstimate{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total, Bounded: completion > 0}
}

// tryPriorityBilling implements the PRD v3 3-priority reservation chain,
// constrained by the API key's billing_source pin.
//
// API key billing_source values:
//
//	"auto"         → original 3-priority chain (subscription → credit → payg)
//	"subscription" → only Priority 1 (unlimited); error if not covered
//	"credit"       → only Priority 2 (credit_package); error if not configured/insufficient
//	"payg"         → only Priority 3 (payg_topup USD balance)
//
// Returns a reservation with BillingMode populated when a priority
// matched; returns zero reservation + zero status when no priority
// matches (caller falls through to legacy quota/credit path).
// Non-zero status means the priority matched but reservation failed
// (e.g. insufficient credits) — caller should surface the HTTP code.
func (h *GatewayHandler) tryPriorityBilling(ctx context.Context, apiKey store.APIKey, subscription *service.ActiveSubscription, payload map[string]any, model *store.AIModel, estimate tokenReservationEstimate) (runtimeReservation, int, string) {
	if model == nil {
		return runtimeReservation{}, 0, ""
	}

	source := apiKey.BillingSource
	if source == "" {
		source = "subscription"
	}

	// API keys are pinned to exactly one billing source. The legacy
	// "auto" cascade was removed in 2026-05; each branch below now
	// resolves billing for its source and either succeeds or fails
	// closed. There is no fall-through between branches — a credit-key
	// hitting "insufficient_credit_balance" must NOT silently consume
	// PAYG balance, and vice versa.
	switch source {
	case "subscription":
		if subscription == nil || subscription.Entitlement == nil {
			return runtimeReservation{}, http.StatusPaymentRequired, "no_active_subscription"
		}
		group := resolveSubscriptionPricingGroup(subscription)
		if group == "" {
			group = pricingGroup(subscription)
		}
		if (group != "unlimited" && group != "unlimited_plan") || !modelCoveredByUnlimited(subscription, model) {
			return runtimeReservation{}, http.StatusPaymentRequired, "subscription_not_covering_model"
		}

		res := runtimeReservation{BillingMode: "unlimited", PlanCode: subscription.Entitlement.PlanCode}
		limits := service.PlanLimitsFromSnapshot(subscriptionPlan(subscription))
		periodStart, periodEnd := activePeriod(subscription.Entitlement)
		limit := quotaLimit(subscription)
		if limit > 0 {
			if !estimate.Bounded {
				return runtimeReservation{}, http.StatusBadRequest, "max_tokens_required"
			}
			if err := h.usage.ReserveQuotaTokens(ctx, apiKey.GenfityUserID, apiKey.GenfityTenantID, periodStart, periodEnd, estimate.TotalTokens, limit); err != nil {
				if errors.Is(err, service.ErrQuotaExceeded) {
					return runtimeReservation{}, http.StatusTooManyRequests, "quota_exceeded"
				}
				return runtimeReservation{}, http.StatusInternalServerError, "quota_reservation_failed"
			}
			res.PeriodStart = periodStart
			res.PeriodEnd = periodEnd
			res.QuotaTokens = estimate.TotalTokens
		}
		if h.rateLimit != nil && (limits.HasCreditPerDay() || limits.HasCreditPerPeriod()) {
			cost, err := h.models.GetModelCreditCost(ctx, model.PublicModel)
			if err != nil || cost == nil || !cost.IsActive {
				if res.QuotaTokens > 0 {
					_ = h.usage.FinalizeQuotaTokens(ctx, apiKey.GenfityUserID, res.PeriodStart, res.PeriodEnd, res.QuotaTokens, 0, false)
				}
				return runtimeReservation{}, http.StatusInternalServerError, "subscription_credit_cost_not_configured"
			}
			creditsPer20k := parseFloatPtr(&cost.CreditsPerReq)
			if !cost.IsFree && creditsPer20k > 0 {
				reservedCredits := estimateReservedRequestCredits(creditsPer20k, estimate)
				periodKeyValue := periodKey(subscription.Entitlement)
				ttl := time.Until(periodEnd)
				if ttl <= 0 {
					ttl = 24 * time.Hour
				}
				h.ensurePeriodCounterBaseline(ctx, apiKey.GenfityUserID, subscription, ttl, false, true)
				if limits.HasCreditPerDay() {
					if err := h.rateLimit.CheckPlanCreditRPD(ctx, apiKey.GenfityUserID, subscription.Entitlement.PlanCode, reservedCredits, limits.CreditLimitPerDay); err != nil {
						if res.QuotaTokens > 0 {
							_ = h.usage.FinalizeQuotaTokens(ctx, apiKey.GenfityUserID, res.PeriodStart, res.PeriodEnd, res.QuotaTokens, 0, false)
						}
						return runtimeReservation{}, http.StatusTooManyRequests, "plan_credit_rpd_exceeded"
					}
					res.PlanCreditsPerDay = reservedCredits
				}
				if limits.HasCreditPerPeriod() {
					if err := h.rateLimit.CheckPlanCreditsPerPeriod(ctx, apiKey.GenfityUserID, periodKeyValue, ttl, reservedCredits, limits.CreditLimitPerPeriod); err != nil {
						if res.PlanCreditsPerDay > 0 {
							_ = h.rateLimit.FinalizePlanCreditRPD(ctx, apiKey.GenfityUserID, subscription.Entitlement.PlanCode, res.PlanCreditsPerDay, 0)
							res.PlanCreditsPerDay = 0
						}
						if res.QuotaTokens > 0 {
							_ = h.usage.FinalizeQuotaTokens(ctx, apiKey.GenfityUserID, res.PeriodStart, res.PeriodEnd, res.QuotaTokens, 0, false)
						}
						return runtimeReservation{}, http.StatusTooManyRequests, "plan_credit_period_exceeded"
					}
					res.PlanCreditsPerPeriod = reservedCredits
					res.PlanCreditPeriodKey = periodKeyValue
				}
				res.PlanCreditPricePer20k = creditsPer20k
			}
		}

		return res, 0, ""

	case "credit":
		fullModelID := model.PublicModel
		if fullModelID == "" {
			return runtimeReservation{}, http.StatusPaymentRequired, "credit_cost_not_configured"
		}
		cost, err := h.models.GetModelCreditCost(ctx, fullModelID)
		if err != nil || cost == nil || !cost.IsActive {
			return runtimeReservation{}, http.StatusPaymentRequired, "credit_cost_not_configured"
		}
		creditsPer20k := parseFloatPtr(&cost.CreditsPerReq)
		if cost.IsFree || creditsPer20k <= 0 {
			// Free model — still require the user to carry a positive
			// credit balance. Users with 0 balance (new accounts or
			// exhausted credits) cannot access even free models.
			// Read CreditBalance from the credit_package row specifically;
			// users with both unlimited + credit_package would otherwise
			// have GetByUser return the unlimited row whose CreditBalance
			// is always NULL.
			if entitlement, err := h.entitlements.GetCreditEntitlementByUser(ctx, apiKey.GenfityUserID); err == nil && entitlement != nil && entitlement.CreditBalance != nil {
				if parseFloatPtr(entitlement.CreditBalance) > 0 {
					return runtimeReservation{BillingMode: "credit_package", RequestCredits: 0}, 0, ""
				}
			}
			return runtimeReservation{}, http.StatusPaymentRequired, "insufficient_credit_balance"
		}
		reservedCredits := estimateReservedRequestCredits(creditsPer20k, estimate)
		if err := h.usage.ReserveRequestCredits(ctx, apiKey.GenfityUserID, reservedCredits); err != nil {
			if errors.Is(err, service.ErrInsufficientBalance) {
				return runtimeReservation{}, http.StatusPaymentRequired, "insufficient_credit_balance"
			}
			return runtimeReservation{}, http.StatusInternalServerError, "credit_reservation_failed"
		}
		return runtimeReservation{
			BillingMode:       "credit_package",
			RequestCredits:    reservedCredits,
			CreditPricePer20k: creditsPer20k,
		}, 0, ""

	case "payg":
		if !model.PaygExposed {
			return runtimeReservation{}, http.StatusPaymentRequired, "payg_model_not_published"
		}
		prices := h.models.ListPrices(ctx)
		price := modelPrice(prices, model.ID)
		if price == nil {
			return runtimeReservation{}, http.StatusPaymentRequired, "payg_price_not_configured"
		}
		if !estimate.Bounded && parseFloatPtr(&price.OutputPricePer1M) > 0 {
			return runtimeReservation{}, http.StatusBadRequest, "max_tokens_required"
		}
		reserveUSD := float64(estimate.PromptTokens)/1_000_000*estimatedPromptReservationPrice(price) +
			float64(estimate.CompletionTokens)/1_000_000*parseFloatPtr(&price.OutputPricePer1M)
		if reserveUSD <= 0 {
			// Zero-priced model under PAYG — still require a positive
			// PAYG balance so $0 accounts cannot reach even free models
			// from a PAYG-pinned key. Read PaygUsdBalance from the
			// payg_topup row specifically — see credit comment above.
			if entitlement, err := h.entitlements.GetPaygEntitlementByUser(ctx, apiKey.GenfityUserID); err == nil && entitlement != nil && entitlement.PaygUsdBalance != nil {
				if parseFloatPtr(entitlement.PaygUsdBalance) > 0 {
					return runtimeReservation{BillingMode: "payg_topup", PaygUSD: 0}, 0, ""
				}
			}
			return runtimeReservation{}, http.StatusPaymentRequired, "insufficient_balance"
		}
		if err := h.usage.ReservePaygUsdBalance(ctx, apiKey.GenfityUserID, reserveUSD); err != nil {
			if errors.Is(err, service.ErrInsufficientBalance) {
				return runtimeReservation{}, http.StatusPaymentRequired, "insufficient_balance"
			}
			return runtimeReservation{}, http.StatusInternalServerError, "payg_reservation_failed"
		}
		return runtimeReservation{BillingMode: "payg_topup", PaygUSD: reserveUSD}, 0, ""

	default:
		// Unknown source — DB CHECK constraint should prevent this, but
		// fail closed defensively if a row slipped through.
		return runtimeReservation{}, http.StatusPaymentRequired, "billing_source_not_applicable"
	}
}

// modelCoveredByUnlimited returns true when the model's public id is
// listed in the active subscription's allowedModels. We resolve from
// the live plan snapshot's metadata FIRST (so admin edits propagate to
// every existing subscriber immediately — the "live policy" model),
// and fall back to the entitlement's frozen metadata only when the
// plan snapshot doesn't carry the field. An unlimited plan with empty
// allowedModels covers ANY model (legacy permissive default).
func modelCoveredByUnlimited(subscription *service.ActiveSubscription, model *store.AIModel) bool {
	if subscription == nil || model == nil || subscription.Entitlement == nil {
		return false
	}
	allowed := resolveAllowedModels(subscription)
	if len(allowed) == 0 {
		// Permissive default for legacy unlimited plans.
		return true
	}
	for _, s := range allowed {
		if s == model.PublicModel {
			return true
		}
		if !strings.Contains(model.PublicModel, "/") && strings.EqualFold(s, "genfity/"+model.PublicModel) {
			return true
		}
	}
	return false
}

// requestWillUseUnlimited returns true when this request will be billed
// against the active unlimited subscription (priority 1 in
// tryPriorityBilling). The plan-period and plan-RPD counters should
// only tick for these requests; users with both an unlimited trial and
// a credit_package previously had credit-billed requests for paid
// models eat the trial's MaxRequestsPerPeriod budget — banning them
// well below their real cap.
//
// Mirror tryPriorityBilling priority-1 exactly:
//   - billing_source is "subscription"
//   - subscription is unlimited (pricing_group = "unlimited"/"unlimited_plan")
//   - model is in allowedModels (or allowedModels is empty = permissive)
func requestWillUseUnlimited(apiKey store.APIKey, subscription *service.ActiveSubscription, model *store.AIModel) bool {
	if subscription == nil || subscription.Entitlement == nil || model == nil {
		return false
	}
	source := apiKey.BillingSource
	if source == "" {
		source = "subscription"
	}
	if source != "subscription" {
		return false
	}
	group := resolveSubscriptionPricingGroup(subscription)
	if group == "" {
		group = pricingGroup(subscription)
	}
	if group != "unlimited" && group != "unlimited_plan" {
		return false
	}
	return modelCoveredByUnlimited(subscription, model)
}

// resolveAllowedModels reads allowedModels from the LIVE plan snapshot
// metadata first, falling back to the entitlement's frozen metadata.
// This is the central plumbing for "Live policy": plan edits affect
// existing users on the next request, no manual entitlement re-sync
// required. Returns an empty slice when neither source defines it.
func resolveAllowedModels(subscription *service.ActiveSubscription) []string {
	if subscription == nil {
		return nil
	}
	// Priority 1: live plan snapshot metadata.
	if subscription.Plan != nil && len(subscription.Plan.Metadata) > 0 {
		planMeta := map[string]any{}
		if err := json.Unmarshal(subscription.Plan.Metadata, &planMeta); err == nil {
			if list, ok := extractAllowedModels(planMeta); ok {
				return list
			}
		}
	}
	// Priority 2: entitlement metadata (frozen at purchase, legacy fallback).
	if subscription.Entitlement != nil && len(subscription.Entitlement.Metadata) > 0 {
		entMeta := map[string]any{}
		if err := json.Unmarshal(subscription.Entitlement.Metadata, &entMeta); err == nil {
			if list, ok := extractAllowedModels(entMeta); ok {
				return list
			}
		}
	}
	return nil
}

func extractAllowedModels(meta map[string]any) ([]string, bool) {
	raw, exists := meta["allowedModels"]
	if !exists {
		raw, exists = meta["allowed_models"]
	}
	if !exists {
		return nil, false
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out, true
}

// resolveSubscriptionPricingGroup returns the active pricing group with
// the same Live-policy chain as resolveAllowedModels: live plan metadata
// first, entitlement column / metadata as fallback. Admin flipping a
// plan's pricingGroup propagates to existing subscribers next request.
func resolveSubscriptionPricingGroup(subscription *service.ActiveSubscription) string {
	if subscription == nil {
		return ""
	}
	// Priority 1: live plan snapshot metadata.
	if subscription.Plan != nil && len(subscription.Plan.Metadata) > 0 {
		planMeta := map[string]any{}
		if err := json.Unmarshal(subscription.Plan.Metadata, &planMeta); err == nil {
			if s, ok := planMeta["pricingGroup"].(string); ok && s != "" {
				return s
			}
			if s, ok := planMeta["pricing_group"].(string); ok && s != "" {
				return s
			}
		}
	}
	// Priority 2: entitlement (legacy fallback).
	if subscription.Entitlement != nil {
		if subscription.Entitlement.PricingGroup != nil && *subscription.Entitlement.PricingGroup != "" {
			return *subscription.Entitlement.PricingGroup
		}
		if len(subscription.Entitlement.Metadata) > 0 {
			entMeta := map[string]any{}
			if err := json.Unmarshal(subscription.Entitlement.Metadata, &entMeta); err == nil {
				if s, ok := entMeta["pricingGroup"].(string); ok && s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func (h *GatewayHandler) reserveRuntimeLimits(ctx context.Context, apiKey store.APIKey, subscription *service.ActiveSubscription, limits service.PlanLimits, payload map[string]any, model *store.AIModel) (runtimeReservation, int, string) {
	estimate := estimateRequestTokens(payload, model)
	if h.rateLimit != nil && limits.HasTPM() {
		if err := h.rateLimit.CheckTPM(ctx, apiKey.GenfityUserID, limits.TPMAllowance(estimate.TotalTokens), limits.TPM); err != nil {
			return runtimeReservation{}, http.StatusTooManyRequests, "rate_limit_exceeded"
		}
	}
	// Free-model per-(user,model) token-per-day cap. We reserve the
	// estimated tokens here; FinalizeFreeModelTPD reconciles after the
	// upstream call returns the real usage.
	if h.rateLimit != nil && model != nil && model.IsFree && model.FreeLimitTPD != nil && *model.FreeLimitTPD > 0 {
		if err := h.rateLimit.CheckFreeModelTPD(ctx, apiKey.GenfityUserID, model.PublicModel, estimate.TotalTokens, *model.FreeLimitTPD); err != nil {
			return runtimeReservation{}, http.StatusTooManyRequests, "free_model_tpd_exceeded"
		}
	}

	// PRD v3 Phase 2: 3-priority billing chain. Before the legacy
	// quota/credit path runs, try the new schemas in priority order.
	// The legacy path stays as a fallback for existing
	// subscription-with-token-quota users and legacy credit_package
	// entitlements that haven't been migrated to the new columns.
	//
	//   1. Unlimited — model in subscription's allowedModels → no debit.
	//   2. Request credits — per-model fixed credit cost from
	//      model_credit_cost table → debit credits.
	//   3. PAYG USD — per-1M-token pricing → debit USD.
	//
	// The chain short-circuits: as soon as priority N succeeds,
	// priorities N+1 and below are skipped. This keeps unlimited
	// users from accidentally burning credits or PAYG balance.
	if pri, status, code := h.tryPriorityBilling(ctx, apiKey, subscription, payload, model, estimate); status != 0 || pri.BillingMode != "" {
		return pri, status, code
	}

	var reservation runtimeReservation
	if pricingGroup(subscription) == "credit_package" {
		if balanceAmount(subscription) <= 0 {
			if reservation.QuotaTokens > 0 {
				_ = h.usage.FinalizeQuotaTokens(ctx, apiKey.GenfityUserID, reservation.PeriodStart, reservation.PeriodEnd, reservation.QuotaTokens, 0, false)
			}
			return runtimeReservation{}, http.StatusPaymentRequired, "insufficient_balance"
		}
		prices := h.models.ListPrices(ctx)
		price := modelPrice(prices, model.ID)
		if price != nil {
			if !estimate.Bounded && parseFloatPtr(&price.OutputPricePer1M) > 0 {
				if reservation.QuotaTokens > 0 {
					_ = h.usage.FinalizeQuotaTokens(ctx, apiKey.GenfityUserID, reservation.PeriodStart, reservation.PeriodEnd, reservation.QuotaTokens, 0, false)
				}
				return runtimeReservation{}, http.StatusBadRequest, "max_tokens_required"
			}
			reserveUSD := float64(estimate.PromptTokens)/1_000_000*estimatedPromptReservationPrice(price) + float64(estimate.CompletionTokens)/1_000_000*parseFloatPtr(&price.OutputPricePer1M)
			if reserveUSD > 0 && subscription != nil && subscription.Entitlement != nil {
				if err := h.usage.ReserveCreditBalance(ctx, apiKey.GenfityUserID, subscription.Entitlement.PlanCode, reserveUSD); err != nil {
					if reservation.QuotaTokens > 0 {
						_ = h.usage.FinalizeQuotaTokens(ctx, apiKey.GenfityUserID, reservation.PeriodStart, reservation.PeriodEnd, reservation.QuotaTokens, 0, false)
					}
					if errors.Is(err, service.ErrInsufficientBalance) {
						return runtimeReservation{}, http.StatusPaymentRequired, "insufficient_balance"
					}
					return runtimeReservation{}, http.StatusInternalServerError, "credit_reservation_failed"
				}
				reservation.CreditUSD = reserveUSD
				reservation.CreditPlanCode = subscription.Entitlement.PlanCode
			}
		}
	}
	return reservation, 0, ""
}

func (h *GatewayHandler) finalizeRuntimeReservation(ctx context.Context, apiKey store.APIKey, reservation runtimeReservation, settlement usageSettlement, success bool, countRequest bool) error {
	usedTokens := int64(0)
	actualUSD := 0.0
	actualCredits := 0.0
	if success {
		usedTokens = settlement.TotalTokens
		actualUSD = settlement.TotalCostUSD
		actualCredits = settlement.ActualCredits
	}
	if reservation.QuotaTokens > 0 || countRequest {
		if err := h.usage.FinalizeQuotaTokens(ctx, apiKey.GenfityUserID, reservation.PeriodStart, reservation.PeriodEnd, reservation.QuotaTokens, usedTokens, countRequest); err != nil {
			return err
		}
	}
	if h.rateLimit != nil {
		if reservation.PlanCreditsPerDay > 0 {
			if err := h.rateLimit.FinalizePlanCreditRPD(ctx, apiKey.GenfityUserID, reservation.PlanCode, reservation.PlanCreditsPerDay, actualCredits); err != nil {
				return err
			}
		}
		if reservation.PlanCreditsPerPeriod > 0 {
			if err := h.rateLimit.FinalizePlanCreditsPerPeriod(ctx, apiKey.GenfityUserID, reservation.PlanCreditPeriodKey, reservation.PlanCreditsPerPeriod, actualCredits); err != nil {
				return err
			}
		}
	}
	if reservation.CreditUSD > 0 {
		if err := h.usage.FinalizeCreditBalance(ctx, apiKey.GenfityUserID, reservation.CreditPlanCode, reservation.CreditUSD, actualUSD); err != nil {
			return err
		}
	}
	// PRD v3 Phase 2 — request-credit finalize. creditsPerReq stores the
	// configured price per 20k tokens; runtime billing charges in 20k
	// buckets based on actual successful usage. On failure we release the
	// full reservation back to the balance.
	if reservation.RequestCredits > 0 {
		actual := 0.0
		if success {
			actual = settlement.ActualCredits
		}
		if err := h.usage.FinalizeRequestCredits(ctx, apiKey.GenfityUserID, reservation.RequestCredits, actual); err != nil {
			return err
		}
	}
	// PRD v3 Phase 2 — PAYG USD finalize. actualUSD comes from
	// recordUsage's per-1M-token price calculation; if actualUSD >
	// reservation the extra is absorbed (balance capped at 0 by the
	// store layer). Over-reservation (common case) releases the delta
	// back to the user's balance.
	if reservation.PaygUSD > 0 {
		actual := 0.0
		if success {
			actual = actualUSD
		}
		if err := h.usage.FinalizePaygUsdBalance(ctx, apiKey.GenfityUserID, reservation.PaygUSD, actual); err != nil {
			return err
		}
	}
	return nil
}

func (h *GatewayHandler) recordAndFinalizeRuntime(ctx context.Context, apiKey store.APIKey, subscription *service.ActiveSubscription, model *store.AIModel, route *store.AIModelRoute, reservation runtimeReservation, started time.Time, statusCode int, body []byte, publicModel string) error {
	settlement, err := h.recordUsage(ctx, apiKey, subscription, model, route, &reservation, started, statusCode, body, publicModel)
	if err != nil {
		_ = h.finalizeRuntimeReservation(ctx, apiKey, reservation, usageSettlement{}, false, true)
		return err
	}
	finalizeErr := h.finalizeRuntimeReservation(ctx, apiKey, reservation, settlement, settlement.Success, true)
	if finalizeErr != nil {
		return finalizeErr
	}

	// Push the debit to genfity-app so User.aiGatewayCreditBalance and
	// the AiGatewayCreditLedger stay in sync. Fire-and-forget — failures
	// do not affect the request response. We only call this when the
	// request actually succeeded AND a paid billing schema settled it
	// (free models / unlimited cover are no-ops for the customer
	// balance).
	if settlement.Success && h.callback != nil && h.callback.Enabled() {
		switch reservation.BillingMode {
		case "credit_package":
			if settlement.ActualCredits > 0 {
				h.callback.PostUsageDebitAsync(service.UsageDebitPayload{
					UserID:        apiKey.GenfityUserID,
					RequestID:     settlement.RequestID,
					BillingMode:   "credit_package",
					AmountCredits: settlement.ActualCredits,
					Model:         publicModel,
					Notes:         "gateway debit",
				})
			}
		case "payg_topup":
			if settlement.TotalCostUSD > 0 {
				h.callback.PostUsageDebitAsync(service.UsageDebitPayload{
					UserID:      apiKey.GenfityUserID,
					RequestID:   settlement.RequestID,
					BillingMode: "payg_topup",
					AmountUSD:   settlement.TotalCostUSD,
					Model:       publicModel,
					Notes:       "gateway debit",
				})
			}
		}
	}

	// Bust entitlement cache so next request sees the new balance, not
	// the pre-debit snapshot. Cheap (one Redis DEL); no need to wait.
	if reservation.RequestCredits > 0 || reservation.PaygUSD > 0 || reservation.CreditUSD > 0 {
		h.entitlements.InvalidateUser(ctx, apiKey.GenfityUserID)
	}
	return finalizeErr
}

// recordAndFinalizeAsync runs recordAndFinalizeRuntime in a detached
// goroutine so the request hot path can return as soon as the upstream
// response is forwarded. Uses context.WithoutCancel so the goroutine
// survives request completion. Recovers from panics so a single bug
// can't crash the server. Has its own 30s timeout.
//
// Trade-off: if the goroutine fails, the usage_ledger row + balance
// debit are lost for that request. Acceptable because:
//  1. Reservation already debited the balance optimistically; if finalize
//     fails the only loss is per-row pricing accuracy, not the user's
//     balance.
//  2. Billing reconciliation cron can detect drift via aggregate sums.
//
// Streaming path still uses the sync version because the client-facing
// response is already complete by the time we settle.
func (h *GatewayHandler) recordAndFinalizeAsync(parentCtx context.Context, apiKey store.APIKey, subscription *service.ActiveSubscription, model *store.AIModel, route *store.AIModelRoute, reservation runtimeReservation, started time.Time, statusCode int, body []byte, publicModel string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), 30*time.Second)
	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				zerolog.Ctx(parentCtx).Error().
					Interface("panic", r).
					Str("public_model", publicModel).
					Str("api_key_id", apiKey.ID.String()).
					Msg("async settlement panicked; reservation may be inconsistent")
			}
		}()
		if err := h.recordAndFinalizeRuntime(ctx, apiKey, subscription, model, route, reservation, started, statusCode, body, publicModel); err != nil {
			zerolog.Ctx(parentCtx).Warn().
				Err(err).
				Str("public_model", publicModel).
				Str("api_key_id", apiKey.ID.String()).
				Msg("async settlement failed")
		}
	}()
}

func (h *GatewayHandler) streamUpstreamResponse(w http.ResponseWriter, resp *http.Response, publicModel string) []byte {
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.Header().Del("Content-Length")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)

	captured := tailCaptureBuffer{limit: maxStreamCaptureBytes}
	rewriter := sseRewriteBuffer{}
	buf := make([]byte, 32*1024)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			// Network reads split SSE payloads arbitrarily, so buffer until
			// we have one or more complete events before sanitizing/rewriting.
			safe := rewriter.append(chunk, publicModel)
			if len(safe) > 0 {
				_, _ = w.Write(safe)
			}
			_, _ = captured.Write(chunk)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if safe := rewriter.flush(publicModel); len(safe) > 0 {
				_, _ = w.Write(safe)
				if flusher != nil {
					flusher.Flush()
				}
			}
			break
		}
	}
	return captured.Bytes()
}

func (h *GatewayHandler) clientForRouter(ctx context.Context, routerInstanceCode string) (*router.CLIProxyClient, bool) {
	code := strings.TrimSpace(routerInstanceCode)
	if h.routers == nil || code == "" {
		return h.cliProxy, true
	}
	instance, err := h.routers.GetInstance(ctx, code)
	if err != nil || instance == nil || strings.TrimSpace(instance.InternalBaseURL) == "" || instance.Status != "active" {
		return nil, false
	}
	timeout := h.routerTimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return router.NewCLIProxyClient(instance.InternalBaseURL, h.routerAPIKey, timeout), true
}

// readBodyWithKeepalive reads the upstream response body while sending periodic
// newlines to the downstream client to prevent proxy idle timeouts (Cloudflare 120s).
// It strips keepalive newlines from the captured body so callers get clean content.
func readBodyWithKeepalive(w http.ResponseWriter, body io.ReadCloser, interval time.Duration) []byte {
	if interval <= 0 {
		interval = 25 * time.Second
	}
	flusher, canFlush := w.(http.Flusher)

	var (
		result []byte
		done   = make(chan struct{})
		mu     sync.Mutex
	)

	go func() {
		defer close(done)
		raw, _ := io.ReadAll(body)
		mu.Lock()
		result = raw
		mu.Unlock()
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			mu.Lock()
			out := bytes.TrimLeft(result, "\n\r ")
			mu.Unlock()
			return out
		case <-ticker.C:
			if canFlush {
				_, _ = w.Write([]byte("\n"))
				flusher.Flush()
			}
		}
	}
}

// startPreResponseKeepalive starts sending SSE-style keep-alive comments to the
// client before the upstream response arrives. This prevents Cloudflare from
// timing out (120s) while the gateway waits for CLIProxyAPI to finish combo
// fallback or model reasoning. Returns a stop function that must be called
// once the upstream response is received.
func startPreResponseKeepalive(w http.ResponseWriter, interval time.Duration) func() {
	if interval <= 0 {
		interval = 25 * time.Second
	}
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		return func() {}
	}

	// Commit headers early with streaming-compatible settings so keepalive
	// bytes actually reach Cloudflare.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	stopCh := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				_, _ = w.Write([]byte(": keep-alive\n\n"))
				flusher.Flush()
			}
		}
	}()

	return func() {
		once.Do(func() { close(stopCh) })
	}
}

// isRetriableStatus returns true when the upstream error code should trigger
// a combo fallback to the next entry.
func isRetriableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429 quota/rate limit
		http.StatusServiceUnavailable,  // 503
		http.StatusBadGateway,          // 502
		http.StatusGatewayTimeout,      // 504
		http.StatusInternalServerError: // 500
		return true
	}
	return false
}

// isRetriableTrigger checks whether an error body contains one of the
// combo entry TriggerOn keywords (e.g. "quota_exceeded", "rate_limit", "error").
func isRetriableTrigger(body []byte, triggers []string) bool {
	if len(triggers) == 0 {
		return true
	}
	text := strings.ToLower(string(body))
	for _, t := range triggers {
		if strings.Contains(text, strings.ToLower(t)) {
			return true
		}
	}
	return false
}

// --- ListModels ---
//
// Returns the model list visible to the caller, filtered by their API
// key's billing_source pin:
//
//   - "credit"       → only models with an ACTIVE credit cost row.
//   - "subscription" → only models in the user's active entitlement
//                      allowedModels (empty allowedModels = all models).
//   - "payg"         → only models with a configured PAYG price.
//
// A model that doesn't pass the filter is hidden so customers don't
// see options they can't actually use with this key.

func (h *GatewayHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	apiKey := middleware.GetAPIKey(ctx)

	source := apiKey.BillingSource
	if source == "" {
		source = "subscription"
	}

	allModels := h.models.ListModels(ctx)
	prices := h.models.ListPrices(ctx)

	// Lookup user's active subscription (entitlement + live plan snapshot)
	// only when the key is pinned to subscription. We need the live plan
	// snapshot because allowedModels is resolved from plan metadata (not
	// entitlement metadata) so admin edits propagate to existing
	// subscribers — see resolveAllowedModels for the full lookup chain.
	var subscription *service.ActiveSubscription
	if source == "subscription" {
		if sub, err := h.entitlements.CheckActiveSubscription(ctx, apiKey.GenfityUserID); err == nil {
			subscription = sub
		}
	}

	allowsViaSubscription := func(m store.AIModel) bool {
		if subscription == nil || subscription.Entitlement == nil {
			return false
		}
		// Plan must be unlimited group; otherwise subscription doesn't apply.
		// Resolve pricing_group from the live plan first so admin edits
		// (e.g. flipping pricingGroup on a plan) take effect immediately.
		group := resolveSubscriptionPricingGroup(subscription)
		if group != "unlimited" && group != "unlimited_plan" {
			return false
		}
		// Empty allowedModels = all models allowed (legacy permissive default).
		allowed := resolveAllowedModels(subscription)
		if len(allowed) == 0 {
			return true
		}
		for _, s := range allowed {
			if s == m.PublicModel {
				return true
			}
		}
		return false
	}

	allowsViaCredit := func(m store.AIModel) bool {
		cost, err := h.models.GetModelCreditCost(ctx, m.PublicModel)
		if err != nil || cost == nil {
			return false
		}
		return cost.IsActive
	}

	allowsViaPayg := func(m store.AIModel) bool {
		if !m.PaygExposed {
			return false
		}
		price := modelPrice(prices, m.ID)
		return price != nil
	}

	var list []map[string]any
	for _, m := range allModels {
		if m.Status != "active" {
			continue
		}

		var visible bool
		switch source {
		case "subscription":
			visible = allowsViaSubscription(m)
		case "credit":
			visible = allowsViaCredit(m)
		case "payg":
			visible = allowsViaPayg(m)
		default:
			// Unknown source — fail closed. Constraint should already
			// reject this at insert time but be defensive.
			visible = false
		}

		if !visible {
			continue
		}

		list = append(list, map[string]any{
			"id":       m.PublicModel,
			"object":   "model",
			"created":  m.CreatedAt.Unix(),
			"owned_by": "genfity",
		})
	}
	respondJSON(w, http.StatusOK, map[string]any{"object": "list", "data": list})
}

// --- Embeddings ---

func (h *GatewayHandler) Embeddings(w http.ResponseWriter, r *http.Request) {
	apiKey := middleware.GetAPIKey(r.Context())
	ctx := r.Context()
	started := time.Now().UTC()

	var payload map[string]any
	if err := decodeRuntimePayload(w, r, &payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	publicModel, ok := payload["model"].(string)
	if !ok || publicModel == "" {
		respondError(w, http.StatusBadRequest, "missing_model")
		return
	}

	subscription, err := h.resolveSubscription(ctx, apiKey.GenfityUserID)
	if err != nil {
		respondError(w, http.StatusPaymentRequired, mapSubscriptionError(ctx, err))
		return
	}
	if shouldEnforceUnlimitedAllowlist(apiKey) && isUnlimitedSubscription(subscription) && !entitlementAllowsModel(subscription, publicModel) {
		respondError(w, http.StatusPaymentRequired, "model_not_in_unlimited_plan")
		return
	}

	limits := service.PlanLimitsFromSnapshot(subscriptionPlan(subscription))

	route, model, err := h.models.ResolveRouteByPublicModel(ctx, publicModel)
	if err != nil {
		code := "model_not_allowed"
		if errors.Is(err, service.ErrModelRetired) {
			code = "model_retired"
		}
		respondError(w, http.StatusBadRequest, code)
		return
	}
	publicModel = model.PublicModel
	// Enforce the same plan/free-model caps as Messages/ChatCompletions.
	// Without this, an embeddings-only key could blow past max_requests
	// _per_period and free_model_rpd because the dashboard count + the
	// gateway counter are completely bypassed.
	if h.rateLimit != nil && limits.HasRPM() {
		if err := h.rateLimit.CheckRPM(ctx, apiKey.GenfityUserID, limits.RPM); err != nil {
			h.recordFailedRequest(ctx, apiKey, publicModel, "rate_limit_exceeded", http.StatusTooManyRequests, started)
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
	}
	errCode, status, preCounters := h.applyPreRequestLimits(ctx, apiKey, subscription, limits, model)
	if status != 0 {
		h.recordFailedRequest(ctx, apiKey, publicModel, errCode, status, started)
		respondError(w, status, errCode)
		return
	}
	defer func() {
		bgCtx := context.Background()
		preCounters.rollback(bgCtx)
	}()

	upstreamPayload, err := clonePayload(payload)
	if err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("clone embeddings payload failed")
		respondError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	upstreamPayload["model"] = route.RouterModel

	client, ok := h.clientForRouter(ctx, route.RouterInstanceCode)
	if !ok {
		respondError(w, http.StatusBadGateway, "router_unavailable")
		return
	}
	resp, err := client.Embeddings(ctx, upstreamPayload)
	if err != nil {
		respondError(w, http.StatusBadGateway, "upstream_error")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// Period/RPD/free-model tick only on real success — 4xx/5xx and
	// in-body provider errors leave the deferred rollback to release
	// the slot.
	if shouldCountAsSuccess(resp.StatusCode, body) {
		preCounters.commit(ctx)
	}
	h.recordUsageWithLegacyBilling(ctx, apiKey, subscription, model, route, started, resp.StatusCode, body, publicModel)
	h.writeUpstreamResponse(w, resp, body, publicModel)
}

// --- Image Generation ---

func (h *GatewayHandler) ImagesGenerations(w http.ResponseWriter, r *http.Request) {
	apiKey := middleware.GetAPIKey(r.Context())
	ctx := r.Context()
	started := time.Now().UTC()

	var payload map[string]any
	if err := decodeRuntimePayload(w, r, &payload); err != nil {
		h.recordFailedRequest(ctx, apiKey, "", "invalid_json", http.StatusBadRequest, started)
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	publicModel, _ := payload["model"].(string)
	if publicModel == "" {
		h.recordFailedRequest(ctx, apiKey, "", "missing_model", http.StatusBadRequest, started)
		respondError(w, http.StatusBadRequest, "missing_model")
		return
	}

	subscription, err := h.resolveSubscription(ctx, apiKey.GenfityUserID)
	if err != nil {
		ec := mapSubscriptionError(ctx, err)
		h.recordFailedRequest(ctx, apiKey, publicModel, ec, http.StatusPaymentRequired, started)
		respondError(w, http.StatusPaymentRequired, ec)
		return
	}

	limits := service.PlanLimitsFromSnapshot(subscriptionPlan(subscription))

	if h.rateLimit != nil && limits.HasRPM() {
		if err := h.rateLimit.CheckRPM(ctx, apiKey.GenfityUserID, limits.RPM); err != nil {
			h.recordFailedRequest(ctx, apiKey, publicModel, "rate_limit_exceeded", http.StatusTooManyRequests, started)
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
	}

	// Soft-lookup model in catalog for billing enforcement.
	// If the model is not in the catalog, we pass through without billing
	// (backward compat for models not yet seeded).
	var model *store.AIModel
	if m, lookupErr := h.models.GetModelByPublicName(ctx, publicModel); lookupErr == nil {
		model = m
	}

	var reservation runtimeReservation
	if model != nil && model.Status == "active" {
		estimate := tokenReservationEstimate{TotalTokens: 0, Bounded: true}
		res, billingStatus, billingCode := h.tryPriorityBilling(ctx, apiKey, subscription, payload, model, estimate)
		if billingStatus != 0 {
			h.recordFailedRequest(ctx, apiKey, publicModel, billingCode, billingStatus, started)
			respondError(w, billingStatus, billingCode)
			return
		}
		reservation = res
	}
	settled := false
	defer func() {
		if settled || reservation.BillingMode == "" {
			return
		}
		bgCtx := context.Background()
		_ = h.finalizeRuntimeReservation(bgCtx, apiKey, reservation, usageSettlement{}, false, false)
	}()

	client, ok := h.clientForRouter(ctx, "")
	if !ok {
		respondError(w, http.StatusBadGateway, "router_unavailable")
		return
	}
	resp, err := client.ImagesGenerations(ctx, payload)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("image generation upstream failed")
		respondError(w, http.StatusBadGateway, "upstream_error")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	success := resp.StatusCode >= 200 && resp.StatusCode < 400
	if success && reservation.BillingMode != "" {
		settled = true
		bgCtx := context.Background()
		_ = h.finalizeRuntimeReservation(bgCtx, apiKey, reservation, usageSettlement{}, true, true)
	}

	finished := time.Now().UTC()
	latencyMs := int32(finished.Sub(started).Milliseconds())
	usageStatus := "success"
	var ec *string
	if resp.StatusCode >= 400 {
		usageStatus = "failed"
		code := "upstream_error"
		ec = &code
	}
	totalCost := "0"
	if success && reservation.RequestCredits > 0 {
		totalCost = strconv.FormatFloat(reservation.RequestCredits, 'f', 4, 64)
	}
	apiKeyID := apiKey.ID
	entry := store.UsageLedgerEntry{
		ID:              uuid.New(),
		RequestID:       uuid.New().String(),
		GenfityUserID:   apiKey.GenfityUserID,
		GenfityTenantID: apiKey.GenfityTenantID,
		APIKeyID:        &apiKeyID,
		PublicModel:     publicModel,
		Status:          usageStatus,
		ErrorCode:       ec,
		LatencyMS:       &latencyMs,
		StartedAt:       started,
		FinishedAt:      &finished,
		InputCost:       "0",
		OutputCost:      "0",
		TotalCost:       totalCost,
	}
	if _, recordErr := h.usage.Record(ctx, entry); recordErr != nil {
		zerolog.Ctx(ctx).Warn().Err(recordErr).Msg("failed to record image generation usage")
	}

	h.writeUpstreamResponse(w, resp, body, publicModel)
}

func (h *GatewayHandler) ImagesEdits(w http.ResponseWriter, r *http.Request) {
	apiKey := middleware.GetAPIKey(r.Context())
	ctx := r.Context()
	started := time.Now().UTC()

	var payload map[string]any
	if err := decodeRuntimePayload(w, r, &payload); err != nil {
		h.recordFailedRequest(ctx, apiKey, "", "invalid_json", http.StatusBadRequest, started)
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	publicModel, _ := payload["model"].(string)
	if publicModel == "" {
		publicModel = "gpt-image-2"
	}

	subscription, err := h.resolveSubscription(ctx, apiKey.GenfityUserID)
	if err != nil {
		ec := mapSubscriptionError(ctx, err)
		h.recordFailedRequest(ctx, apiKey, publicModel, ec, http.StatusPaymentRequired, started)
		respondError(w, http.StatusPaymentRequired, ec)
		return
	}

	limits := service.PlanLimitsFromSnapshot(subscriptionPlan(subscription))

	if h.rateLimit != nil && limits.HasRPM() {
		if err := h.rateLimit.CheckRPM(ctx, apiKey.GenfityUserID, limits.RPM); err != nil {
			h.recordFailedRequest(ctx, apiKey, publicModel, "rate_limit_exceeded", http.StatusTooManyRequests, started)
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
	}

	var model *store.AIModel
	if m, lookupErr := h.models.GetModelByPublicName(ctx, publicModel); lookupErr == nil {
		model = m
	}

	var reservation runtimeReservation
	if model != nil && model.Status == "active" {
		estimate := tokenReservationEstimate{TotalTokens: 0, Bounded: true}
		res, billingStatus, billingCode := h.tryPriorityBilling(ctx, apiKey, subscription, payload, model, estimate)
		if billingStatus != 0 {
			h.recordFailedRequest(ctx, apiKey, publicModel, billingCode, billingStatus, started)
			respondError(w, billingStatus, billingCode)
			return
		}
		reservation = res
	}
	settled := false
	defer func() {
		if settled || reservation.BillingMode == "" {
			return
		}
		bgCtx := context.Background()
		_ = h.finalizeRuntimeReservation(bgCtx, apiKey, reservation, usageSettlement{}, false, false)
	}()

	client, ok := h.clientForRouter(ctx, "")
	if !ok {
		respondError(w, http.StatusBadGateway, "router_unavailable")
		return
	}
	resp, err := client.ImagesEdits(ctx, payload)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("image edit upstream failed")
		respondError(w, http.StatusBadGateway, "upstream_error")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	success := resp.StatusCode >= 200 && resp.StatusCode < 400
	if success && reservation.BillingMode != "" {
		settled = true
		bgCtx := context.Background()
		_ = h.finalizeRuntimeReservation(bgCtx, apiKey, reservation, usageSettlement{}, true, true)
	}

	finished := time.Now().UTC()
	latencyMs := int32(finished.Sub(started).Milliseconds())
	usageStatus := "success"
	var ec2 *string
	if resp.StatusCode >= 400 {
		usageStatus = "failed"
		code := "upstream_error"
		ec2 = &code
	}
	totalCost := "0"
	if success && reservation.RequestCredits > 0 {
		totalCost = strconv.FormatFloat(reservation.RequestCredits, 'f', 4, 64)
	}
	apiKeyID := apiKey.ID
	entry := store.UsageLedgerEntry{
		ID:              uuid.New(),
		RequestID:       uuid.New().String(),
		GenfityUserID:   apiKey.GenfityUserID,
		GenfityTenantID: apiKey.GenfityTenantID,
		APIKeyID:        &apiKeyID,
		PublicModel:     publicModel,
		Status:          usageStatus,
		ErrorCode:       ec2,
		LatencyMS:       &latencyMs,
		StartedAt:       started,
		FinishedAt:      &finished,
		InputCost:       "0",
		OutputCost:      "0",
		TotalCost:       totalCost,
	}
	if _, recordErr := h.usage.Record(ctx, entry); recordErr != nil {
		zerolog.Ctx(ctx).Warn().Err(recordErr).Msg("failed to record image edit usage")
	}

	h.writeUpstreamResponse(w, resp, body, publicModel)
}

// --- Anthropic-compatible Messages with virtual combo fallback ---

func (h *GatewayHandler) Messages(w http.ResponseWriter, r *http.Request) {
	apiKey := middleware.GetAPIKey(r.Context())
	ctx := r.Context()
	started := time.Now().UTC()

	var payload map[string]any
	if err := decodeRuntimePayload(w, r, &payload); err != nil {
		h.recordFailedRequest(ctx, apiKey, "", "invalid_json", http.StatusBadRequest, started)
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	publicModel, ok := payload["model"].(string)
	if !ok || publicModel == "" {
		h.recordFailedRequest(ctx, apiKey, "", "missing_model", http.StatusBadRequest, started)
		respondError(w, http.StatusBadRequest, "missing_model")
		return
	}

	subscription, err := h.resolveSubscription(ctx, apiKey.GenfityUserID)
	if err != nil {
		ec := mapSubscriptionError(ctx, err)
		h.recordFailedRequest(ctx, apiKey, publicModel, ec, http.StatusPaymentRequired, started)
		respondError(w, http.StatusPaymentRequired, ec)
		return
	}
	if shouldEnforceUnlimitedAllowlist(apiKey) && isUnlimitedSubscription(subscription) && !entitlementAllowsModel(subscription, publicModel) {
		h.recordFailedRequest(ctx, apiKey, publicModel, "model_not_in_unlimited_plan", http.StatusPaymentRequired, started)
		respondError(w, http.StatusPaymentRequired, "model_not_in_unlimited_plan")
		return
	}

	limits := service.PlanLimitsFromSnapshot(subscriptionPlan(subscription))

	route, model, err := h.models.ResolveRouteByPublicModel(ctx, publicModel)
	if err != nil {
		code := "model_not_allowed"
		if errors.Is(err, service.ErrModelRetired) {
			code = "model_retired"
		}
		h.recordFailedRequest(ctx, apiKey, publicModel, code, http.StatusBadRequest, started)
		respondError(w, http.StatusBadRequest, code)
		return
	}
	publicModel = model.PublicModel

	if h.rateLimit != nil && limits.HasRPM() {
		if err := h.rateLimit.CheckRPM(ctx, apiKey.GenfityUserID, limits.RPM); err != nil {
			h.recordFailedRequest(ctx, apiKey, publicModel, "rate_limit_exceeded", http.StatusTooManyRequests, started)
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
	}
	errCode, status, preCounters := h.applyPreRequestLimits(ctx, apiKey, subscription, limits, model)
	if status != 0 {
		h.recordFailedRequest(ctx, apiKey, publicModel, errCode, status, started)
		respondError(w, status, errCode)
		return
	}
	// Roll back the period/RPD/free-model counters on any abnormal exit
	// (concurrency rejected, billing reservation failed, payload clone
	// failed, every candidate upstream failed). The success/non-retriable
	// branches below call preCounters.commit(ctx) right before returning, at
	// which point this defer becomes a no-op.
	defer func() {
		bgCtx := context.Background()
		preCounters.rollback(bgCtx)
	}()
	release := func() {}
	accountID := apiKey.GenfityUserID
	if h.rateLimit != nil && limits.HasConcurrency() {
		var acquireErr error
		release, acquireErr = h.rateLimit.AcquireConcurrency(ctx, accountID, limits.ConcurrentLimit)
		if acquireErr != nil {
			h.recordFailedRequest(ctx, apiKey, publicModel, "concurrency_limit_exceeded", http.StatusTooManyRequests, started)
			respondError(w, http.StatusTooManyRequests, "concurrency_limit_exceeded")
			return
		}
	}
	defer release()

	reservation, statusCode, errorCode := h.reserveRuntimeLimits(ctx, apiKey, subscription, limits, payload, model)
	if statusCode != 0 {
		h.recordFailedRequest(ctx, apiKey, publicModel, errorCode, statusCode, started)
		respondError(w, statusCode, errorCode)
		return
	}
	settled := false
	defer func() {
		if settled {
			return
		}
		if !reservationNeedsFinalize(reservation) {
			return
		}
		// Release reservations on early/abnormal exit (panic, ctx cancel before settlement).
		bgCtx := context.Background()
		_ = h.finalizeRuntimeReservation(bgCtx, apiKey, reservation, usageSettlement{}, false, false)
	}()

	type candidate struct {
		routerInstanceCode string
		routerModel        string
		triggerOn          []string
	}

	candidates := []candidate{{routerInstanceCode: route.RouterInstanceCode, routerModel: route.RouterModel}}

	// Combo resolution moved to CLIProxyAPI (PRD §3.3) — the gateway no
	// longer iterates fallback entries here. The slice keeps the same shape
	// so the downstream retry/status-code logic stays untouched; the list
	// simply has a single candidate now.

	streamRequested := isStreamingPayload(payload)
	var lastResp *http.Response
	var lastBody []byte
	var lastStatusCode int

	// Only streaming responses can safely receive pre-response keepalive bytes.
	// For non-streaming JSON responses, writing anything before the final body
	// corrupts the response contract.
	stopKeepalive := func() {}
	if streamRequested {
		stopKeepalive = startPreResponseKeepalive(w, 25*time.Second)
	}

	for _, cand := range candidates {
		p, cloneErr := clonePayload(payload)
		if cloneErr != nil {
			stopKeepalive()
			zerolog.Ctx(ctx).Error().Err(cloneErr).Msg("clone messages payload failed")
			respondError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		p["model"] = cand.routerModel
		if streamRequested {
			ensureStreamUsageOption(p)
		} else {
			ensureNonStreaming(p)
		}

		client, ok := h.clientForRouter(ctx, cand.routerInstanceCode)
		if !ok {
			continue
		}
		resp, callErr := client.Messages(ctx, p)
		if callErr != nil {
			zerolog.Ctx(ctx).Warn().Err(callErr).
				Str("router_instance_code", cand.routerInstanceCode).
				Str("router_model", cand.routerModel).
				Msg("messages upstream candidate failed")
			continue
		}

		statusCode := resp.StatusCode
		if streamRequested && statusCode < 400 {
			if stopKeepalive != nil {
				stopKeepalive()
			}
			effectiveRoute := &store.AIModelRoute{
				ID:                 route.ID,
				ModelID:            route.ModelID,
				RouterInstanceCode: cand.routerInstanceCode,
				RouterModel:        cand.routerModel,
				Status:             route.Status,
				CreatedAt:          route.CreatedAt,
			}
			body := h.streamUpstreamResponse(w, resp, publicModel)
			resp.Body.Close()
			if err := h.recordAndFinalizeRuntime(ctx, apiKey, subscription, model, effectiveRoute, reservation, started, statusCode, body, publicModel); err != nil {
				zerolog.Ctx(ctx).Error().Err(err).
					Str("public_model", publicModel).
					Str("router_instance_code", cand.routerInstanceCode).
					Str("router_model", cand.routerModel).
					Str("api_key_id", apiKey.ID.String()).
					Msg("streaming settlement failed; quota/credit reservation may be inconsistent")
			}
			settled = true
			// Streaming HTTP 200 doesn't guarantee a real completion — the
			// upstream can emit `data: {"error": ...}` or close mid-stream
			// before any content lands. Use the same shouldCountAsSuccess
			// gate as the non-streaming branch so a provider hiccup or
			// truncated stream doesn't burn the user's RPD slot. Without
			// this check, every failed streaming attempt ticked the
			// counter even though recordUsage flagged the row as failed.
			if shouldCountAsSuccess(statusCode, body) {
				preCounters.commit(ctx)
			}
			return
		}

		// Non-streaming success: read body with keepalive to prevent
		// Cloudflare idle timeout (120s).
		if !streamRequested && statusCode < 400 {
			effectiveRoute := &store.AIModelRoute{
				ID:                 route.ID,
				ModelID:            route.ModelID,
				RouterInstanceCode: cand.routerInstanceCode,
				RouterModel:        cand.routerModel,
				Status:             route.Status,
				CreatedAt:          route.CreatedAt,
			}
			body := readBodyWithKeepalive(w, resp.Body, 25*time.Second)
			resp.Body.Close()
			if err := h.recordAndFinalizeRuntime(ctx, apiKey, subscription, model, effectiveRoute, reservation, started, statusCode, body, publicModel); err != nil {
				zerolog.Ctx(ctx).Error().Err(err).
					Str("public_model", publicModel).
					Str("router_instance_code", cand.routerInstanceCode).
					Str("router_model", cand.routerModel).
					Str("api_key_id", apiKey.ID.String()).
					Msg("non-streaming settlement failed")
			}
			settled = true
			if shouldCountAsSuccess(statusCode, body) {
				preCounters.commit(ctx)
			}
			// Rewrite the response "model" field to the requested public
			// model so a disguised combo's real upstream (e.g. minimax)
			// never leaks to the customer.
			outBody := rewriteResponseModel(body, statusCode, publicModel)
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			if len(outBody) != len(body) {
				w.Header().Set("Content-Length", strconv.Itoa(len(outBody)))
			}
			w.WriteHeader(statusCode)
			_, _ = w.Write(outBody)
			return
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if !isRetriableStatus(statusCode) || !isRetriableTrigger(body, cand.triggerOn) {
			effectiveRoute := &store.AIModelRoute{
				ID:                 route.ID,
				ModelID:            route.ModelID,
				RouterInstanceCode: cand.routerInstanceCode,
				RouterModel:        cand.routerModel,
				Status:             route.Status,
				CreatedAt:          route.CreatedAt,
			}
			if err := h.recordAndFinalizeRuntime(ctx, apiKey, subscription, model, effectiveRoute, reservation, started, statusCode, body, publicModel); err != nil {
				respondError(w, http.StatusInternalServerError, "settlement_failed")
				return
			}
			settled = true
			// Period/RPD/free-model counters tick ONLY on real success.
			// 4xx/5xx provider errors and in-body provider errors leave
			// the deferred rollback to release the slot.
			if shouldCountAsSuccess(statusCode, body) {
				preCounters.commit(ctx)
			}
			safeBody := sanitizeErrorBody(body, statusCode)
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			if len(safeBody) != len(body) {
				w.Header().Set("Content-Length", strconv.Itoa(len(safeBody)))
			}
			w.WriteHeader(statusCode)
			_, _ = w.Write(safeBody)
			return
		}

		lastResp = resp
		lastBody = body
		lastStatusCode = statusCode
	}

	if lastResp != nil {
		if stopKeepalive != nil {
			stopKeepalive()
		}
		if err := h.recordAndFinalizeRuntime(ctx, apiKey, subscription, model, route, reservation, started, lastStatusCode, lastBody, publicModel); err != nil {
			respondError(w, http.StatusInternalServerError, "settlement_failed")
			return
		}
		settled = true
		if shouldCountAsSuccess(lastStatusCode, lastBody) {
			preCounters.commit(ctx)
		}
		safeLastBody := sanitizeErrorBody(lastBody, lastStatusCode)
		for k, v := range lastResp.Header {
			w.Header()[k] = v
		}
		if len(safeLastBody) != len(lastBody) {
			w.Header().Set("Content-Length", strconv.Itoa(len(safeLastBody)))
		}
		w.WriteHeader(lastStatusCode)
		_, _ = w.Write(safeLastBody)
		return
	}

	if stopKeepalive != nil {
		stopKeepalive()
	}
	// All candidates failed without ever reaching upstream successfully —
	// release the period slot so the user isn't penalized for our outage.
	// preCounters rollback runs via the deferred call above.
	if err := h.finalizeRuntimeReservation(ctx, apiKey, reservation, usageSettlement{}, false, true); err != nil {
		respondError(w, http.StatusInternalServerError, "settlement_failed")
		return
	}
	settled = true
	respondError(w, http.StatusBadGateway, "all_candidates_failed")
}

func (h *GatewayHandler) CountMessageTokens(w http.ResponseWriter, r *http.Request) {
	apiKey := middleware.GetAPIKey(r.Context())
	ctx := r.Context()

	var payload map[string]any
	if err := decodeRuntimePayload(w, r, &payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	publicModel, ok := payload["model"].(string)
	if !ok || publicModel == "" {
		respondError(w, http.StatusBadRequest, "missing_model")
		return
	}

	subscription, err := h.resolveSubscription(ctx, apiKey.GenfityUserID)
	if err != nil {
		respondError(w, http.StatusPaymentRequired, mapSubscriptionError(ctx, err))
		return
	}
	if shouldEnforceUnlimitedAllowlist(apiKey) && isUnlimitedSubscription(subscription) && !entitlementAllowsModel(subscription, publicModel) {
		respondError(w, http.StatusPaymentRequired, "model_not_in_unlimited_plan")
		return
	}

	// Enforce RPM only — count_tokens is a preflight helper, so charging
	// it against the period cap would punish well-behaved clients that
	// pre-size their context. RPM still protects the upstream from spam.
	limits := service.PlanLimitsFromSnapshot(subscriptionPlan(subscription))
	if h.rateLimit != nil && limits.HasRPM() {
		if err := h.rateLimit.CheckRPM(ctx, apiKey.GenfityUserID, limits.RPM); err != nil {
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
	}

	route, resolvedModel, err := h.models.ResolveRouteByPublicModel(ctx, publicModel)
	if err != nil {
		code := "model_not_allowed"
		if errors.Is(err, service.ErrModelRetired) {
			code = "model_retired"
		}
		respondError(w, http.StatusBadRequest, code)
		return
	}
	publicModel = resolvedModel.PublicModel
	upstreamPayload, err := clonePayload(payload)
	if err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("clone count tokens payload failed")
		respondError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	upstreamPayload["model"] = route.RouterModel

	client, ok := h.clientForRouter(ctx, route.RouterInstanceCode)
	if !ok {
		respondError(w, http.StatusBadGateway, "router_unavailable")
		return
	}
	resp, err := client.CountMessageTokens(ctx, upstreamPayload)
	if err != nil {
		respondError(w, http.StatusBadGateway, "upstream_error")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotImplemented {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"input_tokens":-1}`))
		return
	}
	h.writeUpstreamResponse(w, resp, body, publicModel)
}

// --- ChatCompletions with virtual combo fallback ---

func (h *GatewayHandler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	apiKey := middleware.GetAPIKey(r.Context())
	ctx := r.Context()
	started := time.Now().UTC()

	var payload map[string]any
	if err := decodeRuntimePayload(w, r, &payload); err != nil {
		h.recordFailedRequest(ctx, apiKey, "", "invalid_json", http.StatusBadRequest, started)
		respondError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	publicModel, ok := payload["model"].(string)
	if !ok || publicModel == "" {
		h.recordFailedRequest(ctx, apiKey, "", "missing_model", http.StatusBadRequest, started)
		respondError(w, http.StatusBadRequest, "missing_model")
		return
	}

	subscription, err := h.resolveSubscription(ctx, apiKey.GenfityUserID)
	if err != nil {
		ec := mapSubscriptionError(ctx, err)
		h.recordFailedRequest(ctx, apiKey, publicModel, ec, http.StatusPaymentRequired, started)
		respondError(w, http.StatusPaymentRequired, ec)
		return
	}
	if shouldEnforceUnlimitedAllowlist(apiKey) && isUnlimitedSubscription(subscription) && !entitlementAllowsModel(subscription, publicModel) {
		h.recordFailedRequest(ctx, apiKey, publicModel, "model_not_in_unlimited_plan", http.StatusPaymentRequired, started)
		respondError(w, http.StatusPaymentRequired, "model_not_in_unlimited_plan")
		return
	}

	limits := service.PlanLimitsFromSnapshot(subscriptionPlan(subscription))

	route, model, err := h.models.ResolveRouteByPublicModel(ctx, publicModel)
	if err != nil {
		code := "model_not_allowed"
		if errors.Is(err, service.ErrModelRetired) {
			code = "model_retired"
		}
		h.recordFailedRequest(ctx, apiKey, publicModel, code, http.StatusBadRequest, started)
		respondError(w, http.StatusBadRequest, code)
		return
	}
	publicModel = model.PublicModel

	if h.rateLimit != nil && limits.HasRPM() {
		if err := h.rateLimit.CheckRPM(ctx, apiKey.GenfityUserID, limits.RPM); err != nil {
			h.recordFailedRequest(ctx, apiKey, publicModel, "rate_limit_exceeded", http.StatusTooManyRequests, started)
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
	}
	errCode, status, preCounters := h.applyPreRequestLimits(ctx, apiKey, subscription, limits, model)
	if status != 0 {
		h.recordFailedRequest(ctx, apiKey, publicModel, errCode, status, started)
		respondError(w, status, errCode)
		return
	}
	// Roll back the period/RPD/free-model counters on any abnormal exit
	// (concurrency rejected, billing reservation failed, payload clone
	// failed, every candidate upstream failed). The success/non-retriable
	// branches below call preCounters.commit(ctx) right before returning, at
	// which point this defer becomes a no-op.
	defer func() {
		bgCtx := context.Background()
		preCounters.rollback(bgCtx)
	}()
	release := func() {}
	accountID := apiKey.GenfityUserID
	if h.rateLimit != nil && limits.HasConcurrency() {
		var acquireErr error
		release, acquireErr = h.rateLimit.AcquireConcurrency(ctx, accountID, limits.ConcurrentLimit)
		if acquireErr != nil {
			h.recordFailedRequest(ctx, apiKey, publicModel, "concurrency_limit_exceeded", http.StatusTooManyRequests, started)
			respondError(w, http.StatusTooManyRequests, "concurrency_limit_exceeded")
			return
		}
	}
	defer release()

	reservation, statusCode, errorCode := h.reserveRuntimeLimits(ctx, apiKey, subscription, limits, payload, model)
	if statusCode != 0 {
		h.recordFailedRequest(ctx, apiKey, publicModel, errorCode, statusCode, started)
		respondError(w, statusCode, errorCode)
		return
	}
	settled := false
	defer func() {
		if settled {
			return
		}
		if !reservationNeedsFinalize(reservation) {
			return
		}
		bgCtx := context.Background()
		_ = h.finalizeRuntimeReservation(bgCtx, apiKey, reservation, usageSettlement{}, false, false)
	}()

	// Build the candidate list: primary route first, then combo fallbacks if any.
	type candidate struct {
		routerInstanceCode string
		routerModel        string
		triggerOn          []string
	}

	primaryCandidate := candidate{
		routerInstanceCode: route.RouterInstanceCode,
		routerModel:        route.RouterModel,
	}
	candidates := []candidate{primaryCandidate}

	// Combo resolution moved to CLIProxyAPI (PRD §3.3) — see the identical
	// note in the Messages handler above.

	streamRequested := isStreamingPayload(payload)
	var lastResp *http.Response
	var lastBody []byte
	var lastStatusCode int

	// Only streaming responses can safely receive pre-response keepalive bytes.
	// For non-streaming JSON responses, writing anything before the final body
	// corrupts the response contract.
	stopChatKeepalive := func() {}
	if streamRequested {
		stopChatKeepalive = startPreResponseKeepalive(w, 25*time.Second)
	}

	for _, cand := range candidates {
		p, cloneErr := clonePayload(payload)
		if cloneErr != nil {
			stopChatKeepalive()
			zerolog.Ctx(ctx).Error().Err(cloneErr).Msg("clone chat payload failed")
			respondError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		p["model"] = cand.routerModel
		if streamRequested {
			ensureStreamUsageOption(p)
		} else {
			ensureNonStreaming(p)
		}

		client, ok := h.clientForRouter(ctx, cand.routerInstanceCode)
		if !ok {
			continue
		}
		resp, callErr := client.ChatCompletions(ctx, p)
		if callErr != nil {
			zerolog.Ctx(ctx).Warn().Err(callErr).
				Str("router_instance_code", cand.routerInstanceCode).
				Str("router_model", cand.routerModel).
				Msg("chat upstream candidate failed")
			continue
		}

		statusCode := resp.StatusCode
		if streamRequested && statusCode < 400 {
			if stopChatKeepalive != nil {
				stopChatKeepalive()
			}
			effectiveRoute := &store.AIModelRoute{
				ID:                 route.ID,
				ModelID:            route.ModelID,
				RouterInstanceCode: cand.routerInstanceCode,
				RouterModel:        cand.routerModel,
				Status:             route.Status,
				CreatedAt:          route.CreatedAt,
			}
			body := h.streamUpstreamResponse(w, resp, publicModel)
			resp.Body.Close()
			if err := h.recordAndFinalizeRuntime(ctx, apiKey, subscription, model, effectiveRoute, reservation, started, statusCode, body, publicModel); err != nil {
				zerolog.Ctx(ctx).Error().Err(err).
					Str("public_model", publicModel).
					Str("router_instance_code", cand.routerInstanceCode).
					Str("router_model", cand.routerModel).
					Str("api_key_id", apiKey.ID.String()).
					Msg("streaming settlement failed; quota/credit reservation may be inconsistent")
			}
			settled = true
			// See /v1/messages streaming branch — HTTP 200 alone is not
			// proof of a real completion, so gate the counter commit on
			// shouldCountAsSuccess to avoid charging the user when the
			// stream errored out or never produced content.
			if shouldCountAsSuccess(statusCode, body) {
				preCounters.commit(ctx)
			}
			return
		}

		// Non-streaming success: read body with keepalive to prevent
		// Cloudflare idle timeout (120s).
		if !streamRequested && statusCode < 400 {
			effectiveRoute := &store.AIModelRoute{
				ID:                 route.ID,
				ModelID:            route.ModelID,
				RouterInstanceCode: cand.routerInstanceCode,
				RouterModel:        cand.routerModel,
				Status:             route.Status,
				CreatedAt:          route.CreatedAt,
			}
			body := readBodyWithKeepalive(w, resp.Body, 25*time.Second)
			resp.Body.Close()
			settled = true
			if shouldCountAsSuccess(statusCode, body) {
				preCounters.commit(ctx)
			}
			h.writeUpstreamResponse(w, resp, body, publicModel)
			h.recordAndFinalizeAsync(ctx, apiKey, subscription, model, effectiveRoute, reservation, started, statusCode, body, publicModel)
			return
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if !isRetriableStatus(statusCode) || !isRetriableTrigger(body, cand.triggerOn) {
			// Success or non-retriable error: use this response.
			effectiveRoute := &store.AIModelRoute{
				ID:                 route.ID,
				ModelID:            route.ModelID,
				RouterInstanceCode: cand.routerInstanceCode,
				RouterModel:        cand.routerModel,
				Status:             route.Status,
				CreatedAt:          route.CreatedAt,
			}
			settled = true
			// Period/RPD/free-model counters tick ONLY on real success
			// (HTTP 2xx + no in-body provider error). Non-retriable
			// failures (4xx, 5xx with no retry, in-body errors) leave
			// the deferred rollback to release the slot — the caller
			// shouldn't be charged for our or the provider's failure.
			if shouldCountAsSuccess(statusCode, body) {
				preCounters.commit(ctx)
			}
			h.writeUpstreamResponse(w, resp, body, publicModel)
			h.recordAndFinalizeAsync(ctx, apiKey, subscription, model, effectiveRoute, reservation, started, statusCode, body, publicModel)
			return
		}

		// Retriable: save it and try next candidate.
		lastResp = resp
		lastBody = body
		lastStatusCode = statusCode
	}

	// All candidates exhausted, write the last known response.
	if lastResp != nil {
		if stopChatKeepalive != nil {
			stopChatKeepalive()
		}
		effectiveRoute := route
		settled = true
		// Same rule as above: only commit if the final response is a
		// genuine success.
		if shouldCountAsSuccess(lastStatusCode, lastBody) {
			preCounters.commit(ctx)
		}
		safeLastBody := sanitizeErrorBody(lastBody, lastStatusCode)
		for k, v := range lastResp.Header {
			w.Header()[k] = v
		}
		if len(safeLastBody) != len(lastBody) {
			w.Header().Set("Content-Length", strconv.Itoa(len(safeLastBody)))
		}
		w.WriteHeader(lastStatusCode)
		_, _ = w.Write(safeLastBody)
		h.recordAndFinalizeAsync(ctx, apiKey, subscription, model, effectiveRoute, reservation, started, lastStatusCode, lastBody, publicModel)
		return
	}

	if stopChatKeepalive != nil {
		stopChatKeepalive()
	}
	if err := h.finalizeRuntimeReservation(ctx, apiKey, reservation, usageSettlement{}, false, true); err != nil {
		respondError(w, http.StatusInternalServerError, "settlement_failed")
		return
	}
	settled = true
	respondError(w, http.StatusBadGateway, "all_candidates_failed")
}

// clonePayload deep-copies a JSON payload map so that candidates
// can mutate their own copy independently. Returns an error if marshal/unmarshal fails.
func clonePayload(original map[string]any) (map[string]any, error) {
	b, err := json.Marshal(original)
	if err != nil {
		return nil, err
	}
	var copy map[string]any
	if err := json.Unmarshal(b, &copy); err != nil {
		return nil, err
	}
	return copy, nil
}

// --- usage recording ---

func (h *GatewayHandler) recordUsage(ctx context.Context, apiKey store.APIKey, subscription *service.ActiveSubscription, model *store.AIModel, route *store.AIModelRoute, reservation *runtimeReservation, started time.Time, statusCode int, body []byte, publicModel string) (usageSettlement, error) {
	finished := time.Now().UTC()
	latencyMs := finished.Sub(started).Milliseconds()

	metrics := parseUsageFromBody(body)
	promptTokens := metrics.PromptTokens
	completionTokens := metrics.CompletionTokens
	totalTokens := metrics.TotalTokens
	cachedTokens := metrics.CachedTokens
	cacheReadInputTokens := metrics.CacheReadInputTokens
	cacheCreationInputTokens := metrics.CacheCreationInputTokens
	reasoningTokens := metrics.ReasoningTokens

	prices := h.models.ListPrices(ctx)
	price := modelPrice(prices, model.ID)
	inputCostUsd := 0.0
	outputCostUsd := 0.0
	totalCostUsd := 0.0
	if price != nil {
		nonCachedPromptTokens := promptTokens - cacheReadInputTokens - cacheCreationInputTokens
		if nonCachedPromptTokens < 0 {
			nonCachedPromptTokens = 0
		}
		inputPrice := parseFloatPtr(&price.InputPricePer1M)
		cachedPrice := inputPrice
		if price.CachedPricePer1M != nil {
			cachedPrice = parseFloatPtr(price.CachedPricePer1M)
		}
		inputCostUsd =
			float64(nonCachedPromptTokens)/1_000_000*inputPrice +
				float64(cacheReadInputTokens)/1_000_000*cachedPrice +
				float64(cacheCreationInputTokens)/1_000_000*inputPrice
		outputCostUsd = float64(completionTokens) / 1_000_000 * parseFloatPtr(&price.OutputPricePer1M)
		totalCostUsd = inputCostUsd + outputCostUsd
	}

	statusStr := "success"
	if statusCode >= 400 {
		statusStr = "failed"
	}
	// Some upstreams (notably CLIProxyAPI/ai-core2) return HTTP 200 with an
	// `{"error": ...}` payload when the underlying provider call failed. Treat
	// those as failed so the ledger and reservation finalize reflect reality.
	bodyErrorCode := ""
	if statusCode < 400 {
		bodyErrorCode = namespaceUpstreamErrorCode(detectProviderErrorFromBody(body))
		if bodyErrorCode != "" {
			statusStr = "failed"
		}
	}
	success := statusStr == "success"
	actualCredits := 0.0
	if success && reservation != nil {
		creditPricePer20k := 0.0
		switch reservation.BillingMode {
		case "credit_package":
			creditPricePer20k = reservation.CreditPricePer20k
		case "unlimited":
			creditPricePer20k = reservation.PlanCreditPricePer20k
		}
		if creditPricePer20k > 0 {
			billedTokens := totalTokens
			if billedTokens <= 0 {
				billedTokens = promptTokens + completionTokens
			}
			if billedTokens <= 0 {
				billedTokens = 1
			}
			actualCredits = calculateActualRequestCredits(creditPricePer20k, billedTokens)
		}
	}

	inCost := formatAmount(inputCostUsd)
	outCost := formatAmount(outputCostUsd)
	totCost := formatAmount(totalCostUsd)
	routerModel := route.RouterModel
	routerCode := route.RouterInstanceCode
	apiKeyID := apiKey.ID
	latencyMs32 := int32(latencyMs)

	var entryMeta map[string]any
	// Pricing group source-of-truth: prefer the billing_mode the
	// reservation actually charged on. That keeps the usage_ledger
	// consistent with the debit even when the entitlement metadata is
	// stale or empty (older rows had `pricingGroup` only, never
	// `pricing_group`, which left us with "unknown" buckets).
	pricingGroup := ""
	planCode := ""
	if reservation != nil {
		switch reservation.BillingMode {
		case "unlimited":
			pricingGroup = "unlimited_plan"
		case "credit_package":
			pricingGroup = "credit_package"
		case "payg_topup":
			pricingGroup = "payg_topup"
		}
	}
	if subscription != nil && subscription.Entitlement != nil {
		planCode = subscription.Entitlement.PlanCode
		// Resolve from live plan / column / metadata if billing_mode
		// didn't already pin a group (e.g. when the reservation path
		// is skipped for free-tier validation responses).
		if pricingGroup == "" {
			if g := resolveSubscriptionPricingGroup(subscription); g != "" {
				pricingGroup = g
			} else {
				var subMeta map[string]any
				if len(subscription.Entitlement.Metadata) > 0 {
					_ = json.Unmarshal(subscription.Entitlement.Metadata, &subMeta)
				}
				if v, ok := subMeta["pricing_group"].(string); ok && v != "" {
					pricingGroup = v
				} else if v, ok := subMeta["pricingGroup"].(string); ok && v != "" {
					pricingGroup = v
				}
			}
		}
	}
	if pricingGroup == "" {
		// Last-resort: an API key dedicated to a single billing source
		// (e.g. PAYG topup key with no live entitlement) tells us how
		// the user is paying. Without this, every request from that
		// key would still land in "Unknown".
		switch apiKey.BillingSource {
		case "payg":
			pricingGroup = "payg_topup"
		case "credit":
			pricingGroup = "credit_package"
		case "subscription":
			pricingGroup = "unlimited_plan"
		}
	}
	if pricingGroup != "" {
		entryMeta = map[string]any{
			"pricing_group": pricingGroup,
			"is_unlimited":  pricingGroup == "unlimited_plan" || pricingGroup == "unlimited",
		}
		if planCode != "" {
			entryMeta["plan_code"] = planCode
		}
	}
	var entryMetaJSON []byte
	if entryMeta != nil {
		entryMetaJSON, _ = json.Marshal(entryMeta)
	}

	var errorCodePtr *string
	if bodyErrorCode != "" {
		ec := bodyErrorCode
		errorCodePtr = &ec
	} else if statusCode >= 400 {
		ec := fmt.Sprintf("http_%d", statusCode)
		errorCodePtr = &ec
	}

	// PRD v3 Phase 2 — populate billing-mode and per-row charge so the
	// customer-facing usage page can render "1.5 credits, 8.5 left"
	// instead of converting back from USD. We compute amount + post-balance
	// here (before finalize) since recordUsage runs before the debit.
	var billingModePtr *string
	var amountCreditsPtr *string
	var balanceAfterCreditsPtr *string
	var balanceAfterUsdPtr *string
	if reservation != nil {
		bm := reservation.BillingMode
		if bm != "" {
			billingModePtr = &bm
		}
		if success {
			if actualCredits > 0 {
				amt := fmt.Sprintf("%.4f", actualCredits)
				amountCreditsPtr = &amt
			}
			// Read current balance, subtract this debit to compute the
			// balanceAfter snapshot. Read failures are non-fatal — leave
			// the field null and the FE will fall back to the running
			// snapshot from /api/user/ai-gateway/billing.
			//
			// Read each balance from the row that owns it: credit_package
			// row has CreditBalance, payg_topup row has PaygUsdBalance.
			// GetByUser would return the unlimited row first when the
			// user holds both, leaving credit_balance NULL and snapshot
			// stuck on the previous request's value.
			if actualCredits > 0 {
				if entitlement, err := h.entitlements.GetCreditEntitlementByUser(ctx, apiKey.GenfityUserID); err == nil && entitlement != nil && entitlement.CreditBalance != nil {
					current := parseFloatPtr(entitlement.CreditBalance)
					after := current - actualCredits
					if after < 0 {
						after = 0
					}
					a := fmt.Sprintf("%.4f", after)
					balanceAfterCreditsPtr = &a
				}
			}
			if reservation.PaygUSD > 0 {
				if entitlement, err := h.entitlements.GetPaygEntitlementByUser(ctx, apiKey.GenfityUserID); err == nil && entitlement != nil && entitlement.PaygUsdBalance != nil {
					current := parseFloatPtr(entitlement.PaygUsdBalance)
					actual := totalCostUsd
					if actual <= 0 {
						actual = reservation.PaygUSD
					}
					after := current - actual
					if after < 0 {
						after = 0
					}
					a := formatAmount(after)
					balanceAfterUsdPtr = &a
				}
			}
		}
	}

	entry := store.UsageLedgerEntry{
		ID:                       uuid.New(),
		RequestID:                uuid.New().String(),
		GenfityUserID:            apiKey.GenfityUserID,
		GenfityTenantID:          apiKey.GenfityTenantID,
		APIKeyID:                 &apiKeyID,
		PublicModel:              publicModel,
		RouterModel:              &routerModel,
		RouterInstanceCode:       &routerCode,
		PromptTokens:             promptTokens,
		CompletionTokens:         completionTokens,
		TotalTokens:              totalTokens,
		CachedTokens:             cachedTokens,
		CacheReadInputTokens:     cacheReadInputTokens,
		CacheCreationInputTokens: cacheCreationInputTokens,
		ReasoningTokens:          reasoningTokens,
		InputCost:                inCost,
		OutputCost:               outCost,
		TotalCost:                totCost,
		BillingMode:              billingModePtr,
		AmountCredits:            amountCreditsPtr,
		BalanceAfterCredits:      balanceAfterCreditsPtr,
		BalanceAfterUsd:          balanceAfterUsdPtr,
		Status:                   statusStr,
		ErrorCode:                errorCodePtr,
		LatencyMS:                &latencyMs32,
		StartedAt:                started,
		FinishedAt:               &finished,
		Metadata:                 entryMetaJSON,
	}
	if _, err := h.usage.Record(ctx, entry); err != nil {
		return usageSettlement{}, err
	}

	return usageSettlement{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
		TotalCostUSD:     totalCostUsd,
		ActualCredits:    actualCredits,
		Success:          success,
		ErrorCode:        bodyErrorCode,
		RequestID:        entry.RequestID,
	}, nil
}

func (h *GatewayHandler) recordUsageWithLegacyBilling(ctx context.Context, apiKey store.APIKey, subscription *service.ActiveSubscription, model *store.AIModel, route *store.AIModelRoute, started time.Time, statusCode int, body []byte, publicModel string) {
	settlement, err := h.recordUsage(ctx, apiKey, subscription, model, route, nil, started, statusCode, body, publicModel)
	if err != nil {
		return
	}
	if statusCode < 400 && settlement.TotalTokens > 0 && subscription != nil && subscription.Entitlement != nil {
		// Mirror the pre-request gate: free models don't burn the
		// subscription's token quota or RPD. Without this, a free model
		// running through an unlimited plan would silently chip away at
		// quota_tokens_monthly (visible on the dashboard's "Token
		// terpakai" bar) even though no paid request occurred.
		if model == nil || !model.IsFree {
			periodStart, periodEnd := activePeriod(subscription.Entitlement)
			_ = h.usage.IncrementQuotaCounter(ctx, apiKey.GenfityUserID, apiKey.GenfityTenantID, periodStart, periodEnd, settlement.TotalTokens)
		}
	}
	if statusCode < 400 && settlement.TotalCostUSD > 0 && pricingGroup(subscription) == "credit_package" && subscription != nil && subscription.Entitlement != nil {
		_ = h.usage.DebitCreditBalance(ctx, apiKey.GenfityUserID, subscription.Entitlement.PlanCode, settlement.TotalCostUSD)
	}
}
