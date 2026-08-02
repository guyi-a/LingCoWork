package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"
)

// DefaultRedirectURI is where authorization servers send the user back.
//
// It is the API server's own port with a fixed path, not an ephemeral
// listener opened for the duration of the flow. Authorization servers match
// the redirect URI exactly against what the client registered, so it has to
// be the same string every time — an ephemeral port would work once and then
// be rejected forever after.
const DefaultRedirectURI = "http://localhost:9001/mcp/oauth/callback"

// pendingTTL bounds how long an unfinished authorization stays valid. Long
// enough to read a consent screen and log in to a provider; short enough that
// an abandoned flow does not leave a usable state parameter lying around.
const pendingTTL = 10 * time.Minute

// ErrNeedsAuthorization means the server is reachable but will not talk to us
// until the user completes the browser flow. Distinct from a connection
// failure because the fix is a button, not a config edit.
var ErrNeedsAuthorization = errors.New("mcp server requires authorization")

// pendingAuth is one authorization in flight, waiting for the browser.
//
// The handler is kept rather than rebuilt at callback time because it holds
// three things that cannot be recovered: the expected state, the cached
// server metadata, and any client id obtained by dynamic registration during
// this flow. The verifier is kept because mcp-go deliberately does not store
// it — PKCE only proves anything if the same party holds both halves.
type pendingAuth struct {
	server    string
	verifier  string
	handler   *transport.OAuthHandler
	startedAt time.Time
}

// Authorizer owns everything OAuth: credentials, token stores, and the
// half-finished browser flows.
type Authorizer struct {
	creds    CredentialStore
	scrubber *Scrubber
	redirect string

	mu sync.Mutex
	// stores is one tokenStore per server. Cached because the store holds a
	// read-through cache of the token, and because a re-authorization has to
	// be able to invalidate the one the live transport is reading.
	stores map[string]*tokenStore
	// pending is keyed by the state parameter, which is what comes back on
	// the callback and is unguessable by design.
	pending map[string]*pendingAuth

	// onAuthorized is set by the Manager. Called after a successful token
	// exchange so the server reconnects without the user asking twice.
	onAuthorized func(server string)
}

// NewAuthorizer builds the OAuth layer. The scrubber starts empty and is
// replaced by the Manager with the one that knows the config's static
// secrets; tokens discovered later are added to whichever is current.
func NewAuthorizer(creds CredentialStore, redirect string) *Authorizer {
	if strings.TrimSpace(redirect) == "" {
		redirect = DefaultRedirectURI
	}
	if creds == nil {
		creds = NewMemoryCredentialStore()
	}
	return &Authorizer{
		creds:    creds,
		scrubber: &Scrubber{},
		redirect: redirect,
		stores:   make(map[string]*tokenStore),
		pending:  make(map[string]*pendingAuth),
	}
}

// tokenStoreFor returns the one store for a server, creating it on demand.
func (a *Authorizer) tokenStoreFor(server string) *tokenStore {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.stores[server]; ok {
		return s
	}
	s := newTokenStore(server, a.creds, a.scrubber)
	a.stores[server] = s
	return s
}

// forgetToken drops the cached token for a server so the next request reads
// the database again.
func (a *Authorizer) forgetToken(server string) {
	a.mu.Lock()
	s := a.stores[server]
	a.mu.Unlock()
	if s != nil {
		s.forget()
	}
}

// redirectFor resolves the callback URI for one server.
func (a *Authorizer) redirectFor(srv ServerConfig) string {
	if u := strings.TrimSpace(srv.OAuthOrEmpty().RedirectURI); u != "" {
		return u
	}
	return a.redirect
}

// configFor assembles the OAuth config the transport and the handler share.
//
// Client identity comes from the config file when the user registered a
// client by hand, and otherwise from whatever dynamic registration stored
// earlier. The hand-written one wins: it is the more deliberate statement,
// and a stale dynamic registration should not shadow it.
func (a *Authorizer) configFor(ctx context.Context, srv ServerConfig) (transport.OAuthConfig, error) {
	oc := srv.OAuthOrEmpty()

	storedID, storedSecret, _, err := a.creds.Load(ctx, srv.Name)
	if err != nil {
		return transport.OAuthConfig{}, fmt.Errorf("load credentials for %q: %w", srv.Name, err)
	}
	clientID := oc.ClientID
	if clientID == "" {
		clientID = storedID
	}
	clientSecret := oc.ClientSecret
	if clientSecret == "" {
		clientSecret = storedSecret
	}
	a.scrubber.Add(clientSecret)

	return transport.OAuthConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURI:  a.redirectFor(srv),
		Scopes:       oc.Scopes,
		TokenStore:   a.tokenStoreFor(srv.Name),
		// Always on. MCP's own security guidance requires PKCE for these
		// clients, and a server that does not understand the parameters
		// ignores them.
		PKCEEnabled: true,
	}, nil
}

