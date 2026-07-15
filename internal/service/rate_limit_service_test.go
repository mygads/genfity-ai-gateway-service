package service

import (
	"context"
	"testing"
	"time"

	"genfity-ai-gateway-service/internal/store"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

func newTestRateLimitService(t *testing.T) (*RateLimitService, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewRateLimitService(client, "test", zerolog.Nop()), mr
}

func TestCheckRPMIsAtomicAndDoesNotInflateRejectedRequests(t *testing.T) {
	svc, mr := newTestRateLimitService(t)
	ctx := context.Background()
	key := "test:rl:api-key:user-1:rpm"

	for i := 0; i < 3; i++ {
		if err := svc.CheckRPM(ctx, "user-1", 3); err != nil {
			t.Fatalf("CheckRPM request %d: %v", i+1, err)
		}
	}
	if err := svc.CheckRPM(ctx, "user-1", 3); err == nil || err.Error() != "rate_limit_exceeded" {
		t.Fatalf("CheckRPM over limit error = %v, want rate_limit_exceeded", err)
	}
	if got, _ := mr.Get(key); got != "3" {
		t.Fatalf("counter after rejected request = %q, want 3", got)
	}
	if ttl := mr.TTL(key); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("counter TTL = %v, want (0, 1m]", ttl)
	}

	mr.FastForward(time.Minute)
	if err := svc.CheckRPM(ctx, "user-1", 3); err != nil {
		t.Fatalf("CheckRPM after window expiry: %v", err)
	}
	if got, _ := mr.Get(key); got != "1" {
		t.Fatalf("new-window counter = %q, want 1", got)
	}
}

func TestCheckRPMReplacesLegacyCounterWithoutTTL(t *testing.T) {
	svc, mr := newTestRateLimitService(t)
	ctx := context.Background()
	key := "test:rl:api-key:user-legacy:rpm"
	mr.Set(key, "180")

	if err := svc.CheckRPM(ctx, "user-legacy", 120); err != nil {
		t.Fatalf("CheckRPM should heal stale counter, got %v", err)
	}
	if got, _ := mr.Get(key); got != "1" {
		t.Fatalf("healed counter = %q, want 1", got)
	}
	if ttl := mr.TTL(key); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("healed counter TTL = %v, want (0, 1m]", ttl)
	}
}

func TestGetRPMCountDeletesLegacyCounterWithoutTTL(t *testing.T) {
	svc, mr := newTestRateLimitService(t)
	ctx := context.Background()
	key := "test:rl:api-key:user-dashboard:rpm"
	mr.Set(key, "201")

	if got := svc.GetRPMCount(ctx, "user-dashboard"); got != 0 {
		t.Fatalf("GetRPMCount = %d, want 0 for stale counter", got)
	}
	if mr.Exists(key) {
		t.Fatal("stale counter still exists after dashboard read")
	}
}

func TestPlanLimitsFromSnapshot_MapsCreditLimits(t *testing.T) {
	rpm := int32(120)
	rpd := int32(900)
	maxReq := int32(6000)
	creditDay := 150.5
	creditPeriod := 700.25

	limits := PlanLimitsFromSnapshot(&store.SubscriptionPlanSnapshot{
		RateLimitRPM:         &rpm,
		RateLimitRPD:         &rpd,
		MaxRequestsPerPeriod: &maxReq,
		CreditLimitPerDay:    &creditDay,
		CreditLimitPerPeriod: &creditPeriod,
	})

	if limits.RPM != int(rpm) {
		t.Fatalf("RPM = %d, want %d", limits.RPM, rpm)
	}
	if limits.RPD != int(rpd) {
		t.Fatalf("RPD = %d, want %d", limits.RPD, rpd)
	}
	if limits.MaxRequestsPerPeriod != int(maxReq) {
		t.Fatalf("MaxRequestsPerPeriod = %d, want %d", limits.MaxRequestsPerPeriod, maxReq)
	}
	if limits.CreditLimitPerDay != creditDay {
		t.Fatalf("CreditLimitPerDay = %v, want %v", limits.CreditLimitPerDay, creditDay)
	}
	if limits.CreditLimitPerPeriod != creditPeriod {
		t.Fatalf("CreditLimitPerPeriod = %v, want %v", limits.CreditLimitPerPeriod, creditPeriod)
	}
	if !limits.HasCreditPerDay() {
		t.Fatal("HasCreditPerDay = false, want true")
	}
	if !limits.HasCreditPerPeriod() {
		t.Fatal("HasCreditPerPeriod = false, want true")
	}
}
