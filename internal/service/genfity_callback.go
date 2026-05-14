package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
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
// The callback is fire-and-forget on the request hot path (we already
// debited locally; whether the callback succeeds doesn't change what
// the user can do next request). Failures are logged and would be
// reconciled by a future job. Idempotency lives in the genfity-app
// handler — it dedupes on (request_id, kind).
type GenfityCallback struct {
	baseURL string
	secret  string
	client  *http.Client
	log     zerolog.Logger
}

func NewGenfityCallback(baseURL, secret string, logger zerolog.Logger) *GenfityCallback {
	return &GenfityCallback{
		baseURL: strings.TrimRight(baseURL, "/"),
		secret:  secret,
		client:  &http.Client{Timeout: 10 * time.Second},
		log:     logger.With().Str("component", "genfity_callback").Logger(),
	}
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
		return fmt.Errorf("callback returned %d", resp.StatusCode)
	}
	return nil
}

// PostUsageDebitAsync fires the callback in a background goroutine so
// the request hot path isn't blocked by genfity-app latency. Failures
// are logged. Used from finalizeRuntimeReservation.
func (c *GenfityCallback) PostUsageDebitAsync(payload UsageDebitPayload) {
	if !c.Enabled() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := c.PostUsageDebit(ctx, payload); err != nil {
			c.log.Warn().Err(err).Str("user_id", payload.UserID).Str("request_id", payload.RequestID).Msg("usage debit callback failed")
		}
	}()
}
