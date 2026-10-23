package filestore

import (
	"context"
	"regexp"

	"github.com/nostosx/agentx-contrib/filestore/internal/jsonfile"
	"github.com/nostosx/agentx/loop"
	"github.com/nostosx/agentx/workflow"
)

// The run IDs loop.Checkpointer and workflow.Checkpointer accept.
var (
	runIDPattern         = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	workflowRunIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// CheckpointStore stores each run checkpoint as a JSON file in a
// directory.
type CheckpointStore struct {
	dir *jsonfile.JSONDir[loop.State]
}

var _ loop.Checkpointer = (*CheckpointStore)(nil)

// Checkpoints returns a run checkpointer rooted at dir, creating it if
// needed.
func Checkpoints(dir string) (*CheckpointStore, error) {
	d, err := jsonfile.NewJSONDir(dir, "checkpoint", runIDPattern, loop.ErrCheckpointNotFound, func(st *loop.State) string { return st.RunID })
	if err != nil {
		return nil, err
	}
	return &CheckpointStore{dir: d}, nil
}

// Save implements loop.Checkpointer.
func (c *CheckpointStore) Save(_ context.Context, st *loop.State) error {
	return c.dir.Save(st.RunID, st)
}

// Load implements loop.Checkpointer.
func (c *CheckpointStore) Load(_ context.Context, runID string) (*loop.State, error) {
	return c.dir.Load(runID)
}

// Delete implements loop.Checkpointer.
func (c *CheckpointStore) Delete(_ context.Context, runID string) error { return c.dir.Delete(runID) }

// WorkflowCheckpointStore stores each workflow checkpoint as a JSON file in
// a directory.
type WorkflowCheckpointStore struct {
	dir *jsonfile.JSONDir[workflow.Checkpoint]
}

var _ workflow.Checkpointer = (*WorkflowCheckpointStore)(nil)

// WorkflowCheckpoints returns a workflow checkpointer rooted at dir,
// creating it if needed.
func WorkflowCheckpoints(dir string) (*WorkflowCheckpointStore, error) {
	d, err := jsonfile.NewJSONDir(dir, "workflow checkpoint", workflowRunIDPattern, workflow.ErrCheckpointNotFound, func(cp *workflow.Checkpoint) string { return cp.RunID })
	if err != nil {
		return nil, err
	}
	return &WorkflowCheckpointStore{dir: d}, nil
}

// Save implements workflow.Checkpointer.
func (c *WorkflowCheckpointStore) Save(_ context.Context, cp *workflow.Checkpoint) error {
	return c.dir.Save(cp.RunID, cp)
}

// Load implements workflow.Checkpointer.
func (c *WorkflowCheckpointStore) Load(_ context.Context, runID string) (*workflow.Checkpoint, error) {
	return c.dir.Load(runID)
}

// Delete implements workflow.Checkpointer.
func (c *WorkflowCheckpointStore) Delete(_ context.Context, runID string) error {
	return c.dir.Delete(runID)
}

// IDs returns the run IDs of the stored checkpoints, such as runs to resume
// after a restart, in ascending filename order.
func (c *WorkflowCheckpointStore) IDs() ([]string, error) { return c.dir.IDs() }
