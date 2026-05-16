package middleware

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/redis/go-redis/v9"
)

var rateLimitScript = redis.NewScript(`
local key = KEYS[1]
local count = redis.call("INCR", key)
if count == 1 then
    redis.call("EXPIRE", key, ARGV[1])
end
return count
`)

type GlobalRateLimitMiddleware struct {
	client  *redis.Client
	prefix  string
	enabled bool
	rpm     int
	burst   int
}

func NewGlobalRateLimitMiddleware(client *redis.Client, prefix string, enabled bool, rpm, burst int) *GlobalRateLimitMiddleware {
	return &GlobalRateLimitMiddleware{
		client:  client,
		prefix:  prefix,
		enabled: enabled,
		rpm:     rpm,
		burst:   burst,
	}
}

func (m *GlobalRateLimitMiddleware) Limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.enabled || m.client == nil || m.rpm <= 0 {
			next.ServeHTTP(w, r)
			return
		}

		ip := clientIP(r)
		limit := m.rpm + m.burst
		key := fmt.Sprintf("%s:rl:global:%s:%s", m.prefix, r.URL.Path, ip)
		count, err := rateLimitScript.Run(r.Context(), m.client, []string{key}, 60).Int64()
		if err != nil {
			respondError(w, http.StatusInternalServerError, "rate_limit_unavailable")
			return
		}
		if int(count) > limit {
			respondError(w, http.StatusTooManyRequests, "global_rate_limit_exceeded")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// EvalSha preloads the rate-limit script into Redis on startup.
func (m *GlobalRateLimitMiddleware) Warmup(ctx context.Context) {
	if m.client == nil {
		return
	}
	_ = rateLimitScript.Load(ctx, m.client).Err()
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if isTrustedProxy(host) {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			if ip := strings.TrimSpace(parts[0]); ip != "" {
				return ip
			}
		}
		if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
			return realIP
		}
	}
	return host
}

func isTrustedProxy(ip string) bool {
	trusted := [...]string{"127.0.0.1", "::1", "172.17.", "172.18.", "172.19.", "172.20.", "172.21.", "10.", "192.168."}
	for _, prefix := range trusted {
		if strings.HasPrefix(ip, prefix) {
			return true
		}
	}
	return false
}
