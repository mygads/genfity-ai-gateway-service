package service

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"genfity-ai-gateway-service/internal/store"
)

type RateLimitService struct {
	client *redis.Client
	prefix string
	log    zerolog.Logger
}

type PlanLimits struct {
	RPM             int
	TPM             int
	ConcurrentLimit int
}

func NewRateLimitService(client *redis.Client, prefix string, logger zerolog.Logger) *RateLimitService {
	return &RateLimitService{
		client: client,
		prefix: prefix,
		log:    logger.With().Str("component", "ratelimit_service").Logger(),
	}
}

func (s *RateLimitService) CheckRPM(ctx context.Context, apiKeyID string, limit int) error {
	if limit <= 0 {
		return nil
	}
	key := fmt.Sprintf("%s:rl:api-key:%s:rpm", s.prefix, apiKeyID)
	count, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return err
	}
	if count == 1 {
		_ = s.client.Expire(ctx, key, time.Minute).Err()
	}
	if int(count) > limit {
		return fmt.Errorf("rate_limit_exceeded")
	}
	return nil
}

func (s *RateLimitService) CheckTPM(ctx context.Context, accountID string, estimatedTokens int64, limit int) error {
	if limit <= 0 {
		return nil
	}
	key := fmt.Sprintf("%s:rl:account:%s:tpm", s.prefix, accountID)
	count, err := s.client.IncrBy(ctx, key, estimatedTokens).Result()
	if err != nil {
		return err
	}
	if count == estimatedTokens {
		_ = s.client.Expire(ctx, key, time.Minute).Err()
	}
	if int(count) > limit {
		_ = s.client.IncrBy(ctx, key, -estimatedTokens).Err()
		return fmt.Errorf("rate_limit_exceeded")
	}
	return nil
}

func (s *RateLimitService) AcquireConcurrency(ctx context.Context, accountID string, limit int) (func(), error) {
	if limit <= 0 {
		return func() {}, nil
	}
	key := fmt.Sprintf("%s:concurrent:account:%s", s.prefix, accountID)
	count, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if count == 1 {
		_ = s.client.Expire(ctx, key, 10*time.Minute).Err()
	}
	if int(count) > limit {
		_ = s.client.Decr(ctx, key).Err()
		return nil, fmt.Errorf("rate_limit_exceeded")
	}
	release := func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		next, err := s.client.Decr(releaseCtx, key).Result()
		if err == nil && next <= 0 {
			_ = s.client.Del(releaseCtx, key).Err()
		}
	}
	return release, nil
}

func PlanLimitsFromSnapshot(plan *store.SubscriptionPlanSnapshot) PlanLimits {
	limits := PlanLimits{}
	if plan == nil {
		return limits
	}
	if plan.RateLimitRPM != nil {
		limits.RPM = int(*plan.RateLimitRPM)
	}
	if plan.RateLimitTPM != nil {
		limits.TPM = int(*plan.RateLimitTPM)
	}
	if plan.ConcurrentLimit != nil {
		limits.ConcurrentLimit = int(*plan.ConcurrentLimit)
	}
	return limits
}

func (l PlanLimits) HasRPM() bool {
	return l.RPM > 0
}

func (l PlanLimits) HasTPM() bool {
	return l.TPM > 0
}

func (l PlanLimits) HasConcurrency() bool {
	return l.ConcurrentLimit > 0
}

func (l PlanLimits) HasAny() bool {
	return l.HasRPM() || l.HasTPM() || l.HasConcurrency()
}

func (l PlanLimits) TPMAllowance(estimatedTokens int64) int64 {
	if !l.HasTPM() {
		return 0
	}
	if estimatedTokens > 0 {
		return estimatedTokens
	}
	return 1
}

func (l PlanLimits) TPMExceeded(actualTotalTokens int64) bool {
	return l.HasTPM() && actualTotalTokens > int64(l.TPM)
}
