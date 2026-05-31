-- +goose Up
--
-- usage_ledger_started_idx: index on started_at DESC.
--
-- The admin Request Logs default view runs
--   SELECT ... FROM ai_gateway.usage_ledger ORDER BY started_at DESC
--   LIMIT 100 OFFSET 0
-- with no filter. Without an index on started_at this is a full parallel
-- seq-scan + top-N sort over the whole table (~205 ms at 190k rows, O(n)
-- and growing). Analytics queries that filter `started_at >= since` (the
-- usage dashboard timeseries/top-models/breakdowns) have the same problem.
--
-- A btree on (started_at DESC) turns the default log view into a 100-row
-- index scan (~1 ms) and lets the `since`-filtered analytics use an index
-- range scan. The existing composite indexes (user/apikey/tenant +
-- started_at) only help when those columns are also filtered; the
-- unfiltered "Semua" view and the global analytics had no usable index.
--
-- Plain CREATE INDEX (not CONCURRENTLY): goose wraps each migration in a
-- transaction and CONCURRENTLY cannot run inside one. At ~190k rows the
-- build is ~1 s and takes a SHARE lock (blocks writes briefly, reads
-- unaffected) — acceptable during the deploy restart window.
CREATE INDEX IF NOT EXISTS usage_ledger_started_idx
    ON ai_gateway.usage_ledger (started_at DESC);

-- +goose Down
DROP INDEX IF EXISTS ai_gateway.usage_ledger_started_idx;
