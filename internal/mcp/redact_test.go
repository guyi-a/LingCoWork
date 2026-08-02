package mcp

import (
	"errors"
	"strings"
	"testing"
)

func TestScrubberRemovesConfiguredSecrets(t *testing.T) {
	srv := ServerConfig{
		Name:    "s",
		URL:     "https://x/mcp",
		Headers: map[string]string{"Authorization": "Bearer sk-live-abcdef123456"},
		Env:     map[string]string{"OPENAI_API_KEY": "sk-proj-zzzz9999"},
	}
	s := NewScrubber(srv)

	got := s.Clean(`request failed: header Authorization=Bearer sk-live-abcdef123456, env OPENAI_API_KEY=sk-proj-zzzz9999`)
	if strings.Contains(got, "sk-live-abcdef123456") || strings.Contains(got, "sk-proj-zzzz9999") {
		t.Fatalf("secret survived scrubbing: %q", got)
	}
}

// The header is configured whole but the credential often surfaces alone.
func TestScrubberRemovesBearerTailSeparately(t *testing.T) {
	s := NewScrubber(ServerConfig{
		Headers: map[string]string{"Authorization": "Bearer sk-live-abcdef123456"},
	})
	if got := s.Clean("token sk-live-abcdef123456 rejected"); strings.Contains(got, "sk-live") {
		t.Fatalf("bare credential survived: %q", got)
	}
}

// Scrubbing everything would destroy the stderr we show so a broken server can
// be debugged.
func TestScrubberLeavesNonSecretsAlone(t *testing.T) {
	s := NewScrubber(ServerConfig{
		Env: map[string]string{
			"HOME":        "/Users/someone",
			"NODE_ENV":    "production",
			"API_KEY":     "secret-value-here",
			"SHORT_TOKEN": "ab",
		},
	})
	msg := "spawn failed in /Users/someone with NODE_ENV=production and ab"
	if got := s.Clean(msg); got != msg {
		t.Fatalf("non-secret text was altered: %q", got)
	}
}

func TestCleanErrorHandlesNil(t *testing.T) {
	s := NewScrubber()
	if got := s.CleanError(nil); got != "" {
		t.Fatalf("CleanError(nil) = %q", got)
	}
	if got := s.CleanError(errors.New("boom")); got != "boom" {
		t.Fatalf("CleanError = %q", got)
	}
}

func TestIsSensitiveKey(t *testing.T) {
	sensitive := []string{"Authorization", "API_KEY", "x-api-key", "GITHUB_TOKEN", "db_password", "AUTH_HEADER"}
	for _, k := range sensitive {
		if !IsSensitiveKey(k) {
			t.Errorf("IsSensitiveKey(%q) = false", k)
		}
	}
	for _, k := range []string{"HOME", "PATH", "NODE_ENV", "workspace"} {
		if IsSensitiveKey(k) {
			t.Errorf("IsSensitiveKey(%q) = true", k)
		}
	}
}
