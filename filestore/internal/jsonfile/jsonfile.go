// Package jsonfile replaces JSON files through os.Rename and keeps one
// JSON file per ID in a directory.
package jsonfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nostosx/kitx/atomicfile"

	"github.com/nostosx/agentx-contrib/filestore/internal/filelock"
)

// WriteFileAtomic writes data to a temporary file in the same directory and
// renames it over path. Replacement is atomic on Unix; other platforms follow
// os.Rename's guarantees. The parent directory must be trusted. The file is
// synced before replacement, but the directory is not synced afterwards.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return atomicfile.WriteFile(path, data, perm)
}

// WriteJSONFile encodes v as indented JSON and replaces path using
// WriteFileAtomic with mode 0600, creating the parent directory if needed.
func WriteJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return WriteFileAtomic(path, b, 0o600)
}

// JSONDir stores values of type T as one JSON file per ID in a directory.
//
// On a case-insensitive file system, IDs differing only in case name the
// same file. When the values record their ID (see NewJSONDir), Load and
// Delete ignore a file holding another ID and Save refuses to replace it,
// so such IDs are never confused.
// Each operation holds a persistent sidecar lock, including reads: Windows
// cannot replace a file while another handle has it open. OS locks also
// coordinate separate instances and alternate paths to the same directory.
type JSONDir[T any] struct {
	dir      string
	kind     string
	pattern  *regexp.Regexp
	notFound error
	idOf     func(*T) string
	// folds is set when the file system may fold the case of names.
	folds bool
}

// NewJSONDir returns a JSONDir rooted at dir, creating it if needed. IDs must
// match pattern; kind names the values in error messages and notFound is
// returned by Load for missing IDs. idOf, if not nil, returns the ID a
// value records, or "" for values that do not record it.
func NewJSONDir[T any](dir, kind string, pattern *regexp.Regexp, notFound error, idOf func(*T) string) (*JSONDir[T], error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	d := &JSONDir[T]{dir: dir, kind: kind, pattern: pattern, notFound: notFound, idOf: idOf}
	d.folds = idOf != nil && foldsCase(dir)
	return d, nil
}

// foldsCase reports whether names in dir may be case-insensitive. It
// assumes they are when it cannot tell.
func foldsCase(dir string) bool {
	f, err := os.CreateTemp(dir, ".case-probe-*")
	if err != nil {
		return true
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)
	upper := filepath.Join(filepath.Dir(name), strings.ToUpper(filepath.Base(name)))
	_, err = os.Lstat(upper)
	return err == nil
}

func (d *JSONDir[T]) path(id string) (string, error) {
	if !d.pattern.MatchString(id) || strings.Trim(id, ".") == "" {
		return "", fmt.Errorf("invalid %s id %q", d.kind, id)
	}
	return filepath.Join(d.dir, id+".json"), nil
}

// Load decodes the value stored under id.
func (d *JSONDir[T]) Load(ctx context.Context, id string) (*T, error) {
	p, err := d.path(id)
	if err != nil {
		return nil, err
	}
	lock, err := filelock.Acquire(ctx, p+".lock")
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, d.notFound
	}
	if err != nil {
		return nil, err
	}
	v := new(T)
	if err := json.Unmarshal(b, v); err != nil {
		return nil, fmt.Errorf("decode %s %s: %w", d.kind, id, err)
	}
	if d.idOf != nil {
		if stored := d.idOf(v); stored != "" && stored != id {
			// The file system folded the case of id.
			return nil, d.notFound
		}
	}
	return v, nil
}

// conflictingID returns the ID of a value stored in the file of id under another
// ID, which happens on case-insensitive file systems, or "".
func (d *JSONDir[T]) conflictingID(id, path string) string {
	if !d.folds {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	v := new(T)
	if json.Unmarshal(b, v) != nil {
		return ""
	}
	if stored := d.idOf(v); stored != id {
		return stored
	}
	return ""
}

// Save stores v under id using WriteJSONFile's replacement guarantees.
func (d *JSONDir[T]) Save(ctx context.Context, id string, v *T) error {
	p, err := d.path(id)
	if err != nil {
		return err
	}
	lock, err := filelock.Acquire(ctx, p+".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	if conflict := d.conflictingID(id, p); conflict != "" {
		return fmt.Errorf("%s id %q: the file system does not distinguish it from stored %s %q", d.kind, id, d.kind, conflict)
	}
	return WriteJSONFile(p, v)
}

// Delete removes the value stored under id. Missing IDs are not an error.
func (d *JSONDir[T]) Delete(ctx context.Context, id string) error {
	p, err := d.path(id)
	if err != nil {
		return err
	}
	lock, err := filelock.Acquire(ctx, p+".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	if d.conflictingID(id, p) != "" {
		return nil
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// IDs returns the IDs of the stored values in ascending filename order.
func (d *JSONDir[T]) IDs() ([]string, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok || strings.HasPrefix(id, ".") {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}
