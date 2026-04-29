# API Contracts

## Public

### `GET /v1/models`
OpenAI-compatible daftar model.

Response:

```json
{
  "object": "list",
  "data": [
    {
      "id": "gpt-4o-mini",
      "object": "model",
      "created": 1745551000,
      "owned_by": "genfity"
    }
  ]
}
```

### `POST /v1/chat/completions`
Request OpenAI-compatible dengan API key runtime.

Header:

- `Authorization: Bearer sk_genfity_live_...`

Behavior:

- validates api key
- validates entitlement (`402 insufficient_balance` for credit packages with empty balance)
- resolves route
- enforces RPM/TPM/concurrency by `genfity_user_id` account scope
- reserves quota tokens (returns `429 quota_exceeded` if monthly quota would be exceeded; returns `400 max_tokens_required` if quota or paid credit is in effect and the request neither caps `max_tokens` nor has a model context window to fall back on)
- reserves credit balance for credit packages (returns `402 insufficient_balance` if available - reserved < estimated cost)
- proxies to CLIProxyAPI/ai-core2
- on completion, finalizes quota (debits actual tokens, releases reservation, increments request_count regardless of success/failure) and finalizes credit balance (debits actual cost capped to current snapshot, releases reservation)
- if all upstream candidates fail, releases reservations and returns `502 all_candidates_failed`
- if settlement fails after upstream completed, returns `500 settlement_failed`
- in-body provider errors on HTTP 200 are treated as failures: ledger entry status becomes `failed` with the provider's `error.code`/`error.type` (or `provider_error`), and finalize releases the credit reserve without debit
- `/v1/embeddings` is unchanged for now and is not part of the new reservation flow; it does not currently enforce RPM/TPM/concurrency/quota or perform credit reservation

## Customer JWT

- `GET /customer/overview`
- `GET /customer/api-keys`
- `POST /customer/api-keys`
- `DELETE /customer/api-keys/{id}`
- `GET /customer/models`
- `GET /customer/usage`
- `GET /customer/subscription`

Header:

- `Authorization: Bearer <genfity_jwt>`

## Admin JWT

- `GET/POST /admin/models`
- `PATCH/DELETE /admin/models/{id}`
- `GET/POST /admin/model-prices`
- `PATCH/DELETE /admin/model-prices/{id}`
- `GET/POST /admin/model-routes`
- `PATCH/DELETE /admin/model-routes/{id}`
- `GET/POST /admin/router-instances`
- `PATCH/DELETE /admin/router-instances/{id}`
- `GET/POST/PUT /admin/combos`
- `GET/POST/PUT/DELETE /admin/combos/{id}`
- `GET/POST/PUT/DELETE /admin/routers/{code}/combos...` legacy/global alias for `/admin/combos`, not router-filtered

PATCH behavior:

- Partial update: omitted fields are preserved.
- Explicit JSON `null` clears optional pointer fields (e.g. `cached_price_per_1m`, `reasoning_price_per_1m`, `public_base_url`, `encrypted_api_key`, `health_status`, `last_health_check_at`, `metadata`).
- Required fields cannot be set to `null`; doing so returns `400 <field>_required`.
- Missing IDs return `404 not_found`.

Provider management:

- Legacy 9Router/ai-core1 provider management is deprecated; CLIProxyAPI/ai-core2 is the current upstream.
- Provider endpoints return `501 provider_management_not_supported_by_cliproxy`.

Header:

- `Authorization: Bearer <genfity_jwt>`

## Internal

- `POST /internal/sync/customer-entitlements`
- `GET /internal/export/models`
- `GET /internal/export/model-prices`
- `GET /internal/export/usage-summary?user_id=<uuid>`

Header:

- `X-Internal-Secret: <shared-secret>`
