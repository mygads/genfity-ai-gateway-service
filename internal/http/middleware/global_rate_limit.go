package middleware

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

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
		count, err := m.client.Incr(r.Context(), key).Result()
		if err != nil {
			respondError(w, http.StatusInternalServerError, "rate_limit_unavailable")
			return
		}
		if count == 1 {
			_ = m.client.Expire(r.Context(), key, time.Minute).Err()
		}
		if int(count) > limit {
			respondError(w, http.StatusTooManyRequests, "global_rate_limit_exceeded")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
