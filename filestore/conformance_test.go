package filestore_test

import (
	"path/filepath"
	"testing"

	"github.com/nostosx/agentx-contrib/filestore"
	"github.com/nostosx/agentx/loop"
	"github.com/nostosx/agentx/loop/checkpointtest"
	"github.com/nostosx/agentx/memory"
	"github.com/nostosx/agentx/memory/memorytest"
	"github.com/nostosx/agentx/rag"
	"github.com/nostosx/agentx/rag/ragtest"
	"github.com/nostosx/agentx/session"
	"github.com/nostosx/agentx/session/sessiontest"
	"github.com/nostosx/agentx/workflow"
	workflowcheckpointtest "github.com/nostosx/agentx/workflow/checkpointtest"
)

func TestSessionsConformance(t *testing.T) {
	t.Parallel()
	sessiontest.Run(t, func(t *testing.T) session.Store {
		s, err := filestore.Sessions(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestCheckpointsConformance(t *testing.T) {
	t.Parallel()
	checkpointtest.Run(t, func(t *testing.T) loop.Checkpointer {
		cp, err := filestore.Checkpoints(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return cp
	})
}

func TestWorkflowCheckpointsConformance(t *testing.T) {
	t.Parallel()
	workflowcheckpointtest.Run(t, func(t *testing.T) workflow.Checkpointer {
		cp, err := filestore.WorkflowCheckpoints(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return cp
	})
}

func TestMemoryConformance(t *testing.T) {
	t.Parallel()
	memorytest.Run(t, func(t *testing.T) memory.Store {
		s, err := filestore.Memory(filepath.Join(t.TempDir(), "memory.json"))
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestVectorsConformance(t *testing.T) {
	t.Parallel()
	ragtest.Run(t, func(t *testing.T) rag.VectorStore {
		s, err := filestore.Vectors(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}
