package filestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nostosx/agentx-contrib/filestore/internal/jsonfile"
	"github.com/nostosx/agentx/rag"
)

// VectorStore keeps RAG chunks in memory and writes each source through to
// its own JSON file in a directory, so replacing a source rewrites only
// that source's file. It suits corpora that fit in memory; use a database
// with a vector index for larger ones.
//
// Sources are loaded at construction; later writes by other instances or
// processes are not reloaded. Use one writer per directory. Reads use the
// in-memory store and may observe a replacement before it is persisted.
type VectorStore struct {
	dir string
	mem *rag.MemoryStore
	mu  sync.Mutex
}

var (
	_ rag.VectorStore = (*VectorStore)(nil)
	_ rag.Revisioner  = (*VectorStore)(nil)
)

// Vectors loads the sources stored in dir, which is created if needed.
func Vectors(dir string) (*VectorStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	s := &VectorStore{dir: dir, mem: rag.NewMemoryStore()}
	var loaded []rag.SourceChunks
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var src rag.SourceChunks
		if err := json.Unmarshal(b, &src); err != nil {
			return nil, fmt.Errorf("filestore: decode %s: %w", filepath.Join(dir, e.Name()), err)
		}
		loaded = append(loaded, src)
	}
	if err := s.mem.Replace(context.Background(), loaded...); err != nil {
		return nil, err
	}
	return s, nil
}

// sourceFile returns the file that stores a source, named by a hash of the
// source since sources may contain any character.
func (s *VectorStore) sourceFile(source string) string {
	sum := sha256.Sum256([]byte(source))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:16])+".json")
}

// stored returns a source as the in-memory store holds it, or nil if it
// holds no such source.
func (s *VectorStore) stored(ctx context.Context, source string) (*rag.SourceChunks, error) {
	infos, err := s.mem.Sources(ctx)
	if err != nil {
		return nil, err
	}
	for _, info := range infos {
		if info.Source != source {
			continue
		}
		chunks, err := s.mem.List(ctx, rag.Filter{Sources: []string{source}})
		if err != nil {
			return nil, err
		}
		return &rag.SourceChunks{Info: info, Chunks: chunks}, nil
	}
	return nil, nil
}

// Replace implements rag.VectorStore. Each source replaces the source in
// memory, which validates it, and is then written to its file; when the
// write fails the previous source is restored. Sources replaced before a
// failing one stay replaced.
func (s *VectorStore) Replace(ctx context.Context, sources ...rag.SourceChunks) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, src := range sources {
		previous, err := s.stored(ctx, src.Info.Source)
		if err != nil {
			return err
		}
		if err := s.mem.Replace(ctx, src); err != nil {
			return err
		}
		current, err := s.stored(ctx, src.Info.Source)
		if err == nil {
			err = jsonfile.WriteJSONFile(s.sourceFile(src.Info.Source), current)
		}
		if err != nil {
			if previous != nil {
				_ = s.mem.Replace(ctx, *previous)
			} else {
				_ = s.mem.Delete(ctx, src.Info.Source)
			}
			return err
		}
	}
	return nil
}

// Delete implements rag.VectorStore.
func (s *VectorStore) Delete(ctx context.Context, sources ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, src := range sources {
		if err := os.Remove(s.sourceFile(src)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := s.mem.Delete(ctx, src); err != nil {
			return err
		}
	}
	return nil
}

// Sources implements rag.VectorStore.
func (s *VectorStore) Sources(ctx context.Context) ([]rag.SourceInfo, error) {
	return s.mem.Sources(ctx)
}

// List implements rag.VectorStore.
func (s *VectorStore) List(ctx context.Context, f rag.Filter) ([]*rag.Chunk, error) {
	return s.mem.List(ctx, f)
}

// Search implements rag.VectorStore.
func (s *VectorStore) Search(ctx context.Context, vector []float32, limit int, f rag.Filter) ([]rag.Hit, error) {
	return s.mem.Search(ctx, vector, limit, f)
}

// Revision implements rag.Revisioner.
func (s *VectorStore) Revision() uint64 { return s.mem.Revision() }
