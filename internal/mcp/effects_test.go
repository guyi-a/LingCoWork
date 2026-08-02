package mcp

import (
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/effect"
)

func boolp(b bool) *bool { return &b }

func toolWith(name string, a mcpgo.ToolAnnotation) mcpgo.Tool {
	return mcpgo.Tool{Name: name, Annotations: a}
}

func TestToolEffectCarriesIdentity(t *testing.T) {
	srv := ServerConfig{Name: "deepwiki", URL: "https://mcp.example/mcp"}
	e := toolEffect(srv, toolWith("read_wiki", mcpgo.ToolAnnotation{}))

	if e.Kind != effect.KindMCPCall {
		t.Fatalf("kind = %q, want %q", e.Kind, effect.KindMCPCall)
	}
	if e.Server != "deepwiki" || e.RemoteTool != "read_wiki" {
		t.Fatalf("identity = %q/%q", e.Server, e.RemoteTool)
	}
	if e.Transport != string(TransportHTTP) || e.URL != srv.URL {
		t.Fatalf("transport = %q url = %q", e.Transport, e.URL)
	}
}

// A server that says nothing must not be read as saying "safe". Absent
// annotations and readOnlyHint:false have to reach the same verdict.
func TestToolEffectUnstatedAnnotationsAreNotReadOnly(t *testing.T) {
	srv := ServerConfig{Name: "s", URL: "https://x/mcp", TrustAnnotations: true}
	e := toolEffect(srv, toolWith("t", mcpgo.ToolAnnotation{}))

	if e.ReadOnly || e.Destructive || e.OpenWorld {
		t.Fatalf("unstated annotations produced claims: %+v", e)
	}
	if !approval.NeedsApproval(e) {
		t.Fatal("a tool with no annotations was auto-approved")
	}
}

func TestApprovalMatrix(t *testing.T) {
	cases := []struct {
		name string
		srv  ServerConfig
		ann  mcpgo.ToolAnnotation
		// needsApproval is what the default and auto modes consult.
		needsApproval bool
		// mustAsk holds in every mode, full_access included.
		mustAsk bool
	}{
		{
			name:          "no annotations, untrusted server",
			srv:           ServerConfig{Name: "s", URL: "u"},
			needsApproval: true,
		},
		{
			name:          "read-only claim from an untrusted server is not evidence",
			srv:           ServerConfig{Name: "s", URL: "u"},
			ann:           mcpgo.ToolAnnotation{ReadOnlyHint: boolp(true)},
			needsApproval: true,
		},
		{
			name: "read-only claim from a trusted server passes",
			srv:  ServerConfig{Name: "s", URL: "u", TrustAnnotations: true},
			ann:  mcpgo.ToolAnnotation{ReadOnlyHint: boolp(true)},
		},
		{
			name:          "destructive claim is believed even from a trusted server",
			srv:           ServerConfig{Name: "s", URL: "u", TrustAnnotations: true},
			ann:           mcpgo.ToolAnnotation{ReadOnlyHint: boolp(true), DestructiveHint: boolp(true)},
			needsApproval: true,
			mustAsk:       true,
		},
		{
			name: "autoApprove clears the destructive wall for that one tool",
			srv: ServerConfig{
				Name: "s", URL: "u", AutoApprove: []string{"t"},
			},
			ann: mcpgo.ToolAnnotation{DestructiveHint: boolp(true)},
		},
		{
			name:          "autoApprove names a different tool",
			srv:           ServerConfig{Name: "s", URL: "u", AutoApprove: []string{"other"}},
			ann:           mcpgo.ToolAnnotation{DestructiveHint: boolp(true)},
			needsApproval: true,
			mustAsk:       true,
		},
		{
			name: "open world alone does not gate a trusted read-only tool",
			srv:  ServerConfig{Name: "s", URL: "u", TrustAnnotations: true},
			ann: mcpgo.ToolAnnotation{
				ReadOnlyHint: boolp(true), OpenWorldHint: boolp(true),
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := toolEffect(c.srv, toolWith("t", c.ann))

			if got := approval.NeedsApproval(e); got != c.needsApproval {
				t.Errorf("NeedsApproval = %v, want %v (effect %+v)", got, c.needsApproval, e)
			}
			got, _ := approval.MustAsk(e)
			if got != c.mustAsk {
				t.Errorf("MustAsk = %v, want %v (effect %+v)", got, c.mustAsk, e)
			}
		})
	}
}

// Every branch has to produce a line for the card; a blank note leaves the
// user approving a bare tool name.
func TestNoteForIsAlwaysPopulated(t *testing.T) {
	anns := []mcpgo.ToolAnnotation{
		{},
		{ReadOnlyHint: boolp(true)},
		{ReadOnlyHint: boolp(false)},
		{DestructiveHint: boolp(true)},
		{OpenWorldHint: boolp(true)},
	}
	for _, trusted := range []bool{false, true} {
		for _, auto := range [][]string{nil, {"t"}} {
			for _, a := range anns {
				srv := ServerConfig{
					Name: "s", URL: "u",
					TrustAnnotations: trusted,
					AutoApprove:      auto,
				}
				if note := toolEffect(srv, toolWith("t", a)).Note; note == "" {
					t.Errorf("empty note for trusted=%v auto=%v ann=%+v", trusted, auto, a)
				}
			}
		}
	}
}
