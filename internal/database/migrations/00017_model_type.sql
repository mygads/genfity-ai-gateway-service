-- +goose Up
ALTER TABLE ai_gateway.ai_models
    ADD COLUMN IF NOT EXISTS model_type text NOT NULL DEFAULT 'text';
CREATE INDEX IF NOT EXISTS ai_models_model_type_idx ON ai_gateway.ai_models (model_type);

-- +goose Down
DROP INDEX IF EXISTS ai_gateway.ai_models_model_type_idx;
ALTER TABLE ai_gateway.ai_models DROP COLUMN IF EXISTS model_type;
