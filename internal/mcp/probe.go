package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Whether a server needs authorization is something we find out, not
// something the user tells us.
//
// It used to be a config field: a server only got the OAuth treatment if its
// entry said `"auth": "oauth"`. That asks the wrong person. Someone pasting a
// URL has no way to know whether it speaks OAuth, expects a static API key,
// or is open — and getting it wrong produced a bare "unauthorized (401)" with
// no way forward, because the state that offers an "authorize" button was
// only reachable for servers that had already declared themselves.
//
// The protocol answers this itself. An MCP server that wants OAuth replies
// 401 with a WWW-Authenticate pointing at its protected resource metadata
// (RFC 9728), and that document names the authorization server. A 401 with no
// such document means a static credential instead, which is a config edit
// rather than a browser flow — a different fix, so worth telling apart.

// probeTimeout bounds one discovery request. Short: these are small metadata
// documents on the same host we already failed to reach, and the probe runs
// on a path where the user is already waiting to be told what went wrong.
const probeTimeout = 10 * time.Second

// authKind is what a server wants from us.
type authKind int

const (
	// authUnknown means the probe could not reach a conclusion — the server
	// was unreachable, or answered something we cannot interpret. Callers
	// keep whatever error they already had rather than inventing a diagnosis.
	authUnknown authKind = iota
	// authNone means the endpoint served us without credentials.
	authNone
	// authOAuth means 401 plus discoverable OAuth metadata.
	authOAuth
	// authToken means 401 with no OAuth metadata: it wants a static
	// credential in a header.
	authToken
)

// resourceMetadata is the subset of RFC 9728 we act on.
type resourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

var resourceMetadataRE = regexp.MustCompile(`resource_metadata="?([^",\s]+)"?`)

func probeClient() *http.Client {
	return &http.Client{Timeout: probeTimeout}
}

// probeAuth classifies what an HTTP MCP server wants.
//
// The request is a plain GET. A server that speaks Streamable HTTP answers
// real traffic on POST, so a GET may well come back 405 or an event stream —
// either way it is not a 401, which is the only thing being asked here.
func probeAuth(ctx context.Context, mcpURL string) authKind {
	resp, err := probeGet(ctx, mcpURL)
	if err != nil {
		return authUnknown
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	status := resp.StatusCode
	resp.Body.Close()

	if status != http.StatusUnauthorized {
		return authNone
	}
	meta, err := discoverProtectedResource(ctx, mcpURL, challenge)
	if err != nil || len(meta.AuthorizationServers) == 0 {
		return authToken
	}
	return authOAuth
}

// discoverProtectedResource fetches the server's RFC 9728 document.
//
// Three candidate locations, in the order the spec prefers: whatever the 401
// advertised, then the path-scoped well-known URL, then the host-wide one.
// The fallbacks matter because the advertised value is the field servers most
// often get wrong, and because the probe that would have read it may itself
// have failed.
func discoverProtectedResource(ctx context.Context, mcpURL, challenge string) (resourceMetadata, error) {
	parsed, err := url.Parse(mcpURL)
	if err != nil {
		return resourceMetadata{}, fmt.Errorf("parse %q: %w", mcpURL, err)
	}

	var candidates []string
	if advertised := advertisedMetadataURL(challenge, parsed); advertised != "" {
		candidates = append(candidates, advertised)
	}
	origin := parsed.Scheme + "://" + parsed.Host
	if path := strings.TrimSuffix(parsed.Path, "/"); path != "" {
		candidates = append(candidates, origin+"/.well-known/oauth-protected-resource"+path)
	}
	candidates = append(candidates, origin+"/.well-known/oauth-protected-resource")

	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		if meta, ok := fetchResourceMetadata(ctx, candidate); ok {
			return meta, nil
		}
	}
	return resourceMetadata{}, fmt.Errorf("no protected resource metadata for %s", mcpURL)
}

// advertisedMetadataURL extracts the metadata location from a WWW-Authenticate
// challenge, and refuses to follow it anywhere it should not go.
//
// Two rejections. A different host, because the challenge is the one part of
// this exchange an attacker on the path could rewrite, and following it would
// let them nominate their own authorization server — the user would then be
// sent to a real browser, to a plausible login page, to type real
// credentials. And a downgrade to http, for the same reason: reading the
// document in clear text puts the endpoints it names up for grabs.
//
// Neither rejection loses anything, because the well-known fallbacks derived
// from the MCP URL cover the same ground over https. Kling's own server
// advertises an http URL while serving https, which is how this came up.
func advertisedMetadataURL(challenge string, mcpURL *url.URL) string {
	match := resourceMetadataRE.FindStringSubmatch(challenge)
	if len(match) < 2 {
		return ""
	}
	advertised, err := url.Parse(match[1])
	if err != nil || advertised.Host != mcpURL.Host {
		return ""
	}
	if mcpURL.Scheme == "https" && advertised.Scheme != "https" {
		return ""
	}
	return advertised.String()
}

func fetchResourceMetadata(ctx context.Context, metadataURL string) (resourceMetadata, bool) {
	resp, err := probeGet(ctx, metadataURL)
	if err != nil {
		return resourceMetadata{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resourceMetadata{}, false
	}
	var meta resourceMetadata
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return resourceMetadata{}, false
	}
	return meta, meta.Resource != "" || len(meta.AuthorizationServers) > 0
}

func probeGet(ctx context.Context, target string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := probeClient().Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	// The body is read (or discarded) by the caller, so the deadline has to
	// outlive this function. Closing the body cancels it.
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
