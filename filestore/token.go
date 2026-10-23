package filestore

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/nostosx/agentx/mcp"
)

// TokenStore keeps MCP OAuth tokens and client registrations in a JSON file.
// Each change replaces the file through a temporary file with mode 0600;
// newly created directories use mode 0700. Existing directory permissions
// and platform-specific access controls remain the caller's responsibility.
//
// Updates are serialized within one TokenStore. Separate instances or
// processes writing the same path require external coordination to avoid
// overwriting each other's changes.
type TokenStore struct {
	path string
	mu   sync.Mutex
}

var _ mcp.TokenStore = (*TokenStore)(nil)

// MCPTokens returns a token store in the file at path, which is created on
// first write.
func MCPTokens(path string) *TokenStore { return &TokenStore{path: path} }

// tokenData is the content of the file.
type tokenData struct {
	Tokens  map[string]*mcp.Token              `json:"tokens,omitempty"`
	Clients map[string]*mcp.ClientRegistration `json:"clients,omitempty"`
}

func (s *TokenStore) load() (tokenData, error) {
	var d tokenData
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return d, err
	}
	return d, nil
}

func (s *TokenStore) save(d tokenData) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tokens-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *TokenStore) update(fn func(*tokenData)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return err
	}
	fn(&d)
	return s.save(d)
}

// Token implements mcp.TokenStore.
func (s *TokenStore) Token(_ context.Context, server string) (*mcp.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	return d.Tokens[server], nil
}

// SetToken implements mcp.TokenStore.
func (s *TokenStore) SetToken(_ context.Context, server string, tok *mcp.Token) error {
	return s.update(func(d *tokenData) { d.Tokens = set(d.Tokens, server, tok) })
}

// Client implements mcp.TokenStore.
func (s *TokenStore) Client(_ context.Context, issuer string) (*mcp.ClientRegistration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	return d.Clients[issuer], nil
}

// SetClient implements mcp.TokenStore.
func (s *TokenStore) SetClient(_ context.Context, issuer string, reg *mcp.ClientRegistration) error {
	return s.update(func(d *tokenData) { d.Clients = set(d.Clients, issuer, reg) })
}

// set stores a copy of v under key in m, allocating m if needed; a nil v
// deletes key.
func set[T any](m map[string]*T, key string, v *T) map[string]*T {
	if v == nil {
		delete(m, key)
		return m
	}
	if m == nil {
		m = map[string]*T{}
	}
	c := *v
	m[key] = &c
	return m
}
