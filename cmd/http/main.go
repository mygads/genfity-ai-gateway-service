package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"genfity-ai-gateway-service/internal/config"
	httpserver "genfity-ai-gateway-service/internal/http"
	"genfity-ai-gateway-service/internal/http/middleware"
	"genfity-ai-gateway-service/internal/router"
	"genfity-ai-gateway-service/internal/service"
)

func main() {
	_ = godotenv.Load()

	cfg := config.Load()
	logger := middleware.NewLogger(cfg.LogLevel)
	startupCtx := context.Background()

	dbpool, err := pgxpool.New(startupCtx, cfg.DatabaseURL)
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid database url")
	}
	defer dbpool.Close()

	if err := dbpool.Ping(startupCtx); err != nil {
		logger.Fatal().Err(err).Msg("db down")
	}
	logger.Info().Msg("db up")

	redisOptions, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid redis url")
	}
	redisClient := redis.NewClient(redisOptions)
	defer redisClient.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := redisClient.Ping(startupCtx).Err(); err != nil {
		logger.Warn().Err(err).Msg("redis off, rate limit disabled")
		_ = redisClient.Close()
		redisClient = nil
	} else {
		logger.Info().Msg("redis up")
	}

	nineClient := router.NewNineRouterClient(cfg.NineRouterCore1InternalURL, cfg.NineRouterCore1APIKey, time.Duration(cfg.RequestTimeoutSeconds)*time.Second)
	if _, err := nineClient.RouterHealth(startupCtx); err != nil {
		logger.Fatal().Err(err).Msg("9router down")
	}
	logger.Info().Msg("9router up")

	store := service.NewPostgresStore(dbpool)
	server := httpserver.New(cfg, redisClient, store, logger)

	errCh := make(chan error, 1)
	go func() {
		logger.Info().Str("addr", cfg.HTTPAddr).Msg("gateway ready")
		errCh <- server.ListenAndServe()
	}()

	logger.Info().
		Str("db", "up").
		Str("redis", map[bool]string{true: "up", false: "off"}[redisClient != nil]).
		Str("9router", "up").
		Msg("checks ok")

	select {
	case <-ctx.Done():
		logger.Info().Msg("shutdown signal received")
		time.Sleep(250 * time.Millisecond)
	case err := <-errCh:
		if err != nil {
			logger.Fatal().Err(err).Msg("server stopped")
		}
	}

	zerolog.DefaultContextLogger = nil
}
