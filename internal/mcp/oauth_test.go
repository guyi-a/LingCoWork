package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeAuthServer is an authorization server good enough to run the flow
// end to end: discovery, dynamic registration, and the token exchange.
type fakeAuthServer struct {
	*httptest.Server

	// registrations counts how many clients it has issued. The point of
	// persisting a registration is that this stays at 1.
	registrations int
	// lastTokenForm is what the client posted to exchange the code, so the
	// test can assert PKCE actually happened.
	lastTokenForm url.Values
	// supportsRegistration lets a test play a server that requires a
	// hand-registered client.
	supportsRegistration bool
}

func newFakeAuthServer(t *testing.T) *fakeAuthServer {
	t.Helper()
	f := &fakeAuthServer{supportsRegistration: true}
	mux := http.NewServeMux()

	// Discovery. The handler asks for the protected-resource document first
	// and falls through to this one when that 404s, which is what a plain
	// authorization server looks like.
	mux.HandleFunc("/.well-known/oauth-authorization-server/", func(w http.ResponseWriter, r *http.Request) {
		meta := map[string]any{
			"issuer":                   f.URL,
			"authorization_endpoint":   f.URL + "/authorize",
			"token_endpoint":           f.URL + "/token",
			"response_types_supported": []string{"code"},
		}
		if f.supportsRegistration {
			meta["registration_endpoint"] = f.URL + "/register"
		}
		writeJSON(w, http.StatusOK, meta)
	})

	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		f.registrations++
		writeJSON(w, http.StatusCreated, map[string]any{
			"client_id": "client-issued-by-registration",
		})
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.lastTokenForm = r.PostForm
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token":  "at-issued-by-fake-server",
			"token_type":    "Bearer",
			"refresh_token": "rt-issued-by-fake-server",
			"expires_in":    3600,
		})
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func oauthServerConfig(name, url string) ServerConfig {
	return ServerConfig{Name: name, URL: url + "/mcp", Auth: "oauth"}
}

func TestAuthorizationFlowEndToEnd(t *testing.T) {
	fake := newFakeAuthServer(t)
	creds := NewMemoryCredentialStore()
	auth := NewAuthorizer(creds, DefaultRedirectURI)
	srv := oauthServerConfig("wiki", fake.URL)

	authURL, err := auth.BuildAuthURL(t.Context(), srv)
	if err != nil {
		t.Fatalf("BuildAuthURL: %v", err)
	}

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	q := parsed.Query()
	state := q.Get("state")
	if state == "" {
		t.Fatal("authorization url carries no state")
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("PKCE missing from the authorization url: %v", q)
	}
	if q.Get("redirect_uri") != DefaultRedirectURI {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}

	// The registration has to be on disk before the user even returns —
	// otherwise a restart mid-flow orphans the client.
	if id, _, _, _ := creds.Load(t.Context(), "wiki"); id != "client-issued-by-registration" {
		t.Fatalf("client id not persisted, got %q", id)
	}

	var reconnected string
	auth.onAuthorized = func(server string) { reconnected = server }

	if _, err := auth.CompleteAuth(t.Context(), "the-code", state); err != nil {
		t.Fatalf("CompleteAuth: %v", err)
	}
	if reconnected != "wiki" {
		t.Errorf("server was not reconnected after authorizing, got %q", reconnected)
	}
	if got := fake.lastTokenForm.Get("code_verifier"); got == "" {
		t.Error("token exchange sent no code_verifier; PKCE proved nothing")
	}
	if got := fake.lastTokenForm.Get("code"); got != "the-code" {
		t.Errorf("exchanged code = %q", got)
	}

	tok, err := auth.tokenStoreFor("wiki").GetToken(t.Context())
	if err != nil {
		t.Fatalf("GetToken after authorizing: %v", err)
	}
	if tok.AccessToken != "at-issued-by-fake-server" {
		t.Fatalf("stored token = %+v", tok)
	}
}

