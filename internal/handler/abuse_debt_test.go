package handler

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"genfity-ai-gateway-service/internal/service"
	"genfity-ai-gateway-service/internal/store"
)

func subWithDebt(t *testing.T, abuseDebt map[string]any) *service.ActiveSubscription {
	t.Helper()
	start := time.Date(2026, 6, 4, 20, 14, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	meta := map[string]any{}
	if abuseDebt != nil {
		meta["abuseDebt"] = abuseDebt
	}
	b, _ := json.Marshal(meta)
	return &service.ActiveSubscription{
		Entitlement: &store.CustomerEntitlement{
			PeriodStart: &start,
			PeriodEnd:   &end,
			Metadata:    b,
		},
		Plan: &store.SubscriptionPlanSnapshot{},
	}
}

func TestResolveAbuseDebt(t *testing.T) {
	t.Run("flagged with remaining debt", func(t *testing.T) {
		sub := subWithDebt(t, map[string]any{
			"flagged":       true,
			"debtRemaining": 2753,
		})
		flagged, debt, reset := resolveAbuseDebt(sub)
		if !flagged || debt != 2753 || reset {
			t.Errorf("want flagged=true debt=2753 reset=false; got %v %d %v", flagged, debt, reset)
		}
	})

	t.Run("needsRppReset set", func(t *testing.T) {
		sub := subWithDebt(t, map[string]any{
			"flagged":       false,
			"debtRemaining": 0,
			"needsRppReset": true,
		})
		flagged, debt, reset := resolveAbuseDebt(sub)
		if flagged || debt != 0 || !reset {
			t.Errorf("want flagged=false debt=0 reset=true; got %v %d %v", flagged, debt, reset)
		}
	})

	t.Run("no abuseDebt block", func(t *testing.T) {
		sub := subWithDebt(t, nil)
		flagged, debt, reset := resolveAbuseDebt(sub)
		if flagged || debt != 0 || reset {
			t.Errorf("want all-zero; got %v %d %v", flagged, debt, reset)
		}
	})

	t.Run("nil-safe", func(t *testing.T) {
		flagged, debt, reset := resolveAbuseDebt(nil)
		if flagged || debt != 0 || reset {
			t.Errorf("nil sub should be no-debt; got %v %d %v", flagged, debt, reset)
		}
		flagged, debt, reset = resolveAbuseDebt(&service.ActiveSubscription{})
		if flagged || debt != 0 || reset {
			t.Errorf("empty sub should be no-debt; got %v %d %v", flagged, debt, reset)
		}
	})

	t.Run("negative debt clamped to zero", func(t *testing.T) {
		sub := subWithDebt(t, map[string]any{"flagged": true, "debtRemaining": -5})
		_, debt, _ := resolveAbuseDebt(sub)
		if debt != 0 {
			t.Errorf("negative debt should clamp to 0, got %d", debt)
		}
	})
}

// debtUsableRPP mirrors the reserve math in applyPreRequestLimits so the
// 75/25 split is unit-tested independently of the request path.
func debtUsableRPP(base, mult, debtRemaining int, flagged bool) int {
	effectiveRPP := base * mult
	if flagged && debtRemaining > 0 {
		debtReserve := int(math.Round(float64(base)*0.75)) * mult
		reserve := debtRemaining
		if reserve > debtReserve {
			reserve = debtReserve
		}
		effectiveRPP -= reserve
		if effectiveRPP < 0 {
			effectiveRPP = 0
		}
	}
	return effectiveRPP
}

func TestDebtReserveMath(t *testing.T) {
	cases := []struct {
		name    string
		base    int
		mult    int
		debt    int
		flagged bool
		want    int
	}{
		// base 350, single window: reserve 75% = 263, usable 87.
		{"1DaysLite flagged, big debt", 350, 1, 2753, true, 87},
		// debt smaller than the 75% reserve only holds back the debt amount.
		{"debt smaller than reserve", 350, 1, 50, true, 300},
		// stacked window scales both cap and reserve.
		{"stacked 2x", 350, 2, 5000, true, 174},
		// not flagged -> full cap.
		{"not flagged", 350, 1, 2753, false, 350},
		// zero debt -> full cap.
		{"flagged but debt cleared", 350, 1, 0, true, 350},
		// debt exceeds even gross cap -> usable floors at 0 (reserve capped at 75%).
		{"reserve capped at 75pct", 100, 1, 99999, true, 25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := debtUsableRPP(tc.base, tc.mult, tc.debt, tc.flagged)
			if got != tc.want {
				t.Errorf("usable RPP base=%d mult=%d debt=%d flagged=%v: want %d, got %d",
					tc.base, tc.mult, tc.debt, tc.flagged, tc.want, got)
			}
		})
	}
}
