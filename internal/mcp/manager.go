package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/guyi-a/Interview-Agent/internal/effect"
)

// State is where a configured server currently stands.
type State string

const (
	StateConnecting State = "connecting"
	StateConnected  State = "connected"
	StateDisabled   State = "disabled"
	StateNeedsAuth  State = "needs_auth"
	StateError      State = "error"
)

// ServerStatus is one server as reported to the settings page.
//
// It deliberately has no Headers, Env, or token field. Those hold
// credentials, and this struct is serialised straight to a browser over an
// unauthenticated local port; the only way for a secret not to leak here is
// for there to be nowhere to put it.
type ServerStatus struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Target    string   `json:"target"`
	State     State    `json:"state"`
	ToolCount int      `json:"tool_count"`
	Tools     []string `json:"tools,omitempty"`
	Error     string   `json:"error,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
	// Trusted mirrors trustAnnotations so the page can explain why a server's
	// tools do or do not prompt.
	Trusted     bool     `json:"trusted"`
	AutoApprove []string `json:"auto_approve,omitempty"`
	// OAuth and Authorized drive the authorize button. Authorized means a
	// token is on file, which is not the same as it still working.
	OAuth      bool `json:"oauth"`
	Authorized bool `json:"authorized"`
}

// binding is one remote tool as the manager knows it. This is the single
// source of truth for the public-name mapping: the effect deriver, the status
// listing, and anything debugging a call all read it rather than keeping
// their own copy.
type binding struct {
	publicName string
	server     string
	remoteName string
	effect     effect.Effect
	tool       *remoteTool
}

type serverState struct {
	cfg   ServerConfig
	conn  *connection
	state State
	err   string
	// slug is kept so a disconnect can hand it back to the allocator.
	slug     string
	bindings []*binding
	// gen is bumped by every teardown. A dial that started before the
	// teardown finds its generation stale and throws away its connection
	// instead of installing it over the newer one — the case being an
	// authorize callback and a config save racing on the same server.
	gen uint64
}

func (s *serverState) toolNames() []string {
	out := make([]string, 0, len(s.bindings))
	for _, b := range s.bindings {
		out = append(out, b.publicName)
	}
	return out
}

// Manager owns every MCP connection for the process.
//
// Servers are connected, disconnected and reconnected at runtime rather than
// being fixed at boot. That is not a luxury: OAuth authorization is
// interactive, so a server needing it cannot possibly be up when the process
// starts — the user has not clicked anything yet. Once that machinery exists,
// applying a config edit without a restart is the same code path.
//
// What makes this safe on the agent side is that the supervisor reads its
// MCP tools through a per-run middleware, so a tool appearing or vanishing
// between turns needs no agent rebuild. See runtimectx.DynamicToolsMiddleware.
type Manager struct {
	mu       sync.RWMutex
	cfg      *Config
	scrubber *Scrubber
	auth     *Authorizer
	effects  *effect.Registry
	alloc    *allocator
	servers  map[string]*serverState
	// order preserves the config's sorted server order for status listings
	// and for deterministic name allocation.
	order []string
}

// New builds a manager over an already-loaded config.
//
// reservedToolNames are names a remote tool may not take — the builtins. They
// are passed in rather than imported so this package stays independent of the
// tool package (which drags in a browser runtime).
func New(cfg *Config, reservedToolNames []string, auth *Authorizer) *Manager {
	if cfg == nil {
		cfg = &Config{}
	}
	// One scrubber shared with the authorizer rather than one each. The
	// authorizer learns tokens the manager never sees, the manager learns
	// config secrets the authorizer never sees, and an error message can
	// carry either.
	scrubber := NewScrubber()
	if auth != nil {
		scrubber = auth.scrubber
	}
	scrubber.AddServers(cfg.Servers...)

	m := &Manager{
		cfg:      cfg,
		scrubber: scrubber,
		auth:     auth,
		alloc:    newAllocator(reservedToolNames),
		servers:  make(map[string]*serverState),
	}
	if auth != nil {
		// Finishing the browser flow has to bring the server up by itself.
		// Asking the user to press a second button after authorizing would be
		// a strange thing to make them do.
		auth.onAuthorized = func(server string) {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), reconnectTimeout)
				defer cancel()
				if err := m.Reconnect(ctx, server); err != nil {
					log.Printf("mcp: reconnect after authorizing %q: %v", server, err)
				}
			}()
		}
	}
	return m
}

// reconnectTimeout bounds a background reconnect. Generous because the first
// run of an `npx -y` server downloads a package.
const reconnectTimeout = 120 * time.Second

// SetEffectRegistry wires the registry that remote tools register into.
// Must be called before Connect, or the first batch of tools would come up
// with no effect and prompt on every call.
func (m *Manager) SetEffectRegistry(reg *effect.Registry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.effects = reg
}

// Connect brings up every enabled server in the background.
//
// It does not block and it never fails. Startup used to wait for every
// handshake, which meant one server fetching a package on first run held up
// the HTTP listener for the whole timeout. Since tools are read per agent
// run, a server that finishes connecting ten seconds in is simply available
// from the next turn onward.
func (m *Manager) Connect(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	for _, issue := range m.cfg.Issues {
		log.Printf("mcp: ignoring server %q: %s", issue.Server, issue.Message)
	}
	m.order = m.order[:0]
	for _, srv := range m.cfg.Servers {
		m.order = append(m.order, srv.Name)
		m.servers[srv.Name] = &serverState{cfg: srv, state: initialState(srv)}
	}
	names := append([]string(nil), m.order...)
	m.mu.Unlock()

	for _, name := range names {
		go m.bringUp(ctx, name)
	}
}

func initialState(srv ServerConfig) State {
	if !srv.IsEnabled() {
		return StateDisabled
	}
	return StateConnecting
}

// bringUp dials one server and publishes its tools.
//
// Everything slow — the handshake and the tool listing — happens outside the
// lock. Holding it across a dial would block Tools(), which the supervisor
// calls at the start of every single agent run, so one unreachable server
// would stall every conversation for the length of its timeout.
func (m *Manager) bringUp(ctx context.Context, name string) {
	m.mu.RLock()
	st := m.servers[name]
	var (
		cfg ServerConfig
		gen uint64
	)
	if st != nil {
		cfg, gen = st.cfg, st.gen
	}
	m.mu.RUnlock()
	if st == nil || !cfg.IsEnabled() {
		return
	}

	conn, err := connect(ctx, cfg, m.auth)
	if errors.Is(err, ErrNeedsAuthorization) &&
		m.auth != nil &&
		useOAuthClient(ctx, cfg, m.auth) &&
		m.auth.Refresh(ctx, cfg) {
		// The transport normally refreshes a token whose local expiry has
		// elapsed. Retry explicitly as well: providers can reject early, and
		// OAuth discovered at runtime has no config flag to drive recovery.
		conn, err = connect(ctx, cfg, m.auth)
	}
	if err != nil {
		m.fail(name, gen, err)
		return
	}

	listCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.InitTimeout())*time.Second)
	remote, err := listTools(listCtx, conn.cli)
	cancel()
	if err != nil {
		conn.close()
		m.fail(name, gen, fmt.Errorf("tool discovery failed: %w", err))
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	st = m.servers[name]
	if st == nil || st.gen != gen {
		// Torn down or reconfigured while we were dialling. The newer attempt
		// owns this server now.
		conn.close()
		return
	}
	st.conn = conn
	st.state = StateConnected
	st.err = ""
	m.installLocked(st, remote)
}

// fail records why a server did not come up, unless a newer attempt has since
// taken over.
func (m *Manager) fail(name string, gen uint64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.servers[name]
	if st == nil || st.gen != gen {
		return
	}
	if errors.Is(err, ErrNeedsAuthorization) {
		st.state = StateNeedsAuth
		st.err = ""
		log.Printf("mcp: server %q needs authorization", name)
		return
	}
	st.state = StateError
	st.err = m.scrubber.CleanError(err)
	log.Printf("mcp: server %q unavailable: %s", name, st.err)
}

// installLocked names a server's tools and registers their effects. Pure
// CPU; caller holds the write lock.
func (m *Manager) installLocked(st *serverState, remote []mcpgo.Tool) {
	st.slug = m.alloc.slugFor(st.cfg.Name)
	st.bindings = nil
	recovery := m.recoveryFor(st.cfg)
	for _, rt := range remote {
		publicName := m.alloc.nameFor(st.slug, rt.Name)
		t, err := newRemoteTool(st.conn.cli, st.cfg.Name, publicName, rt, recovery)
		if err != nil {
			// One unusable schema should not cost the server its other
			// tools. Log and carry on; the tool simply never appears.
			log.Printf("mcp: server %q: skipping tool %q: %v", st.cfg.Name, rt.Name, err)
			continue
		}
		b := &binding{
			publicName: publicName,
			server:     st.cfg.Name,
			remoteName: rt.Name,
			effect:     toolEffect(st.cfg, rt),
			tool:       t,
		}
		st.bindings = append(st.bindings, b)
		if m.effects != nil {
			m.effects.Register(publicName, effect.Static(b.effect))
		}
	}
	log.Printf("mcp: server %q connected with %d tool(s)", st.cfg.Name, len(st.bindings))
}

// recoveryFor builds what a live tool needs to survive a token expiry.
//
// OAuth is not always declared in mcp.json: a server can advertise it during
// the initial 401 probe, after which the persisted token is what tells future
// connections to use OAuth. Match useOAuthClient here so those discovered
// servers get the same refresh-and-retry path as explicitly configured ones.
func (m *Manager) recoveryFor(cfg ServerConfig) *authRecovery {
	if m.auth == nil || !useOAuthClient(context.Background(), cfg, m.auth) {
		return nil
	}
	auth := m.auth
	name := cfg.Name
	return &authRecovery{
		refresh:       func(ctx context.Context) bool { return auth.Refresh(ctx, cfg) },
		markNeedsAuth: func() { m.MarkNeedsAuth(name) },
	}
}

// teardownLocked closes a server's connection and gives back everything it
// held: its names, and its tools' effect derivations. Caller holds the write
// lock.
func (m *Manager) teardownLocked(st *serverState) {
	st.gen++
	if st.conn != nil {
		st.conn.close()
		st.conn = nil
	}
	if m.effects != nil {
		for _, b := range st.bindings {
			m.effects.Unregister(b.publicName)
		}
	}
	m.alloc.release(st.slug, st.toolNames())
	st.slug = ""
	st.bindings = nil
	st.err = ""
}

// Tools returns every discovered remote tool. Called on every agent run by
// the supervisor's tool middleware, so it must stay cheap and must never
// block on I/O.
func (m *Manager) Tools() []tool.BaseTool {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []tool.BaseTool
	for _, name := range m.order {
		st := m.servers[name]
		if st == nil {
			continue
		}
		for _, b := range st.bindings {
			out = append(out, b.tool)
		}
	}
	return out
}

// ToolProvider adapts Tools to the signature the agent takes.
func (m *Manager) ToolProvider() func(context.Context) []tool.BaseTool {
	return func(context.Context) []tool.BaseTool { return m.Tools() }
}

// Reconnect tears one server down and brings it back up from the current
// on-disk config. Used after an authorization completes in the browser.
func (m *Manager) Reconnect(ctx context.Context, name string) error {
	if m == nil {
		return errors.New("no mcp manager")
	}
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	srv, ok := cfg.Get(name)
	if !ok {
		return fmt.Errorf("no server named %q in %s", name, cfg.Path)
	}

	m.scrubber.AddServers(cfg.Servers...)

	m.mu.Lock()
	m.cfg = cfg
	st := m.servers[name]
	if st == nil {
		st = &serverState{}
		m.servers[name] = st
		m.order = append(m.order, name)
	}
	m.teardownLocked(st)
	st.cfg = srv
	st.state = initialState(srv)
	m.mu.Unlock()

	if !srv.IsEnabled() {
		return nil
	}
	m.bringUp(ctx, name)
	return nil
}

// Apply reconciles the running set against a freshly saved config.
//
// It diffs rather than restarting everything, because a stdio server is a
// child process: bouncing an untouched one on every save would drop its
// warm state and, for a server that installs itself on first run, cost
// seconds of downtime for an edit that had nothing to do with it.
func (m *Manager) Apply(ctx context.Context, cfg *Config) {
	if m == nil || cfg == nil {
		return
	}
	m.scrubber.AddServers(cfg.Servers...)

	m.mu.Lock()
	m.cfg = cfg

	next := make(map[string]ServerConfig, len(cfg.Servers))
	order := make([]string, 0, len(cfg.Servers))
	for _, srv := range cfg.Servers {
		next[srv.Name] = srv
		order = append(order, srv.Name)
	}

	// Gone from the config entirely.
	for name, st := range m.servers {
		if _, still := next[name]; !still {
			m.teardownLocked(st)
			delete(m.servers, name)
			log.Printf("mcp: server %q removed", name)
		}
	}

	var toStart []string
	for _, name := range order {
		srv := next[name]
		st := m.servers[name]

		switch {
		case st == nil:
			m.servers[name] = &serverState{cfg: srv, state: initialState(srv)}
			if srv.IsEnabled() {
				toStart = append(toStart, name)
			}

		case sameServer(st.cfg, srv) && st.state != StateError:
			// Untouched and not broken: leave the connection alone. A server
			// in error is retried, since a save is a reasonable moment to
			// hope the user fixed whatever it was.
			st.cfg = srv

		default:
			m.teardownLocked(st)
			st.cfg = srv
			st.state = initialState(srv)
			if srv.IsEnabled() {
				toStart = append(toStart, name)
			}
		}
	}
	m.order = order
	m.mu.Unlock()

	for _, name := range toStart {
		go m.bringUp(ctx, name)
	}
}

// sameServer reports whether two config entries describe the same connection.
// Compared by hashing the marshalled entry so a field added later is included
// without anyone having to remember to update this.
func sameServer(a, b ServerConfig) bool {
	return configHash(a) == configHash(b)
}

func configHash(s ServerConfig) string {
	raw, err := json.Marshal(s)
	if err != nil {
		// Unhashable means "assume changed", which costs a reconnect rather
		// than silently keeping a stale connection.
		return "unhashable:" + s.Name
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// MarkNeedsAuth records that a live server started rejecting our token. Called
// from the tool adapter, which is where an expiry mid-session shows up.
func (m *Manager) MarkNeedsAuth(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.servers[name]; st != nil {
		st.state = StateNeedsAuth
		st.err = ""
	}
}

// ServerConfigFor returns a server's current config.
func (m *Manager) ServerConfigFor(name string) (ServerConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := m.servers[name]
	if st == nil {
		return ServerConfig{}, false
	}
	return st.cfg, true
}

// Authorizer exposes the OAuth machinery to the HTTP layer.
func (m *Manager) Authorizer() *Authorizer {
	if m == nil {
		return nil
	}
	return m.auth
}

// Status describes every configured server, including the ones that failed.
func (m *Manager) Status(ctx context.Context) []ServerStatus {
	if m == nil {
		return []ServerStatus{}
	}
	m.mu.RLock()
	out := make([]ServerStatus, 0, len(m.servers))
	for _, name := range m.order {
		st := m.servers[name]
		if st == nil {
			continue
		}
		s := ServerStatus{
			Name:        st.cfg.Name,
			Transport:   string(st.cfg.Transport()),
			Target:      st.cfg.Target(),
			State:       st.state,
			ToolCount:   len(st.bindings),
			Error:       st.err,
			Trusted:     st.cfg.TrustAnnotations,
			AutoApprove: st.cfg.AutoApprove,
			OAuth:       st.cfg.UsesOAuth(),
			Tools:       st.toolNames(),
		}
		if st.conn != nil {
			s.Stderr = strings.TrimSpace(m.scrubber.Clean(st.conn.stderrTail()))
		}
		out = append(out, s)
	}
	m.mu.RUnlock()

	// Outside the lock: reading a token may hit the database.
	//
	// A server the probe found to want OAuth never says so in the config, so
	// OAuth here cannot come from UsesOAuth alone — holding a token is the
	// other way to be an OAuth server, and it is what makes the page offer
	// "revoke" rather than pretending the authorization never happened.
	if m.auth != nil {
		for i := range out {
			if out[i].Transport != string(TransportHTTP) {
				continue
			}
			out[i].Authorized = m.auth.HasToken(ctx, out[i].Name)
			out[i].OAuth = out[i].OAuth || out[i].Authorized
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Issues surfaces the config entries that could not be loaded.
func (m *Manager) Issues() []Issue {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg == nil {
		return nil
	}
	return m.cfg.Issues
}

// ConfigPath is where the config was read from.
func (m *Manager) ConfigPath() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg == nil {
		return ""
	}
	return m.cfg.Path
}

// TestResult is the outcome of a connection test.
type TestResult struct {
	OK        bool     `json:"ok"`
	NeedsAuth bool     `json:"needs_auth,omitempty"`
	ToolCount int      `json:"tool_count"`
	Tools     []string `json:"tools,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// TestConnection dials a server fresh and lists its tools.
