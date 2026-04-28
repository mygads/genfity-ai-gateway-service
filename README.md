# genfity-ai-gateway-service

AI Gateway backend untuk `ai.genfity.com`, dibangun dengan pola Go service.
Didesain untuk berintegrasi penuh dengan `genfity-app` sebagai Frontend / Master Data.

## Fitur Utama Baru (CLIProxyAPI Migration)
- Upstream utama router memakai `CLIProxyAPI`.
- Model virtual / combo fallback di-handle langsung oleh `GatewayHandler` di Genfity Service. Genfity-app (sebagai admin) cukup melakukan CRUD ke route `/admin/combos`.
- Endpoint `/internal/sync/*` dan `/customer/*` tetap dipertahankan karena `genfity-app` (Frontend) bergantung pada endpoint ini untuk:
  - Sinkronisasi plan & entitlement dari DB master (Prisma) ke Gateway.
  - Export laporan usage/token/balance dari Gateway ke admin dashboard.
  - Memfasilitasi proxy dashboard customer untuk melihat API Keys dan pemakaian quota mereka.

## Stack

- Go 1.25
- chi
- pgx/v5 (Postgres)
- sqlc
- goose
- zerolog
- Redis untuk rate limit runtime

## Endpoint Utama

### Public Runtime (dipanggil oleh script AI / OpenAI SDK)
- `GET /v1/models`
- `POST /v1/chat/completions` (combo/fallback berjalan otomatis di sini)
- `POST /v1/embeddings`

### Customer Dashboard (dipanggil via genfity-app proxy, butuh JWT Customer)
- `GET /customer/overview`
- `GET /customer/api-keys`
- `POST /customer/api-keys`
- `DELETE /customer/api-keys/{id}`
- `PATCH /customer/api-keys/{id}`
- `GET /customer/models`
- `GET /customer/usage`
- `GET /customer/usage/summary`
- `GET /customer/quota`
- `GET /customer/subscription`

### Admin Management (dipanggil via genfity-app proxy, butuh JWT Admin)
- `GET /admin/models`
- `POST /admin/models`
- `DELETE /admin/models/{id}`
- `GET /admin/model-prices`
- `POST /admin/model-prices`
- `GET /admin/model-routes`
- `POST /admin/model-routes`
- `GET /admin/router-instances`
- `POST /admin/router-instances`
- `GET /admin/usage`
- `GET /admin/combos`
- `POST /admin/combos`
- `PUT /admin/combos/{comboID}`
- `DELETE /admin/combos/{comboID}`

### Internal Service-to-Service (dipanggil langsung oleh backend genfity-app, butuh INTERNAL_SECRET)
- `POST /internal/sync/subscription-plans`
- `POST /internal/sync/customer-entitlements`
- `POST /internal/sync/customer-balance`
- `GET /internal/export/plans`
- `GET /internal/export/models`
- `GET /internal/export/model-prices`
- `GET /internal/export/usage-summary`

## Environment minimal

```env
HTTP_ADDR=:8080
DATABASE_URL=postgres://postgres:postgres@localhost:5432/genfity_ai_gateway?sslmode=disable
REDIS_URL=redis://localhost:6379/3
REDIS_PREFIX=ai-gateway:prod
GENFITY_JWT_SECRET=<jwt-secret>
GENFITY_INTERNAL_SECRET=<shared-internal-secret>
AI_ROUTER_CORE2_INTERNAL_URL=http://ai-core2-cliproxy:8317
AI_ROUTER_CORE2_API_KEY=
API_KEY_PEPPER=<secret>
```

## Dev commands

```bash
go mod tidy
go build ./...
```
