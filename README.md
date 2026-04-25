# genfity-ai-gateway-service

AI Gateway backend untuk `ai.genfity.com`, dibangun dengan pola Go service yang sama dengan `genfity-cs-ai-services`.

## Stack

- Go 1.25
- chi
- pgx/v5
- sqlc
- goose
- zerolog
- Redis untuk rate limit runtime

## Struktur

- `cmd/http/main.go` entrypoint HTTP service
- `internal/http` routing dan middleware
- `internal/handler` HTTP handlers
- `internal/service` business logic
- `internal/router` client ke 9Router
- `internal/database/migrations` goose migrations
- `internal/database/query` sqlc query files

## Endpoint utama

Public runtime:

- `GET /health`
- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/embeddings`

Customer JWT:

- `GET /customer/overview`
- `GET /customer/api-keys`
- `POST /customer/api-keys`
- `DELETE /customer/api-keys/{id}`
- `GET /customer/models`
- `GET /customer/usage`
- `GET /customer/usage/summary`
- `GET /customer/quota`
- `GET /customer/subscription`

Admin JWT:

- `GET /admin/models`
- `POST /admin/models`
- `GET /admin/model-prices`
- `POST /admin/model-prices`
- `GET /admin/model-routes`
- `POST /admin/model-routes`
- `GET /admin/router-instances`
- `POST /admin/router-instances`
- `GET /admin/routers/{code}/health`
- `GET /admin/routers/{code}/models`
- `GET /admin/routers/{code}/providers`
- `GET /admin/routers/{code}/providers/{providerID}/models`
- `GET /admin/routers/{code}/combos`
- `POST /admin/routers/{code}/combos`
- `PATCH /admin/routers/{code}/combos/{comboID}`
- `DELETE /admin/routers/{code}/combos/{comboID}`

Internal secret:

- `POST /internal/sync/subscription-plans`
- `POST /internal/sync/customer-entitlements`
- `POST /internal/sync/customer-balance`
- `GET /internal/export/plans`
- `GET /internal/export/models`
- `GET /internal/export/model-prices`
- `GET /internal/export/usage-summary?user_id=...`

## Environment minimal

- `HTTP_ADDR=:8080`
- `DATABASE_URL=postgres://postgres:postgres@localhost:5432/genfity_ai_gateway?sslmode=disable`
- `REDIS_URL=redis://localhost:6379/3`
- `REDIS_PREFIX=ai-gateway:prod`
- `GENFITY_JWT_SECRET=<jwt-secret-genfity-app>`
- `GENFITY_INTERNAL_SECRET=<shared-internal-secret>`
- `NINE_ROUTER_CORE1_INTERNAL_URL=http://ai-core1-9router:20128`
- `NINE_ROUTER_CORE1_API_KEY=`
- `API_KEY_PEPPER=<secret>`

## Dev commands

```bash
go mod tidy
go build ./...
```

Generate sqlc:

```bash
cd internal/database
sqlc generate
```

Run migration:

```bash
goose -dir internal/database/migrations postgres "$DATABASE_URL" up
```

## Catatan status

Service layer saat ini bootstrap menggunakan memory store. Migration dan sqlc queries sudah disiapkan untuk tahap persistence pgx/sqlc berikutnya.
