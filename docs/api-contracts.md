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
- applies rate limit
- proxies to 9Router

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
- `GET/POST /admin/model-prices`
- `GET/POST /admin/model-routes`
- `GET/POST /admin/router-instances`

Header:

- `Authorization: Bearer <genfity_jwt>`

## Internal

- `POST /internal/sync/customer-entitlements`
- `GET /internal/export/models`
- `GET /internal/export/model-prices`
- `GET /internal/export/usage-summary?user_id=<uuid>`

Header:

- `X-Internal-Secret: <shared-secret>`
