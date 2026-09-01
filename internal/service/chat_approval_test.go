package service

import (
	"path/filepath"
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/effect"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

func TestRememberedApprovalDoesNotImplicitlyResolveSibling(t *testing.T) {
	db, err := repository.NewDB(filepath.Join(t.TempDir(), "approval.db"))
	if err != nil {
		t.Fatal(err)
	}
	convRepo := repository.NewConversationRepo(db)
	if err := convRepo.Upsert(t.Context(), "conv"); err != nil {
		t.Fatal(err)
	}
	pending := approval.NewPendingStore(nil)
	record := pending.Record("conv")
	firstEffect := effect.Effect{
		Kind: effect.KindFileRead, Scope: effect.ScopeExternal, Path: "/tmp/a",
	}
	record.Record("conv", "first", &stream.ApprovalInfo{
		Tool: "read_file", Args: `{"path":"/tmp/a"}`,
		CallID: "tc-1", EffectJSON: firstEffect.JSON(),
	})
	record.Record("conv", "second", &stream.ApprovalInfo{
		Tool: "read_file", Args: `{"path":"/tmp/a"}`,
		CallID: "tc-2", EffectJSON: firstEffect.JSON(),
	})

	memory := approval.NewMemory()
	svc := &ChatService{
		convRepo: convRepo, pending: pending, approvalMemory: memory,
	}
	found, resumed, err := svc.Resume(
		"conv", "first", approval.Decision{Approved: true}, true,
	)
	if err != nil || !found || resumed {
		t.Fatalf("found=%v resumed=%v err=%v", found, resumed, err)
	}
	if memory.Count("conv") != 1 {
		t.Fatalf("remembered=%d", memory.Count("conv"))
	}
	if got := pending.List("conv"); len(got) != 1 || got[0].InterruptID != "second" {
		t.Fatalf("remaining=%+v", got)
	}
}
