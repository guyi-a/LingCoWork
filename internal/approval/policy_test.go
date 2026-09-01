package approval

import (
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/effect"
)

func outcome(mode Mode, e effect.Effect, args string) string {
	if must, _ := MustAsk(e); must {
		return "ask"
	}
	if sensitive, _ := IsSensitiveCall(e, args); sensitive {
		return "ask"
	}
	if ShouldAsk(mode, e) {
		return "ask"
	}
	return "allow"
}

func TestPolicyMatrix(t *testing.T) {
	tests := []struct {
		name                      string
		e                         effect.Effect
		manual, acceptWrite, auto string
	}{
		{
			name:   "workspace read",
			e:      effect.Effect{Kind: effect.KindFileRead, Scope: effect.ScopeWorkspace},
			manual: "allow", acceptWrite: "allow", auto: "allow",
		},
		{
			name:   "external read",
			e:      effect.Effect{Kind: effect.KindFileRead, Scope: effect.ScopeExternal, Path: "/etc/hosts"},
			manual: "ask", acceptWrite: "ask", auto: "allow",
		},
		{
			name:   "workspace write",
			e:      effect.Effect{Kind: effect.KindFileWrite, Scope: effect.ScopeWorkspace, Path: "/ws/a.go"},
			manual: "ask", acceptWrite: "allow", auto: "allow",
		},
		{
			name:   "external write",
			e:      effect.Effect{Kind: effect.KindFileWrite, Scope: effect.ScopeExternal, Path: "/tmp/a.go"},
			manual: "ask", acceptWrite: "ask", auto: "allow",
		},
		{
			name:   "workspace transfer",
			e:      effect.Effect{Kind: effect.KindFileTransfer, Scope: effect.ScopeWorkspace},
			manual: "ask", acceptWrite: "allow", auto: "allow",
		},
		{
			name:   "harmless command",
			e:      effect.Effect{Kind: effect.KindProcessExec, Classification: effect.Harmless},
			manual: "allow", acceptWrite: "allow", auto: "allow",
		},
		{
			name:   "normal command",
			e:      effect.Effect{Kind: effect.KindProcessExec, Classification: effect.Normal},
			manual: "ask", acceptWrite: "ask", auto: "allow",
		},
		{
			name:   "public network",
			e:      effect.Effect{Kind: effect.KindNetwork, URL: "https://example.com"},
			manual: "ask", acceptWrite: "allow", auto: "allow",
		},
		{
			name:   "private network",
			e:      effect.Effect{Kind: effect.KindNetwork, URL: "http://127.0.0.1", PrivateHost: true},
			manual: "ask", acceptWrite: "ask", auto: "allow",
		},
		{
			name:   "public mcp",
			e:      effect.Effect{Kind: effect.KindMCPCall, Server: "wiki", RemoteTool: "read"},
			manual: "ask", acceptWrite: "allow", auto: "allow",
		},
		{
			name:   "local mcp",
			e:      effect.Effect{Kind: effect.KindMCPCall, Server: "local", RemoteTool: "run", PrivateHost: true},
			manual: "ask", acceptWrite: "ask", auto: "allow",
		},
		{
			name:   "memory write always asks",
			e:      effect.Effect{Kind: effect.KindMemoryWrite},
			manual: "ask", acceptWrite: "ask", auto: "ask",
		},
		{
			name:   "destructive always asks",
			e:      effect.Effect{Kind: effect.KindFileWrite, Destructive: true},
			manual: "ask", acceptWrite: "ask", auto: "ask",
		},
		{
			name:   "unknown always asks",
			e:      effect.Unknown("missing deriver"),
			manual: "ask", acceptWrite: "ask", auto: "ask",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outcome(ModeManual, tt.e, ""); got != tt.manual {
				t.Errorf("manual=%s want %s", got, tt.manual)
			}
			if got := outcome(ModeAcceptWrite, tt.e, ""); got != tt.acceptWrite {
				t.Errorf("accept-write=%s want %s", got, tt.acceptWrite)
			}
			if got := outcome(ModeAuto, tt.e, ""); got != tt.auto {
				t.Errorf("auto=%s want %s", got, tt.auto)
			}
		})
	}
}

func TestSensitiveWritesAlwaysAsk(t *testing.T) {
	e := effect.Effect{Kind: effect.KindFileWrite, Scope: effect.ScopeWorkspace, Path: "/ws/config.go"}
	if got := outcome(ModeAuto, e, `{"content":"AKIAIOSFODNN7EXAMPLE"}`); got != "ask" {
		t.Fatalf("credential write in auto=%s", got)
	}
	e.Path = "/ws/credentials.json"
	if got := outcome(ModeAuto, e, `{"content":"{}"}`); got != "ask" {
		t.Fatalf("protected path in auto=%s", got)
	}
	e.Path = "/ws/.env"
	if got := outcome(ModeAcceptWrite, e, `{"content":"ordinary"}`); got != "allow" {
		t.Fatalf(".env should follow ordinary workspace policy, got %s", got)
	}
}

func TestMustAskReasons(t *testing.T) {
	if _, reason := MustAsk(effect.Unknown("no deriver")); reason != "no deriver" {
		t.Fatalf("reason=%q", reason)
	}
	if must, _ := MustAsk(effect.Effect{Kind: effect.KindMemoryWrite}); !must {
		t.Fatal("memory write must always ask")
	}
}
