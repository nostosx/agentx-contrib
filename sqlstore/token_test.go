package sqlstore_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nostosx/agentx-contrib/sqlstore"
	"github.com/nostosx/agentx/mcp"
)

func tokenCipher(t *testing.T, key byte) cipher.AEAD {
	t.Helper()
	block, err := aes.NewCipher(bytes.Repeat([]byte{key}, 32))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		t.Fatal(err)
	}
	return aead
}

// This suite runs on SQLite and on both configured integration backends.
func TestTokenStoreConformance(t *testing.T) {
	t.Parallel()
	forEach(t, func(t *testing.T, b *backend) {
		prefix := b.freshPrefix(t)
		opts := b.options(prefix)
		aead := tokenCipher(t, 1)
		store := sqlstore.MCPTokens(b.db, "tenant-a/user-1", aead, opts...)
		if err := store.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		peer := sqlstore.MCPTokens(b.open(t), "tenant-a/user-1", aead, opts...)
		other := sqlstore.MCPTokens(b.db, "tenant-b/user-1", aead, opts...)
		key := "https://mcp.example.test/" + string(bytes.Repeat([]byte{'x'}, 1000))
		want := &mcp.Token{AccessToken: "secret-access", RefreshToken: "secret-refresh", TokenType: "Bearer", Expiry: time.Now().UTC().Truncate(time.Millisecond), Issuer: key, Resource: key, Scope: "read write"}
		if got, err := store.Token(t.Context(), key); err != nil || got != nil {
			t.Fatalf("missing token: %v, %v", got, err)
		}
		if err := store.SetToken(t.Context(), key, want); err != nil {
			t.Fatal(err)
		}
		if got, err := peer.Token(t.Context(), key); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("shared token: %v, %v", got, err)
		}
		if got, err := other.Token(t.Context(), key); err != nil || got != nil {
			t.Fatalf("namespace isolation: %v, %v", got, err)
		}
		client := &mcp.ClientRegistration{ClientID: "client-id", ClientSecret: "secret-client", RedirectURIs: []string{"http://127.0.0.1/callback"}}
		if err := store.SetClient(t.Context(), key, client); err != nil {
			t.Fatal(err)
		}
		got, err := peer.Client(t.Context(), key)
		if err != nil || !reflect.DeepEqual(got, client) {
			t.Fatalf("registration: %v, %v", got, err)
		}
		got.RedirectURIs[0] = "mutated"
		if again, err := store.Client(t.Context(), key); err != nil || !reflect.DeepEqual(again, client) {
			t.Fatalf("registration ownership: %v, %v", again, err)
		}
		wrongKey := sqlstore.MCPTokens(b.db, "tenant-a/user-1", tokenCipher(t, 2), opts...)
		if _, err := wrongKey.Token(t.Context(), key); err == nil {
			t.Fatal("wrong encryption key accepted")
		}
		rows, err := b.db.QueryContext(t.Context(), "SELECT data FROM "+prefix+"mcp_credentials")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("secret-")) {
				t.Fatal("plaintext credential persisted")
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := peer.SetToken(t.Context(), key, nil); err != nil {
				t.Fatal(err)
			}
			if err := peer.SetClient(t.Context(), key, nil); err != nil {
				t.Fatal(err)
			}
		}
		if tok, err := store.Token(t.Context(), key); err != nil || tok != nil {
			t.Fatalf("delete token: %v, %v", tok, err)
		}
		if reg, err := store.Client(t.Context(), key); err != nil || reg != nil {
			t.Fatalf("delete registration: %v, %v", reg, err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := store.Token(ctx, key); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	})
}

func TestTokenStoreConcurrentWriters(t *testing.T) {
	t.Parallel()
	forEach(t, func(t *testing.T, b *backend) {
		opts := b.fresh(t)
		aead := tokenCipher(t, 1)
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				s := sqlstore.MCPTokens(b.open(t), "shared", aead, opts...)
				key := fmt.Sprintf("https://mcp.example.test/%d", i)
				if err := s.SetToken(t.Context(), key, &mcp.Token{AccessToken: key}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		s := sqlstore.MCPTokens(b.db, "shared", aead, opts...)
		for i := range 8 {
			key := fmt.Sprintf("https://mcp.example.test/%d", i)
			if tok, err := s.Token(t.Context(), key); err != nil || tok == nil || tok.AccessToken != key {
				t.Fatalf("concurrent credential missing: %v, %v", tok, err)
			}
		}
	})
}
