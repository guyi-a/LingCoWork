package mcp

import (
	"strings"
	"sync"
)

// A server config carries credentials: an Authorization header, an API key in
// the child process env. Those values travel further than the config file —
// a server that fails to start echoes its command line into stderr, an HTTP
// transport puts the request into the error it returns — and both of those
// end up in logs and in the settings page.
//
// Redaction here works from the known secret VALUES rather than by pattern
// matching the output, because we hold the config and therefore know exactly
// what to look for. Guessing at token-shaped substrings in arbitrary text
// would both miss real keys and mangle innocent output.

// sensitiveKeys are matched as substrings against header and env names, case
// insensitively. Only values under a matching key are redacted: a blanket
// rule over every env var would scrub paths and version numbers out of the
// stderr we are showing precisely so someone can debug a server that will not
// start.
var sensitiveKeys = []string{
	"authorization", "api_key", "apikey", "api-key",
	"token", "secret", "password", "passwd", "credential",
	"cookie", "access_key", "private_key", "auth",
}

// minRedactableLen guards against a one-character secret turning every "a" in
// the output into a redaction marker. A credential this short is not one.
const minRedactableLen = 4

const redactionMarker = "[redacted]"

// Scrubber removes known credentials from text bound for a log, an error
// message, or the frontend.
//
// The secret set grows at runtime: OAuth access and refresh tokens arrive
// long after the config is read, and are rewritten on every refresh. Hence
// the lock — tokens are added from the transport's goroutines while a status
// request is scrubbing.
type Scrubber struct {
	mu      sync.RWMutex
	secrets []string
}

// NewScrubber collects the secrets out of the given servers.
func NewScrubber(servers ...ServerConfig) *Scrubber {
	s := &Scrubber{}
	s.AddServers(servers...)
	return s
}

// AddServers folds in the secrets of a freshly loaded config.
//
// It adds and never removes, so a credential deleted from the config stays
// redacted. That is the safe direction to be wrong in: over-redacting a dead
// key costs nothing, and the alternative leaks a live one that is still
// sitting in a connection error from before the edit.
func (s *Scrubber) AddServers(servers ...ServerConfig) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, srv := range servers {
		s.collectLocked(srv.Headers)
		s.collectLocked(srv.Env)
	}
}

// Add registers secrets discovered after construction. Values too short to be
// credentials, and duplicates, are dropped: this is called on every token
// read and the list would otherwise grow without bound.
func (s *Scrubber) Add(values ...string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range values {
		if len(v) < minRedactableLen {
			continue
		}
		if !s.hasLocked(v) {
			s.secrets = append(s.secrets, v)
		}
	}
}

func (s *Scrubber) hasLocked(v string) bool {
	for _, existing := range s.secrets {
		if existing == v {
			return true
		}
	}
	return false
}

func (s *Scrubber) collectLocked(kv map[string]string) {
	for k, v := range kv {
		if len(v) < minRedactableLen || !IsSensitiveKey(k) {
			continue
		}
		if !s.hasLocked(v) {
			s.secrets = append(s.secrets, v)
		}
		// An Authorization header is configured whole ("Bearer abc123") but
		// the credential can surface on its own, so register the tail too.
		if _, rest, ok := strings.Cut(v, " "); ok && len(rest) >= minRedactableLen && !s.hasLocked(rest) {
			s.secrets = append(s.secrets, rest)
		}
	}
}

// IsSensitiveKey reports whether a header or env name names a credential.
func IsSensitiveKey(name string) bool {
	lower := strings.ToLower(name)
	for _, k := range sensitiveKeys {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

// Clean replaces every known secret in text.
func (s *Scrubber) Clean(text string) string {
	if s == nil || text == "" {
		return text
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, secret := range s.secrets {
		text = strings.ReplaceAll(text, secret, redactionMarker)
	}
	return text
}

// CleanError applies Clean to an error's message, returning "" for nil so
// callers can assign straight into a status field.
func (s *Scrubber) CleanError(err error) string {
	if err == nil {
		return ""
	}
	return s.Clean(err.Error())
}
