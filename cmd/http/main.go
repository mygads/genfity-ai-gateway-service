package main

import (
	"context"
	"net/http"
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

	// Retry DB ping with exponential-ish backoff so a temporarily down
	// postgres at startup does not crash the service forever.
	dbBackoffs := []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		15 * time.Second,
		30 * time.Second,
	}
	dbReady := false
	for attempt := 0; attempt < 30; attempt++ {
		pingCtx, cancel := context.WithTimeout(startupCtx, 5*time.Second)
		err := dbpool.Ping(pingCtx)
		cancel()
		if err == nil {
			dbReady = true
			if attempt > 0 {
				logger.Info().Int("attempt", attempt+1).Msg("db up after retry")
			} else {
				logger.Info().Msg("db up")
			}
			break
		}
		wait := dbBackoffs[len(dbBackoffs)-1]
		if attempt < len(dbBackoffs) {
			wait = dbBackoffs[attempt]
		}
		logger.Warn().Err(err).Int("attempt", attempt+1).Dur("retry_in", wait).Msg("db not ready, retrying")
		time.Sleep(wait)
	}
	if !dbReady {
		logger.Fatal().Msg("db unreachable after retries, giving up")
	}

	redisOptions, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid redis url")
	}
	redisClient := redis.NewClient(redisOptions)
	defer redisClient.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Retry redis ping a few times before falling back to "rate limit disabled"
	redisReady := false
	for attempt := 0; attempt < 5; attempt++ {
		pingCtx, cancel := context.WithTimeout(startupCtx, 3*time.Second)
		err := redisClient.Ping(pingCtx).Err()
		cancel()
		if err == nil {
			redisReady = true
			if attempt > 0 {
				logger.Info().Int("attempt", attempt+1).Msg("redis up after retry")
			} else {
				logger.Info().Msg("redis up")
			}
			break
		}
		wait := time.Duration(2*(attempt+1)) * time.Second
		logger.Warn().Err(err).Int("attempt", attempt+1).Dur("retry_in", wait).Msg("redis not ready, retrying")
		time.Sleep(wait)
	}
	if !redisReady {
		logger.Warn().Msg("redis off after retries, rate limit disabled")
		_ = redisClient.Close()
		redisClient = nil
	}

	cliClient := router.NewCLIProxyClient(cfg.AIRouterCore2InternalURL, cfg.AIRouterCore2APIKey, time.Duration(cfg.RequestTimeoutSeconds)*time.Second)
	cliReady := false
	for attempt := 0; attempt < 3; attempt++ {
		hcCtx, cancel := context.WithTimeout(startupCtx, 5*time.Second)
		_, err := cliClient.RouterHealth(hcCtx)
		cancel()
		if err == nil {
			cliReady = true
			if attempt > 0 {
				logger.Info().Int("attempt", attempt+1).Msg("cli_proxy up after retry")
			} else {
				logger.Info().Msg("cli_proxy up")
			}
			break
		}
		wait := time.Duration(2*(attempt+1)) * time.Second
		logger.Warn().Err(err).Int("attempt", attempt+1).Dur("retry_in", wait).Msg("cli_proxy not ready, retrying")
		time.Sleep(wait)
	}
	if !cliReady {
		logger.Warn().Msg("cli_proxy down or unreachable at startup, will keep trying at runtime")
	}

	var store service.Store = service.NewPostgresStore(dbpool)

	server := httpserver.New(cfg, redisClient, store, logger)
	httpServer := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: server.Router,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info().Str("addr", cfg.HTTPAddr).Msg("gateway ready")
		errCh <- httpServer.ListenAndServe()
	}()

	logger.Info().
		Str("db", "up").
		Str("redis", map[bool]string{true: "up", false: "off"}[redisClient != nil]).
		Str("cli_proxy", "checked").
		Msg("checks ok")

	select {
	case <-ctx.Done():
		logger.Info().Msg("shutdown signal received")
		time.Sleep(250 * time.Millisecond)
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			logger.Fatal().Err(err).Msg("server stopped")
		}
	}

	zerolog.DefaultContextLogger = nil
}
