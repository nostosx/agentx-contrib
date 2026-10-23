package filestore_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nostosx/agentx-contrib/filestore"
	"github.com/nostosx/agentx/mcp"
)

func TestMCPTokens(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "auth", "tokens.json")
	s := filestore.MCPTokens(path)
	if tok, err := s.Token(ctx, "https://mcp.example.com"); tok != nil || err != nil {
		t.Fatalf("Token of an empty store = %+v, %v", tok, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("reading created the file: %v", err)
	}
	tok := &mcp.Token{AccessToken: "access", RefreshToken: "refresh", Issuer: "https://as.example.com", Resource: "https://mcp.example.com"}
	if err := s.SetToken(ctx, "https://mcp.example.com", tok); err != nil {
		t.Fatal(err)
	}
	if err := s.SetClient(ctx, "https://as.example.com", &mcp.ClientRegistration{ClientID: "client"}); err != nil {
		t.Fatal(err)
	}
	// The store keeps its own copies.
	tok.AccessToken = "changed"

	reopened := filestore.MCPTokens(path)
	got, err := reopened.Token(ctx, "https://mcp.example.com")
	if err != nil || got == nil || got.AccessToken != "access" || got.RefreshToken != "refresh" || got.Resource != "https://mcp.example.com" {
		t.Fatalf("reopened Token = %+v, %v", got, err)
	}
	if reg, err := reopened.Client(ctx, "https://as.example.com"); err != nil || reg == nil || reg.ClientID != "client" {
		t.Fatalf("reopened Client = %+v, %v", reg, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode %v", fi.Mode().Perm())
	}
	var data map[string]map[string]json.RawMessage
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &data); err != nil || len(data["tokens"]) != 1 || len(data["clients"]) != 1 {
		t.Errorf("file = %s", b)
	}

	// nil deletes.
	if err := s.SetToken(ctx, "https://mcp.example.com", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetClient(ctx, "https://as.example.com", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.Token(ctx, "https://mcp.example.com"); got != nil || err != nil {
		t.Fatalf("deleted Token = %+v, %v", got, err)
	}
	if reg, err := reopened.Client(ctx, "https://as.example.com"); reg != nil || err != nil {
		t.Fatalf("deleted Client = %+v, %v", reg, err)
	}

	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Token(ctx, "https://mcp.example.com"); err == nil {
		t.Fatal("corrupt file accepted")
	}
}
