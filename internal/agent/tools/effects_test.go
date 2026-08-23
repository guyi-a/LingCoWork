package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/effect"
)

func TestScopeOfResolvedPath(t *testing.T) {
	ws := "/Users/me/.workspace/proj"
	tests := []struct {
		name string
		path string
		want effect.Scope
	}{
		{"file directly inside", filepath.Join(ws, "notes.md"), effect.ScopeWorkspace},
		{"nested file", filepath.Join(ws, "a/b/c.txt"), effect.ScopeWorkspace},
		{"the root itself", ws, effect.ScopeWorkspace},
		{"trailing separator on the root", ws + "/", effect.ScopeWorkspace},
		{"sibling directory", "/Users/me/.workspace/other/x", effect.ScopeExternal},
		{"parent", "/Users/me", effect.ScopeExternal},
		{"elsewhere entirely", "/etc/hosts", effect.ScopeExternal},
		// A prefix match on the string would call this workspace. It isn't:
		// "proj-backup" is a different directory.
		{"name that merely shares a prefix", "/Users/me/.workspace/proj-backup/x", effect.ScopeExternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scopeOfResolvedPath(ws, tt.path); got != tt.want {
				t.Fatalf("scopeOfResolvedPath(%q, %q) = %q, want %q", ws, tt.path, got, tt.want)
			}
		})
	}

	// No workspace bound means no path can be established as inside one.
	if got := scopeOfResolvedPath("", "/anything"); got != effect.ScopeExternal {
		t.Errorf("with no workspace root, got %q, want external", got)
	}
}

// The memory gate is a path rule, not a tool-name rule, because the model
// reaches project memory through the ordinary file tools. If this regresses,
// nothing breaks loudly: memory writes just start looking like plain workspace
// writes, which auto mode waves through without showing a card.
func TestWriteKindRecognisesProjectMemory(t *testing.T) {
	ws := "/Users/me/.workspace/proj"
	memoryFile := filepath.Join(ws, "memory.md")

	if got := writeKind(ws, memoryFile); got != effect.KindMemoryWrite {
		t.Errorf("writeKind(%q) = %q, want %q", memoryFile, got, effect.KindMemoryWrite)
	}

	ordinary := []string{
		filepath.Join(ws, "notes.md"),
		// Only the workspace root counts. A memory.md the agent happens to
		// write in a subdirectory is just a file.
		filepath.Join(ws, "reports", "memory.md"),
		filepath.Join(ws, "memory.md.bak"),
	}
	for _, p := range ordinary {
		if got := writeKind(ws, p); got != effect.KindFileWrite {
			t.Errorf("writeKind(%q) = %q, want %q", p, got, effect.KindFileWrite)
		}
	}

	// No workspace means no project memory to protect, and a path check
	// against "" must not match everything.
	if got := writeKind("", "/anywhere/memory.md"); got != effect.KindFileWrite {
		t.Errorf("with no workspace, writeKind = %q, want %q", got, effect.KindFileWrite)
	}
}

// A memory write must not be auto-approved and must not be skippable. These
// two functions have no case for the kind, so both answers come from their
// default branches — this test is what says those defaults are load-bearing
// rather than incidental.
func TestMemoryWriteIsGatedByPolicy(t *testing.T) {
	e := effect.Effect{
		Kind:  effect.KindMemoryWrite,
		Scope: effect.ScopeWorkspace,
		Path:  "/Users/me/.workspace/proj/memory.md",
	}
	if !approval.NeedsApproval(e) {
		t.Error("a memory write does not require approval")
	}
	if safe, reason := approval.IsSafeAuto(e, `{"content":"- 甲\n"}`); safe {
		t.Errorf("auto mode fast-path waved through a memory write: %s", reason)
	}
}

// TestEveryBuiltinToolHasADeriver is the drift guard, and it checks the tools
// Builtin actually constructs rather than a hand-maintained list — a list
// would drift in exactly the same way the registry can.
//
// Without a deriver a tool derives to KindUnknown and the policy asks the user
// to approve every single call to it. That degrades the product quietly
// instead of failing loudly, which is why it needs a test.
func TestEveryBuiltinToolHasADeriver(t *testing.T) {
	ctx := context.Background()
	// Deps{} leaves out the optional dependencies, so the tools gated behind
	// them (browser_use, rag_search, web_search, load_skill) aren't built
	// here. Derivers are registered unconditionally precisely so those can't
	// be the ones that go missing; the second loop covers them by name.
	built, reg, err := Builtin(ctx, Deps{})
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	if len(built) < 10 {
		t.Fatalf("Builtin returned only %d tools — the check below would pass vacuously", len(built))
	}
	for _, tl := range built {
		info, err := tl.Info(ctx)
		if err != nil {
			t.Fatalf("tool info: %v", err)
		}
		if !reg.Has(info.Name) {
			t.Errorf("tool %q is registered with the agent but has no effect deriver", info.Name)
		}
	}

	// The declared list has to stay in step too: derivers are registered
	// unconditionally, so it's the only handle callers have on the full set.
	for _, name := range BuiltinToolNames() {
		if !reg.Has(name) {
			t.Errorf("BuiltinToolNames lists %q but nothing registered a deriver for it", name)
		}
	}
}