// handlerFor builds a standalone handler for the authorization dance.
//
// Deliberately not the handler inside the live transport: that one is
// unreachable (the constructor builds it internally and hands back only the
// client), and more importantly it may not exist at all — a user can press
// "authorize" for a server that has never once connected. Both handlers share
// the same TokenStore, so whichever one obtains the token, the other sees it.
func (a *Authorizer) handlerFor(ctx context.Context, srv ServerConfig) (*transport.OAuthHandler, error) {
	cfg, err := a.configFor(ctx, srv)
	if err != nil {
		return nil, err
	}
	h := transport.NewOAuthHandler(cfg)
	// Without this the handler falls back to guessing discovery endpoints
	// from the redirect URI, which points at us, not at the server.
	h.SetBaseURL(srv.URL)
	return h, nil
}

// BuildAuthURL starts an authorization and returns the URL to open.
//
// The caller must send the user to this URL in a real browser: the flow
// depends on cookies and possibly a passkey prompt at the provider, neither
// of which work in an embedded view.
func (a *Authorizer) BuildAuthURL(ctx context.Context, srv ServerConfig) (string, error) {
	// Not gated on the config declaring oauth. A server is far more often
	// discovered to want it than declared to — see probe.go — and refusing
	// here would leave the connectors page showing a button that always
	// failed. A stdio server is still refused: it is a child process, with no
	// request to carry a bearer token and no browser flow that would mean
	// anything.
	if srv.Transport() != TransportHTTP {
		return "", fmt.Errorf("server %q is not reachable over http; oauth does not apply", srv.Name)
	}
	srv = a.withDiscoveredScopes(ctx, srv)
	h, err := a.handlerFor(ctx, srv)
	if err != nil {
		return "", err
	}

	verifier, err := transport.GenerateCodeVerifier()
	if err != nil {
		return "", fmt.Errorf("generate pkce verifier: %w", err)
	}
	state, err := transport.GenerateState()
	if err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}

	if h.GetClientID() == "" {
		if err := a.register(ctx, srv, h); err != nil {
			return "", err
		}
	}

	authURL, err := h.GetAuthorizationURL(ctx, state, transport.GenerateCodeChallenge(verifier))
	if err != nil {
		return "", fmt.Errorf("build authorization url: %w", err)
	}

	a.mu.Lock()
	a.sweepLocked()
	a.pending[state] = &pendingAuth{
		server:    srv.Name,
		verifier:  verifier,
		handler:   h,
		startedAt: time.Now(),
	}
	a.mu.Unlock()

	return authURL, nil
}

// withDiscoveredScopes fills in the scopes to ask for when the config names
// none.
//
// Which scopes exist is the server's to say, and it says so in the same
// RFC 9728 document the probe already reads. Asking for none leaves the
// authorization server to pick a default, which for a server that publishes
// scopes tends to mean a token that authorizes nothing — the flow then
// appears to succeed and every subsequent call still fails.
//
// Best effort: a probe that comes back empty leaves the config untouched and
// the request goes out without a scope parameter, which is what happened
// before this existed.
func (a *Authorizer) withDiscoveredScopes(ctx context.Context, srv ServerConfig) ServerConfig {
	if len(srv.OAuthOrEmpty().Scopes) > 0 {
		return srv
	}
	meta, err := discoverProtectedResource(ctx, srv.URL, "")
	if err != nil || len(meta.ScopesSupported) == 0 {
		return srv
	}
	oc := srv.OAuthOrEmpty()
	oc.Scopes = meta.ScopesSupported
	srv.OAuth = &oc
	return srv
}

