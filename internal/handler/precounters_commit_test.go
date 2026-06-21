package handler

import (
	"context"
	"testing"
)

// TestPreRequestCountersCommit_SkipsOnClientDisconnect proves the billing
// commit guard: when the client has already disconnected (ctx canceled), the
// request must NOT consume the user's rate-limit slot — the deferred rollback
// then releases it. This is what prevents an RTO'd request that the gateway
// later books as 200 from burning the customer's RPD/RPP slot.
func TestPreRequestCountersCommit_SkipsOnClientDisconnect(t *testing.T) {
	t.Run("live context commits", func(t *testing.T) {
		p := &preRequestCounters{}
		p.commit(context.Background())
		if !p.committed {
			t.Fatal("expected commit with a live context to mark committed")
		}
	})

	t.Run("canceled context does not commit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		p := &preRequestCounters{}
		p.commit(ctx)
		if p.committed {
			t.Fatal("expected commit with a canceled context to be skipped (slot rolled back)")
		}
	})

	t.Run("nil context commits", func(t *testing.T) {
		p := &preRequestCounters{}
		p.commit(nil)
		if !p.committed {
			t.Fatal("expected commit with nil context to mark committed")
		}
	})
}
