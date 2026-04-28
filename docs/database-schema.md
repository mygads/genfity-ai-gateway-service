# Database Schema

Schema PostgreSQL: `ai_gateway`

## Tables

### `subscription_plan_snapshots`
Snapshot plan AI dari Genfity agar runtime gateway tidak bergantung langsung ke query bisnis saat request berlangsung.

### `customer_entitlements`
Snapshot subscription aktif customer/tenant, period, quota token, dan balance snapshot.

### `api_keys`
Menyimpan API key customer dalam bentuk `key_prefix` dan `key_hash`, bukan raw key.

### `ai_models`
Daftar public model yang diekspos ke customer.

### `ai_model_prices`
Pricing runtime per model.

### `ai_model_routes`
Mapping dari public model ke target model/route di router instance tertentu.

### `router_instances`
Metadata router internal seperti `ai-core2`.

### `usage_ledger`
Ledger durable untuk token usage, latency, cost, dan error runtime.

### `quota_counters`
Counter durable untuk tokens used/reserved per period.

### `request_logs`
Metadata request level ringan tanpa menyimpan prompt penuh secara default.

### `sync_runs`
Audit trail sinkronisasi dari Genfity ke gateway.

## Key indexes

- unique `subscription_plan_snapshots.plan_code`
- unique `ai_models.public_model`
- unique `usage_ledger.request_id`
- unique `router_instances.code`
- index `(genfity_user_id, status)` di `api_keys`
- index `(genfity_user_id, started_at desc)` di `usage_ledger`
- index `(genfity_tenant_id, started_at desc)` di `usage_ledger`
- index `(status, period_end)` di `customer_entitlements`

## Seed

Migration awal menambahkan router instance default:

- `code=ai-core2`
- `public_base_url=https://ai-core2.genfity.com`
- `internal_base_url=http://cli-proxy-api:8317`
