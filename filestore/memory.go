package filestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nostosx/agentx-contrib/filestore/internal/filelock"
	"github.com/nostosx/agentx-contrib/filestore/internal/jsonfile"
	"github.com/nostosx/agentx/memory"
)

// MemoryStore keeps memory entries in one JSON file. Operations reload the
// file under an OS lock, so independent stores and processes see completed
// writes and cannot overwrite each other's unrelated changes.
//
// The persistent path + ".lock" sidecar must not be removed while any
// store is in use. Use a local filesystem supporting OS file locks and
// atomic replacement; processes writing this file must all use Memory.
type MemoryStore struct {
	path string
}

var (
	_ memory.Store         = (*MemoryStore)(nil)
	_ memory.ScopedDeleter = (*MemoryStore)(nil)
)

// Memory validates the entries stored at path, which is created on first
// write. Reads report file errors rather than returning a stale snapshot.
func Memory(path string) (*MemoryStore, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	s := &MemoryStore{path: path}
	if _, err := s.load(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *MemoryStore) load(ctx context.Context) (*memory.MemoryStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mem := memory.NewMemoryStore()
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return mem, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []*memory.Entry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("decode memory file %s: %w", s.path, err)
	}
	for _, e := range entries {
		if e == nil {
			return nil, fmt.Errorf("memory file %s: null entry", s.path)
		}
		if err := mem.Put(ctx, e); err != nil {
			return nil, fmt.Errorf("memory file %s: %w", s.path, err)
		}
	}
	return mem, nil
}

// Put implements memory.Store.
func (s *MemoryStore) Put(ctx context.Context, e *memory.Entry) error {
	if e == nil {
		return errors.New("memory: nil entry")
	}
	return s.update(ctx, func(mem *memory.MemoryStore) error { return mem.Put(ctx, e) })
}

// Get implements memory.Store.
func (s *MemoryStore) Get(ctx context.Context, id string) (*memory.Entry, error) {
	mem, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	return mem.Get(ctx, id)
}

// Delete implements memory.Store.
func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	return s.DeleteInScope(ctx, id, "")
}

// DeleteInScope implements memory.ScopedDeleter: it atomically checks the
// current scope and persists the deletion. An empty scope allows deletion
// of any entry.
func (s *MemoryStore) DeleteInScope(ctx context.Context, id, scope string) error {
	return s.update(ctx, func(mem *memory.MemoryStore) error { return mem.DeleteInScope(ctx, id, scope) })
}

// List implements memory.Store.
func (s *MemoryStore) List(ctx context.Context, f memory.Filter) ([]*memory.Entry, error) {
	mem, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	return mem.List(ctx, f)
}

func (s *MemoryStore) read(ctx context.Context) (*memory.MemoryStore, error) {
	lock, err := filelock.Acquire(ctx, s.path+".lock")
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	return s.load(ctx)
}

func (s *MemoryStore) update(ctx context.Context, apply func(*memory.MemoryStore) error) error {
	lock, err := filelock.Acquire(ctx, s.path+".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	mem, err := s.load(ctx)
	if err != nil {
		return err
	}
	if err := apply(mem); err != nil {
		return err
	}
	entries, err := mem.List(ctx, memory.Filter{})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return jsonfile.WriteJSONFile(s.path, entries)
}
