-- +goose Up
ALTER TABLE ai_gateway.model_credit_cost
    ALTER COLUMN credits_per_req TYPE numeric(12, 6);

-- +goose Down
ALTER TABLE ai_gateway.model_credit_cost
    ALTER COLUMN credits_per_req TYPE numeric(10, 4);
