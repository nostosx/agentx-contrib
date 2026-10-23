package filestore_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/nostosx/agentx-contrib/filestore"
	"github.com/nostosx/agentx/rag"
	"github.com/nostosx/agentx/rag/ragtest"
)

// recordingEmbedder embeds with ragtest.Embedder and records the texts.
type recordingEmbedder struct {
	mu    sync.Mutex
	texts []string
}

func (e *recordingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.texts = append(e.texts, texts...)
	e.mu.Unlock()
	return ragtest.Embedder{}.Embed(ctx, texts)
}

func (e *recordingEmbedder) embedded() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.texts)
}

func embedText(t *testing.T, text string) []float32 {
	t.Helper()
	v, err := ragtest.Embedder{}.Embed(t.Context(), []string{text})
	if err != nil {
		t.Fatal(err)
	}
	return v[0]
}

func TestVectorsPersist(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "store")
	s, err := filestore.Vectors(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := &rag.Chunk{ID: "c1", Source: "https://example.com/a?b=c", Content: "persisted text",
		Metadata: map[string]any{rag.MetaHeaders: []string{"A", "B"}, "n": 3}, Embedding: embedText(t, "persisted text")}
	if err := s.Replace(ctx, rag.SourceChunks{Info: rag.SourceInfo{Source: c.Source, Hash: "h"}, Chunks: []*rag.Chunk{c}}); err != nil {
		t.Fatal(err)
	}
	rev := s.Revision()
	if err := s.Replace(ctx, rag.SourceChunks{Info: rag.SourceInfo{Source: "gone"}}); err != nil {
		t.Fatal(err)
	}
	if s.Revision() == rev {
		t.Fatal("revision did not change")
	}
	if err := s.Delete(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	re, err := filestore.Vectors(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := re.List(ctx, rag.Filter{Metadata: map[string]any{rag.MetaHeaders: []string{"A", "B"}, "n": 3}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "persisted text" || len(got[0].Embedding) != 64 || got[0].Source != c.Source {
		t.Fatalf("reopened = %+v", got)
	}
	infos, err := re.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Hash != "h" || infos[0].Chunks != 1 {
		t.Fatalf("reopened sources = %+v", infos)
	}
	// A rejected source writes no file.
	if err := re.Replace(ctx, rag.SourceChunks{Info: rag.SourceInfo{Source: "dup"}, Chunks: []*rag.Chunk{{ID: "x"}, {ID: "x"}}}); err == nil {
		t.Fatal("duplicate chunk IDs accepted")
	}
	if files, err := os.ReadDir(dir); err != nil || len(files) != 1 {
		t.Fatalf("files after a rejected source = %v, %v", files, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := filestore.Vectors(dir); err == nil {
		t.Fatal("corrupt file accepted")
	}
}

func TestVectorsFailedWriteRestoresSource(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "store")
	s, err := filestore.Vectors(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := rag.SourceChunks{Info: rag.SourceInfo{Source: "a.md", Hash: "h1"}, Chunks: []*rag.Chunk{{ID: "a1", Content: "old"}}}
	if err := s.Replace(ctx, old); err != nil {
		t.Fatal(err)
	}
	// Replacing the directory with a file makes every write fail, even as
	// root.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated := rag.SourceChunks{Info: rag.SourceInfo{Source: "a.md", Hash: "h2"}, Chunks: []*rag.Chunk{{ID: "a2", Content: "new"}}}
	added := rag.SourceChunks{Info: rag.SourceInfo{Source: "b.md", Hash: "h3"}, Chunks: []*rag.Chunk{{ID: "b1"}}}
	if err := s.Replace(ctx, updated); err == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	if err := s.Replace(ctx, added); err == nil {
		t.Fatal("addition unexpectedly succeeded")
	}
	infos, err := s.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := s.List(ctx, rag.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Hash != "h1" || len(chunks) != 1 || chunks[0].Content != "old" {
		t.Fatalf("after failed writes: sources %+v, chunks %+v", infos, chunks)
	}
}

func TestIndexSyncAcrossRestarts(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "kb")
	fsys := fstest.MapFS{
		"guide.md": {Data: []byte("# Guide\n\n## Install\n\nRun make install.\n\n## Usage\n\nCall agentx.Run.")},
		"main.go":  {Data: []byte("package main\n\nfunc main() {}\n")},
	}
	open := func() (*rag.Index, *recordingEmbedder) {
		store, err := filestore.Vectors(dir)
		if err != nil {
			t.Fatal(err)
		}
		emb := &recordingEmbedder{}
		return rag.NewIndex(rag.Config{Store: store, Embedder: emb}), emb
	}
	load := func() []*rag.Document {
		docs, err := (&rag.FSLoader{FS: fsys}).Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return docs
	}
	ix, _ := open()
	res, err := ix.Sync(t.Context(), load())
	if err != nil || len(res.Added) != 2 {
		t.Fatalf("first sync = %+v, %v", res, err)
	}
	// After a restart, unchanged files are not embedded again.
	fsys["guide.md"] = &fstest.MapFile{Data: []byte("# Guide\n\n## Install\n\nRun go install.")}
	ix, emb := open()
	res, err = ix.Sync(t.Context(), load())
	if err != nil || !slices.Equal(res.Updated, []string{"guide.md"}) || !slices.Equal(res.Unchanged, []string{"main.go"}) {
		t.Fatalf("restart sync = %+v, %v", res, err)
	}
	if got := emb.embedded(); len(got) != 1 || !strings.Contains(got[0], "Run go install.") {
		t.Fatalf("embedded after restart %q", got)
	}
	// Metadata decoded from JSON still matches filters written in Go.
	hits, err := ix.Retrieve(t.Context(), rag.Query{Text: "install", Filter: rag.Filter{Metadata: map[string]any{rag.MetaHeaders: []string{"Guide", "Install"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Chunk.Context != "Guide > Install" {
		t.Fatalf("metadata filter after reload = %+v", hits)
	}
}
