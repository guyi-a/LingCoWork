package mcp

import (
	"errors"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client/transport"
)

func TestTokenStoreRoundTrip(t *testing.T) {
	store := newTokenStore("s", NewMemoryCredentialStore(), NewScrubber())

	want := &transport.Token{
		AccessToken:  "at-abcdefghijklmnop",
		TokenType:    "Bearer",
		RefreshToken: "rt-qrstuvwxyz012345",
		ExpiresIn:    3600,
	}
	if err := store.SaveToken(t.Context(), want); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	store.forget()

	got, err := store.GetToken(t.Context())
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Fatalf("token = %+v, want %+v", got, want)
	}
}

// The library treats any other error as an operational failure and gives up;
// only ErrNoToken means "go and authorize".
func TestTokenStoreReportsNoToken(t *testing.T) {
	store := newTokenStore("s", NewMemoryCredentialStore(), NewScrubber())
	if _, err := store.GetToken(t.Context()); !errors.Is(err, transport.ErrNoToken) {
		t.Fatalf("GetToken on an empty store = %v, want ErrNoToken", err)
	}
}

// A row nobody can parse must not wedge the server forever on an error no
// user can act on. It reads as "not authorized", which has a fix.
func TestTokenStoreTreatsCorruptRowAsUnauthorized(t *testing.T) {
	creds := NewMemoryCredentialStore()
	_ = creds.SaveToken(t.Context(), "s", "{not json")
	store := newTokenStore("s", creds, NewScrubber())

	if _, err := store.GetToken(t.Context()); !errors.Is(err, transport.ErrNoToken) {
		t.Fatalf("GetToken on a corrupt row = %v, want ErrNoToken", err)
	}
}

// A live token that reaches a status page or a log is the whole reason the
// scrubber is wired through the store.
func TestTokenStoreFeedsTheScrubber(t *testing.T) {
	scrubber := NewScrubber()
	store := newTokenStore("s", NewMemoryCredentialStore(), scrubber)

	tok := &transport.Token{
		AccessToken:  "at-supersecretvalue",
		RefreshToken: "rt-alsosecretvalue",
	}
	if err := store.SaveToken(t.Context(), tok); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	got := scrubber.Clean("failed with Bearer at-supersecretvalue / rt-alsosecretvalue")
	if strings.Contains(got, "supersecret") || strings.Contains(got, "alsosecret") {
		t.Fatalf("token survived scrubbing: %q", got)
	}
}

// Reading it back out of the database has to register it too: after a
// restart, SaveToken never runs but the token is just as live.
func TestTokenStoreScrubsTokensLoadedFromDisk(t *testing.T) {
	creds := NewMemoryCredentialStore()
	_ = creds.SaveToken(t.Context(), "s", `{"access_token":"at-loadedfromdisk1"}`)

	scrubber := NewScrubber()
	store := newTokenStore("s", creds, scrubber)
	if _, err := store.GetToken(t.Context()); err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if got := scrubber.Clean("token at-loadedfromdisk1 rejected"); strings.Contains(got, "loadedfromdisk") {
		t.Fatalf("token survived scrubbing: %q", got)
	}
}

func TestScrubberAddIgnoresShortAndDuplicateValues(t *testing.T) {
	s := NewScrubber()
	s.Add("ab", "", "long-enough-secret", "long-enough-secret")

	if n := len(s.secrets); n != 1 {
		t.Fatalf("secrets = %d, want 1: %v", n, s.secrets)
	}
	if got := s.Clean("saw ab and long-enough-secret"); !strings.Contains(got, "ab ") {
		t.Errorf("a two-character value was redacted: %q", got)
	}
}
