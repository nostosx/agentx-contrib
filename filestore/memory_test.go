package filestore_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nostosx/agentx-contrib/filestore"
	"github.com/nostosx/agentx/memory"
)

type scopedStore interface {
	memory.Store
	DeleteInScope(context.Context, string, string) error
}

func testDeleteInScope(t *testing.T, newStore func(*testing.T) (scopedStore, func() memory.Store)) {
	t.Helper()
	for _, tt := range []struct {
		name, id, scope string
		allowed         bool
	}{
		{"matching scope", "fact", "project", true},
		{"other scope", "fact", "other", false},
		{"empty scope", "fact", "", true},
		{"missing ID", "missing", "project", false},
		{"missing ID empty scope", "missing", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, reopen := newStore(t)
			original := &memory.Entry{ID: "fact", Scope: "project", Content: "durable fact", Tags: []string{"go"}}
			if err := s.Put(t.Context(), original); err != nil {
				t.Fatal(err)
			}
			err := s.DeleteInScope(t.Context(), tt.id, tt.scope)
			if tt.allowed {
				if err != nil {
					t.Fatalf("allowed DeleteInScope() = %v", err)
				}
			} else if !errors.Is(err, memory.ErrNotFound) {
				t.Errorf("denied DeleteInScope() = %v, want ErrNotFound", err)
			}
			for _, check := range []struct {
				name  string
				store memory.Store
			}{
				{"current", s},
				{"reopened", reopen()},
			} {
				got, err := check.store.Get(t.Context(), "fact")
				if tt.allowed {
					if !errors.Is(err, memory.ErrNotFound) {
						t.Errorf("%s store retained deleted entry: %+v, %v", check.name, got, err)
					}
				} else if err != nil || !reflect.DeepEqual(got, original) {
					t.Errorf("%s store changed entry after denied deletion: %+v, %v", check.name, got, err)
				}
			}
		})
	}
}

func TestMemoryDeleteInScope(t *testing.T) {
	t.Parallel()
	testDeleteInScope(t, func(t *testing.T) (scopedStore, func() memory.Store) {
		path := filepath.Join(t.TempDir(), "memory.json")
		s, err := filestore.Memory(path)
		if err != nil {
			t.Fatal(err)
		}
		return s, func() memory.Store {
			reopened, err := filestore.Memory(path)
			if err != nil {
				t.Fatal(err)
			}
			return reopened
		}
	})
}

func TestMemoryPersistsWritesAndDeletes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "memory.json")
	s, err := filestore.Memory(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("opening missing store created file: %v", err)
	}
	e := &memory.Entry{ID: "fact", Content: "durable fact", Scope: "project", Tags: []string{"go"}, CreatedAt: time.Unix(1, 0).UTC()}
	if err := s.Put(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	reopened, err := filestore.Memory(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get(t.Context(), e.ID)
	if err != nil || !reflect.DeepEqual(got, e) {
		t.Fatalf("reopened Get() = %+v, %v; want %+v", got, err, e)
	}
	listed, err := reopened.List(t.Context(), memory.Filter{Scope: "project", Tags: []string{"go"}})
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], e) {
		t.Fatalf("reopened List() = %+v, %v", listed, err)
	}
	if err := reopened.Delete(t.Context(), e.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err = filestore.Memory(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Get(t.Context(), e.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("delete was not persisted: %v", err)
	}
	if err := reopened.Put(t.Context(), &memory.Entry{}); err == nil {
		t.Error("Memory accepted empty ID")
	}
	if err := reopened.Delete(t.Context(), e.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("deleting missing entry = %v", err)
	}
}

func TestMemoryOpenErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := filestore.Memory(dir); err == nil {
		t.Error("opening directory succeeded")
	}
	path := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := filestore.Memory(path); err == nil {
		t.Error("malformed JSON accepted")
	}
}

func TestMemoryFailedWritePreservesVisibleState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.json")
	s, err := filestore.Memory(path)
	if err != nil {
		t.Fatal(err)
	}
	original := &memory.Entry{ID: "existing", Content: "original", Scope: "project"}
	if err := s.Put(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A directory at the lock path prevents updates, even when running as root.
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	checkOriginal := func() {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, committed) {
			t.Errorf("failed update changed persisted JSON: %s", got)
		}
	}
	if err := s.Put(t.Context(), &memory.Entry{ID: "new", Content: "not durable"}); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	checkOriginal()
	if err := s.Put(t.Context(), &memory.Entry{ID: "existing", Content: "replacement"}); err == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	checkOriginal()
	if err := s.Delete(t.Context(), "existing"); err == nil {
		t.Fatal("delete unexpectedly succeeded")
	}
	checkOriginal()
	if err := s.DeleteInScope(t.Context(), "existing", "project"); err == nil {
		t.Fatal("scoped delete unexpectedly succeeded")
	}
	checkOriginal()
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	reopened, err := filestore.Memory(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range []*filestore.MemoryStore{s, reopened} {
		got, err := store.Get(t.Context(), "existing")
		if err != nil || !reflect.DeepEqual(got, original) {
			t.Errorf("failed update changed visible entry: %+v, %v", got, err)
		}
		if _, err := store.Get(t.Context(), "new"); !errors.Is(err, memory.ErrNotFound) {
			t.Errorf("failed Put exposed unpersisted entry: %v", err)
		}
	}
}

