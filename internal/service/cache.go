package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// Cache is a thin Redis-backed read-through cache used to skip DB lookups
// on the hot request path. It is intentionally minimal — TTLs are short
// (seconds, not minutes) so stale reads can never linger long enough to
// matter.
//
// Keys are namespaced with a version prefix so a deploy can invalidate
// the entire cache by bumping CacheVersion without flushing Redis.
//
// All errors are silently logged at debug level. The cache is an
// optimisation, not a source of truth — callers should fall back to the
// database when Get returns ErrMiss.
type Cache struct {
	client *redis.Client
	prefix string
	log    zerolog.Logger
}

// CacheVersion bumps when the cached value layout changes incompatibly.
// Bumping this invalidates all entries on next read without an explicit
// FLUSHDB. Coordinate with team if you change it.
const CacheVersion = "v1"

// ErrMiss is returned by Get when the key is absent or unmarshalable.
var ErrMiss = errors.New("cache miss")

func NewCache(client *redis.Client, prefix string, logger zerolog.Logger) *Cache {
	if client == nil {
		return nil
	}
	return &Cache{
		client: client,
		prefix: prefix,
		log:    logger.With().Str("component", "cache").Logger(),
	}
}

// Enabled reports whether the cache is usable. Callers can use this to
// skip Get/Set entirely when running in environments without Redis (e.g.
// local dev with the in-memory store).
func (c *Cache) Enabled() bool {
	return c != nil && c.client != nil
}

// key builds the full Redis key: <prefix>:<CacheVersion>:<namespace>:<id>
// e.g. "ai-gateway:prod:v1:apikey:genfity_4c5b7eb5".
func (c *Cache) key(namespace, id string) string {
	return c.prefix + ":" + CacheVersion + ":" + namespace + ":" + id
}

// Get unmarshals the JSON value into out. Returns ErrMiss when the key is
// missing OR when the cached payload fails to decode (we treat the entry
// as poisoned and let the caller refresh from DB).
func (c *Cache) Get(ctx context.Context, namespace, id string, out any) error {
	if !c.Enabled() {
		return ErrMiss
	}
	raw, err := c.client.Get(ctx, c.key(namespace, id)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return ErrMiss
		}
		c.log.Debug().Err(err).Str("ns", namespace).Str("id", id).Msg("cache get failed")
		return ErrMiss
	}
	if err := json.Unmarshal(raw, out); err != nil {
		c.log.Debug().Err(err).Str("ns", namespace).Str("id", id).Msg("cache decode failed; deleting poisoned entry")
		_ = c.client.Del(ctx, c.key(namespace, id)).Err()
		return ErrMiss
	}
	return nil
}

// Set serialises value to JSON and stores it with the given TTL. A nil/zero
// TTL is rejected — short TTLs are required to keep stale reads bounded.
func (c *Cache) Set(ctx context.Context, namespace, id string, value any, ttl time.Duration) {
	if !c.Enabled() || ttl <= 0 {
		return
	}
	raw, err := json.Marshal(value)
	if err != nil {
		c.log.Debug().Err(err).Str("ns", namespace).Str("id", id).Msg("cache encode failed")
		return
	}
	if err := c.client.Set(ctx, c.key(namespace, id), raw, ttl).Err(); err != nil {
		c.log.Debug().Err(err).Str("ns", namespace).Str("id", id).Msg("cache set failed")
	}
}

// Del removes a single key. Used by mutators (regenerate, revoke,
// upsert-balance, etc.) to invalidate stale cached reads.
func (c *Cache) Del(ctx context.Context, namespace, id string) {
	if !c.Enabled() {
		return
	}
	if err := c.client.Del(ctx, c.key(namespace, id)).Err(); err != nil {
		c.log.Debug().Err(err).Str("ns", namespace).Str("id", id).Msg("cache del failed")
	}
}
