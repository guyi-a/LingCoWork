package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseConfigInfersTransport(t *testing.T) {
	raw := []byte(`{
	  "mcpServers": {
	    "fs":   {"command": "npx", "args": ["-y", "server-filesystem"]},
	    "wiki": {"url": "https://mcp.example/mcp"}
	  }
	}`)
	cfg, err := parseConfig("mcp.json", raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.Issues) != 0 {
		t.Fatalf("unexpected issues: %+v", cfg.Issues)
	}
	fs, _ := cfg.Get("fs")
	if fs.Transport() != TransportStdio {
		t.Errorf("fs transport = %q", fs.Transport())
	}
	wiki, _ := cfg.Get("wiki")
	if wiki.Transport() != TransportHTTP {
		t.Errorf("wiki transport = %q", wiki.Transport())
	}
}

// Servers are sorted so the name allocator sees them in the same order every
// boot; otherwise Go's map iteration could shuffle which of two colliding
// tools gets the suffix, and stored transcripts would stop matching.
func TestParseConfigSortsServers(t *testing.T) {
	raw := []byte(`{"mcpServers": {"zeta": {"url": "u"}, "alpha": {"url": "u"}, "mid": {"url": "u"}}}`)
	cfg, err := parseConfig("mcp.json", raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	want := []string{"alpha", "mid", "zeta"}
	for i, w := range want {
		if cfg.Servers[i].Name != w {
			t.Fatalf("servers[%d] = %q, want %q", i, cfg.Servers[i].Name, w)
		}
	}
}

// A server one brace out of place is still valid JSON, so nothing downstream
// notices — the entry just isn't a server, and the page shows a saved config
// with a server missing and no explanation. It has to be reported.
func TestParseConfigReportsServerOutsideMCPServers(t *testing.T) {
	raw := []byte(`{
	  "mcpServers": {
	    "deepwiki": {"url": "https://mcp.deepwiki.com/mcp"}
	  },
	  "kling": {"type": "http", "url": "https://klingai.com/mcp"}
	}`)
	cfg, err := parseConfig("mcp.json", raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	// The healthy server still loads.
	if len(cfg.Servers) != 1 || cfg.Servers[0].Name != "deepwiki" {
		t.Fatalf("servers = %+v", cfg.Servers)
	}
	if len(cfg.Issues) != 1 || cfg.Issues[0].Server != "kling" {
		t.Fatalf("issues = %+v, want one naming kling", cfg.Issues)
	}
	if !strings.Contains(cfg.Issues[0].Message, "mcpServers") {
		t.Errorf("message does not say where it belongs: %q", cfg.Issues[0].Message)
	}
}

// $schema is what an editor adds on its own, and complaining about it would
// train the user to ignore the warning that matters.
func TestParseConfigToleratesSchemaKey(t *testing.T) {
	raw := []byte(`{
	  "$schema": "https://example.com/mcp.schema.json",
	  "mcpServers": {"wiki": {"url": "https://mcp.example/mcp"}}
	}`)
	cfg, err := parseConfig("mcp.json", raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.Issues) != 0 {
		t.Fatalf("unexpected issues: %+v", cfg.Issues)
	}
}

// One broken entry must not take the healthy ones with it.
func TestParseConfigIsolatesBadEntries(t *testing.T) {
	raw := []byte(`{
	  "mcpServers": {
	    "good":    {"url": "https://mcp.example/mcp"},
	    "neither": {"enabled": true},
	    "both":    {"command": "npx", "url": "https://x/mcp"},
	    "mistyped":{"type": "stdio", "url": "https://x/mcp"}
	  }
	}`)
	cfg, err := parseConfig("mcp.json", raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].Name != "good" {
		t.Fatalf("servers = %+v", cfg.Servers)
	}
	if len(cfg.Issues) != 3 {
		t.Fatalf("issues = %+v, want 3", cfg.Issues)
	}
}

// A pasted Claude Desktop config has no "enabled" field and every server in it
// is meant to run.
func TestMissingEnabledMeansEnabled(t *testing.T) {
	raw := []byte(`{"mcpServers": {"a": {"url": "u"}, "b": {"url": "u", "enabled": false}}}`)
	cfg, _ := parseConfig("mcp.json", raw)

	a, _ := cfg.Get("a")
	if !a.IsEnabled() {
		t.Error("a with no enabled field was treated as disabled")
	}
	b, _ := cfg.Get("b")
	if b.IsEnabled() {
		t.Error("b was explicitly disabled but reports enabled")
	}
}

func TestWriteConfigIsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigDirName, ConfigFileName)
	body := []byte(`{"mcpServers": {"a": {"url": "https://x/mcp"}}}`)

	if err := WriteConfig(path, body); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600 — the file holds API keys", perm)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(body) {
		t.Errorf("content = %q", got)
	}
	// The temp file must not be left behind next to the real one.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("leftover files in config dir: %d", len(entries))
	}
}

func TestWriteConfigRejectsUnparseable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)

	if err := WriteConfig(path, []byte("{not json")); err == nil {
		t.Fatal("WriteConfig accepted a document that will not parse")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a rejected save still touched the file")
	}
}

