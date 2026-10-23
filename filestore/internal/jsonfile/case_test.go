package jsonfile

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

type record struct {
	ID    string `json:"id"`
	Value int    `json:"value"`
}

// TestJSONDirCaseFolding simulates a case-insensitive file system, where
// the file of one ID holds a value saved under an ID differing in case.
func TestJSONDirCaseFolding(t *testing.T) {
	t.Parallel()
	notFound := errors.New("not found")
	dir := t.TempDir()
	d, err := NewJSONDir(dir, "record", regexp.MustCompile(`^[A-Za-z]+$`), notFound, func(r *record) string { return r.ID })
	if err != nil {
		t.Fatal(err)
	}
	d.folds = true
	// The file of "abc" holds the value saved as "ABC".
	if err := os.WriteFile(filepath.Join(dir, "abc.json"), []byte(`{"id":"ABC","value":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Load("abc"); !errors.Is(err, notFound) {
		t.Fatalf("Load of a folded ID = %v, want not found", err)
	}
	if err := d.Save("abc", &record{ID: "abc", Value: 2}); err == nil {
		t.Fatal("Save replaced the value of another ID")
	}
	if err := d.Delete("abc"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "abc.json")); err != nil || string(b) != `{"id":"ABC","value":1}` {
		t.Fatalf("value of another ID changed: %s, %v", b, err)
	}
	// Values of the same ID, and values that do not record theirs, are
	// used as before.
	if err := d.Save("same", &record{ID: "same", Value: 3}); err != nil {
		t.Fatal(err)
	}
	if err := d.Save("same", &record{ID: "same", Value: 4}); err != nil {
		t.Fatal(err)
	}
	if r, err := d.Load("same"); err != nil || r.Value != 4 {
		t.Fatalf("Load = %+v, %v", r, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy.json"), []byte(`{"value":5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := d.Load("legacy"); err != nil || r.Value != 5 {
		t.Fatalf("Load of a value without ID = %+v, %v", r, err)
	}
	if err := d.Delete("legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Load("legacy"); !errors.Is(err, notFound) {
		t.Fatalf("Load deleted = %v", err)
	}
}

func TestFoldsCase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// The probe leaves nothing behind, whatever the file system.
	foldsCase(dir)
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("probe left %v, %v", entries, err)
	}
	if !foldsCase(filepath.Join(dir, "missing")) {
		t.Fatal("a directory that cannot be probed must be assumed to fold case")
	}
}
