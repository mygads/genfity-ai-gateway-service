package handler

import (
	"encoding/json"
	"testing"
	"time"

	"genfity-ai-gateway-service/internal/service"
	"genfity-ai-gateway-service/internal/store"
)

func subWithWindowAndDuration(t *testing.T, windowDays int, baseDurationDays int) *service.ActiveSubscription {
	t.Helper()
	start := time.Date(2026, 6, 4, 20, 14, 0, 0, time.UTC)
	end := start.AddDate(0, 0, windowDays)
	ent := &store.CustomerEntitlement{
		PeriodStart: &start,
		PeriodEnd:   &end,
	}
	var meta json.RawMessage
	if baseDurationDays > 0 {
		b, _ := json.Marshal(map[string]any{"durationDays": baseDurationDays})
		meta = b
	}
	return &service.ActiveSubscription{
		Entitlement: ent,
		Plan:        &store.SubscriptionPlanSnapshot{Metadata: meta},
	}
}

func TestPeriodRPPMultiplier(t *testing.T) {
	cases := []struct {
		name       string
		windowDays int
		baseDays   int
		want       int
	}{
		{"single 1-day purchase", 1, 1, 1},
		{"1-day plan stacked 4x (Zan)", 4, 1, 4},
		{"7-day plan bought once", 7, 7, 1},
		{"7-day plan stacked 2x", 14, 7, 2},
		{"30-day plan bought once", 30, 30, 1},
		{"unknown base duration -> no scaling", 4, 0, 1},
		{"window shorter than base -> floor at 1", 1, 7, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := subWithWindowAndDuration(t, tc.windowDays, tc.baseDays)
			got := periodRPPMultiplier(sub)
			if got != tc.want {
				t.Errorf("multiplier window=%dd base=%dd: want %d, got %d", tc.windowDays, tc.baseDays, tc.want, got)
			}
		})
	}
}

func TestPeriodRPPMultiplier_NilSafe(t *testing.T) {
	if periodRPPMultiplier(nil) != 1 {
		t.Error("nil subscription should yield multiplier 1")
	}
	if periodRPPMultiplier(&service.ActiveSubscription{}) != 1 {
		t.Error("empty subscription should yield multiplier 1")
	}
}

func TestPlanBaseDurationDays(t *testing.T) {
	sub := subWithWindowAndDuration(t, 1, 7)
	if got := planBaseDurationDays(sub); got != 7 {
		t.Errorf("want 7, got %d", got)
	}
	if got := planBaseDurationDays(subWithWindowAndDuration(t, 1, 0)); got != 0 {
		t.Errorf("missing durationDays should be 0, got %d", got)
	}
}