func TestMemoryIndependentStoresObserveCurrentFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "memory.json")
	a, err := filestore.Memory(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := filestore.Memory(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*filestore.MemoryStore{a, b} {
		id := fmt.Sprintf("fact-%p", s)
		if err := s.Put(t.Context(), &memory.Entry{ID: id, Scope: "old"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []*filestore.MemoryStore{a, b} {
		entries, err := s.List(t.Context(), memory.Filter{})
		if err != nil || len(entries) != 2 {
			t.Fatalf("independent stores lost writes: %v, %v", entries, err)
		}
	}
	if err := a.Put(t.Context(), &memory.Entry{ID: "fact", Scope: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Put(t.Context(), &memory.Entry{ID: "fact", Scope: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteInScope(t.Context(), "fact", "old"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("scoped deletion used stale ownership: %v", err)
	}
	if err := a.DeleteInScope(t.Context(), "fact", "new"); err != nil {
		t.Fatal(err)
	}
	if err := b.Put(t.Context(), &memory.Entry{ID: "unrelated"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get(t.Context(), "fact"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("deleted entry resurrected: %v", err)
	}
}

func TestMemoryReadErrorsDoNotServeStaleEntries(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "memory.json")
	s, err := filestore.Memory(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(t.Context(), &memory.Entry{ID: "fact"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(t.Context(), "fact"); err == nil {
		t.Fatal("Get served a stale entry after a read failure")
	}
	if _, err := s.List(t.Context(), memory.Filter{}); err == nil {
		t.Fatal("List served a stale snapshot after a read failure")
	}
	if err := s.Put(t.Context(), &memory.Entry{ID: "another"}); err == nil {
		t.Fatal("Put overwrote an unreadable current file")
	}
}

func startMemoryWriter(t *testing.T, path, id string) (io.WriteCloser, func()) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestMemoryProcessWriter$")
	cmd.Env = append(os.Environ(), "AGENTX_MEMORY_TEST_PATH="+path, "AGENTX_MEMORY_TEST_WRITER="+id)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close() })
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdout.Close() })
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	reader := bufio.NewReader(stdout)
	if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("writer startup = %q, %v; %s", line, err, stderr.String())
	}
	return stdin, func() {
		t.Helper()
		output, readErr := io.ReadAll(reader)
		if err := cmd.Wait(); err != nil || readErr != nil {
			t.Fatalf("writer = %v, read = %v; %s%s", err, readErr, output, stderr.String())
		}
	}
}

func TestMemoryConcurrentProcessesAndDeletion(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "memory.json")
	s, err := filestore.Memory(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(t.Context(), &memory.Entry{ID: "forget", Scope: "private"}); err != nil {
		t.Fatal(err)
	}
	var starts []io.WriteCloser
	var waits []func()
	for i := range 3 {
		start, wait := startMemoryWriter(t, path, fmt.Sprint(i))
		starts = append(starts, start)
		waits = append(waits, wait)
	}
	// Every writer has already opened the old file. A later write must not
	// restore this deleted entry from its startup snapshot.
	if err := s.DeleteInScope(t.Context(), "forget", "private"); err != nil {
		t.Fatal(err)
	}
	for _, start := range starts {
		if _, err := io.WriteString(start, "start\n"); err != nil {
			t.Fatal(err)
		}
		if err := start.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, wait := range waits {
		wait()
	}
	entries, err := s.List(t.Context(), memory.Filter{})
	if err != nil || len(entries) != 3*20 {
		t.Fatalf("concurrent writers: count = %d, error = %v; want 60", len(entries), err)
	}
	if _, err := s.Get(t.Context(), "forget"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("subprocess write resurrected a deletion: %v", err)
	}
}

func TestMemoryProcessWriter(t *testing.T) {
	path := os.Getenv("AGENTX_MEMORY_TEST_PATH")
	if path == "" {
		t.Skip("subprocess helper")
	}
	s, err := filestore.Memory(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		id := fmt.Sprintf("%s-%d", os.Getenv("AGENTX_MEMORY_TEST_WRITER"), i)
		if err := s.Put(t.Context(), &memory.Entry{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
}