// A per-entry problem is a normal state to leave an editor in and must not
// block the save, unlike a document that will not parse at all.
func TestValidateRawSeparatesEntryIssuesFromParseFailure(t *testing.T) {
	issues, err := ValidateRaw([]byte(`{"mcpServers": {"a": {}}}`))
	if err != nil {
		t.Fatalf("a bad entry became a document error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %+v, want 1", issues)
	}
	if _, err := ValidateRaw([]byte("nope")); err == nil {
		t.Fatal("ValidateRaw accepted a non-JSON document")
	}
}

// OAuth needs an HTTP request to attach a bearer token to. A stdio server is
// a child process; there is nowhere to put one and no browser flow that would
// mean anything.
func TestOAuthOnlyAppliesToHTTPServers(t *testing.T) {
	raw := []byte(`{
	  "mcpServers": {
	    "remote": {"url": "https://mcp.example/mcp", "auth": "oauth"},
	    "local":  {"command": "npx", "args": ["srv"], "auth": "oauth"},
	    "typo":   {"url": "https://mcp.example/mcp", "auth": "oauth2"},
	    "orphan": {"url": "https://mcp.example/mcp", "oauth": {"scopes": ["read"]}}
	  }
	}`)
	cfg, err := parseConfig("mcp.json", raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].Name != "remote" {
		t.Fatalf("servers = %+v, want only remote", cfg.Servers)
	}
	if len(cfg.Issues) != 3 {
		t.Fatalf("issues = %+v, want 3", cfg.Issues)
	}
}

func TestOAuthBlockIsParsed(t *testing.T) {
	raw := []byte(`{
	  "mcpServers": {
	    "remote": {
	      "url": "https://mcp.example/mcp",
	      "auth": "oauth",
	      "oauth": {
	        "clientId": "abc",
	        "scopes": ["read", "write"],
	        "redirectUri": "http://127.0.0.1:9001/mcp/oauth/callback"
	      }
	    }
	  }
	}`)
	cfg, err := parseConfig("mcp.json", raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	srv, ok := cfg.Get("remote")
	if !ok {
		t.Fatal("remote missing")
	}
	if !srv.UsesOAuth() {
		t.Error("UsesOAuth = false")
	}
	oc := srv.OAuthOrEmpty()
	if oc.ClientID != "abc" || len(oc.Scopes) != 2 || oc.RedirectURI == "" {
		t.Fatalf("oauth block = %+v", oc)
	}
}

// A server with no oauth block at all is the normal case: dynamic
// registration fills in the client id, so there is nothing to write.
func TestOAuthWithNoBlockIsValid(t *testing.T) {
	cfg, err := parseConfig("mcp.json",
		[]byte(`{"mcpServers": {"r": {"url": "https://x/mcp", "auth": "oauth"}}}`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.Issues) != 0 {
		t.Fatalf("issues = %+v", cfg.Issues)
	}
	srv, _ := cfg.Get("r")
	if srv.OAuthOrEmpty().ClientID != "" {
		t.Error("OAuthOrEmpty invented a client id")
	}
}

func TestRemoveServerKeepsEverythingElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	write(t, path, `{
	  "$schema": "https://example/schema.json",
	  "mcpServers": {
	    "deepwiki": {"url": "https://mcp.deepwiki.com/mcp", "trustAnnotations": true},
	    "kling":    {"type": "http", "url": "https://klingai.com/mcp?a=1&b=2"}
	  }
	}`)

	removed, err := RemoveServer(path, "kling")
	if err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	if !removed {
		t.Fatal("RemoveServer reported nothing removed")
	}

	cfg, err := parseConfig(path, []byte(read(t, path)))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, still := cfg.Get("kling"); still {
		t.Error("kling survived the delete")
	}
	srv, ok := cfg.Get("deepwiki")
	if !ok {
		t.Fatal("deepwiki was taken down with it")
	}
	if !srv.TrustAnnotations {
		t.Error("deepwiki lost trustAnnotations in the rewrite")
	}

	raw := read(t, path)
	if !strings.Contains(raw, `"$schema"`) {
		t.Errorf("an unrelated top-level key was dropped: %s", raw)
	}
}

// A URL's query separator must come back as itself. Marshal's default HTML
// escaping would write \u0026, which parses the same but is not what the user
// typed into a file they still have to read.
func TestRemoveServerDoesNotEscapeURLs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	write(t, path, `{"mcpServers": {
	  "gone": {"url": "https://x/mcp"},
	  "kept": {"url": "https://y/mcp?a=1&b=2"}
	}}`)

	if _, err := RemoveServer(path, "gone"); err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	if raw := read(t, path); !strings.Contains(raw, "?a=1&b=2") {
		t.Errorf("url was escaped: %s", raw)
	}
}

// Deleting a name that is not in the file is not an error: the config may
// have been edited by hand between the page loading and the click.
func TestRemoveServerReportsAMiss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	write(t, path, `{"mcpServers": {"a": {"url": "https://x/mcp"}}}`)

	removed, err := RemoveServer(path, "b")
	if err != nil || removed {
		t.Fatalf("RemoveServer = %v, %v; want false, nil", removed, err)
	}
	if _, err := RemoveServer(filepath.Join(t.TempDir(), "absent.json"), "a"); err != nil {
		t.Fatalf("RemoveServer on a missing file: %v", err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestResolveConfigPathHonoursEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_CONFIG_PATH", path)

	got, found := ResolveConfigPath()
	if !found || got != path {
		t.Fatalf("ResolveConfigPath = %q, %v; want %q, true", got, found, path)
	}
}
