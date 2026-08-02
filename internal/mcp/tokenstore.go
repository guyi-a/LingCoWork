package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/client/transport"

	"github.com/guyi-a/Interview-Agent/internal/repository"
)

// CredentialStore is the persistence the OAuth layer needs, narrowed to the
// three things it does. An interface rather than the repo directly so tests
// can run the whole authorization flow without a database.
type CredentialStore interface {
	Load(ctx context.Context, server string) (clientID, clientSecret, tokenJSON string, err error)
	SaveToken(ctx context.Context, server, tokenJSON string) error
	SaveClient(ctx context.Context, server, clientID, clientSecret string) error
	Delete(ctx context.Context, server string) error
}

// DBCredentialStore adapts the GORM repo to CredentialStore.
type DBCredentialStore struct {
	repo *repository.MCPCredentialRepo
}

func NewDBCredentialStore(repo *repository.MCPCredentialRepo) *DBCredentialStore {
	return &DBCredentialStore{repo: repo}
}

func (s *DBCredentialStore) Load(ctx context.Context, server string) (string, string, string, error) {
	row, err := s.repo.Get(ctx, server)
	if err != nil || row == nil {
		return "", "", "", err
	}
	return row.ClientID, row.ClientSecret, row.Token, nil
}

func (s *DBCredentialStore) SaveToken(ctx context.Context, server, tokenJSON string) error {
	return s.repo.SaveToken(ctx, server, tokenJSON)
}

func (s *DBCredentialStore) SaveClient(ctx context.Context, server, clientID, clientSecret string) error {
	return s.repo.SaveClient(ctx, server, clientID, clientSecret)
}

func (s *DBCredentialStore) Delete(ctx context.Context, server string) error {
	return s.repo.Delete(ctx, server)
}

// MemoryCredentialStore keeps credentials for the life of the process.
//
// It is what an Authorizer built without a store falls back to, so that no
// call site has to nil-check: authorization then works for one run and the
// user re-authorizes after a restart, which is a much better failure than
// panicking on the first request that reaches a token.
type MemoryCredentialStore struct {
	mu   sync.Mutex
	rows map[string]memCredentials
}

type memCredentials struct {
	clientID     string
	clientSecret string
	token        string
}

func NewMemoryCredentialStore() *MemoryCredentialStore {
	return &MemoryCredentialStore{rows: make(map[string]memCredentials)}
}

func (m *MemoryCredentialStore) Load(_ context.Context, server string) (string, string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rows[server]
	return r.clientID, r.clientSecret, r.token, nil
}

func (m *MemoryCredentialStore) SaveToken(_ context.Context, server, tokenJSON string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rows[server]
	r.token = tokenJSON
	m.rows[server] = r
	return nil
}

func (m *MemoryCredentialStore) SaveClient(_ context.Context, server, clientID, clientSecret string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rows[server]
	r.clientID, r.clientSecret = clientID, clientSecret
	m.rows[server] = r
	return nil
}

func (m *MemoryCredentialStore) Delete(_ context.Context, server string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, server)
	return nil
}

// tokenStore implements transport.TokenStore for one server.
//
// The library's interface has no server parameter — it assumes one store per
// connection — so the server name is captured here and one of these is built
// per server.
//
// Every token that passes through is handed to the scrubber. The library
// writes tokens on refresh as well as on first authorization, and this is the
// only place all of them go past; miss it and a live access token can end up
// verbatim in a connection error on the settings page.
type tokenStore struct {
	server   string
	store    CredentialStore
	scrubber *Scrubber

	// mu guards cache, which exists because the transport asks for the token
	// before every single request. Going to SQLite each time would be a
	// pointless round trip on the hot path.
	mu    sync.Mutex
	cache *transport.Token
}

var _ transport.TokenStore = (*tokenStore)(nil)

func newTokenStore(server string, store CredentialStore, scrubber *Scrubber) *tokenStore {
	return &tokenStore{server: server, store: store, scrubber: scrubber}
}

func (t *tokenStore) GetToken(ctx context.Context) (*transport.Token, error) {
	t.mu.Lock()
	if t.cache != nil {
		tok := *t.cache
		t.mu.Unlock()
		return &tok, nil
	}
	t.mu.Unlock()

	_, _, raw, err := t.store.Load(ctx, t.server)
	if err != nil {
		return nil, fmt.Errorf("load token for %q: %w", t.server, err)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, transport.ErrNoToken
	}
	var tok transport.Token
	if err := json.Unmarshal([]byte(raw), &tok); err != nil {
		// A row we cannot parse is the same as no authorization: the user has
		// to go through the browser again. Better than failing every request
		// forever on a value nobody can fix by hand.
		return nil, transport.ErrNoToken
	}
	t.scrubber.Add(tok.AccessToken, tok.RefreshToken)

	t.mu.Lock()
	t.cache = &tok
	t.mu.Unlock()

	copied := tok
	return &copied, nil
}

func (t *tokenStore) SaveToken(ctx context.Context, token *transport.Token) error {
	if token == nil {
		return nil
	}
	t.scrubber.Add(token.AccessToken, token.RefreshToken)

	raw, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("marshal token for %q: %w", t.server, err)
	}
	if err := t.store.SaveToken(ctx, t.server, string(raw)); err != nil {
		return fmt.Errorf("save token for %q: %w", t.server, err)
	}

	t.mu.Lock()
	copied := *token
	t.cache = &copied
	t.mu.Unlock()
	return nil
}

// forget drops the cached token so the next read goes back to the database.
// Used after a re-authorization completes on a different store instance.
func (t *tokenStore) forget() {
	t.mu.Lock()
	t.cache = nil
	t.mu.Unlock()
}
