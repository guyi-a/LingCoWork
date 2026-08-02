// Package mcp connects to Model Context Protocol servers and exposes their
// tools to the agent as ordinary eino tools.
//
// The approval layer never learns that a tool came from a remote server: it
// reads effects, and this package derives one per remote tool from the
// server's declared annotations. Anything it cannot describe stays
// undescribed, which the policy treats as "always ask".
package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ConfigDirName / ConfigFileName locate the config relative to the project.
// The layout matches Claude Desktop's so an existing mcpServers block can be
// pasted across without editing.
const (
	ConfigDirName  = ".lingcowork"
	ConfigFileName = "mcp.json"

	// defaultInitTimeoutSec covers spawning the child process and the
	// initialize handshake. Generous because a stdio server launched via
	// `npx -y` may download the package on first run.
	defaultInitTimeoutSec = 30
)

// Transport is how we reach a server.
type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
)

// ServerConfig is one entry under "mcpServers".
//
// Which transport an entry uses is inferred from its shape — `command` means
// stdio, `url` means http — so the common Claude Desktop config works with no
// `type` field. An explicit `type` is accepted and must agree.
type ServerConfig struct {
	// Name is the map key, copied in during load so a ServerConfig can be
	// passed around alone.
	Name string `json:"-"`

	// stdio
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`

	// http
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// Auth selects how to authenticate an HTTP server. "" means whatever is
	// in Headers, which covers the "paste a personal access token" case.
	// "oauth" runs the authorization code flow.
	Auth  string       `json:"auth,omitempty"`
	OAuth *OAuthConfig `json:"oauth,omitempty"`

	Type string `json:"type,omitempty"`

	// Enabled is a pointer so that a missing field means enabled. A plain
	// bool would make every entry that omits it default to off, which is the
	// opposite of what someone pasting a Claude Desktop config expects.
	Enabled *bool `json:"enabled,omitempty"`

	// TrustAnnotations makes this server's readOnlyHint load-bearing. Off by
	// default: annotations are self-reported and the MCP spec says clients
	// must not trust them from an unverified server, so a tool claiming to be
	// read-only still goes through approval until the user vouches for the
	// server here.
	TrustAnnotations bool `json:"trustAnnotations,omitempty"`

	// AutoApprove lists REMOTE tool names (as the server publishes them, not
	// the prefixed name the model sees) the user has explicitly waved
	// through. Note this cannot escape the destructive wall — a tool that
	// declares destructiveHint still asks.
	AutoApprove []string `json:"autoApprove,omitempty"`

	InitTimeoutSec int `json:"initTimeoutSec,omitempty"`
}

// OAuthConfig is the per-server half of the authorization code flow. Nothing
// here is secret on its own; the tokens it leads to live in the database.
type OAuthConfig struct {
	// ClientID and ClientSecret are for a server where the user pre-
	// registered a client by hand. Left empty, the client registers itself
	// dynamically on first authorization and the result is persisted, so the
	// usual case is to leave both out.
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`

	Scopes []string `json:"scopes,omitempty"`

	// RedirectURI overrides the default callback. Needed because some
	// authorization servers reject "localhost" and insist on 127.0.0.1, and
	// because a hand-registered client has to match whatever it registered.
	RedirectURI string `json:"redirectUri,omitempty"`
}

// UsesOAuth reports whether this server authenticates via the OAuth flow.
func (s ServerConfig) UsesOAuth() bool {
	return strings.EqualFold(strings.TrimSpace(s.Auth), "oauth")
}

// OAuthOrEmpty saves every caller a nil check.
func (s ServerConfig) OAuthOrEmpty() OAuthConfig {
	if s.OAuth == nil {
		return OAuthConfig{}
	}
	return *s.OAuth
}

// IsEnabled reports whether the server should be connected. Absent means yes.
func (s ServerConfig) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// Transport reports how to reach this server. Only meaningful after validate.
func (s ServerConfig) Transport() Transport {
	if s.Command != "" {
		return TransportStdio
	}
	return TransportHTTP
}

// InitTimeout returns the handshake budget in seconds.
func (s ServerConfig) InitTimeout() int {
	if s.InitTimeoutSec > 0 {
		return s.InitTimeoutSec
	}
	return defaultInitTimeoutSec
}

// AutoApproves reports whether the user pre-approved this remote tool.
func (s ServerConfig) AutoApproves(remoteTool string) bool {
	for _, n := range s.AutoApprove {
		if n == remoteTool {
			return true
		}
	}
	return false
}

