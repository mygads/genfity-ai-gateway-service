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
	"genfity-ai-gateway-service/internal/service"
)

func main() {
	_ = godotenv.Load()

	cfg := config.Load()
	logger := middleware.NewLogger(cfg.LogLevel)

	dbpool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid database url")
	}
	defer dbpool.Close()

	if err := dbpool.Ping(context.Background()); err != nil {
		logger.Fatal().Err(err).Msg("database ping failed")
	}

	redisOptions, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid redis url")
	}
	redisClient := redis.NewClient(redisOptions)
	defer redisClient.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.Warn().Err(err).Msg("redis ping failed; rate limiting disabled")
		_ = redisClient.Close()
		redisClient = nil
	}

	store := service.NewPostgresStore(dbpool)
	server := httpserver.New(cfg, redisClient, store, logger)

	errCh := make(chan error, 1)
	go func() {
		logger.Info().Str("addr", cfg.HTTPAddr).Msg("starting ai gateway service")
		errCh <- server.ListenAndServe()
	}()

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
