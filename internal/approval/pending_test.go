package approval

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/hitl"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

func TestPendingStoreResolveWaitsForWholeCheckpoint(t *testing.T) {
	store := NewPendingStore(nil)
	record := store.Record("conv")
	for _, item := range []struct {
		interruptID string
		callID      string
	}{
		{"interrupt-1", "call-1"},
		{"interrupt-2", "call-2"},
		{"interrupt-3", "call-3"},
	} {
		record.Record("checkpoint", item.interruptID, &stream.ApprovalInfo{
			CallID: item.callID,
			Tool:   "run_command",
		})
	}

	_, targets, found, ready := store.Resolve("conv", "interrupt-1", Decision{Approved: true})
	if !found || ready || targets != nil {
		t.Fatalf("first resolution = found %v ready %v targets %#v", found, ready, targets)
	}
	pending := store.List("conv")
	if len(pending) != 2 || pending[0].InterruptID != "interrupt-2" {
		t.Fatalf("pending after first resolution = %#v", pending)
	}

	store.Resolve("conv", "interrupt-2", Decision{Approved: false, Reason: "no"})
	checkpointID, targets, found, ready := store.Resolve(
		"conv",
		"interrupt-3",
		Decision{Approved: true},
	)
	if !found || !ready {
		t.Fatalf("last resolution = found %v ready %v", found, ready)
	}
	if checkpointID != "checkpoint" || len(targets) != 3 {
		t.Fatalf("batch = checkpoint %q targets %#v", checkpointID, targets)
	}
	if got := targets["interrupt-2"].(Decision); got.Approved || got.Reason != "no" {
		t.Fatalf("denied target = %#v", got)
	}
	if store.HasPending("conv") || len(store.List("conv")) != 0 {
		t.Fatal("completed checkpoint should have no pending items")
	}
}

func TestPendingStoreCarriesPlanInterruptThroughBatch(t *testing.T) {
	store := NewPendingStore(nil)
	store.Record("conv").Record("checkpoint", "plan-interrupt", &stream.PlanInfo{
		PlanID:   "plan-1",
		PlanJSON: `{"id":"plan-1"}`,
		CallID:   "call-plan",
	})
	pending := store.List("conv")
	if len(pending) != 1 ||
		pending[0].Kind != hitl.KindPlan ||
		pending[0].Args != `{"id":"plan-1"}` {
		t.Fatalf("pending plan = %#v", pending)
	}
	_, targets, found, ready := store.Resolve(
		"conv",
		"plan-interrupt",
		hitl.PlanDecision{PlanJSON: pending[0].Args},
	)
	if !found || !ready {
		t.Fatalf("resolve = found %v ready %v", found, ready)
	}
	decision, ok := targets["plan-interrupt"].(hitl.PlanDecision)
	if !ok || decision.PlanJSON != `{"id":"plan-1"}` {
		t.Fatalf("plan target = %#v", targets["plan-interrupt"])
	}
}

func TestPendingStoreResolveConcurrentCompletesBatchOnce(t *testing.T) {
	store := NewPendingStore(nil)
	record := store.Record("conv")
	for _, id := range []string{"a", "b", "c"} {
		record.Record("checkpoint", id, &stream.ApprovalInfo{CallID: id})
	}

	var readyCount atomic.Int32
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func(interruptID string) {
			defer wg.Done()
			_, targets, found, ready := store.Resolve(
				"conv",
				interruptID,
				Decision{Approved: true},
			)
			if !found {
				t.Errorf("resolution %q was not found", interruptID)
			}
			if ready {
				if len(targets) != 3 {
					t.Errorf("ready targets = %#v", targets)
				}
				readyCount.Add(1)
			}
		}(id)
	}
	wg.Wait()

	if got := readyCount.Load(); got != 1 {
		t.Fatalf("ready count = %d, want 1", got)
	}
}