// Target is a one-line description of where the server lives, for status
// listings. It deliberately omits headers and env, which hold credentials.
func (s ServerConfig) Target() string {
	if s.Transport() == TransportStdio {
		if len(s.Args) == 0 {
			return s.Command
		}
		return s.Command + " " + strings.Join(s.Args, " ")
	}
	return s.URL
}

func (s ServerConfig) validate() error {
	switch {
	case s.Command == "" && s.URL == "":
		return fmt.Errorf(`needs either "command" (stdio) or "url" (http)`)
	case s.Command != "" && s.URL != "":
		return fmt.Errorf(`has both "command" and "url"; an entry is one transport or the other`)
	}
	switch strings.ToLower(strings.TrimSpace(s.Type)) {
	case "", "stdio", "http", "sse", "streamable-http":
	default:
		return fmt.Errorf("unknown type %q", s.Type)
	}
	if t := strings.ToLower(strings.TrimSpace(s.Type)); t != "" {
		declaredStdio := t == "stdio"
		if declaredStdio != (s.Command != "") {
			return fmt.Errorf("type %q disagrees with the fields given", s.Type)
		}
	}
	switch a := strings.ToLower(strings.TrimSpace(s.Auth)); a {
	case "", "none", "headers":
	case "oauth":
		// A stdio server is a child process we started; there is no HTTP
		// request to attach a bearer token to and no browser flow that would
		// mean anything.
		if s.Transport() != TransportHTTP {
			return fmt.Errorf(`"auth": "oauth" needs a "url"; a stdio server has nowhere to put a token`)
		}
	default:
		return fmt.Errorf("unknown auth %q", s.Auth)
	}
	if s.OAuth != nil && !s.UsesOAuth() {
		return fmt.Errorf(`has an "oauth" block but no "auth": "oauth"`)
	}
	return nil
}

// Issue is one entry that failed to load. Issues are reported rather than
// returned as an error so that one malformed server does not take the healthy
// ones down with it.
type Issue struct {
	Server  string `json:"server"`
	Message string `json:"message"`
}

// Config is the loaded file.
type Config struct {
	// Path is where the file was read from, or where it would be written if
	// it does not exist yet. Always set.
	Path string
	// Exists is false when no config file was found. Not an error: no config
	// simply means no MCP servers.
	Exists bool
	// Servers is sorted by name so tool ordering is stable across restarts.
	Servers []ServerConfig
	Issues  []Issue
}

// Get returns the named server.
func (c *Config) Get(name string) (ServerConfig, bool) {
	for _, s := range c.Servers {
		if s.Name == name {
			return s, true
		}
	}
	return ServerConfig{}, false
}

