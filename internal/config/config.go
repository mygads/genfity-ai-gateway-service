package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	AppEnv   string
	HTTPAddr string

	DatabaseURL string
	RedisURL    string
	RedisPrefix string

	GenfityAuthMode       string
	GenfityAuthJWKSURL    string
	GenfityJWTSecret      string
	GenfityInternalSecret string

	NineRouterCore1InternalURL string
	NineRouterCore1PublicURL   string
	NineRouterCore1APIKey      string

	APIKeyPepper          string
	EncryptionKey         string
	DefaultCurrency       string
	LogLevel              string
	RequestTimeoutSeconds int
	RateLimitRPM            int
	RateLimitTPM            int
	ConcurrentLimit         int
	GlobalRateLimitEnabled  bool
	GlobalRateLimitRPM      int
	GlobalRateLimitBurst    int
}

func Load() Config {
	return Config{
		AppEnv:   getEnv("APP_ENV", "development"),
		HTTPAddr: getEnv("HTTP_ADDR", ":8080"),

		DatabaseURL: getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/genfity_ai_gateway?sslmode=disable"),
		RedisURL:    getEnv("REDIS_URL", "redis://localhost:6379/3"),
		RedisPrefix: getEnv("REDIS_PREFIX", "ai-gateway:prod"),

		GenfityAuthMode:       getEnv("GENFITY_AUTH_MODE", "jwt"),
		GenfityAuthJWKSURL:    getEnv("GENFITY_AUTH_JWKS_URL", ""),
		GenfityJWTSecret:      getEnv("GENFITY_JWT_SECRET", getEnv("JWT_SECRET", "")),
		GenfityInternalSecret: getEnv("GENFITY_INTERNAL_SECRET", ""),

		NineRouterCore1InternalURL: getEnv("NINE_ROUTER_CORE1_INTERNAL_URL", "http://ai-core1-9router:20128"),
		NineRouterCore1PublicURL:   getEnv("NINE_ROUTER_CORE1_PUBLIC_URL", "https://ai-core1.genfity.com"),
		NineRouterCore1APIKey:      getEnv("NINE_ROUTER_CORE1_API_KEY", ""),

		APIKeyPepper:          getEnv("API_KEY_PEPPER", ""),
		EncryptionKey:         getEnv("ENCRYPTION_KEY", ""),
		DefaultCurrency:       getEnv("DEFAULT_CURRENCY", "IDR"),
		LogLevel:              getEnv("LOG_LEVEL", "info"),
		RequestTimeoutSeconds: getEnvInt("REQUEST_TIMEOUT_SECONDS", 120),
		RateLimitRPM:           getEnvInt("RATE_LIMIT_RPM", 60),
		RateLimitTPM:           getEnvInt("RATE_LIMIT_TPM", 120000),
		ConcurrentLimit:        getEnvInt("CONCURRENT_LIMIT", 5),
		GlobalRateLimitEnabled: getEnvBool("GLOBAL_RATE_LIMIT_ENABLED", true),
		GlobalRateLimitRPM:     getEnvInt("GLOBAL_RATE_LIMIT_RPM", 300),
		GlobalRateLimitBurst:   getEnvInt("GLOBAL_RATE_LIMIT_BURST", 60),
	}
}

func (c Config) AllowedCustomerRoles() []string {
	return []string{"customer", "admin", "super_admin"}
}

func (c Config) AllowedAdminRoles() []string {
	return []string{"admin", "super_admin"}
}

func getEnv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func getEnvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
