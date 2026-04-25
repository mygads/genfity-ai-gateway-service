# Deployment

## Containers

Use one Go service container for AI Gateway and one existing 9Router container.

Gateway listens on `HTTP_ADDR`, default `:8080`.

9Router internal URL should be reachable from the gateway container:

```env
NINE_ROUTER_CORE1_INTERNAL_URL=http://ai-core1-9router:20128
```

## Database

Use existing PostgreSQL container. Create database `genfity_ai_gateway` and run goose migrations:

```bash
goose -dir internal/database/migrations postgres "$DATABASE_URL" up
```

## Redis

Use existing Redis container. Runtime keys should be prefixed with:

```env
REDIS_PREFIX=ai-gateway:prod
```

## Reverse proxy

Public routing:

- `https://ai.genfity.com` -> Go AI Gateway service
- `https://ai-core1.genfity.com` -> 9Router dashboard/core if admin access is needed

Runtime gateway must call 9Router by internal Docker hostname, not public DNS.

## Required secrets

- `GENFITY_JWT_SECRET`
- `GENFITY_INTERNAL_SECRET`
- `API_KEY_PEPPER`
- `NINE_ROUTER_CORE1_API_KEY` if 9Router requires API key

## Verification

- `GET /health`
- `GET /v1/models`
- `POST /customer/api-keys` with Genfity JWT
- `POST /v1/chat/completions` with generated runtime key
- invalid key returns 401
- inactive subscription returns 402
- rate limited request returns 429
