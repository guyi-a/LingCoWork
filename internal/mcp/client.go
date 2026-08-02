package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	clientName    = "lingcowork"
	clientVersion = "0.1.0"

	// stderrTailBytes bounds what we keep of a stdio server's stderr. A
	// server that will not start is the most common MCP failure and the
	// reason only ever appears here, but a chatty server logging every
	// request must not grow this without limit.
	stderrTailBytes = 8 << 10
)

// connection is one live server: the client plus what we need to describe it
// afterwards.
type connection struct {
	cli    *client.Client
	stderr *tailBuffer
}

// connect brings up one server and completes the MCP handshake.
//
// The two transports differ in a way that is easy to get wrong: the stdio
// constructor starts its transport internally, while the HTTP one only wraps
// it, so Start has to be called explicitly there. Initialize must follow Start
// in both cases. Everything is bounded by one deadline covering spawn,
// connect, and handshake together — a server that hangs half way through is
// as broken as one that refuses outright.
func connect(ctx context.Context, srv ServerConfig, auth *Authorizer) (*connection, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(srv.InitTimeout())*time.Second)
	defer cancel()

	conn, err := dial(ctx, srv, auth)
	if err != nil {
		return nil, classifyFailure(ctx, srv, err)
	}

	// Streamable HTTP defers everything to the first request, so for an
	// OAuth server this is where a missing or dead token surfaces, not in
	// Start above.
	if _, err := conn.cli.Initialize(ctx, initRequest()); err != nil {
		conn.close()
		if client.IsOAuthAuthorizationRequiredError(err) {
			return nil, ErrNeedsAuthorization
		}
		return nil, classifyFailure(ctx, srv, fmt.Errorf("handshake failed: %w%s", err, conn.stderrSuffix()))
	}
	return conn, nil
}

// classifyFailure asks the server why it turned us away, when the answer
// changes what the user should do about it.
//
// Only for an HTTP server that has not already been told how to authenticate.
// One that carries an OAuth config or a static Authorization header has an
// answer configured, so its failure is about that answer being wrong, not
// about which kind of answer it needs.
//
// A probe that finds OAuth converts the error into ErrNeedsAuthorization,
// which is the state that puts an "authorize" button on the connectors page.
// Before this, that state was reachable only for servers whose config already
// said `"auth": "oauth"` — so the one case where the button was most needed,
// a server the user had just added without knowing it wanted OAuth, was
// exactly the case that never got one.
func classifyFailure(ctx context.Context, srv ServerConfig, err error) error {
	if srv.Transport() != TransportHTTP || srv.UsesOAuth() || hasAuthorizationHeader(srv) {
		return err
	}
	switch probeAuth(ctx, srv.URL) {
	case authOAuth:
		return ErrNeedsAuthorization
	case authToken:
		return fmt.Errorf(
			"%w; this server wants a credential but does not offer OAuth — "+
				`add it under "headers" for this server in mcp.json`, err)
	default:
		// authNone or authUnknown: nothing to add, and guessing would bury
		// the real reason under a wrong one.
		return err
	}
}

// hasAuthorizationHeader reports whether the config already carries a bearer
// token by hand, which is the user opting this server out of OAuth.
func hasAuthorizationHeader(srv ServerConfig) bool {
	for name := range srv.Headers {
		if strings.EqualFold(name, "Authorization") {
			return true
		}
	}
	return false
}

func initRequest() mcp.InitializeRequest {
	var req mcp.InitializeRequest
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: clientName, Version: clientVersion}
	return req
}

func dial(ctx context.Context, srv ServerConfig, auth *Authorizer) (*connection, error) {
	if srv.Transport() == TransportStdio {
		return dialStdio(ctx, srv)
	}
	return dialHTTP(ctx, srv, auth)
}

func dialStdio(ctx context.Context, srv ServerConfig) (*connection, error) {
	cli, err := client.NewStdioMCPClientWithOptions(
		srv.Command,
		envPairs(srv.Env),
		srv.Args,
		transport.WithCommandFunc(commandFunc(srv)),
	)
	if err != nil {
		return nil, fmt.Errorf("start %q: %w", srv.Command, err)
	}
	conn := &connection{cli: cli}
	if r, ok := client.GetStderr(cli); ok {
		conn.stderr = newTailBuffer(stderrTailBytes)
		go func() { _, _ = io.Copy(conn.stderr, r) }()
	}
	// The constructor already started the transport; Start is documented as
	// idempotent, so calling it keeps the two paths symmetric rather than
	// relying on that constructor detail staying true.
	if err := cli.Start(ctx); err != nil {
		conn.close()
		return nil, fmt.Errorf("start %q: %w%s", srv.Command, err, conn.stderrSuffix())
	}
	return conn, nil
}

// commandFunc builds the child process. mcp-go's default gives no way to set
// a working directory, and it replaces the environment wholesale rather than
// extending it — a server launched with only the config's env would lose PATH
// and never find its interpreter.
func commandFunc(srv ServerConfig) transport.CommandFunc {
	return func(ctx context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, command, args...)
		cmd.Env = append(os.Environ(), env...)
		cmd.Dir = srv.Cwd
		return cmd, nil
	}
}

