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
	RPM                  int
	TPM                  int
	ConcurrentLimit      int
	// MaxRequestsPerPeriod caps total requests for one entitlement period
	// (period_start..period_end). 0 = unlimited.
	MaxRequestsPerPeriod int
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
	if plan.MaxRequestsPerPeriod != nil {
		limits.MaxRequestsPerPeriod = int(*plan.MaxRequestsPerPeriod)
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

func (l PlanLimits) HasMaxRequestsPerPeriod() bool {
	return l.MaxRequestsPerPeriod > 0
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

// CheckRequestsPerPeriod increments and validates the per-(user,plan,period)
// request counter. periodKey identifies the subscription period — the
// caller must derive it from the entitlement (e.g. period_end timestamp).
// ttl is how long the counter should live in Redis after first set; pass
// the time until period_end for accuracy. limit <= 0 means unlimited.
func (s *RateLimitService) CheckRequestsPerPeriod(ctx context.Context, userID, periodKey string, ttl time.Duration, limit int) error {
	if limit <= 0 {
		return nil
	}
	if userID == "" || periodKey == "" {
		return nil
	}
	key := fmt.Sprintf("%s:rl:plan-period:%s:%s", s.prefix, userID, periodKey)
	count, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return err
	}
	if count == 1 && ttl > 0 {
		_ = s.client.Expire(ctx, key, ttl).Err()
	}
	if int(count) > limit {
		// Roll back the increment so the counter doesn't drift past the cap.
		_ = s.client.Decr(ctx, key).Err()
		return fmt.Errorf("plan_period_limit_exceeded")
	}
	return nil
}

// CheckFreeModelRPM/RPD/TPD enforce per-(user,model) limits for models
// flagged is_free=true. They share the same window semantics as the plan
// limits (per-minute, per-day) but are scoped to a single model.
func (s *RateLimitService) CheckFreeModelRPM(ctx context.Context, userID, publicModel string, limit int) error {
	if limit <= 0 || userID == "" || publicModel == "" {
		return nil
	}
	key := fmt.Sprintf("%s:rl:free-model:%s:%s:rpm", s.prefix, userID, publicModel)
	count, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return err
	}
	if count == 1 {
		_ = s.client.Expire(ctx, key, time.Minute).Err()
	}
	if int(count) > limit {
		_ = s.client.Decr(ctx, key).Err()
		return fmt.Errorf("free_model_rpm_exceeded")
	}
	return nil
}

func (s *RateLimitService) CheckFreeModelRPD(ctx context.Context, userID, publicModel string, limit int) error {
	if limit <= 0 || userID == "" || publicModel == "" {
		return nil
	}
	day := time.Now().UTC().Format("20060102")
	key := fmt.Sprintf("%s:rl:free-model:%s:%s:rpd:%s", s.prefix, userID, publicModel, day)
	count, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return err
	}
	if count == 1 {
		// Expire after 25 hours to safely cover any clock skew across the
		// UTC day boundary.
		_ = s.client.Expire(ctx, key, 25*time.Hour).Err()
	}
	if int(count) > limit {
		_ = s.client.Decr(ctx, key).Err()
		return fmt.Errorf("free_model_rpd_exceeded")
	}
	return nil
}

// CheckFreeModelTPD reserves estimatedTokens against today's free-model
// token budget for (user,model). Returns an error if the reservation
// would exceed the cap. estimatedTokens<=0 is treated as 1 so we still
// reject zero-token requests once the cap is hit.
func (s *RateLimitService) CheckFreeModelTPD(ctx context.Context, userID, publicModel string, estimatedTokens, limit int64) error {
	if limit <= 0 || userID == "" || publicModel == "" {
		return nil
	}
	if estimatedTokens <= 0 {
		estimatedTokens = 1
	}
	day := time.Now().UTC().Format("20060102")
	key := fmt.Sprintf("%s:rl:free-model:%s:%s:tpd:%s", s.prefix, userID, publicModel, day)
	count, err := s.client.IncrBy(ctx, key, estimatedTokens).Result()
	if err != nil {
		return err
	}
	if count == estimatedTokens {
		_ = s.client.Expire(ctx, key, 25*time.Hour).Err()
	}
	if count > limit {
		_ = s.client.IncrBy(ctx, key, -estimatedTokens).Err()
		return fmt.Errorf("free_model_tpd_exceeded")
	}
	return nil
}

// FinalizeFreeModelTPD reconciles the actual tokens used against the
// estimate previously reserved. Call once the request finishes. Negative
// adjustments shrink the reserved budget; positive adjustments extend it
// up to the cap and fail the request retroactively when over.
func (s *RateLimitService) FinalizeFreeModelTPD(ctx context.Context, userID, publicModel string, delta int64) {
	if delta == 0 || userID == "" || publicModel == "" {
		return
	}
	day := time.Now().UTC().Format("20060102")
	key := fmt.Sprintf("%s:rl:free-model:%s:%s:tpd:%s", s.prefix, userID, publicModel, day)
	_, _ = s.client.IncrBy(ctx, key, delta).Result()
}
