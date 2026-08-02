package approval

import (
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/effect"
)

// modeOf mirrors the middleware's order of operations so the tests assert the
// behaviour a user actually gets, not just what one function returns in
// isolation. The ordering is the security property: MustAsk has to run BEFORE
// the mode is consulted, or full_access becomes a way around it.
func modeOf(mode Mode, e effect.Effect) string {
	if must, _ := MustAsk(e); must {
		return "ask"
	}
	if mode == ModeFullAccess {
		return "allow"
	}
	if !NeedsApproval(e) {
		return "allow"
	}
	if mode == ModeAuto {
		if ok, _ := IsSafeAuto(e, ""); ok {
			return "allow"
		}
		return "classifier" // falls through to LLM, then to the human
	}
	return "ask"
}

func TestPolicyMatrix(t *testing.T) {
	tests := []struct {
		name                            string
		e                               effect.Effect
		wantDefault, wantAuto, wantFull string
	}{
		{
			name:        "readonly query",
			e:           effect.Effect{Kind: effect.KindReadOnlyQuery},
			wantDefault: "allow", wantAuto: "allow", wantFull: "allow",
		},
		{
			// Approving a question would be approving being asked a question.
			name:        "ask_user is never gated",
			e:           effect.Effect{Kind: effect.KindUserInteraction},
			wantDefault: "allow", wantAuto: "allow", wantFull: "allow",
		},
		{
			// The sub-agent's own middleware gates whatever it does next.
			name:        "sub-agent delegation is not fail-closed",
			e:           effect.Effect{Kind: effect.KindDelegate, Agent: "deep_research"},
			wantDefault: "allow", wantAuto: "allow", wantFull: "allow",
		},
		{
			name:        "mkdir / create_workspace",
			e:           effect.Effect{Kind: effect.KindFileStructure, Scope: effect.ScopeWorkspace},
			wantDefault: "allow", wantAuto: "allow", wantFull: "allow",
		},
		{
			name:        "network request",
			e:           effect.Effect{Kind: effect.KindNetwork, URL: "https://example.com"},
			wantDefault: "allow", wantAuto: "allow", wantFull: "allow",
		},
		{
			name:        "skill load",
			e:           effect.Effect{Kind: effect.KindSkillLoad},
			wantDefault: "allow", wantAuto: "allow", wantFull: "allow",
		},
		{
			name:        "read inside the workspace",
			e:           effect.Effect{Kind: effect.KindFileRead, Scope: effect.ScopeWorkspace},
			wantDefault: "allow", wantAuto: "allow", wantFull: "allow",
		},
		{
			// The gap this refactor closes: reading anywhere on the machine
			// used to run silently in every mode.
			name:        "read outside the workspace",
			e:           effect.Effect{Kind: effect.KindFileRead, Scope: effect.ScopeExternal, Path: "/etc/hosts"},
			wantDefault: "ask", wantAuto: "classifier", wantFull: "allow",
		},
		{
			name:        "workspace write",
			e:           effect.Effect{Kind: effect.KindFileWrite, Scope: effect.ScopeWorkspace, Path: "/ws/notes.md"},
			wantDefault: "ask", wantAuto: "allow", wantFull: "allow",
		},
		{
			name: "workspace write to a sensitive path",
			e: effect.Effect{
				Kind: effect.KindFileWrite, Scope: effect.ScopeWorkspace, Path: "/ws/.env",
			},
			wantDefault: "ask", wantAuto: "classifier", wantFull: "allow",
		},
		{
			name:        "write outside the workspace",
			e:           effect.Effect{Kind: effect.KindFileWrite, Scope: effect.ScopeExternal, Path: "/etc/hosts"},
			wantDefault: "ask", wantAuto: "classifier", wantFull: "allow",
		},
		{
			// Ordinary deletes are not irreversible enough to override an
			// explicit full_access; they still stop the other two modes.
			name: "plain delete",
			e: effect.Effect{
				Kind: effect.KindFileWrite, Scope: effect.ScopeWorkspace, Path: "/ws/tmp.txt",
			},
			wantDefault: "ask", wantAuto: "allow", wantFull: "allow",
		},
		{
			name: "recursive delete",
			e: effect.Effect{
				Kind: effect.KindFileWrite, Scope: effect.ScopeWorkspace,
				Path: "/ws/build", Destructive: true,
			},
			wantDefault: "ask", wantAuto: "ask", wantFull: "ask",
		},
		{
			name: "harmless command",
			e: effect.Effect{
				Kind: effect.KindProcessExec, Command: "ls -la",
				Classification: effect.Harmless, Scope: effect.ScopeWorkspace,
			},
			wantDefault: "ask", wantAuto: "allow", wantFull: "allow",
		},
		{
			name: "ordinary command",
			e: effect.Effect{
				Kind: effect.KindProcessExec, Command: "go build ./...",
				Classification: effect.Normal, Scope: effect.ScopeWorkspace,
			},
			wantDefault: "ask", wantAuto: "classifier", wantFull: "allow",
		},
		{
			name: "destructive command",
			e: effect.Effect{
				Kind: effect.KindProcessExec, Command: "rm -rf /",
				Classification: effect.Destructive, Destructive: true,
			},
			wantDefault: "ask", wantAuto: "ask", wantFull: "ask",
		},
		{
			name: "copy into the workspace",
			e: effect.Effect{
				Kind: effect.KindFileTransfer, Scope: effect.ScopeExternal,
				Path: "/tmp/data.csv", PathScope: effect.ScopeExternal,
				DestPath: "/ws/data.csv", DestScope: effect.ScopeWorkspace,
			},
			wantDefault: "ask", wantAuto: "classifier", wantFull: "allow",
		},
		{
			name: "copy within the workspace",
			e: effect.Effect{
				Kind: effect.KindFileTransfer, Scope: effect.ScopeWorkspace,
				Path: "/ws/a.md", PathScope: effect.ScopeWorkspace,
				DestPath: "/ws/b.md", DestScope: effect.ScopeWorkspace,
			},
			wantDefault: "ask", wantAuto: "allow", wantFull: "allow",
		},
		{
			// The case the whole refactor exists for: an unregistered tool
			// must not be waved through, and full_access must not be a way
			// around that.
			name:        "unknown effect fails closed in every mode",
			e:           effect.Unknown("no effect deriver registered for mcp_deploy"),
			wantDefault: "ask", wantAuto: "ask", wantFull: "ask",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modeOf(ModeDefault, tt.e); got != tt.wantDefault {
				t.Errorf("default: got %q, want %q", got, tt.wantDefault)
			}
			if got := modeOf(ModeAuto, tt.e); got != tt.wantAuto {
				t.Errorf("auto: got %q, want %q", got, tt.wantAuto)
			}
			if got := modeOf(ModeFullAccess, tt.e); got != tt.wantFull {
				t.Errorf("full_access: got %q, want %q", got, tt.wantFull)
			}
		})
	}
}

