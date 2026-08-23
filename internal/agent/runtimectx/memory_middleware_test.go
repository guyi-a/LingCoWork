package runtimectx

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"

	"github.com/guyi-a/Interview-Agent/internal/memory"
)

// 这个中间件是"手写记忆能被模型看到"的唯一通路。没有 conversation repo 的场景
// （nil repo）走的是临时对话分支，正好是覆盖用户级注入的最小装置。
func TestMemoryMiddlewareInjectsUserMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.md")
	registry := memory.NewRegistry()
	if _, err := registry.For(path).Write("- [2026-08-23] 回答用中文\n", time.Now()); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	mw := NewMemoryMiddleware(registry, path, nil, nil)
	runCtx := &adk.ChatModelAgentContext{Instruction: "BASE"}
	_, out, err := mw.BeforeAgent(context.Background(), runCtx)
	if err != nil {
		t.Fatalf("BeforeAgent: %v", err)
	}

	if !strings.HasPrefix(out.Instruction, "BASE") {
		t.Errorf("the base instruction was not preserved:\n%s", out.Instruction)
	}
	if !strings.Contains(out.Instruction, "回答用中文") {
		t.Errorf("user memory was not injected:\n%s", out.Instruction)
	}
	// 没有工作区时不该假装有项目记忆。
	if strings.Contains(out.Instruction, "### 项目约定") {
		t.Errorf("project section rendered without a workspace:\n%s", out.Instruction)
	}
}

// 记忆为空时不能往提示词里塞占位段落 —— 那是每轮都付的 token，换不来任何信息。
func TestMemoryMiddlewareLeavesInstructionAloneWhenEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.md")
	mw := NewMemoryMiddleware(memory.NewRegistry(), path, nil, nil)
	runCtx := &adk.ChatModelAgentContext{Instruction: "BASE"}
	_, out, err := mw.BeforeAgent(context.Background(), runCtx)
	if err != nil {
		t.Fatalf("BeforeAgent: %v", err)
	}
	if out.Instruction != "BASE" {
		t.Errorf("instruction = %q, want it untouched", out.Instruction)
	}
}

// 同一份记忆连续两轮必须注入完全相同的字节，否则提示词前缀缓存每轮都会 miss。
func TestMemoryMiddlewareIsByteStableAcrossRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.md")
	registry := memory.NewRegistry()
	if _, err := registry.For(path).Write("- [2026-08-23] 甲\n- [2026-08-22] 乙\n", time.Now()); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	mw := NewMemoryMiddleware(registry, path, nil, nil)

	render := func() string {
		_, out, err := mw.BeforeAgent(context.Background(), &adk.ChatModelAgentContext{Instruction: "BASE"})
		if err != nil {
			t.Fatalf("BeforeAgent: %v", err)
		}
		return out.Instruction
	}
	first := render()
	for i := 0; i < 3; i++ {
		if got := render(); got != first {
			t.Fatalf("run %d produced different bytes than the first", i)
		}
	}
}