// register performs dynamic client registration and persists the result.
//
// Persisting is the whole point. RegisterClient only updates the handler's
// own copy of the config, so without this every restart would register a new
// client with the provider, leaving a trail of orphaned registrations and
// asking the user to consent again each time.
func (a *Authorizer) register(ctx context.Context, srv ServerConfig, h *transport.OAuthHandler) error {
	meta, err := h.GetServerMetadata(ctx)
	if err != nil {
		// Scrubbed and flattened to a string rather than wrapped: discovery
		// errors from the transport can carry the request, headers included.
		return fmt.Errorf("discover %q authorization server: %s", srv.Name, a.scrubber.CleanError(err))
	}
	if meta.RegistrationEndpoint == "" {
		return fmt.Errorf(
			"server %q has no client id and does not support dynamic registration; "+
				"register a client manually and set oauth.clientId (redirect uri: %s)",
			srv.Name, a.redirectFor(srv))
	}
	if err := h.RegisterClient(ctx, clientName); err != nil {
		return fmt.Errorf("register client with %q: %s", srv.Name, a.scrubber.CleanError(err))
	}
	id, secret := h.GetClientID(), h.GetClientSecret()
	a.scrubber.Add(secret)
	if err := a.creds.SaveClient(ctx, srv.Name, id, secret); err != nil {
		return fmt.Errorf("persist client registration for %q: %w", srv.Name, err)
	}
	log.Printf("mcp: registered oauth client for %q", srv.Name)
	return nil
}

// CompleteAuth finishes the flow with what came back on the callback.
func (a *Authorizer) CompleteAuth(ctx context.Context, code, state string) (server string, err error) {
	a.mu.Lock()
	a.sweepLocked()
	p := a.pending[state]
	delete(a.pending, state)
	a.mu.Unlock()

	if p == nil {
		// Either a forged callback, a stale one from an abandoned flow, or a
		// reload of the callback page after it already succeeded. All three
		// get the same answer; distinguishing them would only help an
		// attacker probe.
		return "", errors.New("no authorization is pending for this request")
	}
	if strings.TrimSpace(code) == "" {
		return p.server, errors.New("authorization server returned no code")
	}

	if err := p.handler.ProcessAuthorizationResponse(ctx, code, state, p.verifier); err != nil {
		return p.server, fmt.Errorf("exchange authorization code: %s", a.scrubber.CleanError(err))
	}

	// The token landed in the shared store, but a live transport may still be
	// holding the old one in memory.
	a.forgetToken(p.server)

	if a.onAuthorized != nil {
		a.onAuthorized(p.server)
	}
	return p.server, nil
}

// Refresh forces a token refresh for a server, for the case where the server
// rejected a token we still believed in. Reports whether a new token was
// obtained.
func (a *Authorizer) Refresh(ctx context.Context, srv ServerConfig) bool {
	store := a.tokenStoreFor(srv.Name)
	store.forget()
	tok, err := store.GetToken(ctx)
	if err != nil || tok == nil || tok.RefreshToken == "" {
		return false
	}
	h, err := a.handlerFor(ctx, srv)
	if err != nil {
		return false
	}
	newToken, err := h.RefreshToken(ctx, tok.RefreshToken)
	if err != nil || newToken == nil {
		return false
	}
	// RefreshToken does not write to the store, so this is what persists it.
	if err := store.SaveToken(ctx, newToken); err != nil {
		log.Printf("mcp: refreshed token for %q but could not save it: %v", srv.Name, err)
	}
	return true
}

// Revoke forgets a server's credentials locally. The authorization server is
// not told, because the OAuth revocation endpoint is optional and a failure
// there must not stop us dropping our copy.
func (a *Authorizer) Revoke(ctx context.Context, server string) error {
	a.forgetToken(server)
	a.mu.Lock()
	for state, p := range a.pending {
		if p.server == server {
			delete(a.pending, state)
		}
	}
	a.mu.Unlock()
	return a.creds.Delete(ctx, server)
}

// HasToken reports whether a server has a stored token at all, without
// checking whether it still works.
func (a *Authorizer) HasToken(ctx context.Context, server string) bool {
	tok, err := a.tokenStoreFor(server).GetToken(ctx)
	return err == nil && tok != nil && tok.AccessToken != ""
}

// sweepLocked drops expired pending flows. Called on both ends of the flow
// rather than from a ticker: the map only grows when someone starts an
// authorization, so that is the only time it needs pruning.
func (a *Authorizer) sweepLocked() {
	cutoff := time.Now().Add(-pendingTTL)
	for state, p := range a.pending {
		if p.startedAt.Before(cutoff) {
			delete(a.pending, state)
		}
	}
}
