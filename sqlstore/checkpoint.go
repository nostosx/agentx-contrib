package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/nostosx/agentx/loop"
	"github.com/nostosx/agentx/workflow"
)

// The run IDs loop.Checkpointer and workflow.Checkpointer accept.
var (
	runIDPattern         = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	workflowRunIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// documents is a table of JSON documents keyed by run ID.
type documents struct {
	c        config
	name     string
	kind     string
	pattern  *regexp.Regexp
	keySize  int
	notFound error
}

func (d documents) migrate(ctx context.Context) error {
	return d.c.migrate(ctx, d.c.createTable(d.name, []string{
		"run_id " + d.c.keyType(d.keySize) + " NOT NULL",
		"updated_at " + timeType + " NOT NULL",
		"data " + d.c.textType() + " NOT NULL",
	}, "run_id"))
}

func (d documents) check(id string) error {
	if d.c.err != nil {
		return d.c.err
	}
	if !d.pattern.MatchString(id) {
		return fmt.Errorf("invalid %s id %q", d.kind, id)
	}
	return nil
}

func (d documents) save(ctx context.Context, id string, updated time.Time, v any) error {
	if err := d.check(id); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = d.c.exec(ctx, d.c.upsert(d.name, []string{"run_id", "updated_at", "data"}, "run_id"), id, formatTime(updated), string(data))
	return err
}

func (d documents) load(ctx context.Context, id string, v any) error {
	if err := d.check(id); err != nil {
		return err
	}
	var data []byte
	err := d.c.queryRow(ctx, "SELECT data FROM "+d.c.table(d.name)+" WHERE run_id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return d.notFound
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("decode %s %s: %w", d.kind, id, err)
	}
	return nil
}

func (d documents) delete(ctx context.Context, id string) error {
	if err := d.check(id); err != nil {
		return err
	}
	_, err := d.c.exec(ctx, "DELETE FROM "+d.c.table(d.name)+" WHERE run_id = ?", id)
	return err
}

func (d documents) ids(ctx context.Context) ([]string, error) {
	if d.c.err != nil {
		return nil, d.c.err
	}
	rows, err := d.c.query(ctx, "SELECT run_id FROM "+d.c.table(d.name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Sort(ids)
	return ids, nil
}

// CheckpointStore is a loop.Checkpointer in a SQL table. It accepts
// the run IDs loop.Checkpointer documents.
type CheckpointStore struct {
	c config
}

var _ loop.Checkpointer = (*CheckpointStore)(nil)

// Checkpoints returns a run checkpoint store in db.
func Checkpoints(db DB, opts ...Option) *CheckpointStore {
	return &CheckpointStore{c: newConfig(db, opts)}
}

func (s *CheckpointStore) docs() documents {
	return documents{c: s.c, name: "checkpoints", kind: "checkpoint", pattern: runIDPattern, keySize: 128, notFound: loop.ErrCheckpointNotFound}
}

// Migrate creates the checkpoints table if it does not exist.
func (s *CheckpointStore) Migrate(ctx context.Context) error { return s.docs().migrate(ctx) }

// Save implements loop.Checkpointer.
func (s *CheckpointStore) Save(ctx context.Context, st *loop.State) error {
	return s.docs().save(ctx, st.RunID, st.UpdatedAt, st)
}

// Load implements loop.Checkpointer.
func (s *CheckpointStore) Load(ctx context.Context, runID string) (*loop.State, error) {
	var st loop.State
	if err := s.docs().load(ctx, runID, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Delete implements loop.Checkpointer. Deleting a missing checkpoint
// succeeds.
func (s *CheckpointStore) Delete(ctx context.Context, runID string) error {
	return s.docs().delete(ctx, runID)
}

// IDs returns the IDs of the stored runs in ascending order.
func (s *CheckpointStore) IDs(ctx context.Context) ([]string, error) { return s.docs().ids(ctx) }

// WorkflowCheckpointStore is a workflow.Checkpointer in a SQL table. It
// accepts the run IDs workflow.MemoryCheckpointer does.
type WorkflowCheckpointStore struct {
	c config
}

var _ workflow.Checkpointer = (*WorkflowCheckpointStore)(nil)

// WorkflowCheckpoints returns a workflow checkpoint store in db.
func WorkflowCheckpoints(db DB, opts ...Option) *WorkflowCheckpointStore {
	return &WorkflowCheckpointStore{c: newConfig(db, opts)}
}

func (s *WorkflowCheckpointStore) docs() documents {
	return documents{c: s.c, name: "workflow_checkpoints", kind: "workflow checkpoint", pattern: workflowRunIDPattern, keySize: 64, notFound: workflow.ErrCheckpointNotFound}
}

// Migrate creates the workflow_checkpoints table if it does not exist.
func (s *WorkflowCheckpointStore) Migrate(ctx context.Context) error { return s.docs().migrate(ctx) }

// Save implements workflow.Checkpointer.
func (s *WorkflowCheckpointStore) Save(ctx context.Context, cp *workflow.Checkpoint) error {
	return s.docs().save(ctx, cp.RunID, cp.UpdatedAt, cp)
}

// Load implements workflow.Checkpointer.
func (s *WorkflowCheckpointStore) Load(ctx context.Context, runID string) (*workflow.Checkpoint, error) {
	var cp workflow.Checkpoint
	if err := s.docs().load(ctx, runID, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

// Delete implements workflow.Checkpointer. Deleting a missing checkpoint
// succeeds.
func (s *WorkflowCheckpointStore) Delete(ctx context.Context, runID string) error {
	return s.docs().delete(ctx, runID)
}

// IDs returns the IDs of the stored runs in ascending order, for example
// to resume them after a restart.
func (s *WorkflowCheckpointStore) IDs(ctx context.Context) ([]string, error) {
	return s.docs().ids(ctx)
}
