package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		source = "auto"
	}
	return source == "auto" || source == "subscription"
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

func parseUsageFromBody(body []byte) (prompt int64, completion int64, total int64, cached int64, reasoning int64) {
	dec := json.NewDecoder(bytes.NewReader(body))
	var payload map[string]any
	if err := dec.Decode(&payload); err == nil {
		prompt, completion, total, cached, reasoning = parseUsageFromPayload(payload)
		// If completion is 0 but the response has content (text or tool_use),
		// estimate from body. Anthropic upstream sometimes reports
		// output_tokens=0 for tool_use responses; without this fallback,
		// credit/PAYG users get charged $0 for tool calls.
		if completion == 0 {
			if est := estimateOutputTokensFromPayload(payload); est > 0 {
				completion = est
				if total < prompt+completion {
					total = prompt + completion
				}
			}
		}
		if prompt != 0 || completion != 0 || total != 0 {
			return prompt, completion, total, cached, reasoning
		}
	}
	p, c, t := parseUsageFromSSEBody(body)
	return p, c, t, 0, 0
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
		p, c, t, _, _ := parseUsageFromPayload(payload)
		if p != 0 {
			prompt = p
		}
		if c != 0 {
			completion = c
		}
		if t != 0 {
			total = t
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

func parseUsageFromPayload(payload map[string]any) (prompt int64, completion int64, total int64, cached int64, reasoning int64) {
	usage, ok := payload["usage"].(map[string]any)
	if !ok {
		if message, ok := payload["message"].(map[string]any); ok {
			usage, _ = message["usage"].(map[string]any)
		}
	}
	if usage == nil {
		return 0, 0, 0, 0, 0
	}

	prompt = anyToInt64(usage["prompt_tokens"])
	completion = anyToInt64(usage["completion_tokens"])
	total = anyToInt64(usage["total_tokens"])
	if prompt == 0 {
		prompt = anyToInt64(usage["input_tokens"])
	}
	if completion == 0 {
		completion = anyToInt64(usage["output_tokens"])
	}
	if total == 0 {
		total = prompt + completion
	}

	reasoning = anyToInt64(usage["reasoning_tokens"])
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

	cached = anyToInt64(usage["cached_tokens"])
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

	return prompt, completion, total, cached, reasoning
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

func (h *GatewayHandler) writeUpstreamResponse(w http.ResponseWriter, resp *http.Response, body []byte) {
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func isStreamingPayload(payload map[string]any) bool {
	stream, _ := payload["stream"].(bool)
	return stream
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
func (p *preRequestCounters) commit() {
	if p == nil {
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
	if limits.HasMaxRequestsPerPeriod() && subscription != nil && subscription.Entitlement != nil {
		pk := periodKey(subscription.Entitlement)
		tracker.periodKey = pk
		_, end := activePeriod(subscription.Entitlement)
		ttl := time.Until(end)
		if ttl <= 0 {
			ttl = 24 * time.Hour
		}
		if err := h.rateLimit.CheckRequestsPerPeriod(ctx, apiKey.GenfityUserID, pk, ttl, limits.MaxRequestsPerPeriod); err != nil {
			return "plan_period_limit_exceeded", http.StatusTooManyRequests, tracker
		}
		tracker.periodConsumed = true
	}
	if limits.HasRPD() && subscription != nil && subscription.Plan != nil {
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
// period for use as part of a Redis counter key. Buying a new
// subscription cycle changes period_start/period_end and yields a
// different key, so the counter starts fresh per cycle.
func periodKey(entitlement *store.CustomerEntitlement) string {
	if entitlement == nil {
		return ""
	}
	start, end := activePeriod(entitlement)
	return fmt.Sprintf("%d-%d", start.Unix(), end.Unix())
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

func quotaLimit(subscription *service.ActiveSubscription) int64 {
	if subscription == nil {
		return 0
	}
	if subscription.Entitlement != nil && subscription.Entitlement.QuotaTokensMonthly != nil {
		return *subscription.Entitlement.QuotaTokensMonthly
	}
	if subscription.Plan != nil && subscription.Plan.QuotaTokensMonthly != nil {
		return *subscription.Plan.QuotaTokensMonthly
	}
	return 0
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
	CreditUSD      float64
	CreditPlanCode string

	// PRD v3 Phase 2: 3-priority chain reservation bookkeeping.
	//
	// BillingMode records which schema paid for the request so the
	// finalize path knows which Finalize* helper to call. Empty when
	// the request is covered by an unlimited entitlement (no debit).
	//
	// RequestCredits is the reserved credit amount for the
	// credit_package schema (integer/fractional per model).
	// PaygUSD is the reserved USD amount for the payg_topup schema
	// (actual-cost per-1M pricing). Exactly one of these is non-zero
	// when BillingMode != "unlimited".
	BillingMode    string  // "unlimited" | "credit_package" | "payg_topup" | ""
	RequestCredits float64
	PaygUSD        float64
}

type usageSettlement struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	TotalCostUSD     float64
	Success          bool
	ErrorCode        string
	RequestID        string
}

func estimateRequestTokens(payload map[string]any, model *store.AIModel) tokenReservationEstimate {
	prompt := estimatePayloadTokens(payload)
	completion := anyToInt64(payload["max_tokens"])
	if completion == 0 {
		completion = anyToInt64(payload["max_completion_tokens"])
	}
	bounded := completion > 0
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
	return tokenReservationEstimate{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total, Bounded: bounded}
}

// tryPriorityBilling implements the PRD v3 3-priority reservation chain,
// constrained by the API key's billing_source pin.
//
// API key billing_source values:
//   "auto"         → original 3-priority chain (subscription → credit → payg)
//   "subscription" → only Priority 1 (unlimited); error if not covered
//   "credit"       → only Priority 2 (credit_package); error if not configured/insufficient
//   "payg"         → only Priority 3 (payg_topup USD balance)
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
		source = "auto"
	}

	allowSubscription := source == "auto" || source == "subscription"
	allowCredit := source == "auto" || source == "credit"
	allowPayg := source == "auto" || source == "payg"

	// Priority 1: Unlimited coverage. The subscription must be an
	// "unlimited" plan AND the model must appear in allowedModels.
	// pricingGroup + allowedModels are resolved live from the plan
	// snapshot so admin edits propagate to existing subscribers
	// immediately; the entitlement's frozen copy is the legacy fallback.
	if allowSubscription && subscription != nil && subscription.Entitlement != nil {
		group := resolveSubscriptionPricingGroup(subscription)
		if group == "" {
			group = pricingGroup(subscription)
		}
		if (group == "unlimited" || group == "unlimited_plan") && modelCoveredByUnlimited(subscription, model) {
			return runtimeReservation{BillingMode: "unlimited"}, 0, ""
		}
		// If key is pinned to subscription only, fail closed when
		// unlimited isn't applicable to this request.
		if source == "subscription" {
			return runtimeReservation{}, http.StatusPaymentRequired, "subscription_not_covering_model"
		}
	} else if source == "subscription" {
		// Pinned to subscription but no active entitlement.
		return runtimeReservation{}, http.StatusPaymentRequired, "no_active_subscription"
	}

	// Priority 2: request credits. Look up the per-model credit cost.
	// We use the model's PublicModel (e.g. "mtr/claude-opus-4.7") as
	// the lookup key — this is what sync pushes from genfity-app.
	if allowCredit {
		fullModelID := model.PublicModel
		if fullModelID != "" {
			if cost, err := h.models.GetModelCreditCost(ctx, fullModelID); err == nil && cost != nil && cost.IsActive {
				if cost.IsFree || parseFloatPtr(&cost.CreditsPerReq) <= 0 {
					// Free model — still require the user to have a positive
					// credit balance. Users with 0 balance (new accounts or
					// exhausted credits) cannot access even free models.
					if entitlement, err := h.entitlements.GetByUser(ctx, apiKey.GenfityUserID); err == nil && entitlement != nil && entitlement.CreditBalance != nil {
						if parseFloatPtr(entitlement.CreditBalance) > 0 {
							return runtimeReservation{BillingMode: "credit_package", RequestCredits: 0}, 0, ""
						}
					}
					if source == "credit" {
						return runtimeReservation{}, http.StatusPaymentRequired, "insufficient_credit_balance"
					}
					// Fall through to PAYG check
				} else {
					credits := parseFloatPtr(&cost.CreditsPerReq)
					if err := h.usage.ReserveRequestCredits(ctx, apiKey.GenfityUserID, credits); err != nil {
						if errors.Is(err, service.ErrInsufficientBalance) {
							if source == "credit" {
								return runtimeReservation{}, http.StatusPaymentRequired, "insufficient_credit_balance"
							}
							// Fall through to PAYG — user may have credits
							// exhausted but still carry a PAYG balance.
						} else {
							return runtimeReservation{}, http.StatusInternalServerError, "credit_reservation_failed"
						}
					} else {
						return runtimeReservation{BillingMode: "credit_package", RequestCredits: credits}, 0, ""
					}
				}
			} else if source == "credit" {
				return runtimeReservation{}, http.StatusPaymentRequired, "credit_cost_not_configured"
			}
		} else if source == "credit" {
			return runtimeReservation{}, http.StatusPaymentRequired, "credit_cost_not_configured"
		}
	}

	// Priority 3: PAYG USD balance.
	if !allowPayg {
		// Key is pinned to a higher-priority schema that didn't match.
		return runtimeReservation{}, http.StatusPaymentRequired, "billing_source_not_applicable"
	}
	if !model.PaygExposed {
		if source == "payg" {
			return runtimeReservation{}, http.StatusPaymentRequired, "payg_model_not_published"
		}
		return runtimeReservation{}, 0, ""
	}
	prices := h.models.ListPrices(ctx)
	price := modelPrice(prices, model.ID)
	if price == nil {
		// No PAYG price configured.
		if source == "payg" {
			return runtimeReservation{}, http.StatusPaymentRequired, "payg_price_not_configured"
		}
		// Auto mode: nothing more we can do at the priority chain level.
		// Return "no match" so caller falls back to legacy paths.
		return runtimeReservation{}, 0, ""
	}
	if !estimate.Bounded && parseFloatPtr(&price.OutputPricePer1M) > 0 {
		return runtimeReservation{}, http.StatusBadRequest, "max_tokens_required"
	}
	reserveUSD := float64(estimate.PromptTokens)/1_000_000*parseFloatPtr(&price.InputPricePer1M) +
		float64(estimate.CompletionTokens)/1_000_000*parseFloatPtr(&price.OutputPricePer1M)
	if reserveUSD <= 0 {
		// Free model under PAYG schema — still require the user to have
		// a positive PAYG balance. Users with $0 cannot access even free models.
		if entitlement, err := h.entitlements.GetByUser(ctx, apiKey.GenfityUserID); err == nil && entitlement != nil && entitlement.PaygUsdBalance != nil {
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
	}
	return false
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
	limit := quotaLimit(subscription)
	if limit > 0 && subscription != nil && subscription.Entitlement != nil {
		if !estimate.Bounded {
			return runtimeReservation{}, http.StatusBadRequest, "max_tokens_required"
		}
		periodStart, periodEnd := activePeriod(subscription.Entitlement)
		if err := h.usage.ReserveQuotaTokens(ctx, apiKey.GenfityUserID, apiKey.GenfityTenantID, periodStart, periodEnd, estimate.TotalTokens, limit); err != nil {
			if errors.Is(err, service.ErrQuotaExceeded) {
				return runtimeReservation{}, http.StatusTooManyRequests, "quota_exceeded"
			}
			return runtimeReservation{}, http.StatusInternalServerError, "quota_reservation_failed"
		}
		reservation.PeriodStart = periodStart
		reservation.PeriodEnd = periodEnd
		reservation.QuotaTokens = estimate.TotalTokens
	}
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
			reserveUSD := float64(estimate.PromptTokens)/1_000_000*parseFloatPtr(&price.InputPricePer1M) + float64(estimate.CompletionTokens)/1_000_000*parseFloatPtr(&price.OutputPricePer1M)
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
	if success {
		usedTokens = settlement.TotalTokens
		actualUSD = settlement.TotalCostUSD
	}
	if reservation.QuotaTokens > 0 || countRequest {
		if err := h.usage.FinalizeQuotaTokens(ctx, apiKey.GenfityUserID, reservation.PeriodStart, reservation.PeriodEnd, reservation.QuotaTokens, usedTokens, countRequest); err != nil {
			return err
		}
	}
	if reservation.CreditUSD > 0 {
		if err := h.usage.FinalizeCreditBalance(ctx, apiKey.GenfityUserID, reservation.CreditPlanCode, reservation.CreditUSD, actualUSD); err != nil {
			return err
		}
	}
	// PRD v3 Phase 2 — request-credit finalize. Request-credit pricing
	// is a fixed cost per request (not per-token), so actualAmount ==
	// reservedAmount when the request succeeded; on failure we release
	// the full reservation back to the balance.
	if reservation.RequestCredits > 0 {
		actual := 0.0
		if success {
			actual = reservation.RequestCredits
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
			if reservation.RequestCredits > 0 {
				h.callback.PostUsageDebitAsync(service.UsageDebitPayload{
					UserID:        apiKey.GenfityUserID,
					RequestID:     settlement.RequestID,
					BillingMode:   "credit_package",
					AmountCredits: reservation.RequestCredits,
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

func (h *GatewayHandler) streamUpstreamResponse(w http.ResponseWriter, resp *http.Response) []byte {
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.Header().Del("Content-Length")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)

	captured := tailCaptureBuffer{limit: maxStreamCaptureBytes}
	buf := make([]byte, 32*1024)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			_, _ = w.Write(chunk)
			_, _ = captured.Write(chunk)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
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
//   - "auto"         → union of all three (any model the user could pay
//                      for via any priority).
//
// A model that doesn't pass the filter is hidden so customers don't
// see options they can't actually use with this key.

func (h *GatewayHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	apiKey := middleware.GetAPIKey(ctx)

	source := apiKey.BillingSource
	if source == "" {
		source = "auto"
	}

	allModels := h.models.ListModels(ctx)
	prices := h.models.ListPrices(ctx)

	// Lookup user's active subscription (entitlement + live plan snapshot)
	// once for "subscription" and "auto". We need the live plan snapshot
	// because allowedModels is resolved from plan metadata (not entitlement
	// metadata) so admin edits propagate to existing subscribers — see
	// resolveAllowedModels for the full lookup chain.
	var subscription *service.ActiveSubscription
	if source == "subscription" || source == "auto" {
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
		default: // "auto"
			visible = allowsViaSubscription(m) || allowsViaCredit(m) || allowsViaPayg(m)
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

	limits := service.PlanLimitsFromSnapshot(subscription.Plan)

	route, model, err := h.models.ResolveRouteByPublicModel(ctx, publicModel)
	if err != nil {
		code := "model_not_allowed"
		if errors.Is(err, service.ErrModelRetired) {
			code = "model_retired"
		}
		respondError(w, http.StatusBadRequest, code)
		return
	}
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
	// Request reached upstream and got a response — count it against the
	// period cap regardless of HTTP status, mirroring Messages/ChatCompletions.
	preCounters.commit()
	h.recordUsageWithLegacyBilling(ctx, apiKey, subscription, model, route, started, resp.StatusCode, body, publicModel)
	h.writeUpstreamResponse(w, resp, body)
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

	limits := service.PlanLimitsFromSnapshot(subscription.Plan)

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
	// branches below call preCounters.commit() right before returning, at
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
			h.recordFailedRequest(ctx, apiKey, publicModel, "rate_limit_exceeded", http.StatusTooManyRequests, started)
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
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
		if reservation.QuotaTokens == 0 && reservation.CreditUSD == 0 {
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

	// Start sending keepalive to client immediately to prevent Cloudflare
	// 120s idle timeout while waiting for CLIProxyAPI combo fallback / model
	// reasoning. Applies to both streaming and non-streaming requests.
	stopKeepalive := startPreResponseKeepalive(w, 25*time.Second)

	for _, cand := range candidates {
		p, cloneErr := clonePayload(payload)
		if cloneErr != nil {
			stopKeepalive()
			zerolog.Ctx(ctx).Error().Err(cloneErr).Msg("clone messages payload failed")
			respondError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		p["model"] = cand.routerModel

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
			body := h.streamUpstreamResponse(w, resp)
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
			preCounters.commit()
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
			preCounters.commit()
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(statusCode)
			_, _ = w.Write(body)
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
			// Non-retriable provider error: the request reached upstream
			// and got a real response, so it should count against the
			// period cap. Without commit() the deferred rollback would
			// release the slot — making provider errors free, which is
			// the wrong incentive.
			preCounters.commit()
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(statusCode)
			_, _ = w.Write(body)
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
		preCounters.commit()
		for k, v := range lastResp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(lastStatusCode)
		_, _ = w.Write(lastBody)
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
	limits := service.PlanLimitsFromSnapshot(subscription.Plan)
	if h.rateLimit != nil && limits.HasRPM() {
		if err := h.rateLimit.CheckRPM(ctx, apiKey.GenfityUserID, limits.RPM); err != nil {
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
	}

	route, _, err := h.models.ResolveRouteByPublicModel(ctx, publicModel)
	if err != nil {
		code := "model_not_allowed"
		if errors.Is(err, service.ErrModelRetired) {
			code = "model_retired"
		}
		respondError(w, http.StatusBadRequest, code)
		return
	}
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
	h.writeUpstreamResponse(w, resp, body)
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
	// branches below call preCounters.commit() right before returning, at
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
			h.recordFailedRequest(ctx, apiKey, publicModel, "rate_limit_exceeded", http.StatusTooManyRequests, started)
			respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
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
		if reservation.QuotaTokens == 0 && reservation.CreditUSD == 0 {
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

	// Start sending keepalive to client immediately to prevent Cloudflare
	// 120s idle timeout while waiting for CLIProxyAPI.
	stopChatKeepalive := startPreResponseKeepalive(w, 25*time.Second)

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
			body := h.streamUpstreamResponse(w, resp)
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
			h.writeUpstreamResponse(w, resp, body)
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
			h.writeUpstreamResponse(w, resp, body)
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
		for k, v := range lastResp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(lastStatusCode)
		_, _ = w.Write(lastBody)
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

	promptTokens, completionTokens, totalTokens, cachedTokens, reasoningTokens := parseUsageFromBody(body)

	prices := h.models.ListPrices(ctx)
	price := modelPrice(prices, model.ID)
	inputCostUsd := 0.0
	outputCostUsd := 0.0
	totalCostUsd := 0.0
	if price != nil {
		inputCostUsd = float64(promptTokens) / 1_000_000 * parseFloatPtr(&price.InputPricePer1M)
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
		bodyErrorCode = detectProviderErrorFromBody(body)
		if bodyErrorCode != "" {
			statusStr = "failed"
		}
	}
	success := statusStr == "success"

	inCost := formatAmount(inputCostUsd)
	outCost := formatAmount(outputCostUsd)
	totCost := formatAmount(totalCostUsd)
	routerModel := route.RouterModel
	routerCode := route.RouterInstanceCode
	apiKeyID := apiKey.ID
	latencyMs32 := int32(latencyMs)

	var entryMeta map[string]any
	if subscription != nil && subscription.Entitlement != nil {
		var subMeta map[string]any
		if len(subscription.Entitlement.Metadata) > 0 {
			_ = json.Unmarshal(subscription.Entitlement.Metadata, &subMeta)
		}
		pg, _ := subMeta["pricingGroup"].(string)
		isUnlim := pg == "unlimited_plan"
		entryMeta = map[string]any{
			"pricing_group": pg,
			"is_unlimited":  isUnlim,
			"plan_code":     subscription.Entitlement.PlanCode,
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
			if reservation.RequestCredits > 0 {
				amt := fmt.Sprintf("%.4f", reservation.RequestCredits)
				amountCreditsPtr = &amt
			}
			// Read current balance, subtract this debit to compute the
			// balanceAfter snapshot. Read failures are non-fatal — leave
			// the field null and the FE will fall back to the running
			// snapshot from /api/user/ai-gateway/billing.
			if reservation.RequestCredits > 0 || reservation.PaygUSD > 0 {
				if entitlement, err := h.entitlements.GetByUser(ctx, apiKey.GenfityUserID); err == nil && entitlement != nil {
					if reservation.RequestCredits > 0 && entitlement.CreditBalance != nil {
						current := parseFloatPtr(entitlement.CreditBalance)
						after := current - reservation.RequestCredits
						if after < 0 {
							after = 0
						}
						a := fmt.Sprintf("%.4f", after)
						balanceAfterCreditsPtr = &a
					}
					if reservation.PaygUSD > 0 && entitlement.PaygUsdBalance != nil {
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
	}

	entry := store.UsageLedgerEntry{
		ID:                  uuid.New(),
		RequestID:           uuid.New().String(),
		GenfityUserID:       apiKey.GenfityUserID,
		GenfityTenantID:     apiKey.GenfityTenantID,
		APIKeyID:            &apiKeyID,
		PublicModel:         publicModel,
		RouterModel:         &routerModel,
		RouterInstanceCode:  &routerCode,
		PromptTokens:        promptTokens,
		CompletionTokens:    completionTokens,
		TotalTokens:         totalTokens,
		CachedTokens:        cachedTokens,
		ReasoningTokens:     reasoningTokens,
		InputCost:           inCost,
		OutputCost:          outCost,
		TotalCost:           totCost,
		BillingMode:         billingModePtr,
		AmountCredits:       amountCreditsPtr,
		BalanceAfterCredits: balanceAfterCreditsPtr,
		BalanceAfterUsd:     balanceAfterUsdPtr,
		Status:              statusStr,
		ErrorCode:           errorCodePtr,
		LatencyMS:           &latencyMs32,
		StartedAt:           started,
		FinishedAt:          &finished,
		Metadata:            entryMetaJSON,
	}
	if _, err := h.usage.Record(ctx, entry); err != nil {
		return usageSettlement{}, err
	}

	return usageSettlement{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
		TotalCostUSD:     totalCostUsd,
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
		periodStart, periodEnd := activePeriod(subscription.Entitlement)
		_ = h.usage.IncrementQuotaCounter(ctx, apiKey.GenfityUserID, apiKey.GenfityTenantID, periodStart, periodEnd, settlement.TotalTokens)
	}
	if statusCode < 400 && settlement.TotalCostUSD > 0 && pricingGroup(subscription) == "credit_package" && subscription != nil && subscription.Entitlement != nil {
		_ = h.usage.DebitCreditBalance(ctx, apiKey.GenfityUserID, subscription.Entitlement.PlanCode, settlement.TotalCostUSD)
	}
}
