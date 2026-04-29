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

	AIRouterCore2InternalURL string
	AIRouterCore2PublicURL   string
	AIRouterCore2APIKey      string

	APIKeyPepper           string
	EncryptionKey          string
	DefaultCurrency        string
	LogLevel               string
	RequestTimeoutSeconds  int
	GlobalRateLimitEnabled bool
	GlobalRateLimitRPM     int
	GlobalRateLimitBurst   int
}

func Load() Config {
	cfg := Config{
		AppEnv:   getEnv("APP_ENV", "development"),
		HTTPAddr: getEnv("HTTP_ADDR", ":8080"),

		DatabaseURL: getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/genfity_ai_gateway?sslmode=disable"),
		RedisURL:    getEnv("REDIS_URL", "redis://localhost:6379/3"),
		RedisPrefix: getEnv("REDIS_PREFIX", "ai-gateway:dev"),

		GenfityAuthMode:       getEnv("GENFITY_AUTH_MODE", "jwt"),
		GenfityAuthJWKSURL:    getEnv("GENFITY_AUTH_JWKS_URL", ""),
		GenfityJWTSecret:      getEnv("GENFITY_JWT_SECRET", getEnv("JWT_SECRET", "")),
		GenfityInternalSecret: getEnv("GENFITY_INTERNAL_SECRET", ""),

		AIRouterCore2InternalURL: getEnv("AI_ROUTER_CORE2_INTERNAL_URL", "http://localhost:8317"),
		AIRouterCore2PublicURL:   getEnv("AI_ROUTER_CORE2_PUBLIC_URL", ""),
		AIRouterCore2APIKey:      getEnv("AI_ROUTER_CORE2_API_KEY", ""),

		APIKeyPepper:           getEnv("API_KEY_PEPPER", ""),
		EncryptionKey:          getEnv("ENCRYPTION_KEY", ""),
		DefaultCurrency:        getEnv("DEFAULT_CURRENCY", "IDR"),
		LogLevel:               getEnv("LOG_LEVEL", "info"),
		RequestTimeoutSeconds:  getEnvInt("REQUEST_TIMEOUT_SECONDS", 120),
		GlobalRateLimitEnabled: getEnvBool("GLOBAL_RATE_LIMIT_ENABLED", true),
		GlobalRateLimitRPM:     getEnvInt("GLOBAL_RATE_LIMIT_RPM", 300),
		GlobalRateLimitBurst:   getEnvInt("GLOBAL_RATE_LIMIT_BURST", 60),
	}
	cfg.validate()
	return cfg
}

func (c Config) IsDevelopment() bool {
	return strings.EqualFold(c.AppEnv, "development") || strings.EqualFold(c.AppEnv, "dev") || strings.EqualFold(c.AppEnv, "local")
}

func (c Config) validate() {
	if c.IsDevelopment() {
		return
	}
	requireConfig("DATABASE_URL", c.DatabaseURL)
	requireConfig("REDIS_URL", c.RedisURL)
	requireConfig("GENFITY_INTERNAL_SECRET", c.GenfityInternalSecret)
	requireConfig("API_KEY_PEPPER", c.APIKeyPepper)
	requireConfig("ENCRYPTION_KEY", c.EncryptionKey)
	requireConfig("AI_ROUTER_CORE2_API_KEY", c.AIRouterCore2APIKey)
	if strings.EqualFold(c.GenfityAuthMode, "jwt") {
		requireConfig("GENFITY_JWT_SECRET", c.GenfityJWTSecret)
	}
}

func requireConfig(name, value string) {
	if strings.TrimSpace(value) == "" {
		panic("missing required config: " + name)
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
