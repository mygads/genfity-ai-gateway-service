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
- validates entitlement
- resolves route
- applies RPM by API key
- applies TPM/concurrency/quota by `genfity_user_id` account scope
- reserves quota/credit before upstream call and finalizes after usage is known
- proxies to CLIProxyAPI
- `/v1/embeddings` is unchanged for now and is not part of the new reservation flow

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
- Missing IDs return `404 not_found`.

Provider management:

- 9Router-style provider management is deprecated for CLIProxyAPI.
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
