# Architecture

`genfity-ai-gateway-service` adalah runtime AI Gateway untuk `ai.genfity.com`. Service ini berada di antara customer API clients dan CLIProxyAPI.

## Ownership

- `genfity-app`: login, session, RBAC, payment, order, subscription package, customer/admin UI.
- `genfity-ai-gateway-service`: API keys, runtime model catalog, model pricing, route mapping, usage ledger, request metadata, quota/rate limit state.
- `cli-proxy-api`: provider config, OAuth/API provider credentials, combos, fallback, provider model routing, proxy pools.

## Runtime flow

1. Customer calls `POST /v1/chat/completions` with `Authorization: Bearer sk_genfity_live_...`.
2. Gateway validates API key by prefix and HMAC hash.
3. Gateway checks active Genfity entitlement snapshot.
4. Gateway resolves public model to CLIProxyAPI route.
5. Gateway enforces RPM by API key and TPM/concurrency by `genfity_user_id` account scope.
6. Gateway reserves account quota/credit before calling upstream.
7. Gateway forwards request to `AI_ROUTER_CORE2_INTERNAL_URL`.
8. Gateway streams/copies upstream response back to customer.
9. Gateway records usage metadata in the durable ledger and finalizes quota/credit from actual usage.

## Auth boundaries

- `/v1/*`: customer runtime API key.
- `/customer/*`: Genfity JWT role `customer`, `admin`, or `super_admin`.
- `/admin/*`: Genfity JWT role `admin` or `super_admin`.
- `/internal/*`: `X-Internal-Secret` shared with trusted Genfity services only.

## Network boundary

Gateway should call CLIProxyAPI through Docker/private networking:

- `http://cli-proxy-api:8317`

Customer-facing traffic should only use:

- `https://ai.genfity.com`
