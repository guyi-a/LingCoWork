package mcp

import (
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/effect"
)

// A server that reconnects must get its old names back. The public name ends
// up in stored transcripts, so a tool silently becoming wiki__lookup_2 after
// a reconnect would break every reference to it.
func TestAllocatorReleaseRestoresNames(t *testing.T) {
	a := newAllocator([]string{"read_file"})

	slug := a.slugFor("wiki")
	first := a.nameFor(slug, "lookup")

	a.release(slug, []string{first})

	slugAgain := a.slugFor("wiki")
	if slugAgain != slug {
		t.Fatalf("slug after release = %q, want %q", slugAgain, slug)
	}
	if again := a.nameFor(slugAgain, "lookup"); again != first {
		t.Fatalf("name after release = %q, want %q", again, first)
	}
}

// Builtins are reserved, not allocated. Releasing a server that collided with
// one must not hand the builtin's name to the next remote tool.
func TestAllocatorReleaseKeepsBuiltinsReserved(t *testing.T) {
	a := newAllocator([]string{"fs__read"})
	slug := a.slugFor("fs")
	taken := a.nameFor(slug, "read")

	a.release(slug, []string{taken, "fs__read"})

	if got := a.nameFor(a.slugFor("fs"), "read"); got == "fs__read" {
		t.Fatal("a released server took over a builtin name")
	}
}

func cfgWith(servers ...ServerConfig) *Config {
	return &Config{Servers: servers, Path: "mcp.json"}
}

// snapshot reads a server's state under the lock. Apply starts background
// dials, so an unguarded read here is a genuine race, not test pedantry.
func snapshot(t *testing.T, m *Manager, name string) (st serverState, ok bool) {
	t.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.servers[name]
	if s == nil {
		return serverState{}, false
	}
	return *s, true
}

// Apply must leave an untouched server's connection alone. Bouncing a stdio
// child process on every save would cost seconds of downtime for an edit that
// had nothing to do with it.
func TestApplyLeavesUnchangedServersConnected(t *testing.T) {
	keep := ServerConfig{Name: "keep", URL: "https://a.invalid/mcp"}
	m := New(cfgWith(keep), nil, nil)

	// Stand in for a connected server: Apply must not disturb this state.
	m.servers["keep"] = &serverState{cfg: keep, state: StateConnected, slug: "keep"}
	m.order = []string{"keep"}
	genBefore := m.servers["keep"].gen

	m.Apply(t.Context(), cfgWith(keep))

	st, ok := snapshot(t, m, "keep")
	if !ok {
		t.Fatal("server disappeared")
	}
	if st.gen != genBefore {
		t.Error("an unchanged server was torn down")
	}
	if st.state != StateConnected {
		t.Errorf("state = %q, want connected", st.state)
	}
}

func TestApplyDropsRemovedServers(t *testing.T) {
	gone := ServerConfig{Name: "gone", URL: "https://a.invalid/mcp"}
	m := New(cfgWith(gone), nil, nil)
	m.servers["gone"] = &serverState{cfg: gone, state: StateConnected}
	m.order = []string{"gone"}

	m.Apply(t.Context(), cfgWith())

	if _, still := snapshot(t, m, "gone"); still {
		t.Fatal("a server removed from the config is still tracked")
	}
}

// A changed entry is torn down so the new settings actually take effect.
func TestApplyTearsDownChangedServers(t *testing.T) {
	before := ServerConfig{Name: "s", URL: "https://a.invalid/mcp"}
	// Apply starts a background dial for the changed entry. Point it at a
	// closed local port so that dial is refused instantly instead of going
	// out to a resolver and hanging around past the end of the test.
	after := ServerConfig{Name: "s", URL: "http://127.0.0.1:1/mcp"}

	m := New(cfgWith(before), nil, nil)
	m.servers["s"] = &serverState{cfg: before, state: StateConnected}
	m.order = []string{"s"}
	genBefore := m.servers["s"].gen

	m.Apply(t.Context(), cfgWith(after))

	st, _ := snapshot(t, m, "s")
	if st.gen == genBefore {
		t.Fatal("a changed server kept its old connection")
	}
	if st.cfg.URL != after.URL {
		t.Errorf("config not updated: %q", st.cfg.URL)
	}
}

