package service

import (
	"encoding/json"
	"testing"

	"genfity-ai-gateway-service/internal/store"
)

func TestReplayPayloadFromUsageAcceptsBillableDisconnect(t *testing.T) {
	mode := "credit_package"
	credits := "2.0000"
	entry := store.UsageLedgerEntry{
		RequestID:     "req-disconnected",
		GenfityUserID: "user-1",
		PublicModel:   "genfity/test",
		BillingMode:   &mode,
		AmountCredits: &credits,
		Status:        "failed",
		Metadata:      json.RawMessage(`{"billable":true,"billing_reason":"client_disconnected_with_usage"}`),
	}

	payload, ok := replayPayloadFromUsage(entry)
	if !ok {
		t.Fatal("billable disconnect was excluded from debit replay")
	}
	if payload.AmountCredits != 2 || payload.RequestID != entry.RequestID {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestReplayPayloadFromUsageRejectsFreeDisconnect(t *testing.T) {
	mode := "credit_package"
	credits := "2.0000"
	entry := store.UsageLedgerEntry{
		RequestID:     "req-free-disconnect",
		GenfityUserID: "user-1",
		BillingMode:   &mode,
		AmountCredits: &credits,
		Status:        "failed",
		Metadata:      json.RawMessage(`{"billable":false}`),
	}

	if _, ok := replayPayloadFromUsage(entry); ok {
		t.Fatal("non-billable disconnect was included in debit replay")
	}
}