func envPairs(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// useOAuthClient decides whether to attach the OAuth transport.
//
// The config saying `"auth": "oauth"` is one way in. The other is having a
// stored token, which is what a server discovered by probe looks like after
// the user authorizes it: nothing was written back to mcp.json, so the entry
// still declares nothing, and reading only the config would dial without the
// token and collect the same 401 the authorization was meant to clear.
//
// A hand-written Authorization header wins over both. It is an explicit
// choice, and layering a bearer token over it would silently override what
// the user configured.
func useOAuthClient(ctx context.Context, srv ServerConfig, auth *Authorizer) bool {
	if srv.UsesOAuth() {
		return true
	}
	if auth == nil || hasAuthorizationHeader(srv) {
		return false
	}
	return auth.HasToken(ctx, srv.Name)
}

// dialHTTP prefers streamable HTTP and falls back to the older HTTP+SSE
// transport. The two are not distinguishable from the URL, and servers in the
// wild still speak either, so the only way to tell is to try.
func dialHTTP(ctx context.Context, srv ServerConfig, auth *Authorizer) (*connection, error) {
	var oauthCfg *transport.OAuthConfig
	if useOAuthClient(ctx, srv, auth) {
		if auth == nil {
			return nil, fmt.Errorf(`server %q is configured for oauth but no authorizer is wired`, srv.Name)
		}
		cfg, err := auth.configFor(ctx, srv)
		if err != nil {
			return nil, err
		}
		oauthCfg = &cfg
	}

	cli, httpErr := startHTTP(ctx, srv, oauthCfg)
	if httpErr == nil {
		return &connection{cli: cli}, nil
	}
	// SSE authorizes at connection time, so an OAuth server can fail here
	// rather than at Initialize. Surface that instead of burying it in a
	// generic "both transports failed".
	if client.IsOAuthAuthorizationRequiredError(httpErr) {
		return nil, ErrNeedsAuthorization
	}

	cli, sseErr := startSSE(ctx, srv, oauthCfg)
	if sseErr == nil {
		return &connection{cli: cli}, nil
	}
	if client.IsOAuthAuthorizationRequiredError(sseErr) {
		return nil, ErrNeedsAuthorization
	}
	// Report the streamable-HTTP failure: it is the transport a current
	// server is expected to speak, so its error is the more useful one.
	return nil, fmt.Errorf("connect %s: %w (sse fallback also failed: %v)", srv.URL, httpErr, sseErr)
}

func startHTTP(ctx context.Context, srv ServerConfig, oauthCfg *transport.OAuthConfig) (*client.Client, error) {
	opts := []transport.StreamableHTTPCOption{
		transport.WithHTTPTimeout(time.Duration(srv.InitTimeout()) * time.Second),
	}
	if len(srv.Headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(srv.Headers))
	}

	var (
		cli *client.Client
		err error
	)
	if oauthCfg != nil {
		cli, err = client.NewOAuthStreamableHttpClient(srv.URL, *oauthCfg, opts...)
	} else {
		cli, err = client.NewStreamableHttpClient(srv.URL, opts...)
	}
	if err != nil {
		return nil, err
	}
	if err := cli.Start(ctx); err != nil {
		_ = cli.Close()
		return nil, err
	}
	return cli, nil
}

func startSSE(ctx context.Context, srv ServerConfig, oauthCfg *transport.OAuthConfig) (*client.Client, error) {
	var opts []transport.ClientOption
	if len(srv.Headers) > 0 {
		opts = append(opts, transport.WithHeaders(srv.Headers))
	}

	var (
		cli *client.Client
		err error
	)
	if oauthCfg != nil {
		cli, err = client.NewOAuthSSEClient(srv.URL, *oauthCfg, opts...)
	} else {
		cli, err = client.NewSSEMCPClient(srv.URL, opts...)
	}
	if err != nil {
		return nil, err
	}
	if err := cli.Start(ctx); err != nil {
		_ = cli.Close()
		return nil, err
	}
	return cli, nil
}

func (c *connection) close() {
	if c == nil || c.cli == nil {
		return
	}
	_ = c.cli.Close()
}

// stderrTail returns what the child process wrote to stderr, or "".
func (c *connection) stderrTail() string {
	if c == nil || c.stderr == nil {
		return ""
	}
	return c.stderr.String()
}

// stderrSuffix formats the stderr tail for appending to an error. A server
// that fails to start usually says why here and nowhere else.
func (c *connection) stderrSuffix() string {
	tail := strings.TrimSpace(c.stderrTail())
	if tail == "" {
		return ""
	}
	return "; stderr: " + tail
}

// tailBuffer keeps the last n bytes written to it. Safe for the copier
// goroutine to write while a status request reads.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newTailBuffer(max int) *tailBuffer {
	return &tailBuffer{max: max}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
