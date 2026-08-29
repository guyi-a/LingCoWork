package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/compose"

	agenttools "github.com/guyi-a/Interview-Agent/internal/agent/tools"
	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/effect"
)

func TestSelectExploreToolsUsesExactAllowlist(t *testing.T) {
	all, _, err := agenttools.Builtin(context.Background(), agenttools.Deps{})
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	selected, err := selectExploreTools(context.Background(), all)
	if err != nil {
		t.Fatalf("selectExploreTools: %v", err)
	}
	got := make(map[string]bool)
	for _, candidate := range selected {
		info, err := candidate.Info(context.Background())
		if err != nil {
			t.Fatalf("tool info: %v", err)
		}
		got[info.Name] = true
	}
	if len(got) != len(exploreToolNames) {
		t.Fatalf("selected tools = %#v", got)
	}
	for name := range exploreToolNames {
		if !got[name] {
			t.Errorf("missing Explore tool %s", name)
		}
	}
	for _, forbidden := range []string{
		"apply_patch", "write_file", "rm", "browser_use", "web_search", "remember",
	} {
		if got[forbidden] {
			t.Errorf("Explore includes forbidden tool %s", forbidden)
		}
	}
}

func TestExploreGuardAllowsWorkspaceReadsAndSafeProbes(t *testing.T) {
	registry := effect.NewRegistry()
	registry.Register("read_file", effect.Static(effect.Effect{
		Kind: effect.KindFileRead, Scope: effect.ScopeWorkspace,
	}))
	registry.Register("run_command", func(
		_ context.Context,
		argsJSON string,
	) (effect.Effect, error) {
		var args struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return effect.Effect{}, err
		}
		return effect.Effect{
			Kind: effect.KindProcessExec, Scope: effect.ScopeWorkspace,
			Command:        args.Command,
			Classification: approval.ClassifyShellCommand(args.Command),
		}, nil
	})
	guard := exploreGuard(registry)
	called := 0
	endpoint := guard.Invokable(func(
		_ context.Context,
		_ *compose.ToolInput,
	) (*compose.ToolOutput, error) {
		called++
		return &compose.ToolOutput{Result: "ok"}, nil
	})
	for _, input := range []*compose.ToolInput{
		{Name: "read_file", Arguments: `{"path":"internal/agent/adk_agent.go"}`},
		{Name: "run_command", Arguments: `{"command":"git status --short"}`},
	} {
		if _, err := endpoint(context.Background(), input); err != nil {
			t.Fatalf("%s rejected: %v", input.Name, err)
		}
	}
	if called != 2 {
		t.Fatalf("next called %d times", called)
	}
}

func TestExploreGuardRejectsExternalReadWriteAndNormalCommand(t *testing.T) {
	registry := effect.NewRegistry()
	registry.Register("read_file", effect.Static(effect.Effect{
		Kind: effect.KindFileRead, Scope: effect.ScopeExternal,
	}))
	registry.Register("write_file", effect.Static(effect.Effect{
		Kind: effect.KindFileWrite, Scope: effect.ScopeWorkspace,
	}))
	registry.Register("run_command", effect.Static(effect.Effect{
		Kind: effect.KindProcessExec, Scope: effect.ScopeWorkspace,
		Classification: effect.Normal,
	}))
	endpoint := exploreGuard(registry).Invokable(func(
		_ context.Context,
		_ *compose.ToolInput,
	) (*compose.ToolOutput, error) {
		t.Fatal("forbidden tool reached endpoint")
		return nil, nil
	})
	for _, input := range []*compose.ToolInput{
		{Name: "read_file", Arguments: `{"path":"/etc/hosts"}`},
		{Name: "write_file", Arguments: `{"path":"out.txt","content":"x"}`},
		{Name: "run_command", Arguments: `{"command":"go test ./..."}`},
	} {
		if _, err := endpoint(context.Background(), input); err == nil {
			t.Errorf("%s unexpectedly allowed", input.Name)
		}
	}
}

func TestExploreDelegateEffectIsRegistered(t *testing.T) {
	registry := effect.NewRegistry()
	registerDelegateEffects(registry)
	got := registry.Derive(context.Background(), ExploreAgentName, `{}`)
	if got.Kind != effect.KindDelegate || got.Agent != ExploreAgentName {
		t.Fatalf("Explore effect = %#v", got)
	}
}

func TestExploreHasEnoughIterationsToReturnAfterBroadSearch(t *testing.T) {
	if exploreMaxIterations < 50 {
		t.Fatalf("Explore max iterations = %d, want at least 50", exploreMaxIterations)
	}
}
