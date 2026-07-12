package service

import (
	"context"
	"testing"
	"time"
)

// Regression for the token-usage leak: extending a subscription re-keys the
// quota_counters row on (period_start, period_end), so each extension used to
// strand prior usage in an old-end row and reset the live counter to 0. The
// old single-row migrate also broke once 3+ ends shared a period_start. The
// fix folds ALL same-period_start rows into the current end, summing usage.
func TestMigrateQuotaCounterConsolidatesMultipleEnds(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	user := "user-leak"
	start := time.Date(2026, 7, 9, 13, 0, 0, 0, time.UTC)

	end1 := start.AddDate(0, 0, 1)
	end2 := start.AddDate(0, 0, 2)
	end3 := start.AddDate(0, 0, 3) // current window after two extensions

	_ = s.IncrementQuotaCounter(ctx, user, nil, start, end1, 100)
	_ = s.IncrementQuotaCounter(ctx, user, nil, start, end2, 250)
	_ = s.IncrementQuotaCounter(ctx, user, nil, start, end3, 30)

	moved, err := s.MigrateQuotaCounterPeriodEnd(ctx, user, start, end3)
	if err != nil {
		t.Fatalf("migrate returned error: %v", err)
	}
	if !moved {
		t.Fatalf("expected rows to be consolidated")
	}

	qc, err := s.GetQuotaCounter(ctx, user, start, end3)
	if err != nil {
		t.Fatalf("get counter: %v", err)
	}
	if qc == nil {
		t.Fatalf("expected a consolidated counter row")
	}
	if qc.TokensUsed != 380 {
		t.Fatalf("expected summed tokens_used=380, got %d", qc.TokensUsed)
	}
	if qc.RequestCount != 3 {
		t.Fatalf("expected summed request_count=3, got %d", qc.RequestCount)
	}

	// Stale-end rows must be gone so they can't resurface as visible usage.
	if c, _ := s.GetQuotaCounter(ctx, user, start, end1); c != nil {
		t.Fatalf("expected end1 row folded away, still present: %+v", c)
	}
	if c, _ := s.GetQuotaCounter(ctx, user, start, end2); c != nil {
		t.Fatalf("expected end2 row folded away, still present: %+v", c)
	}

	// Idempotent: a second call is a no-op and preserves the total.
	if _, err := s.MigrateQuotaCounterPeriodEnd(ctx, user, start, end3); err != nil {
		t.Fatalf("second migrate errored: %v", err)
	}
	qc2, _ := s.GetQuotaCounter(ctx, user, start, end3)
	if qc2 == nil || qc2.TokensUsed != 380 {
		t.Fatalf("expected total preserved at 380 after idempotent re-run, got %+v", qc2)
	}
}
