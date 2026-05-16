-- +goose Up
ALTER TABLE ai_gateway.ai_models
    ADD COLUMN IF NOT EXISTS payg_exposed boolean NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS ai_models_payg_exposed_idx
    ON ai_gateway.ai_models (payg_exposed) WHERE payg_exposed = true;

-- +goose Down
DROP INDEX IF EXISTS ai_gateway.ai_models_payg_exposed_idx;
ALTER TABLE ai_gateway.ai_models DROP COLUMN IF EXISTS payg_exposed;
