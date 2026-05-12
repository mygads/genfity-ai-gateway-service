-- +goose Up
-- Virtual combos moved from genfity-ai-gateway-service to CLIProxyAPI on
-- 2026-05 (PRD §3.3 / §3.6). The gateway no longer owns the combo tables;
-- CLIProxyAPI persists combos under its auth-dir/combos.json instead.
--
-- Before applying this migration in production:
--   1. Export the virtual_combos + virtual_combo_entries rows.
--   2. Replay them against CLIProxyAPI's /v0/management/combos endpoint.
--   3. Verify the combo count via GET /v0/management/combos.
--
-- Only then run `goose up` to drop the gateway tables.

DROP TABLE IF EXISTS ai_gateway.virtual_combo_entries;
DROP TABLE IF EXISTS ai_gateway.virtual_combos;

-- +goose Down
-- No rollback. If combos need to live in the gateway again, restore
-- migration 00002_virtual_combos.sql from git history. The generated sqlc
-- models for AiGatewayVirtualCombo / AiGatewayVirtualComboEntry are still
-- in internal/database/generated/models.go as of 2026-05 — regenerate from
-- query/combos.sql (also restore from git) before re-running goose up.
SELECT 1;
