-- +goose Up
ALTER TABLE ai_gateway.customer_entitlements
    ADD COLUMN IF NOT EXISTS balance_reserved numeric(18, 6) NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE ai_gateway.customer_entitlements
    DROP COLUMN IF EXISTS balance_reserved;