//
// It reads the config off disk and opens its own connection rather than
// reusing the live one, so an edit can be checked before it is applied.
func (m *Manager) TestConnection(ctx context.Context, name string) (TestResult, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return TestResult{}, err
	}
	srv, ok := cfg.Get(name)
	if !ok {
		return TestResult{}, fmt.Errorf("no server named %q in %s", name, cfg.Path)
	}
	scrubber := NewScrubber(srv)

	conn, err := connect(ctx, srv, m.auth)
	if errors.Is(err, ErrNeedsAuthorization) {
		return TestResult{OK: false, NeedsAuth: true, Error: "需要授权"}, nil
	}
	if err != nil {
		return TestResult{OK: false, Error: scrubber.CleanError(err)}, nil
	}
	defer conn.close()

	remote, err := listTools(ctx, conn.cli)
	if err != nil {
		return TestResult{OK: false, Error: scrubber.CleanError(err)}, nil
	}
	res := TestResult{OK: true, ToolCount: len(remote)}
	for _, rt := range remote {
		res.Tools = append(res.Tools, rt.Name)
	}
	return res, nil
}

// Close shuts every connection down, which for a stdio server also reaps its
// child process.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, st := range m.servers {
		if st.conn != nil {
			st.conn.close()
			st.conn = nil
		}
	}
}