func TestMustAskReasons(t *testing.T) {
	if _, reason := MustAsk(effect.Unknown("no deriver for mcp_deploy")); reason != "no deriver for mcp_deploy" {
		t.Errorf("unknown effect should surface its note as the reason, got %q", reason)
	}
	if must, _ := MustAsk(effect.Effect{Kind: effect.KindUnknown}); !must {
		t.Error("an unknown effect with no note must still ask")
	}
	if must, _ := MustAsk(effect.Effect{Kind: effect.KindFileRead, Scope: effect.ScopeWorkspace}); must {
		t.Error("an ordinary read must not hit the wall")
	}
}

func TestIsSafeAutoContentScanning(t *testing.T) {
	e := effect.Effect{Kind: effect.KindFileWrite, Scope: effect.ScopeWorkspace, Path: "/ws/config.go"}

	if ok, _ := IsSafeAuto(e, `{"path":"config.go","content":"package main"}`); !ok {
		t.Error("an ordinary workspace write should take the fast path")
	}
	if ok, why := IsSafeAuto(e, `{"path":"config.go","content":"AKIAIOSFODNN7EXAMPLE"}`); ok {
		t.Errorf("a credential in content must not fast-path (%s)", why)
	}
	// edit_file carries its payload in new_string, which the old fast path
	// never looked at.
	if ok, why := IsSafeAuto(e, `{"path":"config.go","new_string":"-----BEGIN RSA PRIVATE KEY-----"}`); ok {
		t.Errorf("a credential in new_string must not fast-path (%s)", why)
	}
	if ok, why := IsSafeAuto(e, `{"path":"config.go","new_content":"ghp_abcdefghijklmnopqrstuvwxyz"}`); ok {
		t.Errorf("a credential in new_content must not fast-path (%s)", why)
	}
}
