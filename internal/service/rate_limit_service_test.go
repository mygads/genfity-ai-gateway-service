package service

import (
	"testing"

	"genfity-ai-gateway-service/internal/store"
)

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
