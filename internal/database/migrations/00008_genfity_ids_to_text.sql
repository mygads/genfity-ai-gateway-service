-- +goose Up
--
-- Convert genfity_user_id / genfity_tenant_id from uuid to text.
--
-- genfity-app uses CUID strings (e.g. "cmp5fqfkk001qp6076sevxfrd"), not
-- UUIDs. The original 00001_init migration declared these columns as
-- text, matching the source of truth, but a drift in some environments
-- left them typed as uuid — every /internal/sync/customer-entitlements
-- POST then failed with `request_failed` because the pgx driver
-- rejected the CUID at the parameter-bind step.
--
-- This migration is defensive: it converts uuid → text idempotently. If
-- a column is already text it is a no-op (the inner DO block skips when
-- the data_type is not 'uuid'). The Go models already use `string` for
-- these fields, so no application code needs to change.

-- +goose StatementBegin
DO $$
DECLARE
    rec record;
BEGIN
    FOR rec IN
        SELECT table_name, column_name
        FROM information_schema.columns
        WHERE table_schema = 'ai_gateway'
          AND column_name IN ('genfity_user_id', 'genfity_tenant_id')
          AND data_type = 'uuid'
    LOOP
        EXECUTE format(
            'ALTER TABLE ai_gateway.%I ALTER COLUMN %I TYPE text USING %I::text',
            rec.table_name, rec.column_name, rec.column_name
        );
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
--
-- The Down direction would only succeed if every existing value parses
-- as a valid UUID. Since genfity-app emits CUIDs, a real rollback would
-- require deleting all rows first. We intentionally do not provide an
-- automatic destructive Down — operators who need to revert can run
-- the cast manually after deciding how to handle non-UUID data.
SELECT 1;