// The persisted registration is what stops a restart from creating a new
// client with the provider every time.
func TestAuthorizationReusesPersistedRegistration(t *testing.T) {
	fake := newFakeAuthServer(t)
	creds := NewMemoryCredentialStore()
	srv := oauthServerConfig("wiki", fake.URL)

	for i := 0; i < 3; i++ {
		// A fresh Authorizer each round stands in for a restart.
		if _, err := NewAuthorizer(creds, DefaultRedirectURI).BuildAuthURL(t.Context(), srv); err != nil {
			t.Fatalf("BuildAuthURL round %d: %v", i, err)
		}
	}
	if fake.registrations != 1 {
		t.Fatalf("registered %d times, want 1", fake.registrations)
	}
}

func TestCompleteAuthRejectsUnknownState(t *testing.T) {
	auth := NewAuthorizer(NewMemoryCredentialStore(), DefaultRedirectURI)
	if _, err := auth.CompleteAuth(t.Context(), "code", "never-issued"); err == nil {
		t.Fatal("a callback with a state we never issued was accepted")
	}
}

func TestCompleteAuthRejectsExpiredPending(t *testing.T) {
	fake := newFakeAuthServer(t)
	auth := NewAuthorizer(NewMemoryCredentialStore(), DefaultRedirectURI)
	srv := oauthServerConfig("wiki", fake.URL)

	authURL, err := auth.BuildAuthURL(t.Context(), srv)
	if err != nil {
		t.Fatalf("BuildAuthURL: %v", err)
	}
	state := mustQuery(t, authURL, "state")

	auth.mu.Lock()
	auth.pending[state].startedAt = time.Now().Add(-pendingTTL - time.Minute)
	auth.mu.Unlock()

	if _, err := auth.CompleteAuth(t.Context(), "code", state); err == nil {
		t.Fatal("an expired authorization was still completed")
	}
}

// Replaying a callback must not work a second time, which is what deleting
// the pending entry on use buys.
func TestCompleteAuthIsSingleUse(t *testing.T) {
	fake := newFakeAuthServer(t)
	auth := NewAuthorizer(NewMemoryCredentialStore(), DefaultRedirectURI)
	srv := oauthServerConfig("wiki", fake.URL)

	authURL, _ := auth.BuildAuthURL(t.Context(), srv)
	state := mustQuery(t, authURL, "state")

	if _, err := auth.CompleteAuth(t.Context(), "code", state); err != nil {
		t.Fatalf("first CompleteAuth: %v", err)
	}
	if _, err := auth.CompleteAuth(t.Context(), "code", state); err == nil {
		t.Fatal("the same callback was accepted twice")
	}
}

// Without dynamic registration and without a configured client id there is
// nothing to authorize with, and the error has to say what to do about it.
func TestBuildAuthURLExplainsMissingRegistrationSupport(t *testing.T) {
	fake := newFakeAuthServer(t)
	fake.supportsRegistration = false
	auth := NewAuthorizer(NewMemoryCredentialStore(), DefaultRedirectURI)

	_, err := auth.BuildAuthURL(t.Context(), oauthServerConfig("wiki", fake.URL))
	if err == nil {
		t.Fatal("BuildAuthURL succeeded with no way to obtain a client id")
	}
	if !strings.Contains(err.Error(), "clientId") || !strings.Contains(err.Error(), DefaultRedirectURI) {
		t.Fatalf("error does not say how to fix it: %v", err)
	}
}

// A hand-configured client must not be shadowed by a stale dynamic one.
func TestConfiguredClientIDWins(t *testing.T) {
	creds := NewMemoryCredentialStore()
	_ = creds.SaveClient(t.Context(), "wiki", "dynamically-registered", "")

	auth := NewAuthorizer(creds, DefaultRedirectURI)
	srv := oauthServerConfig("wiki", "https://example.invalid")
	srv.OAuth = &OAuthConfig{ClientID: "configured-by-hand"}

	cfg, err := auth.configFor(t.Context(), srv)
	if err != nil {
		t.Fatalf("configFor: %v", err)
	}
	if cfg.ClientID != "configured-by-hand" {
		t.Fatalf("ClientID = %q", cfg.ClientID)
	}
	if !cfg.PKCEEnabled {
		t.Error("PKCE is off")
	}
}

