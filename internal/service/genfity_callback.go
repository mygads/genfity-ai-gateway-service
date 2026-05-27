package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"genfity-ai-gateway-service/internal/store"
)

// GenfityCallback posts settled-usage debit notifications to genfity-app
// so the customer-facing User.aiGatewayCreditBalance and the
// AiGatewayCreditLedger stay in sync with the gateway's local debit.
//
// Why this exists: the gateway debits its OWN copy of credit_balance
// when a request settles, but genfity-app holds the source-of-truth
// running snapshot for billing display + ledger history. Without this
// callback those two diverge: the gateway charges the user but the
// customer dashboard still shows the pre-debit balance.
//
// Delivery semantics: in-process retry on the request hot path (3
// attempts), then — if all in-process attempts fail — the payload is
// persisted to ai_gateway.pending_callbacks for the background
// retry worker to keep delivering. genfity-app's handler is idempotent
// on (request_id, kind), so re-deliveries are safe even if a previous
// attempt secretly succeeded.
type GenfityCallback struct {
	baseURL string
	secret  string
	client  *http.Client
	log     zerolog.Logger
	queue   PendingCallbackQueue
}

// PendingCallbackQueue is the persistence interface used to durably
// hold callbacks that the in-process retry could not deliver. The
// real implementation is the gateway's PostgresStore; the gateway
// only needs Enqueue here so genfity_callback.go is decoupled from
// the full Store interface.
type PendingCallbackQueue interface {
	EnqueuePendingCallback(ctx context.Context, item store.PendingCallback) error
}

const usageDebitCallbackAttempts = 3

func NewGenfityCallback(baseURL, secret string, logger zerolog.Logger) *GenfityCallback {
	return &GenfityCallback{
		baseURL: strings.TrimRight(baseURL, "/"),
		secret:  secret,
		client:  &http.Client{Timeout: 10 * time.Second},
		log:     logger.With().Str("component", "genfity_callback").Logger(),
	}
}

// WithQueue attaches a durable backstop. When all in-process retries
// fail, PostUsageDebitAsync persists the payload here so a background
// worker can keep retrying across restarts. Without a queue, callbacks
// are lost if genfity-app outage outlasts ~3 seconds (the
// in-process retry window).
func (c *GenfityCallback) WithQueue(queue PendingCallbackQueue) *GenfityCallback {
	if c == nil {
		return nil
	}
	c.queue = queue
	return c
}

// Enabled returns true when the callback is configured. We keep this
// optional so local dev / test deployments without a genfity-app
// peer don't crash the gateway.
func (c *GenfityCallback) Enabled() bool {
	return c != nil && c.baseURL != "" && c.secret != ""
}

type UsageDebitPayload struct {
	UserID        string  `json:"user_id"`
	RequestID     string  `json:"request_id"`
	BillingMode   string  `json:"billing_mode"` // "credit_package" | "payg_topup"
	AmountCredits float64 `json:"amount_credits,omitempty"`
	AmountUSD     float64 `json:"amount_usd,omitempty"`
	Model         string  `json:"model,omitempty"`
	Notes         string  `json:"notes,omitempty"`
}

func (c *GenfityCallback) PostUsageDebit(ctx context.Context, payload UsageDebitPayload) error {
	if !c.Enabled() {
		return nil
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal callback payload: %w", err)
	}

	url := c.baseURL + "/api/internal/ai-gateway/usage-debit"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build callback request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", c.secret)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("post callback: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return &callbackStatusError{statusCode: resp.StatusCode}
	}
	return nil
}

// callbackStatusError lets the retry worker classify whether a
// non-2xx response is retriable (5xx / 429) or permanent (4xx).
type callbackStatusError struct {
	statusCode int
}

func (e *callbackStatusError) Error() string {
	return fmt.Sprintf("callback returned %d", e.statusCode)
}

// PostUsageDebitAsync fires the callback in a background goroutine so
// the request hot path isn't blocked by genfity-app latency. After 3
// in-process attempts fail, the payload is persisted to the durable
// queue (if configured) so a background worker can keep retrying
// across restarts. Without the queue this would be the legacy
// fire-and-forget that loses callbacks during outages > ~3 seconds.
func (c *GenfityCallback) PostUsageDebitAsync(payload UsageDebitPayload) {
	if !c.Enabled() {
		return
	}
	go func() {
		var lastErr error
		for attempt := 1; attempt <= usageDebitCallbackAttempts; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			lastErr = c.PostUsageDebit(ctx, payload)
			cancel()
			if lastErr == nil {
				return
			}
			if attempt < usageDebitCallbackAttempts {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
		}
		if lastErr == nil {
			return
		}
		c.log.Warn().Err(lastErr).
			Str("user_id", payload.UserID).
			Str("request_id", payload.RequestID).
			Msg("usage debit callback failed in-process; persisting for background retry")
		if c.queue == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		item := store.PendingCallback{
			RequestID:   payload.RequestID,
			UserID:      payload.UserID,
			BillingMode: payload.BillingMode,
			Model:       optionalString(payload.Model),
			Notes:       optionalString(payload.Notes),
		}
		if payload.AmountCredits > 0 {
			s := strconv.FormatFloat(payload.AmountCredits, 'f', 4, 64)
			item.AmountCredits = &s
		}
		if payload.AmountUSD > 0 {
			s := strconv.FormatFloat(payload.AmountUSD, 'f', 6, 64)
			item.AmountUSD = &s
		}
		if err := c.queue.EnqueuePendingCallback(ctx, item); err != nil {
			c.log.Error().Err(err).
				Str("user_id", payload.UserID).
				Str("request_id", payload.RequestID).
				Msg("failed to persist callback to retry queue; debit drift may occur")
		}
	}()
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
