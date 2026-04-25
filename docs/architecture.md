# Architecture

`genfity-ai-gateway-service` adalah runtime AI Gateway untuk `ai.genfity.com`. Service ini berada di antara customer API clients dan 9Router.

## Ownership

- `genfity-app`: login, session, RBAC, payment, order, subscription package, customer/admin UI.
- `genfity-ai-gateway-service`: API keys, runtime model catalog, model pricing, route mapping, usage ledger, request metadata, quota/rate limit state.
- `9router`: provider config, OAuth/API provider credentials, combos, fallback, provider model routing, proxy pools.

## Runtime flow

1. Customer calls `POST /v1/chat/completions` with `Authorization: Bearer sk_genfity_live_...`.
2. Gateway validates API key by prefix and HMAC hash.
3. Gateway checks active Genfity entitlement snapshot.
4. Gateway resolves public model to 9Router route.
5. Gateway enforces Redis RPM/concurrency limits.
6. Gateway forwards request to `NINE_ROUTER_CORE1_INTERNAL_URL`.
7. Gateway streams/copies upstream response back to customer.
8. Gateway records usage metadata in the durable ledger once persistence is wired.

## Auth boundaries

- `/v1/*`: customer runtime API key.
- `/customer/*`: Genfity JWT role `customer`, `admin`, or `super_admin`.
- `/admin/*`: Genfity JWT role `admin` or `super_admin`.
- `/internal/*`: `X-Internal-Secret` shared with trusted Genfity services only.

## Network boundary

Gateway should call 9Router through Docker/private networking:

- `http://ai-core1-9router:20128`

Customer-facing traffic should only use:

- `https://ai.genfity.com`