func TestRedirectURIPerServerOverride(t *testing.T) {
	auth := NewAuthorizer(NewMemoryCredentialStore(), DefaultRedirectURI)
	srv := oauthServerConfig("wiki", "https://example.invalid")
	srv.OAuth = &OAuthConfig{RedirectURI: "http://127.0.0.1:9001/mcp/oauth/callback"}

	if got := auth.redirectFor(srv); got != srv.OAuth.RedirectURI {
		t.Fatalf("redirectFor = %q, want the per-server override", got)
	}
}

func TestRevokeForgetsEverything(t *testing.T) {
	fake := newFakeAuthServer(t)
	creds := NewMemoryCredentialStore()
	auth := NewAuthorizer(creds, DefaultRedirectURI)
	srv := oauthServerConfig("wiki", fake.URL)

	authURL, _ := auth.BuildAuthURL(t.Context(), srv)
	state := mustQuery(t, authURL, "state")
	if _, err := auth.CompleteAuth(t.Context(), "code", state); err != nil {
		t.Fatalf("CompleteAuth: %v", err)
	}
	if !auth.HasToken(t.Context(), "wiki") {
		t.Fatal("no token after a successful authorization")
	}

	if err := auth.Revoke(t.Context(), "wiki"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if auth.HasToken(t.Context(), "wiki") {
		t.Fatal("token survived revocation")
	}
}

// A refresh has to land in the store, or the next request goes out with the
// token the server already rejected.
func TestRefreshPersistsTheNewToken(t *testing.T) {
	fake := newFakeAuthServer(t)
	creds := NewMemoryCredentialStore()
	_ = creds.SaveToken(t.Context(), "wiki",
		`{"access_token":"at-old-value-here","refresh_token":"rt-old-value-here"}`)

	auth := NewAuthorizer(creds, DefaultRedirectURI)
	srv := oauthServerConfig("wiki", fake.URL)

	if !auth.Refresh(t.Context(), srv) {
		t.Fatal("Refresh reported failure")
	}
	tok, err := auth.tokenStoreFor("wiki").GetToken(t.Context())
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if tok.AccessToken != "at-issued-by-fake-server" {
		t.Fatalf("store still holds %q", tok.AccessToken)
	}
}

func TestRefreshWorksForOAuthDiscoveredAtRuntime(t *testing.T) {
	fake := newFakeAuthServer(t)
	creds := NewMemoryCredentialStore()
	_ = creds.SaveClient(t.Context(), "kling", "stored-client", "")
	_ = creds.SaveToken(
		t.Context(),
		"kling",
		`{"access_token":"expired","refresh_token":"refresh"}`,
	)
	auth := NewAuthorizer(creds, DefaultRedirectURI)
	srv := ServerConfig{Name: "kling", URL: fake.URL + "/mcp"}

	if srv.UsesOAuth() {
		t.Fatal("fixture must not declare oauth in config")
	}
	if !auth.Refresh(t.Context(), srv) {
		t.Fatal("runtime-discovered oauth token was not refreshed")
	}
	if got := fake.lastTokenForm.Get("grant_type"); got != "refresh_token" {
		t.Fatalf("grant_type = %q, want refresh_token", got)
	}
}

func TestRefreshWithoutARefreshTokenFails(t *testing.T) {
	fake := newFakeAuthServer(t)
	auth := NewAuthorizer(NewMemoryCredentialStore(), DefaultRedirectURI)
	if auth.Refresh(t.Context(), oauthServerConfig("wiki", fake.URL)) {
		t.Fatal("Refresh claimed success with no token at all")
	}
}

func TestBuildAuthURLRejectsNonOAuthServer(t *testing.T) {
	auth := NewAuthorizer(NewMemoryCredentialStore(), DefaultRedirectURI)
	srv := ServerConfig{Name: "plain", URL: "https://example.invalid/mcp"}
	if _, err := auth.BuildAuthURL(t.Context(), srv); err == nil {
		t.Fatal("BuildAuthURL accepted a server that does not use oauth")
	}
}

func mustQuery(t *testing.T, rawURL, key string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	v := parsed.Query().Get(key)
	if v == "" {
		t.Fatalf("no %q in %q", key, rawURL)
	}
	return v
}
