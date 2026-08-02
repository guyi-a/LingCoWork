package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// oauthResourceServer mimics the shape kling's endpoint actually has: 401 on
// the MCP path with a WWW-Authenticate challenge, and an RFC 9728 document at
// both well-known locations.
func oauthResourceServer(t *testing.T, challengeFor func(base string) string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server

	metadata := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"resource": "` + srvURL(&srv) + `/mcp",
			"authorization_servers": ["` + srvURL(&srv) + `/auth"],
			"scopes_supported": ["generation.create", "account.read"]
		}`))
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", metadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource", metadata)
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
		if challengeFor != nil {
			w.Header().Set("WWW-Authenticate", challengeFor(srvURL(&srv)))
		}
		w.WriteHeader(http.StatusUnauthorized)
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func srvURL(srv **httptest.Server) string {
	if *srv == nil {
		return ""
	}
	return (*srv).URL
}

func TestProbeAuthDetectsOAuth(t *testing.T) {
	srv := oauthResourceServer(t, func(base string) string {
		return `Bearer resource_metadata=` + base + `/.well-known/oauth-protected-resource/mcp`
	})
	if got := probeAuth(context.Background(), srv.URL+"/mcp"); got != authOAuth {
		t.Fatalf("probeAuth = %v, want authOAuth", got)
	}
}

// A 401 with nothing discoverable behind it wants a static credential, and
// the fix is a config edit rather than a browser flow.
func TestProbeAuthDetectsStaticToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if got := probeAuth(context.Background(), srv.URL+"/mcp"); got != authToken {
		t.Fatalf("probeAuth = %v, want authToken", got)
	}
}

func TestProbeAuthDetectsOpenServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Streamable HTTP servers commonly reject a bare GET. Anything that
		// is not a 401 means credentials are not the problem.
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer srv.Close()

	if got := probeAuth(context.Background(), srv.URL+"/mcp"); got != authNone {
		t.Fatalf("probeAuth = %v, want authNone", got)
	}
}

func TestProbeAuthUnreachableIsInconclusive(t *testing.T) {
	if got := probeAuth(context.Background(), "http://127.0.0.1:1/mcp"); got != authUnknown {
		t.Fatalf("probeAuth = %v, want authUnknown", got)
	}
}

// Discovery still works when the server sends no challenge at all: the
// well-known paths are derived from the MCP URL.
func TestDiscoverFallsBackToWellKnownPaths(t *testing.T) {
	srv := oauthResourceServer(t, nil)
	meta, err := discoverProtectedResource(context.Background(), srv.URL+"/mcp", "")
	if err != nil {
		t.Fatalf("discoverProtectedResource: %v", err)
	}
	if len(meta.ScopesSupported) != 2 {
		t.Fatalf("scopes = %v", meta.ScopesSupported)
	}
}

// The challenge is the one part of the exchange someone on the path can
// rewrite, so it is only followed when it cannot redirect us anywhere.
func TestAdvertisedMetadataURLRejectsUntrustworthyTargets(t *testing.T) {
	mcpURL, err := url.Parse("https://klingai.com/mcp")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		challenge string
		want      string
	}{
		{
			name:      "same host over https is followed",
			challenge: `Bearer resource_metadata="https://klingai.com/.well-known/oauth-protected-resource/mcp"`,
			want:      "https://klingai.com/.well-known/oauth-protected-resource/mcp",
		},
		{
			// Kling's own server does this. Falling back to the https
			// well-known path costs nothing and reads the same document.
			name:      "downgrade to http is refused",
			challenge: `Bearer resource_metadata=http://klingai.com/.well-known/oauth-protected-resource/mcp`,
			want:      "",
		},
		{
			name:      "another host is refused",
			challenge: `Bearer resource_metadata=https://evil.example/.well-known/oauth-protected-resource`,
			want:      "",
		},
		{
			name:      "no challenge",
			challenge: "",
			want:      "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := advertisedMetadataURL(tc.challenge, mcpURL); got != tc.want {
				t.Errorf("advertisedMetadataURL = %q, want %q", got, tc.want)
			}
		})
	}
}

// The whole point of the probe: a server nobody declared as OAuth still ends
// up in the state that offers an authorize button.
func TestConnectClassifiesUndeclaredOAuthServer(t *testing.T) {
	srv := oauthResourceServer(t, nil)
	cfg := ServerConfig{Name: "kling", URL: srv.URL + "/mcp", InitTimeoutSec: 5}

	_, err := connect(context.Background(), cfg, nil)
	if !errors.Is(err, ErrNeedsAuthorization) {
		t.Fatalf("connect error = %v, want ErrNeedsAuthorization", err)
	}
}

// A server wanting a static key must NOT be offered a browser flow that
// cannot help it; it gets told where to put the credential instead.
func TestConnectClassifiesStaticTokenServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	cfg := ServerConfig{Name: "keyed", URL: srv.URL + "/mcp", InitTimeoutSec: 5}

	_, err := connect(context.Background(), cfg, nil)
	if errors.Is(err, ErrNeedsAuthorization) {
		t.Fatal("a static-credential server was offered the oauth flow")
	}
	if err == nil || !strings.Contains(err.Error(), "headers") {
		t.Fatalf("error = %v, want a pointer at the headers config", err)
	}
}

// A server with its own Authorization header opted out of OAuth; probing it
// would only produce advice about a flow it is not using.
func TestConnectLeavesHeaderAuthenticatedServerAlone(t *testing.T) {
	srv := oauthResourceServer(t, nil)
	cfg := ServerConfig{
		Name:           "keyed",
		URL:            srv.URL + "/mcp",
		Headers:        map[string]string{"authorization": "Bearer static"},
		InitTimeoutSec: 5,
	}

	_, err := connect(context.Background(), cfg, nil)
	if errors.Is(err, ErrNeedsAuthorization) {
		t.Fatal("a header-authenticated server was pushed into the oauth flow")
	}
}