// ResolveConfigPath finds the config file.
//
// MCP_CONFIG_PATH wins outright. Otherwise walk up from the working directory
// looking for .lingcowork/mcp.json, the same way config.loadDotenv finds .env
// — the server is started from various depths and a fixed relative path only
// works from the repo root. When nothing is found, the returned path is where
// the file would go, so callers can create it.
func ResolveConfigPath() (path string, found bool) {
	if p := strings.TrimSpace(os.Getenv("MCP_CONFIG_PATH")); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		_, statErr := os.Stat(abs)
		return abs, statErr == nil
	}

	dir, err := os.Getwd()
	if err != nil {
		return filepath.Join(ConfigDirName, ConfigFileName), false
	}
	start := dir
	for range 8 {
		candidate := filepath.Join(dir, ConfigDirName, ConfigFileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return filepath.Join(start, ConfigDirName, ConfigFileName), false
}

// LoadConfig reads and parses the config file.
//
// It returns an error only when the file exists but is not usable at all
// (unreadable, or not a JSON object). Problems confined to a single server
// land in Issues, because losing every server to one typo in one entry is a
// worse failure than running with a subset.
func LoadConfig() (*Config, error) {
	path, found := ResolveConfigPath()
	cfg := &Config{Path: path, Exists: found}
	if !found {
		return cfg, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return parseConfig(path, raw)
}

func parseConfig(path string, raw []byte) (*Config, error) {
	cfg := &Config{Path: path, Exists: true}

	// Decode the server map lazily so a bad entry can be reported by name
	// instead of failing the whole document.
	var file struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.Issues = append(cfg.Issues, strayTopLevelIssues(raw)...)

	names := make([]string, 0, len(file.MCPServers))
	for name := range file.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		var s ServerConfig
		if err := json.Unmarshal(file.MCPServers[name], &s); err != nil {
			cfg.Issues = append(cfg.Issues, Issue{Server: name, Message: err.Error()})
			continue
		}
		s.Name = name
		if strings.TrimSpace(name) == "" {
			cfg.Issues = append(cfg.Issues, Issue{Server: name, Message: "server name is empty"})
			continue
		}
		if err := s.validate(); err != nil {
			cfg.Issues = append(cfg.Issues, Issue{Server: name, Message: err.Error()})
			continue
		}
		cfg.Servers = append(cfg.Servers, s)
	}
	return cfg, nil
}

// knownTopLevelKeys are the keys parseConfig actually reads. "$schema" is
// tolerated because editors add it and it means nothing to us.
var knownTopLevelKeys = map[string]bool{"mcpServers": true, "$schema": true}

// strayTopLevelIssues reports entries that sit next to "mcpServers" instead of
// inside it.
//
// This is a brace away from correct and the JSON stays valid, so nothing else
// in the pipeline notices: the entry is simply not a server, and the page
// shows a saved config with a server missing from the list and no explanation
// anywhere. Adding a second server by hand is exactly when it happens, since
// that is when a closing brace has to move.
//
// Reported as an Issue rather than an error because the rest of the document
// is fine and refusing the save would cost the user their other edits.
func strayTopLevelIssues(raw []byte) []Issue {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		// Not an object at all. The caller's own decode already reported
		// whatever is wrong with it.
		return nil
	}
	stray := make([]string, 0, len(top))
	for key := range top {
		if !knownTopLevelKeys[key] {
			stray = append(stray, key)
		}
	}
	if len(stray) == 0 {
		return nil
	}
	sort.Strings(stray)

	issues := make([]Issue, 0, len(stray))
	for _, key := range stray {
		msg := `is at the top level, outside "mcpServers", so it is not loaded as a server`
		// Distinguishing the two shapes matters: one is a misplaced server
		// and the fix is to move it, the other is a typo or a stale field
		// and the fix is to delete it.
		if isJSONObject(top[key]) {
			msg += "; move it inside the mcpServers block"
		} else {
			msg += "; remove it or move it inside the mcpServers block"
		}
		issues = append(issues, Issue{Server: key, Message: msg})
	}
	return issues
}

func isJSONObject(raw json.RawMessage) bool {
	return strings.HasPrefix(strings.TrimSpace(string(raw)), "{")
}

// ValidateRaw parses a candidate config without touching disk, for the
// settings page to check before saving. It returns the issues found; a
// document-level parse failure comes back as err.
func ValidateRaw(raw []byte) (issues []Issue, err error) {
	cfg, err := parseConfig("", raw)
	if err != nil {
		return nil, err
	}
	return cfg.Issues, nil
}

// RemoveServer deletes one entry from the config file, reporting whether it
// was there to begin with.
//
// The document is decoded and re-printed rather than edited as text. Each
// entry is carried across as raw bytes, so only the ordering of the top-level
// keys can change, and it changes to sorted — which is what the settings
// page's format button produces anyway. Anything else at the top level is
// preserved, since this has no business dropping a key it does not read.
func RemoveServer(path, name string) (removed bool, err error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	var servers map[string]json.RawMessage
	if blob, ok := top["mcpServers"]; ok {
		if err := json.Unmarshal(blob, &servers); err != nil {
			return false, fmt.Errorf(`parse "mcpServers" in %s: %w`, path, err)
		}
	}
	if _, ok := servers[name]; !ok {
		return false, nil
	}
	delete(servers, name)

	blob, err := encodeJSON(servers, "")
	if err != nil {
		return false, fmt.Errorf("encode mcpServers: %w", err)
	}
	top["mcpServers"] = blob

	out, err := encodeJSON(top, "  ")
	if err != nil {
		return false, fmt.Errorf("encode %s: %w", path, err)
	}
	if err := WriteConfig(path, append(out, '\n')); err != nil {
		return false, err
	}
	return true, nil
}

// encodeJSON marshals with HTML escaping off, at both levels of the rewrite:
// the default would turn the & in a URL's query string into \u0026, and once
// a nested value is escaped the outer pass cannot put it back.
func encodeJSON(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// WriteConfig saves a new config atomically.
//
// The file holds API keys and Authorization headers, so it is written 0600
// and swapped in with a rename: a half-written config that still parses would
// silently drop servers, and a crash mid-write must not leave one behind.
func WriteConfig(path string, raw []byte) error {
	if _, err := ValidateRaw(raw); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".mcp-*.json")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
