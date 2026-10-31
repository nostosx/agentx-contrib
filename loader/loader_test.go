package loader

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nostosx/servex/storage/cache"
)

type jsonCodec struct{}

func (jsonCodec) Name() string                       { return "json" }
func (jsonCodec) Marshal(v any) ([]byte, error)      { return json.Marshal(v) }
func (jsonCodec) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

type testCache struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (c *testCache) Get(_ context.Context, k string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.data[k]
	if !ok {
		return nil, cache.ErrNotFound
	}
	return v, nil
}
func (c *testCache) Set(_ context.Context, k string, v []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data == nil {
		c.data = map[string][]byte{}
	}
	c.data[k] = v
	return nil
}
func (c *testCache) Delete(_ context.Context, k string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, k)
	return nil
}

func TestGitHubSnapshotDeltaAndFailure(t *testing.T) {
	mode := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/repos/org/.github/") {
			t.Error("repository name was not preserved", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("X-GitHub-Api-Version") == "" {
			t.Error("missing API authentication/version")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/trees/"):
			if mode == 2 {
				http.Error(w, "private detail", http.StatusForbidden)
				return
			}
			if mode == 3 {
				fmt.Fprint(w, `{"sha":"tree","truncated":true,"tree":[]}`)
				return
			}
			entries := `[{"path":"docs/a#b.md","type":"blob","mode":"100644","sha":"abc","size":5}]`
			if mode == 1 {
				entries = `[]`
			}
			fmt.Fprintf(w, `{"sha":"tree","tree":%s}`, entries)
		case strings.Contains(r.URL.Path, "/blobs/"):
			fmt.Fprintf(w, `{"encoding":"base64","sha":"abc","content":%q}`, base64.StdEncoding.EncodeToString([]byte("hello")))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	options := GitHubOptions{Endpoint: server.URL, Client: server.Client(), Token: func(context.Context) (string, error) { return "fixture", nil }, Scope: "tenant-a:parser1", Cache: &testCache{}, Codec: jsonCodec{}, Owner: "org", Repo: ".github", Ref: "feature/docs", Paths: []string{"docs"}}
	c, err := NewGitHub(options)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	initial, err := c.LoadChanges(t.Context(), "")
	if err != nil || len(initial.Documents) != 1 || initial.Documents[0].Source != "github://org/.github/docs/a#b.md" || initial.Documents[0].Metadata["url"] != "https://github.com/org/.github/blob/feature%2Fdocs/docs/a%23b.md" {
		t.Fatalf("baseline: %+v %v", initial, err)
	}
	unchanged, err := c.LoadChanges(t.Context(), initial.Cursor)
	if err != nil || len(unchanged.Documents) != 0 || len(unchanged.DeletedSources) != 0 {
		t.Fatalf("unchanged: %+v %v", unchanged, err)
	}
	mode = 1
	deleted, err := c.LoadChanges(t.Context(), initial.Cursor)
	if err != nil || len(deleted.DeletedSources) != 1 {
		t.Fatalf("deletion: %+v %v", deleted, err)
	}
	for _, m := range []int{2, 3} {
		mode = m
		delta, err := c.LoadChanges(t.Context(), initial.Cursor)
		if err == nil || delta != nil || strings.Contains(err.Error(), "private detail") {
			t.Fatalf("partial failure: %+v %v", delta, err)
		}
	}
	if _, err := c.LoadChanges(t.Context(), strings.Repeat("a", 48)); !errors.Is(err, ErrCursorExpired) {
		t.Fatal(err)
	}
	options.Scope = "tenant-b"
	other, err := NewGitHub(options)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.LoadChanges(t.Context(), initial.Cursor); !errors.Is(err, ErrCursorExpired) {
		t.Fatal("cursor crossed authorization scope", err)
	}
}

func TestNotionPaginationAndNestedFailure(t *testing.T) {
	fail := false
	loop := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/pages/page":
			fmt.Fprint(w, `{"id":"page","url":"https://notion.so/page","properties":{"Name":{"type":"title","title":[{"plain_text":"Guide"}]}},"last_edited_time":"stable"}`)
		case "/v1/blocks/page/children":
			if r.URL.Query().Get("start_cursor") == "next" && !loop {
				fmt.Fprint(w, `{"results":[{"id":"code","type":"code","code":{"language":"go","rich_text":[{"plain_text":"fmt.Println(1)"}]}}],"has_more":false}`)
			} else {
				fmt.Fprint(w, `{"results":[{"id":"nested","type":"toggle","has_children":true,"toggle":{"rich_text":[{"plain_text":"Section"}]}}],"has_more":true,"next_cursor":"next"}`)
			}
		case "/v1/blocks/nested/children":
			if fail {
				http.Error(w, "denied", http.StatusNotFound)
			} else {
				fmt.Fprint(w, `{"results":[{"id":"body","type":"paragraph","paragraph":{"rich_text":[{"plain_text":"Nested body"}]}}],"has_more":false}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c, err := NewNotion(NotionOptions{Endpoint: server.URL, Client: server.Client(), PageIDs: []string{"page"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	docs, err := c.Load(t.Context())
	if err != nil || len(docs) != 1 || !strings.Contains(docs[0].Content, "```go") || !strings.Contains(docs[0].Content, "Nested body") {
		t.Fatalf("snapshot: %+v %v", docs, err)
	}
	fail = true
	if docs, err := c.Load(t.Context()); err == nil || docs != nil {
		t.Fatal("permission failure returned partial snapshot")
	}
	fail = false
	loop = true
	if _, err := c.Load(t.Context()); err == nil {
		t.Fatal("pagination loop accepted")
	}
}

func TestFeishuRevisionHierarchyAndEnvelope(t *testing.T) {
	failure := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/blocks") {
			fmt.Fprint(w, `{"code":0,"data":{"document":{"document_id":"doc","title":"Guide","revision_id":7}}}`)
			return
		}
		if r.URL.Query().Get("document_revision_id") != "7" {
			t.Error("snapshot revision not pinned")
		}
		if failure {
			fmt.Fprint(w, `{"code":999,"msg":"permission denied"}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"items":[{"block_id":"doc","block_type":1,"children":["heading","body"]},{"block_id":"body","block_type":2,"text":{"elements":[{"text_run":{"content":"Document body"}}]}},{"block_id":"heading","block_type":3,"heading1":{"elements":[{"text_run":{"content":"Title"}}]}}],"has_more":false}}`)
	}))
	defer server.Close()
	c, err := NewFeishu(FeishuOptions{Endpoint: server.URL, Client: server.Client(), DocumentIDs: []string{"doc"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	docs, err := c.Load(t.Context())
	if err != nil || len(docs) != 1 || !strings.Contains(docs[0].Content, "# Title\nDocument body") {
		t.Fatalf("snapshot: %+v %v", docs, err)
	}
	failure = true
	if docs, err := c.Load(t.Context()); err == nil || docs != nil {
		t.Fatal("provider error accepted")
	}
}

func TestCredentialRedirectAndBodyBounds(t *testing.T) {
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer target.Close()
	redirect := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirect {
			http.Redirect(w, r, target.URL, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, strings.Repeat(" ", 200))
	}))
	defer server.Close()
	c, err := NewNotion(NotionOptions{Endpoint: server.URL, Client: server.Client(), MaxBytes: 100, PageIDs: []string{"page"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Load(t.Context()); err == nil || reached {
		t.Fatal("credential API followed redirect")
	}
	redirect = false
	if _, err := c.Load(t.Context()); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestGitHubRejectsRepositoryPathSegments(t *testing.T) {
	for _, repo := range []string{".", "..", "owner/repository"} {
		if c, err := NewGitHub(GitHubOptions{Owner: "org", Repo: repo, Ref: "main", Paths: []string{"."}}); err == nil {
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			t.Fatal("repository path escape accepted", repo)
		}
	}
}
