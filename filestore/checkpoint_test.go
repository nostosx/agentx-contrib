package filestore_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/nostosx/agentx-contrib/filestore"
	"github.com/nostosx/agentx/loop"
	"github.com/nostosx/agentx/model"
	"github.com/nostosx/agentx/workflow"
)

func testCheckpointer(t *testing.T, cp loop.Checkpointer) {
	t.Helper()
	if _, err := cp.Load(t.Context(), "missing"); !errors.Is(err, loop.ErrCheckpointNotFound) {
		t.Fatalf("Load missing = %v", err)
	}
	st := &loop.State{RunID: "run-1", Status: loop.StatusInterrupted, Messages: []*model.Message{model.UserText("original")}, Values: map[string]string{"key": "value"}, Pending: []*loop.PendingCall{{Call: &model.ToolCall{ID: "call", Name: "echo", Arguments: []byte(`{"text":"original"}`)}, NeedsApproval: true}}}
	want := st.Clone()
	if err := cp.Save(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	st.Messages[0].Parts[0].Text = "caller mutation"
	st.Values["key"] = "caller mutation"
	st.Pending[0].Call.Arguments[0] = 'x'
	got, err := cp.Load(t.Context(), "run-1")
	if err != nil || !reflect.DeepEqual(got.Clone(), want) {
		t.Fatalf("Load = %+v, %v; want %+v", got, err, want)
	}
	got.Messages[0].Parts[0].Text = "reader mutation"
	got.Values["key"] = "reader mutation"
	again, err := cp.Load(t.Context(), "run-1")
	if err != nil || !reflect.DeepEqual(again.Clone(), want) {
		t.Fatalf("checkpoint changed through reader: %+v, %v", again, err)
	}
	again.Status = loop.StatusStopped
	if err := cp.Save(t.Context(), again); err != nil {
		t.Fatal(err)
	}
	if updated, err := cp.Load(t.Context(), "run-1"); err != nil || updated.Status != loop.StatusStopped {
		t.Fatalf("overwrite = %+v, %v", updated, err)
	}
	if err := cp.Delete(t.Context(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := cp.Load(t.Context(), "run-1"); !errors.Is(err, loop.ErrCheckpointNotFound) {
		t.Fatalf("Load deleted = %v", err)
	}
}

func TestCheckpoints(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "checkpoints")
	cp, err := filestore.Checkpoints(dir)
	if err != nil {
		t.Fatal(err)
	}
	testCheckpointer(t, cp)
	if err := cp.Save(t.Context(), &loop.State{RunID: "persisted", Status: loop.StatusInterrupted}); err != nil {
		t.Fatal(err)
	}
	reopened, err := filestore.Checkpoints(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := reopened.Load(t.Context(), "persisted"); err != nil || st.Status != loop.StatusInterrupted {
		t.Fatalf("reopened checkpoint = %+v, %v", st, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cp.Load(t.Context(), "broken"); err == nil {
		t.Fatal("Load accepted malformed checkpoint")
	}
}

func TestCheckpointsErrors(t *testing.T) {
	t.Parallel()
	cp, err := filestore.Checkpoints(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../escape", "dot.name"} {
		t.Run(id, func(t *testing.T) {
			if err := cp.Save(t.Context(), &loop.State{RunID: id}); err == nil {
				t.Fatal("Save accepted invalid run ID")
			}
			if _, err := cp.Load(t.Context(), id); err == nil {
				t.Fatal("Load accepted invalid run ID")
			}
			if err := cp.Delete(t.Context(), id); err == nil {
				t.Fatal("Delete accepted invalid run ID")
			}
		})
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := filestore.Checkpoints(filepath.Join(file, "child")); err == nil {
		t.Fatal("Checkpoints accepted file as directory")
	}
}

func TestWorkflowCheckpoints(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "runs")
	cp, err := filestore.WorkflowCheckpoints(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../escape", "dot.name"} {
		if cp.Save(t.Context(), &workflow.Checkpoint{RunID: id}) == nil {
			t.Errorf("Save accepted run ID %q", id)
		}
	}
	if err := cp.Save(t.Context(), &workflow.Checkpoint{RunID: "listed", Status: workflow.StatusSuspended}); err != nil {
		t.Fatal(err)
	}
	if ids, err := cp.IDs(); err != nil || !slices.Equal(ids, []string{"listed"}) {
		t.Fatalf("IDs = %v, %v", ids, err)
	}
	reopened, err := filestore.WorkflowCheckpoints(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := reopened.Load(t.Context(), "listed"); err != nil || c.Status != workflow.StatusSuspended {
		t.Fatalf("reopened checkpoint = %+v, %v", c, err)
	}
	if _, err := filestore.WorkflowCheckpoints(filepath.Join(dir, "listed.json", "child")); err == nil {
		t.Fatal("WorkflowCheckpoints accepted file as directory")
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cp.Load(t.Context(), "broken"); err == nil || errors.Is(err, workflow.ErrCheckpointNotFound) {
		t.Fatalf("Load of malformed checkpoint = %v", err)
	}
}
