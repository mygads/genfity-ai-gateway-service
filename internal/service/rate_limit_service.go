package service

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

type RateLimitService struct {
	client          *redis.Client
	prefix          string
	rpmLimit        int
	tpmLimit        int
	concurrentLimit int
	log             zerolog.Logger
}

func NewRateLimitService(client *redis.Client, prefix string, rpmLimit, tpmLimit, concurrentLimit int, logger zerolog.Logger) *RateLimitService {
	return &RateLimitService{
		client:          client,
		prefix:          prefix,
		rpmLimit:        rpmLimit,
		tpmLimit:        tpmLimit,
		concurrentLimit: concurrentLimit,
		log:             logger.With().Str("component", "ratelimit_service").Logger(),
	}
}

func (s *RateLimitService) CheckRPM(ctx context.Context, apiKeyID string) error {
	key := fmt.Sprintf("%s:rl:api-key:%s:rpm", s.prefix, apiKeyID)
	count, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return err
	}
	if count == 1 {
		_ = s.client.Expire(ctx, key, time.Minute).Err()
	}
	if int(count) > s.rpmLimit {
		return fmt.Errorf("rate_limit_exceeded")
	}
	return nil
}

func (s *RateLimitService) CheckTPM(ctx context.Context, tenantID string, estimatedTokens int64) error {
	key := fmt.Sprintf("%s:rl:tenant:%s:tpm", s.prefix, tenantID)
	count, err := s.client.IncrBy(ctx, key, estimatedTokens).Result()
	if err != nil {
		return err
	}
	if count == estimatedTokens {
		_ = s.client.Expire(ctx, key, time.Minute).Err()
	}
	if int(count) > s.tpmLimit {
		return fmt.Errorf("rate_limit_exceeded")
	}
	return nil
}

func (s *RateLimitService) AcquireConcurrency(ctx context.Context, tenantID string) (func(), error) {
	key := fmt.Sprintf("%s:concurrent:tenant:%s", s.prefix, tenantID)
	count, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if int(count) > s.concurrentLimit {
		_ = s.client.Decr(ctx, key).Err()
		return nil, fmt.Errorf("rate_limit_exceeded")
	}
	release := func() {
		_ = s.client.Decr(ctx, key).Err()
	}
	return release, nil
}
