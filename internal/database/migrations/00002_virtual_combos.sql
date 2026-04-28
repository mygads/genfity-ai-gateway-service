-- +goose Up
CREATE TABLE IF NOT EXISTS ai_gateway.virtual_combos (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    model_id uuid NOT NULL REFERENCES ai_gateway.ai_models(id) ON DELETE CASCADE,
    name text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ai_gateway.virtual_combo_entries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    combo_id uuid NOT NULL REFERENCES ai_gateway.virtual_combos(id) ON DELETE CASCADE,
    priority integer NOT NULL,
    router_instance_code text NOT NULL REFERENCES ai_gateway.router_instances(code),
    router_model text NOT NULL,
    trigger_on jsonb NOT NULL DEFAULT '[]'::jsonb,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS virtual_combo_entries_combo_priority_idx ON ai_gateway.virtual_combo_entries (combo_id, priority);

-- +goose Down
DROP TABLE IF EXISTS ai_gateway.virtual_combo_entries;
DROP TABLE IF EXISTS ai_gateway.virtual_combos;
