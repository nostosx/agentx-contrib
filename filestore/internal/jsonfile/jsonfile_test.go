package jsonfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"testing"

	"github.com/nostosx/agentx-contrib/filestore/internal/jsonfile"
)

func TestWriteJSONFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "value.json")
	if err := jsonfile.WriteJSONFile(path, map[string]int{"value": 1}); err != nil {
		t.Fatal(err)
	}
	if err := jsonfile.WriteJSONFile(path, map[string]int{"value": 2}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{\n  \"value\": 2\n}" {
		t.Fatalf("file = %s, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o", info.Mode().Perm())
	}
	if err := jsonfile.WriteJSONFile(path, make(chan int)); err == nil {
		t.Fatal("unencodable value accepted")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "{\n  \"value\": 2\n}" {
		t.Fatalf("failed write changed published data: %s, %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v, %v", entries, err)
	}
}

func TestJSONDir(t *testing.T) {
	t.Parallel()
	notFound := errors.New("not found")
	dir := t.TempDir()
	store, err := jsonfile.NewJSONDir[map[string]int](dir, "record", regexp.MustCompile(`^[a-z.]+$`), notFound, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("missing"); !errors.Is(err, notFound) {
		t.Fatalf("missing error = %v", err)
	}
	value := map[string]int{"n": 1}
	if err := store.Save("a", &value); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load("a")
	if err != nil || !reflect.DeepEqual(*got, value) {
		t.Fatalf("record = %v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ignored.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "folder.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if ids, err := store.IDs(); err != nil || !reflect.DeepEqual(ids, []string{"a"}) {
		t.Fatalf("IDs = %v, %v", ids, err)
	}
	for _, tc := range []struct{ name, id string }{{name: "traversal", id: "../escape"}, {name: "empty"}, {name: "dots", id: "..."}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.Save(tc.id, &value); err == nil {
				t.Fatal("invalid ID saved")
			}
			if _, err := store.Load(tc.id); err == nil {
				t.Fatal("invalid ID loaded")
			}
			if err := store.Delete(tc.id); err == nil {
				t.Fatal("invalid ID deleted")
			}
		})
	}
	if err := store.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("a"); err != nil {
		t.Fatalf("missing delete = %v", err)
	}
}

func TestWriteFileAtomicFailureCleansUp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "occupied")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := jsonfile.WriteFileAtomic(target, []byte("data"), 0o600); err == nil {
		t.Fatal("replaced directory with file")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "occupied" {
		t.Fatalf("temporary files leaked: %v, %v", entries, err)
	}
}
