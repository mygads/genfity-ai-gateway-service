# Deployment

## Containers

Use one Go service container for AI Gateway and one CLIProxyAPI container (`cli-proxy-api`) as the upstream router.

Gateway listens on `HTTP_ADDR`, default `:8080`.

CLIProxyAPI internal URL should be reachable from the gateway container:

```env
AI_ROUTER_CORE2_INTERNAL_URL=http://cli-proxy-api:8317
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
- `https://ai-core2.genfity.com` -> CLIProxyAPI dashboard/core if admin access is needed

Runtime gateway must call CLIProxyAPI by internal Docker hostname, not public DNS.

## Required secrets

- `GENFITY_JWT_SECRET`
- `GENFITY_INTERNAL_SECRET`
- `API_KEY_PEPPER`
- `AI_ROUTER_CORE2_API_KEY` if CLIProxyAPI requires API key

## Verification

- `GET /health`
- `GET /v1/models`
- `POST /customer/api-keys` with Genfity JWT
- `POST /v1/chat/completions` with generated runtime key
- invalid key returns 401
- inactive subscription returns 402
- rate limited request returns 429