// Disabling a server should stop it without removing it from the list.
func TestApplyDisablesWithoutForgetting(t *testing.T) {
	enabled := ServerConfig{Name: "s", URL: "https://a.invalid/mcp"}
	no := false
	disabled := ServerConfig{Name: "s", URL: "https://a.invalid/mcp", Enabled: &no}

	m := New(cfgWith(enabled), nil, nil)
	m.servers["s"] = &serverState{cfg: enabled, state: StateConnected}
	m.order = []string{"s"}

	m.Apply(t.Context(), cfgWith(disabled))

	st, ok := snapshot(t, m, "s")
	if !ok {
		t.Fatal("a disabled server was forgotten entirely")
	}
	if st.state != StateDisabled {
		t.Errorf("state = %q, want disabled", st.state)
	}
}

// Tearing a server down has to withdraw its tools' effects. Leaving them
// registered would let the next server to take that name inherit trust the
// user granted to somebody else.
func TestTeardownUnregistersEffects(t *testing.T) {
	reg := effect.NewRegistry()
	m := New(cfgWith(), nil, nil)
	m.SetEffectRegistry(reg)

	reg.Register("wiki__lookup", effect.Static(effect.Effect{Kind: effect.KindMCPCall}))
	st := &serverState{
		cfg:      ServerConfig{Name: "wiki", URL: "https://a.invalid/mcp"},
		slug:     "wiki",
		bindings: []*binding{{publicName: "wiki__lookup"}},
	}
	m.servers["wiki"] = st

	m.mu.Lock()
	m.teardownLocked(st)
	m.mu.Unlock()

	if reg.Has("wiki__lookup") {
		t.Fatal("a departed server's effect is still registered")
	}
}

func TestSameServerDetectsEveryFieldChange(t *testing.T) {
	base := ServerConfig{Name: "s", URL: "https://a.invalid/mcp"}
	if !sameServer(base, base) {
		t.Fatal("a config is not equal to itself")
	}

	changed := []ServerConfig{
		{Name: "s", URL: "https://b.invalid/mcp"},
		{Name: "s", URL: "https://a.invalid/mcp", Headers: map[string]string{"X": "1"}},
		{Name: "s", URL: "https://a.invalid/mcp", TrustAnnotations: true},
		{Name: "s", URL: "https://a.invalid/mcp", AutoApprove: []string{"t"}},
		{Name: "s", URL: "https://a.invalid/mcp", Auth: "oauth"},
		{Name: "s", URL: "https://a.invalid/mcp", InitTimeoutSec: 60},
	}
	for _, c := range changed {
		if sameServer(base, c) {
			t.Errorf("change not detected: %+v", c)
		}
	}
}

// Tools is called at the start of every agent run, so it must answer for a
// manager where nothing has connected yet.
func TestToolsOnAnEmptyManager(t *testing.T) {
	m := New(nil, nil, nil)
	if got := m.Tools(); len(got) != 0 {
		t.Fatalf("Tools = %v, want none", got)
	}
	if got := m.ToolProvider()(t.Context()); len(got) != 0 {
		t.Fatalf("ToolProvider = %v, want none", got)
	}
}

// Status is serialised straight to the browser. The check that matters is
// structural: there is nowhere in ServerStatus for a credential to sit.
func TestStatusReportsDisabledAndOAuthServers(t *testing.T) {
	no := false
	m := New(cfgWith(
		ServerConfig{Name: "off", URL: "https://a.invalid/mcp", Enabled: &no},
		ServerConfig{Name: "secure", URL: "https://b.invalid/mcp", Auth: "oauth"},
	), nil, nil)
	m.Connect(t.Context())

	got := m.Status(t.Context())
	if len(got) != 2 {
		t.Fatalf("Status returned %d servers, want 2", len(got))
	}
	byName := map[string]ServerStatus{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if byName["off"].State != StateDisabled {
		t.Errorf("disabled server state = %q", byName["off"].State)
	}
	if !byName["secure"].OAuth {
		t.Error("an oauth server did not report itself as one")
	}
}

func TestRecoveryForDiscoveredOAuthServerWithStoredToken(t *testing.T) {
	creds := NewMemoryCredentialStore()
	if err := creds.SaveToken(
		t.Context(),
		"kling",
		`{"access_token":"old-access","refresh_token":"refresh","token_type":"Bearer"}`,
	); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthorizer(creds, DefaultRedirectURI)
	m := New(cfgWith(), nil, auth)
	cfg := ServerConfig{Name: "kling", URL: "https://example.invalid/mcp"}

	if cfg.UsesOAuth() {
		t.Fatal("fixture must represent OAuth discovered at runtime")
	}
	if recovery := m.recoveryFor(cfg); recovery == nil {
		t.Fatal("stored OAuth token did not enable refresh recovery")
	}
}
