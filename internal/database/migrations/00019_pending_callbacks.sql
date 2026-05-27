-- +goose Up
--
-- pending_callbacks: a durable retry queue for callbacks from gateway
-- to genfity-app that failed at the network layer (genfity-app down,
-- DNS misbehaving, connection refused). Without this, the legacy
-- fire-and-forget retry (3 attempts in-process) loses every debit
-- when the outage outlasts ~3 seconds — which is exactly what
-- happened on 2026-05-25 when genfity-app DNS resolution glitched
-- and 141 debits silently dropped from one user's app-side ledger.
--
-- Lifecycle:
--   1. PostUsageDebitAsync writes a row here AFTER the in-process
--      retries fail (best-effort durable backstop).
--   2. The background callback retry worker scans pending_callbacks
--      every 30s and reposts each row to genfity-app. genfity-app's
--      handler is already idempotent on (request_id, kind), so
--      replaying a callback that secretly succeeded is safe — it
--      no-ops on the second attempt.
--   3. On a 2xx response, the row is deleted.
--   4. On a 4xx response that isn't recoverable (auth, malformed
--      payload), the row is deleted with status='failed_permanent'
--      preserved in the audit log if needed.
--   5. Otherwise we keep retrying with capped backoff. After
--      max_attempts retries the row is parked at status='abandoned'
--      so it stays visible for manual inspection but stops consuming
--      sweeper time.
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS ai_gateway.pending_callbacks (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id      text NOT NULL,
    user_id         text NOT NULL,
    billing_mode    text NOT NULL CHECK (billing_mode IN ('credit_package', 'payg_topup')),
    amount_credits  numeric(18,4),
    amount_usd      numeric(18,6),
    model           text,
    notes           text,
    attempts        int  NOT NULL DEFAULT 0,
    last_error      text,
    last_attempt_at timestamptz,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    status          text NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'abandoned', 'failed_permanent')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (request_id, billing_mode)
);

-- Sweeper picks pending rows due to retry, oldest first.
CREATE INDEX IF NOT EXISTS pending_callbacks_due_idx
    ON ai_gateway.pending_callbacks (next_attempt_at)
    WHERE status = 'pending';
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS ai_gateway.pending_callbacks;
