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
	// MaxRequestsPerPeriod caps total requests for one entitlement period
	// (period_start..period_end). 0 = unlimited.
	MaxRequestsPerPeriod int
	// RPD caps requests per calendar day (UTC) per user on the plan. 0 =
	// no daily limit. Independent of MaxRequestsPerPeriod.
	RPD                  int
	CreditLimitPerDay    float64
	CreditLimitPerPeriod float64
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
	if plan.RateLimitRPD != nil {
		limits.RPD = int(*plan.RateLimitRPD)
	}
	if plan.CreditLimitPerDay != nil {
		limits.CreditLimitPerDay = *plan.CreditLimitPerDay
	}
	if plan.CreditLimitPerPeriod != nil {
		limits.CreditLimitPerPeriod = *plan.CreditLimitPerPeriod
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

func (l PlanLimits) HasRPD() bool {
	return l.RPD > 0
}

func (l PlanLimits) HasCreditPerDay() bool {
	return l.CreditLimitPerDay > 0
}

func (l PlanLimits) HasCreditPerPeriod() bool {
	return l.CreditLimitPerPeriod > 0
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

// CheckPlanRPD enforces the per-(user,plan) request count for the current
// UTC calendar day. Independent of MaxRequestsPerPeriod, which is scoped
// to the entitlement period; RPD resets at UTC midnight every day. The
// counter expires after 25 hours so it survives clock skew across the
// day boundary. limit <= 0 means no daily cap.
func (s *RateLimitService) CheckPlanRPD(ctx context.Context, userID, planCode string, limit int) error {
	if limit <= 0 || userID == "" {
		return nil
	}
	day := time.Now().UTC().Format("20060102")
	key := fmt.Sprintf("%s:rl:plan-day:%s:%s:%s", s.prefix, userID, planCode, day)
	count, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return err
	}
	if count == 1 {
		_ = s.client.Expire(ctx, key, 25*time.Hour).Err()
	}
	if int(count) > limit {
		_ = s.client.Decr(ctx, key).Err()
		return fmt.Errorf("plan_rpd_exceeded")
	}
	return nil
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

// GetPlanRPDCount reads (does NOT increment) the per-(user,plan) request
// count for the current UTC day. Mirrors the key scheme of CheckPlanRPD
// so the admin billing-detail modal can show "rpd_used / rpd_limit"
// without consuming the user's daily quota. Returns 0 when the key is
// missing/expired or on any read error.
func (s *RateLimitService) GetPlanRPDCount(ctx context.Context, userID, planCode string) int {
	if userID == "" || planCode == "" {
		return 0
	}
	day := time.Now().UTC().Format("20060102")
	key := fmt.Sprintf("%s:rl:plan-day:%s:%s:%s", s.prefix, userID, planCode, day)
	n, err := s.client.Get(ctx, key).Int()
	if err != nil {
		return 0
	}
	if n < 0 {
		return 0
	}
	return n
}

// GetRequestsPerPeriodCount reads (does NOT increment) the
// per-(user,period) request count. Mirrors the key scheme of
// CheckRequestsPerPeriod. periodKey must be derived the same way the
// request path derives it. Returns 0 when missing/expired or on error.
func (s *RateLimitService) GetRequestsPerPeriodCount(ctx context.Context, userID, periodKey string) int {
	if userID == "" || periodKey == "" {
		return 0
	}
	key := fmt.Sprintf("%s:rl:plan-period:%s:%s", s.prefix, userID, periodKey)
	n, err := s.client.Get(ctx, key).Int()
	if err != nil {
		return 0
	}
	if n < 0 {
		return 0
	}
	return n
}

func (s *RateLimitService) CheckPlanCreditRPD(ctx context.Context, userID, planCode string, amount, limit float64) error {
	if amount <= 0 || limit <= 0 || userID == "" || planCode == "" {
		return nil
	}
	day := time.Now().UTC().Format("20060102")
	key := fmt.Sprintf("%s:rl:plan-credit-day:%s:%s:%s", s.prefix, userID, planCode, day)
	count, err := s.client.IncrByFloat(ctx, key, amount).Result()
	if err != nil {
		return err
	}
	if count == amount {
		_ = s.client.Expire(ctx, key, 25*time.Hour).Err()
	}
	if count > limit {
		_ = s.client.IncrByFloat(ctx, key, -amount).Err()
		return fmt.Errorf("plan_credit_rpd_exceeded")
	}
	return nil
}

func (s *RateLimitService) FinalizePlanCreditRPD(ctx context.Context, userID, planCode string, reserved, actual float64) error {
	if reserved <= 0 || userID == "" || planCode == "" {
		return nil
	}
	day := time.Now().UTC().Format("20060102")
	key := fmt.Sprintf("%s:rl:plan-credit-day:%s:%s:%s", s.prefix, userID, planCode, day)
	delta := actual - reserved
	if delta == 0 {
		return nil
	}
	return s.client.IncrByFloat(ctx, key, delta).Err()
}

func (s *RateLimitService) CheckPlanCreditsPerPeriod(ctx context.Context, userID, periodKey string, ttl time.Duration, amount, limit float64) error {
	if amount <= 0 || limit <= 0 || userID == "" || periodKey == "" {
		return nil
	}
	key := fmt.Sprintf("%s:rl:plan-credit-period:%s:%s", s.prefix, userID, periodKey)
	count, err := s.client.IncrByFloat(ctx, key, amount).Result()
	if err != nil {
		return err
	}
	if count == amount && ttl > 0 {
		_ = s.client.Expire(ctx, key, ttl).Err()
	}
	if count > limit {
		_ = s.client.IncrByFloat(ctx, key, -amount).Err()
		return fmt.Errorf("plan_credit_period_exceeded")
	}
	return nil
}

func (s *RateLimitService) FinalizePlanCreditsPerPeriod(ctx context.Context, userID, periodKey string, reserved, actual float64) error {
	if reserved <= 0 || userID == "" || periodKey == "" {
		return nil
	}
	key := fmt.Sprintf("%s:rl:plan-credit-period:%s:%s", s.prefix, userID, periodKey)
	delta := actual - reserved
	if delta == 0 {
		return nil
	}
	return s.client.IncrByFloat(ctx, key, delta).Err()
}

func (s *RateLimitService) GetPlanCreditRPDCount(ctx context.Context, userID, planCode string) float64 {
	if userID == "" || planCode == "" {
		return 0
	}
	day := time.Now().UTC().Format("20060102")
	key := fmt.Sprintf("%s:rl:plan-credit-day:%s:%s:%s", s.prefix, userID, planCode, day)
	n, err := s.client.Get(ctx, key).Float64()
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (s *RateLimitService) GetPlanCreditsPerPeriodCount(ctx context.Context, userID, periodKey string) float64 {
	if userID == "" || periodKey == "" {
		return 0
	}
	key := fmt.Sprintf("%s:rl:plan-credit-period:%s:%s", s.prefix, userID, periodKey)
	n, err := s.client.Get(ctx, key).Float64()
	if err != nil || n < 0 {
		return 0
	}
	return n
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

// RollbackRequestsPerPeriod undoes one increment of the per-(user,plan,
// period) counter. Use when a later check (free-model RPM/RPD,
// concurrency, balance reservation) rejected the request after the
// period counter was already incremented — without this, the period
// counter leaks every rejection and bans the user well below the
// configured cap.
func (s *RateLimitService) RollbackRequestsPerPeriod(ctx context.Context, userID, periodKey string) {
	if userID == "" || periodKey == "" {
		return
	}
	key := fmt.Sprintf("%s:rl:plan-period:%s:%s", s.prefix, userID, periodKey)
	_, _ = s.client.Decr(ctx, key).Result()
}

// RollbackPlanRPD undoes one increment of the per-(user,plan) daily
// counter. Same rationale as RollbackRequestsPerPeriod.
func (s *RateLimitService) RollbackPlanRPD(ctx context.Context, userID, planCode string) {
	if userID == "" {
		return
	}
	day := time.Now().UTC().Format("20060102")
	key := fmt.Sprintf("%s:rl:plan-day:%s:%s:%s", s.prefix, userID, planCode, day)
	_, _ = s.client.Decr(ctx, key).Result()
}

// RollbackFreeModelRPD/RPM mirror the period/RPD rollbacks for the
// per-(user,model) counters.
func (s *RateLimitService) RollbackFreeModelRPM(ctx context.Context, userID, publicModel string) {
	if userID == "" || publicModel == "" {
		return
	}
	key := fmt.Sprintf("%s:rl:free-model:%s:%s:rpm", s.prefix, userID, publicModel)
	_, _ = s.client.Decr(ctx, key).Result()
}

func (s *RateLimitService) RollbackFreeModelRPD(ctx context.Context, userID, publicModel string) {
	if userID == "" || publicModel == "" {
		return
	}
	day := time.Now().UTC().Format("20060102")
	key := fmt.Sprintf("%s:rl:free-model:%s:%s:rpd:%s", s.prefix, userID, publicModel, day)
	_, _ = s.client.Decr(ctx, key).Result()
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
