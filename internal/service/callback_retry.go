package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"genfity-ai-gateway-service/internal/store"
)

// CallbackRetryStore is the slice of Store the retry worker needs.
// Defining a narrow interface keeps the worker testable.
type CallbackRetryStore interface {
	ListDuePendingCallbacks(ctx context.Context, limit int) ([]store.PendingCallback, error)
	MarkCallbackSucceeded(ctx context.Context, id uuid.UUID) error
	MarkCallbackRetry(ctx context.Context, id uuid.UUID, lastError string, nextAttemptAt time.Time) error
	MarkCallbackAbandoned(ctx context.Context, id uuid.UUID, status string, lastError string) error
}

// CallbackRetryWorker drains the durable pending_callbacks queue.
//
// On each tick:
//   1. Read up to N rows whose next_attempt_at <= now() and status='pending'.
//   2. Replay each one. genfity-app's handler is idempotent on
//      (request_id, kind), so a re-delivery of a row that already
//      landed is a no-op.
//   3. On 2xx → delete the queue row.
//   4. On 4xx that isn't retriable (auth, malformed JSON, missing
//      fields) → mark abandoned so we stop retrying but the row stays
//      visible in the queue for manual inspection.
//   5. Otherwise (5xx, network) → schedule a retry with exponential
//      backoff capped at 10 minutes. After maxAttempts we give up
//      and mark abandoned.
type CallbackRetryWorker struct {
	store     CallbackRetryStore
	callback  *GenfityCallback
	log       zerolog.Logger
	tick      time.Duration
	batch     int
	maxRetry  int
}

func NewCallbackRetryWorker(store CallbackRetryStore, callback *GenfityCallback, logger zerolog.Logger) *CallbackRetryWorker {
	return &CallbackRetryWorker{
		store:    store,
		callback: callback,
		log:      logger.With().Str("component", "callback_retry").Logger(),
		tick:     30 * time.Second,
		batch:    50,
		maxRetry: 24, // ~ 24 attempts × ~10min backoff cap = ~4h ceiling
	}
}

// Run blocks until ctx is cancelled, draining the queue on every tick.
// Run must be invoked once per process; calling it twice from
// different goroutines will deliver each pending callback twice (the
// receive side is idempotent, but it is still wasteful).
func (w *CallbackRetryWorker) Run(ctx context.Context) {
	if w == nil || w.store == nil || w.callback == nil || !w.callback.Enabled() {
		return
	}
	ticker := time.NewTicker(w.tick)
	defer ticker.Stop()
	// Process once immediately at startup so callbacks queued during
	// the previous process lifetime drain without waiting a full tick.
	w.processBatch(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processBatch(ctx)
		}
	}
}

func (w *CallbackRetryWorker) processBatch(ctx context.Context) {
	rows, err := w.store.ListDuePendingCallbacks(ctx, w.batch)
	if err != nil {
		w.log.Warn().Err(err).Msg("list pending callbacks failed")
		return
	}
	for _, row := range rows {
		w.deliver(ctx, row)
	}
}

func (w *CallbackRetryWorker) deliver(ctx context.Context, row store.PendingCallback) {
	payload := UsageDebitPayload{
		UserID:      row.UserID,
		RequestID:   row.RequestID,
		BillingMode: row.BillingMode,
	}
	if row.AmountCredits != nil {
		if v, err := strconv.ParseFloat(*row.AmountCredits, 64); err == nil {
			payload.AmountCredits = v
		}
	}
	if row.AmountUSD != nil {
		if v, err := strconv.ParseFloat(*row.AmountUSD, 64); err == nil {
			payload.AmountUSD = v
		}
	}
	if row.Model != nil {
		payload.Model = *row.Model
	}
	if row.Notes != nil {
		payload.Notes = *row.Notes
	}

	deliverCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	err := w.callback.PostUsageDebit(deliverCtx, payload)
	if err == nil {
		if markErr := w.store.MarkCallbackSucceeded(ctx, row.ID); markErr != nil {
			w.log.Warn().Err(markErr).
				Str("request_id", row.RequestID).
				Msg("delivered callback but failed to mark succeeded; will retry idempotently")
		}
		return
	}

	// 4xx (other than 429) is non-retriable: malformed payload, auth
	// rejected, etc. Mark abandoned so the row stops consuming worker
	// time but stays visible for inspection.
	var nonRetriable bool
	var statusErr *callbackStatusError
	if errors.As(err, &statusErr) {
		if statusErr.statusCode >= 400 && statusErr.statusCode < 500 && statusErr.statusCode != http.StatusTooManyRequests {
			nonRetriable = true
		}
	}
	if nonRetriable {
		_ = w.store.MarkCallbackAbandoned(ctx, row.ID, "failed_permanent", err.Error())
		w.log.Error().Err(err).
			Str("request_id", row.RequestID).
			Str("user_id", row.UserID).
			Msg("callback abandoned (non-retriable status)")
		return
	}

	if row.Attempts+1 >= w.maxRetry {
		_ = w.store.MarkCallbackAbandoned(ctx, row.ID, "abandoned", err.Error())
		w.log.Error().Err(err).
			Str("request_id", row.RequestID).
			Str("user_id", row.UserID).
			Int("attempts", row.Attempts+1).
			Msg("callback abandoned after max retries; reconcile manually")
		return
	}

	// Exponential backoff with 10-minute ceiling. Each retry doubles
	// the wait so the queue tolerates extended outages without
	// hammering genfity-app on every tick.
	backoff := time.Duration(1<<uint(row.Attempts+1)) * time.Second
	if backoff > 10*time.Minute {
		backoff = 10 * time.Minute
	}
	next := time.Now().Add(backoff)
	if markErr := w.store.MarkCallbackRetry(ctx, row.ID, err.Error(), next); markErr != nil {
		w.log.Warn().Err(markErr).
			Str("request_id", row.RequestID).
			Msg("failed to schedule callback retry")
	}
}
