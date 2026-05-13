package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"genfity-ai-gateway-service/internal/store"
)

// PRD v3 Phase 2 tests — verify the reserve/finalize invariants on the
// memory store. These run without a database so they can catch logic
// bugs (sign flips, double-spending, reservation release) before
// integration tests get a chance.
//
// The memory store is what tests and dev machines use, so keeping these
// behaviours correct keeps the dev feedback loop short.

// mkEntitlement builds a minimal entitlement with credit/PAYG balances
// seeded. Strings are formatted to the storage precision so comparing
// later won't trip over float formatting drift.
func mkEntitlement(userID string, creditBalance string, paygBalance string) store.CustomerEntitlement {
	metadata, _ := json.Marshal(map[string]any{"pricingGroup": "credit_package"})
	return store.CustomerEntitlement{
		GenfityUserID:   userID,
		PlanCode:        "test_plan",
		Status:          "active",
		CreditBalance:   &creditBalance,
		PaygUsdBalance:  &paygBalance,
		Metadata:        metadata,
	}
}

func TestMemoryStore_ReserveRequestCredits_HappyPath(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	ent := mkEntitlement("user1", "100.0000", "0.000000")
	if _, err := s.UpsertEntitlement(ctx, ent); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := s.ReserveRequestCredits(ctx, "user1", 10.5); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	got, _ := s.GetEntitlementByUser(ctx, "user1")
	if got == nil || got.CreditBalanceReserved == nil {
		t.Fatal("reservation should have been recorded")
	}
	if *got.CreditBalanceReserved != "10.5000" {
		t.Fatalf("reserved = %q, want %q", *got.CreditBalanceReserved, "10.5000")
	}
}

func TestMemoryStore_ReserveRequestCredits_InsufficientBalance(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	ent := mkEntitlement("user1", "5.0000", "0.000000")
	_, _ = s.UpsertEntitlement(ctx, ent)

	err := s.ReserveRequestCredits(ctx, "user1", 10)
	if err != ErrInsufficientBalance {
		t.Fatalf("expected ErrInsufficientBalance, got %v", err)
	}
}

func TestMemoryStore_ReserveRequestCredits_ConsidersReservedHoldings(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	ent := mkEntitlement("user1", "10.0000", "0.000000")
	_, _ = s.UpsertEntitlement(ctx, ent)

	// Two in-flight reservations totaling 9 should succeed; a third
	// reservation for 2 more must fail because 10 - 9 - 2 < 0.
	if err := s.ReserveRequestCredits(ctx, "user1", 5); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if err := s.ReserveRequestCredits(ctx, "user1", 4); err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	if err := s.ReserveRequestCredits(ctx, "user1", 2); err != ErrInsufficientBalance {
		t.Fatalf("third reserve should fail, got %v", err)
	}
}

func TestMemoryStore_FinalizeRequestCredits_SettlesReservationAndDebit(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	ent := mkEntitlement("user1", "100.0000", "0.000000")
	_, _ = s.UpsertEntitlement(ctx, ent)

	_ = s.ReserveRequestCredits(ctx, "user1", 10)
	// Actual debit less than reserved — the delta should release back
	// to the available balance.
	_ = s.FinalizeRequestCredits(ctx, "user1", 10, 7)

	got, _ := s.GetEntitlementByUser(ctx, "user1")
	if got == nil {
		t.Fatal("entitlement missing")
	}
	if *got.CreditBalance != "93.0000" {
		t.Fatalf("balance = %q, want %q (100 - 7)", *got.CreditBalance, "93.0000")
	}
	if *got.CreditBalanceReserved != "0.0000" {
		t.Fatalf("reserved = %q, want 0 (fully released)", *got.CreditBalanceReserved)
	}
}

