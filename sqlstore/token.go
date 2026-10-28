package sqlstore

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nostosx/agentx/mcp"
)

// TokenStore keeps encrypted MCP OAuth credentials in SQL. Each namespace
// belongs to one user or trust domain; server URLs and issuers are separate
// keys within it. Reuse the same namespace and encryption key across replicas.
// The caller owns key rotation, access control and OAuth refresh coordination.
// Atomic record replacement does not serialize a remote refresh exchange.
type TokenStore struct {
	c         config
	namespace string
	aead      cipher.AEAD
}

var _ mcp.TokenStore = (*TokenStore)(nil)

// MCPTokens returns a credential store. namespace must be non-empty and at
// most 255 UTF-8 bytes. aead is required: credentials are never stored in
// plaintext. It must be safe for concurrent use. AES-GCM implementations from
// crypto/cipher are suitable. Keep the key outside the database; changing it
// requires re-encrypting existing records before replacing the old key.
func MCPTokens(db DB, namespace string, aead cipher.AEAD, opts ...Option) *TokenStore {
	s := &TokenStore{c: newConfig(db, opts), namespace: namespace, aead: aead}
	if s.c.err == nil {
		switch {
		case namespace == "" || !validMemoryKey(namespace):
			s.c.err = errors.New("sqlstore: invalid MCP credential namespace")
		case aead == nil:
			s.c.err = errors.New("sqlstore: MCP credentials require authenticated encryption")
		}
	}
	return s
}

// Migrate creates the credential table. It is also created by package Migrate.
func (s *TokenStore) Migrate(ctx context.Context) error { return migrateTokens(ctx, s.c) }

func migrateTokens(ctx context.Context, c config) error {
	return c.migrate(ctx, c.createTable("mcp_credentials", []string{
		"id " + c.keyType(64) + " NOT NULL",
		"data " + c.blobType() + " NOT NULL",
	}, "id"))
}

// credentialID encodes the namespace, record kind and full URL without
// database key-length limits. The table and ID also bind the ciphertext to
// its storage location, preventing record substitution across namespaces.
func (s *TokenStore) credentialID(kind, key string) (string, []byte, error) {
	if s.c.err != nil {
		return "", nil, s.c.err
	}
	if key == "" {
		return "", nil, errors.New("sqlstore: empty MCP credential key")
	}
	sum := sha256.Sum256([]byte(s.namespace + "\x00" + kind + "\x00" + key))
	id := hex.EncodeToString(sum[:])
	return id, []byte(s.c.table("mcp_credentials") + "\x00" + id), nil
}

func (s *TokenStore) load(ctx context.Context, kind, key string, value any) (bool, error) {
	id, aad, err := s.credentialID(kind, key)
	if err != nil {
		return false, err
	}
	var data []byte
	err = s.c.queryRow(ctx, "SELECT data FROM "+s.c.table("mcp_credentials")+" WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	n := s.aead.NonceSize()
	if len(data) < n+s.aead.Overhead() {
		return false, errors.New("sqlstore: truncated MCP credential")
	}
	plain, err := s.aead.Open(nil, data[:n], data[n:], aad)
	if err != nil {
		return false, fmt.Errorf("sqlstore: decrypt MCP credential: %w", err)
	}
	if err := json.Unmarshal(plain, value); err != nil {
		return false, fmt.Errorf("sqlstore: decode MCP credential: %w", err)
	}
	return true, nil
}

func (s *TokenStore) save(ctx context.Context, kind, key string, value any) error {
	id, aad, err := s.credentialID(kind, key)
	if err != nil {
		return err
	}
	if value == nil {
		_, err := s.c.exec(ctx, "DELETE FROM "+s.c.table("mcp_credentials")+" WHERE id = ?", id)
		return err
	}
	plain, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("sqlstore: encode MCP credential: %w", err)
	}
	nonce := make([]byte, s.aead.NonceSize())
	if len(nonce) != 0 {
		if _, err := rand.Read(nonce); err != nil {
			return fmt.Errorf("sqlstore: MCP credential nonce: %w", err)
		}
	}
	data := s.aead.Seal(nonce, nonce, plain, aad)
	_, err = s.c.exec(ctx, s.c.upsert("mcp_credentials", []string{"id", "data"}, "id"), id, data)
	return err
}

// Token returns the credential for server, or nil when none exists.
func (s *TokenStore) Token(ctx context.Context, server string) (*mcp.Token, error) {
	var tok mcp.Token
	ok, err := s.load(ctx, "token", server, &tok)
	if err != nil || !ok {
		return nil, err
	}
	return &tok, nil
}

// SetToken atomically replaces the credential; nil deletes it idempotently.
func (s *TokenStore) SetToken(ctx context.Context, server string, tok *mcp.Token) error {
	if tok == nil {
		return s.save(ctx, "token", server, nil)
	}
	return s.save(ctx, "token", server, tok)
}

// Client returns the registration for issuer, or nil when none exists.
func (s *TokenStore) Client(ctx context.Context, issuer string) (*mcp.ClientRegistration, error) {
	var client mcp.ClientRegistration
	ok, err := s.load(ctx, "client", issuer, &client)
	if err != nil || !ok {
		return nil, err
	}
	return &client, nil
}

// SetClient atomically replaces the registration; nil deletes it idempotently.
func (s *TokenStore) SetClient(ctx context.Context, issuer string, client *mcp.ClientRegistration) error {
	if client == nil {
		return s.save(ctx, "client", issuer, nil)
	}
	return s.save(ctx, "client", issuer, client)
}
