package filestore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nostosx/agentx-contrib/filestore"
	"github.com/nostosx/agentx/model"
	"github.com/nostosx/agentx/session"
)

func testStore(t *testing.T, store session.Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Get(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Get missing = %v", err)
	}
	stamp := time.Unix(100, 0).UTC()
	for _, id := range []string{"b", "a", "newest"} {
		updated := stamp
		if id == "newest" {
			updated = stamp.Add(time.Second)
		}
		s := session.FromSnapshot(&session.Snapshot{ID: id, Messages: []*model.Message{model.UserText(id)}, State: map[string]any{"value": "saved"}, CreatedAt: stamp, UpdatedAt: updated})
		if err := store.Save(ctx, s); err != nil {
			t.Fatal(err)
		}
		s.Append(model.AssistantText("unsaved"))
		s.State().Set("value", "unsaved")
	}
	infos, err := store.List(ctx)
	if err != nil || len(infos) != 3 {
		t.Fatalf("List = %#v, %v", infos, err)
	}
	for i, id := range []string{"newest", "a", "b"} {
		if infos[i].ID != id || infos[i].Messages != 1 || !infos[i].CreatedAt.Equal(stamp) {
			t.Fatalf("List[%d] = %#v, want %s with one message", i, infos[i], id)
		}
	}
	got, err := store.Get(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages()) != 1 || got.Messages()[0].Text() != "a" {
		t.Fatalf("saved messages = %#v", got.Messages())
	}
	if v, _ := got.State().Get("value"); v != "saved" {
		t.Fatalf("saved value = %v", v)
	}
	got.Replace([]*model.Message{model.UserText("reader mutation")})
	got.State().Set("value", "reader mutation")
	again, err := store.Get(ctx, "a")
	if err != nil || len(again.Messages()) != 1 || again.Messages()[0].Text() != "a" {
		t.Fatalf("Get after reader mutation = %#v, %v", again, err)
	}
	if v, _ := again.State().Get("value"); v != "saved" {
		t.Fatalf("stored state changed through reader: %v", v)
	}
	if err := store.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "a"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Get deleted = %v", err)
	}
}

func TestSessions(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	store, err := filestore.Sessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	testStore(t, store)
	reopened, err := filestore.Sessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.Get(t.Context(), "newest"); err != nil || len(got.Messages()) != 1 || got.Messages()[0].Text() != "newest" {
		t.Fatalf("reopened Get = %#v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(t.Context(), "broken"); err == nil {
		t.Fatal("corrupt file loaded without error")
	}
	infos, err := store.List(t.Context())
	if err != nil || len(infos) != 2 {
		t.Fatalf("List should skip corrupt files: %#v, %v", infos, err)
	}
}

func TestSessionsErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := filestore.Sessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../escape", "", "a/b"} {
		t.Run(id, func(t *testing.T) {
			if _, err := store.Get(t.Context(), id); err == nil {
				t.Fatal("Get accepted invalid ID")
			}
			if err := store.Delete(t.Context(), id); err == nil {
				t.Fatal("Delete accepted invalid ID")
			}
		})
	}
	if err := store.Save(t.Context(), session.New("../escape")); err == nil {
		t.Fatal("Save accepted path traversal")
	}
	s := session.New("unserializable")
	s.State().Set("bad", make(chan int))
	if err := store.Save(t.Context(), s); err == nil {
		t.Fatal("Save accepted unserializable state")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := filestore.Sessions(filepath.Join(file, "child")); err == nil {
		t.Fatal("Sessions accepted file as directory")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(t.Context()); err == nil {
		t.Fatal("List accepted missing root")
	}
}

func TestSessionsConflictAcrossStoresAndLegacyFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "legacy.json"), []byte(`{"id":"legacy","messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	one, err := filestore.Sessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	two, err := filestore.Sessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := one.Get(t.Context(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	b, err := two.Get(t.Context(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := one.Save(t.Context(), a); err != nil {
		t.Fatalf("Save of legacy file = %v", err)
	}
	if err := two.Save(t.Context(), b); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("Save from second store = %v, want ErrConflict", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := one.Save(t.Context(), session.New("corrupt")); err != nil {
		t.Fatalf("Save over corrupt file = %v", err)
	}
}

func TestSessionsRejectsHiddenIDs(t *testing.T) {
	t.Parallel()
	store, err := filestore.Sessions(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), session.New(".hidden")); err == nil {
		t.Fatal("Save accepted an ID with a leading dot")
	}
	if err := store.Save(t.Context(), session.New("v1.2")); err != nil {
		t.Fatalf("Save rejected an inner dot: %v", err)
	}
}