func TestDeriveShellEffect(t *testing.T) {
	reg := effect.NewRegistry()
	registerEffects(reg, &fsDeps{})
	ctx := context.Background()

	tests := []struct {
		command        string
		wantClass      effect.Classification
		wantDestuctive bool
	}{
		{"ls -la", effect.Harmless, false},
		{"go build ./...", effect.Normal, false},
		{"rm -rf build", effect.Destructive, true},
		{"ls | xargs rm -rf", effect.Destructive, true},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			e := reg.Derive(ctx, "run_command", `{"command":`+quote(tt.command)+`}`)
			if e.Kind != effect.KindProcessExec {
				t.Fatalf("kind = %q, want process-exec", e.Kind)
			}
			if e.Classification != tt.wantClass {
				t.Errorf("classification = %q, want %q", e.Classification, tt.wantClass)
			}
			if e.Destructive != tt.wantDestuctive {
				t.Errorf("destructive = %v, want %v", e.Destructive, tt.wantDestuctive)
			}
			if e.Command != tt.command {
				t.Errorf("command = %q, want %q", e.Command, tt.command)
			}
		})
	}
}

func TestDeriveRmDestructiveGranularity(t *testing.T) {
	reg := effect.NewRegistry()
	registerEffects(reg, &fsDeps{})
	ctx := context.Background()

	tests := []struct {
		name string
		args string
		want bool
	}{
		{"plain file", `{"path":"/tmp/x/notes.md"}`, false},
		{"recursive", `{"path":"/tmp/x/build","recursive":true}`, true},
		{"home-relative", `{"path":"~/Documents"}`, true},
		{"variable-expanded home", `{"path":"$HOME/Documents"}`, true},
		{"system path", `{"path":"/usr/local/bin/tool"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := reg.Derive(ctx, "rm", tt.args)
			if e.Destructive != tt.want {
				t.Fatalf("destructive = %v, want %v (effect: %s)", e.Destructive, tt.want, e.JSON())
			}
		})
	}
}

// TestDeriveTransferKeepsBothEnds is the reason a transfer carries two scopes
// rather than one worst case: `cp ./secret.md /tmp` and `cp /tmp/data.csv ./`
// both come out "external" overall and need to be told apart.
func TestDeriveTransferKeepsBothEnds(t *testing.T) {
	reg := effect.NewRegistry()
	registerEffects(reg, &fsDeps{})
	ctx := context.Background()

	// With no workspace bound, absolute paths resolve and land external while
	// relative ones can't be placed. That's enough to prove both ends are
	// tracked independently and that Scope is the worse of the two.
	e := reg.Derive(ctx, "cp", `{"src":"/tmp/data.csv","dst":"/etc/data.csv"}`)
	if e.Kind != effect.KindFileTransfer {
		t.Fatalf("kind = %q, want filesystem-transfer", e.Kind)
	}
	if e.Path != "/tmp/data.csv" || e.DestPath != "/etc/data.csv" {
		t.Errorf("paths = %q → %q, want both preserved", e.Path, e.DestPath)
	}
	if e.PathScope != effect.ScopeExternal || e.DestScope != effect.ScopeExternal {
		t.Errorf("scopes = %q / %q, want both external", e.PathScope, e.DestScope)
	}
	if e.Scope != effect.ScopeExternal {
		t.Errorf("worst scope = %q, want external", e.Scope)
	}
}

func TestDeriveUnregisteredToolIsUnknown(t *testing.T) {
	reg := effect.NewRegistry()
	registerEffects(reg, &fsDeps{})
	e := reg.Derive(context.Background(), "mcp_deploy_to_prod", `{"env":"production"}`)
	if e.Kind != effect.KindUnknown {
		t.Fatalf("kind = %q, want unknown — an unregistered tool must fail closed", e.Kind)
	}
	if e.Note == "" {
		t.Error("unknown effect should carry a diagnostic note")
	}
}

func TestDeriveChunkedWriteOnlyGatesStart(t *testing.T) {
	reg := effect.NewRegistry()
	registerEffects(reg, &fsDeps{})
	ctx := context.Background()

	start := reg.Derive(ctx, "write_file_chunked", `{"mode":"start","path":"out.md"}`)
	if start.Kind != effect.KindFileWrite {
		t.Errorf("mode=start kind = %q, want filesystem-write", start.Kind)
	}
	// Every subsequent chunk would otherwise re-prompt for a write the user
	// already agreed to.
	for _, mode := range []string{"append", "finish", "abort"} {
		e := reg.Derive(ctx, "write_file_chunked", `{"mode":"`+mode+`","session_id":"s1"}`)
		if e.Kind != effect.KindFileStructure {
			t.Errorf("mode=%s kind = %q, want filesystem-structure", mode, e.Kind)
		}
	}

	// The tool normalizes the mode and infers it from the argument shape when
	// it is absent, so anything the tool will run AS a start has to be gated
	// as one. Comparing the raw field here would let these through.
	for _, args := range []string{
		`{"mode":" Start ","path":"out.md"}`,
		`{"mode":"START","path":"out.md"}`,
		`{"path":"out.md","content":"chapter one"}`,
	} {
		if e := reg.Derive(ctx, "write_file_chunked", args); e.Kind != effect.KindFileWrite {
			t.Errorf("%s kind = %q, want filesystem-write — this call starts a write", args, e.Kind)
		}
	}
}

func TestDeriveMalformedArgsFailsClosed(t *testing.T) {
	reg := effect.NewRegistry()
	registerEffects(reg, &fsDeps{})
	e := reg.Derive(context.Background(), "rm", `{"path": unterminated`)
	if e.Kind != effect.KindUnknown {
		t.Fatalf("kind = %q, want unknown for unparseable arguments", e.Kind)
	}
}

func quote(s string) string {
	out := []byte{'"'}
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(append(out, '"'))
}