func TestMemoryStore_FinalizeRequestCredits_FailedRequestReleasesFull(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	ent := mkEntitlement("user1", "50.0000", "0.000000")
	_, _ = s.UpsertEntitlement(ctx, ent)

	_ = s.ReserveRequestCredits(ctx, "user1", 5)
	// Request failed — finalize with actualAmount=0 must release the
	// reservation without debiting the balance.
	_ = s.FinalizeRequestCredits(ctx, "user1", 5, 0)

	got, _ := s.GetEntitlementByUser(ctx, "user1")
	if *got.CreditBalance != "50.0000" {
		t.Fatalf("balance = %q, want 50 (no debit)", *got.CreditBalance)
	}
	if *got.CreditBalanceReserved != "0.0000" {
		t.Fatalf("reserved = %q, want 0", *got.CreditBalanceReserved)
	}
}

func TestMemoryStore_PaygUsd_RoundTrip(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	ent := mkEntitlement("user1", "0.0000", "25.000000")
	_, _ = s.UpsertEntitlement(ctx, ent)

	if err := s.ReservePaygUsdBalance(ctx, "user1", 1.5); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	_ = s.FinalizePaygUsdBalance(ctx, "user1", 1.5, 1.2)

	got, _ := s.GetEntitlementByUser(ctx, "user1")
	if got == nil {
		t.Fatal("entitlement missing")
	}
	// 25 - 1.2 = 23.8, formatted to 6 decimals
	if *got.PaygUsdBalance != "23.800000" {
		t.Fatalf("payg balance = %q, want 23.800000", *got.PaygUsdBalance)
	}
	if *got.PaygUsdBalanceReserved != "0.000000" {
		t.Fatalf("payg reserved = %q, want 0", *got.PaygUsdBalanceReserved)
	}
}

func TestMemoryStore_ReservePaygUsdBalance_InsufficientBalance(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	ent := mkEntitlement("user1", "0.0000", "1.000000")
	_, _ = s.UpsertEntitlement(ctx, ent)

	if err := s.ReservePaygUsdBalance(ctx, "user1", 5); err != ErrInsufficientBalance {
		t.Fatalf("expected ErrInsufficientBalance, got %v", err)
	}
}

func TestMemoryStore_ReserveRequestCredits_NonNegativeInvariant(t *testing.T) {
	// Zero amount is a silent no-op (treated as "nothing to reserve").
	s := NewMemoryStore()
	ctx := context.Background()
	_, _ = s.UpsertEntitlement(ctx, mkEntitlement("user1", "10.0000", "0.000000"))

	if err := s.ReserveRequestCredits(ctx, "user1", 0); err != nil {
		t.Fatalf("zero reserve should be no-op, got %v", err)
	}
	if err := s.ReserveRequestCredits(ctx, "user1", -5); err != nil {
		t.Fatalf("negative reserve should be no-op, got %v", err)
	}
	got, _ := s.GetEntitlementByUser(ctx, "user1")
	// No reservation was recorded at all.
	if got.CreditBalanceReserved != nil && *got.CreditBalanceReserved != "" && *got.CreditBalanceReserved != "0.0000" {
		t.Fatalf("reserved should be empty, got %v", got.CreditBalanceReserved)
	}
}

func TestMemoryStore_ModelCreditCost_CRUD(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	cost := store.ModelCreditCost{
		FullModelID:   "mtr/test",
		CreditsPerReq: "0.5000",
		IsFree:        false,
		SyncedAt:      time.Now(),
	}
	if _, err := s.UpsertModelCreditCost(ctx, cost); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.GetModelCreditCost(ctx, "mtr/test")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil || got.FullModelID != "mtr/test" {
		t.Fatalf("expected mtr/test, got %+v", got)
	}
	list := s.ListModelCreditCosts(ctx)
	if len(list) != 1 {
		t.Fatalf("expected 1 cost in list, got %d", len(list))
	}
	// Upsert again with updated value — should overwrite not duplicate.
	cost.CreditsPerReq = "0.7500"
	if _, err := s.UpsertModelCreditCost(ctx, cost); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	list = s.ListModelCreditCosts(ctx)
	if len(list) != 1 {
		t.Fatalf("expected 1 cost after re-upsert, got %d", len(list))
	}
	if list[0].CreditsPerReq != "0.7500" {
		t.Fatalf("expected 0.7500, got %q", list[0].CreditsPerReq)
	}
}
