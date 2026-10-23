package sqlstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nostosx/agentx-contrib/sqlstore"
	"github.com/nostosx/agentx/agent"
	"github.com/nostosx/agentx/loop"
	"github.com/nostosx/agentx/memory"
	"github.com/nostosx/agentx/model"
	"github.com/nostosx/agentx/model/modeltest"
	"github.com/nostosx/agentx/session"
	"github.com/nostosx/agentx/tool"
	"github.com/nostosx/agentx/workflow"
)

func TestMigrate(t *testing.T) {
	t.Parallel()
	forEach(t, func(t *testing.T, b *backend) {
		ctx := t.Context()
		opts := b.fresh(t)
		// Migrating again, per store or for all, leaves the tables as
		// they are.
		save(t, sqlstore.Sessions(b.db, opts...), "kept")
		if err := sqlstore.Migrate(ctx, b.db, opts...); err != nil {
			t.Fatalf("second Migrate = %v", err)
		}
		for _, m := range []interface{ Migrate(context.Context) error }{
			sqlstore.Sessions(b.db, opts...), sqlstore.Checkpoints(b.db, opts...),
			sqlstore.WorkflowCheckpoints(b.db, opts...), sqlstore.Memory(b.db, opts...),
		} {
			if err := m.Migrate(ctx); err != nil {
				t.Fatalf("store Migrate = %v", err)
			}
		}
		if _, err := sqlstore.Sessions(b.db, opts...).Get(ctx, "kept"); err != nil {
			t.Fatalf("Migrate lost data: %v", err)
		}
		// Each prefix has its own tables.
		other := b.fresh(t)
		if _, err := sqlstore.Sessions(b.db, other...).Get(ctx, "kept"); !errors.Is(err, session.ErrNotFound) {
			t.Fatalf("Get under another prefix = %v, want ErrNotFound", err)
		}
		// Without Migrate, the stores report the missing table.
		missing := b.options("missing_" + strings.ReplaceAll(t.Name(), "/", "_") + "_")
		if _, err := sqlstore.Sessions(b.db, missing...).Get(ctx, "kept"); err == nil || errors.Is(err, session.ErrNotFound) {
			t.Fatalf("Get without a table = %v", err)
		}
	})
}

func save(t *testing.T, store session.Store, id string) {
	t.Helper()
	s := session.New(id)
	s.Append(model.UserText(id))
	if err := store.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
}

// TestSessionConflictAcrossHandles checks conflict detection between
// separate connection pools, as between processes.
func TestSessionConflictAcrossHandles(t *testing.T) {
	t.Parallel()
	forEach(t, func(t *testing.T, b *backend) {
		ctx := t.Context()
		opts := b.fresh(t)
		one, two := sqlstore.Sessions(b.db, opts...), sqlstore.Sessions(b.open(t), opts...)
		save(t, one, "shared")
		a, err := one.Get(ctx, "shared")
		if err != nil {
			t.Fatal(err)
		}
		c, err := two.Get(ctx, "shared")
		if err != nil {
			t.Fatal(err)
		}
		a.Append(model.UserText("from one"))
		if err := one.Save(ctx, a); err != nil {
			t.Fatal(err)
		}
		c.Append(model.UserText("from two"))
		if err := two.Save(ctx, c); !errors.Is(err, session.ErrConflict) {
			t.Fatalf("Save from the second pool = %v, want ErrConflict", err)
		}
		// A store also works on a single connection or in a transaction.
		conn, err := b.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		save(t, sqlstore.Sessions(conn, opts...), "on-conn")
		tx, err := b.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		inTx := sqlstore.Sessions(tx, opts...)
		fresh, _, err := session.GetOrCreate(ctx, inTx, "on-conn")
		if err != nil {
			t.Fatal(err)
		}
		if err := inTx.Save(ctx, session.New("in-tx")); err != nil {
			t.Fatal(err)
		}
		if err := inTx.Save(ctx, fresh); err != nil {
			t.Fatalf("Save of a loaded session in a transaction = %v", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if _, err := one.Get(ctx, "in-tx"); !errors.Is(err, session.ErrNotFound) {
			t.Fatalf("rolled back session stored: %v", err)
		}
	})
}

func TestCorruptRows(t *testing.T) {
	t.Parallel()
	forEach(t, func(t *testing.T, b *backend) {
		ctx := t.Context()
		prefix := b.freshPrefix(t)
		opts := b.options(prefix)
		for _, stmt := range []string{
			"INSERT INTO " + prefix + "sessions (id, revision, messages, created_at, updated_at, data) VALUES ('bad', 1, 0, '2024-01-01T00:00:00.000000000Z', 'not a time', '{')",
			"INSERT INTO " + prefix + "checkpoints (run_id, updated_at, data) VALUES ('bad', '', '{')",
			"INSERT INTO " + prefix + "memories (id, scope, created_at, updated_at, data) VALUES ('bad', '', '', '', '{')",
		} {
			if _, err := b.db.ExecContext(ctx, stmt); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := sqlstore.Sessions(b.db, opts...).Get(ctx, "bad"); err == nil || errors.Is(err, session.ErrNotFound) {
			t.Errorf("Get of a corrupt session = %v", err)
		}
		if _, err := sqlstore.Sessions(b.db, opts...).List(ctx); err == nil {
			t.Error("List accepted a corrupt timestamp")
		}
		if _, err := sqlstore.Checkpoints(b.db, opts...).Load(ctx, "bad"); err == nil || errors.Is(err, loop.ErrCheckpointNotFound) {
			t.Errorf("Load of a corrupt checkpoint = %v", err)
		}
		if _, err := sqlstore.Memory(b.db, opts...).List(ctx, memory.Filter{}); err == nil {
			t.Error("List accepted a corrupt memory")
		}
	})
}

func TestRunnerResumesFromSQL(t *testing.T) {
	t.Parallel()
	forEach(t, func(t *testing.T, b *backend) {
		opts := b.fresh(t)
		var calls atomic.Int32
		write := tool.Func("write", "write", func(context.Context, struct{}) (string, error) { calls.Add(1); return "written", nil }, tool.RequireApproval())
		newRunner := func(m model.Model) *agent.Runner {
			a := agent.MustNew("assistant", agent.WithModel(m), agent.WithTools(write), agent.WithCheckpointer(sqlstore.Checkpoints(b.db, opts...)))
			return agent.NewRunner(a, sqlstore.Sessions(b.db, opts...))
		}
		m := modeltest.New(modeltest.CallTools(modeltest.Call{ID: "call", Name: "write"}), modeltest.Reply("done"))
		res, err := newRunner(m).RunText(t.Context(), "conversation", "write")
		if err != nil || res.Status != loop.StatusInterrupted || calls.Load() != 0 {
			t.Fatalf("Run = %+v, %v", res, err)
		}
		if _, err := sqlstore.Checkpoints(b.db, opts...).Load(t.Context(), res.RunID); err != nil {
			t.Fatalf("checkpoint of the interrupted run: %v", err)
		}
		// A new Runner, as after a restart, resumes from the database.
		res, err = loop.Collect(newRunner(m).Resume(t.Context(), "conversation", map[string]loop.Approval{"call": loop.Approve}), nil)
		if err != nil || res.Output != "done" || calls.Load() != 1 {
			t.Fatalf("Resume = %+v, %v; calls = %d", res, err, calls.Load())
		}
		saved, err := sqlstore.Sessions(b.db, opts...).Get(t.Context(), "conversation")
		if err != nil || len(saved.Messages()) != 4 {
			t.Fatalf("saved session = %v, %v", saved, err)
		}
		if _, err := sqlstore.Checkpoints(b.db, opts...).Load(t.Context(), res.RunID); !errors.Is(err, loop.ErrCheckpointNotFound) {
			t.Fatalf("checkpoint of the finished run = %v, want ErrCheckpointNotFound", err)
		}
	})
}

func TestWorkflowResumesFromSQL(t *testing.T) {
	t.Parallel()
	forEach(t, func(t *testing.T, b *backend) {
		opts := b.fresh(t)
		cps := sqlstore.WorkflowCheckpoints(b.db, opts...)
		build := func() *workflow.Workflow {
			return workflow.NewGraph().
				AddFunc("ask", func(ctx context.Context, st *workflow.State) error {
					v, err := workflow.Suspend(ctx, "approve?")
					if err != nil {
						return err
					}
					var answer string
					if err := json.Unmarshal(v, &answer); err != nil {
						return err
					}
					st.Set("answer", answer)
					return nil
				}).
				AddChain(workflow.Start, "ask", workflow.End).MustCompile(workflow.WithCheckpointer(cps))
		}
		if _, err := build().Run(t.Context(), workflow.NewState(nil), workflow.WithRunID("approval")); !errors.Is(err, workflow.ErrSuspended) {
			t.Fatalf("Run = %v, want a suspension", err)
		}
		if ids, err := cps.IDs(t.Context()); err != nil || !slices.Equal(ids, []string{"approval"}) {
			t.Fatalf("IDs = %v, %v", ids, err)
		}
		st, err := build().Resume(t.Context(), "approval", map[string]any{"ask": "yes"})
		if err != nil || st.String("answer") != "yes" {
			t.Fatalf("Resume = %v, %v", st, err)
		}
	})
}

func TestMemoryManagerOnSQL(t *testing.T) {
	t.Parallel()
	forEach(t, func(t *testing.T, b *backend) {
		ctx := t.Context()
		store := sqlstore.Memory(b.db, b.fresh(t)...)
		m := memory.New(store)
		kept, err := m.Remember(ctx, "The user prefers Go for services", memory.WithScope("alice"), memory.WithTags("pref"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Remember(ctx, "Bob prefers Rust", memory.WithScope("bob")); err != nil {
			t.Fatal(err)
		}
		hits, err := m.Recall(ctx, "which language for services", memory.InScope("alice"))
		if err != nil || len(hits) != 1 || hits[0].Entry.ID != kept.ID {
			t.Fatalf("Recall = %+v, %v", hits, err)
		}
		if err := m.ForgetInScope(ctx, kept.ID, "bob"); !errors.Is(err, memory.ErrNotFound) {
			t.Fatalf("ForgetInScope in another scope = %v, want ErrNotFound", err)
		}
		if err := m.ForgetInScope(ctx, kept.ID, "alice"); err != nil {
			t.Fatal(err)
		}
		if hits, err := m.Recall(ctx, "services", memory.InScope("alice")); err != nil || len(hits) != 0 {
			t.Fatalf("Recall after Forget = %+v, %v", hits, err)
		}
	})
}

// TestHandoffMediaAndRewindOnSQL keeps a handed-off run whose tool
// returned media in SQL: it resumes on the agent handed off to after a
// restart, with the media intact, and rewinding returns the session to the
// first agent.
func TestHandoffMediaAndRewindOnSQL(t *testing.T) {
	t.Parallel()
	forEach(t, func(t *testing.T, b *backend) {
		opts := b.fresh(t)
		png := []byte{0x89, 'P', 'N', 'G', 0, 0xff}
		shot := tool.Func("shot", "Screenshot.", func(context.Context, struct{}) (*tool.Result, error) {
			return tool.Media("screen", model.ImagePart("image/png", png)), nil
		}, tool.RequireApproval())
		newRunner := func(tm, bm model.Model) *agent.Runner {
			cp := sqlstore.Checkpoints(b.db, opts...)
			billing := agent.MustNew("billing", agent.WithModel(bm), agent.WithTools(shot), agent.WithCheckpointer(cp))
			triage := agent.MustNew("triage", agent.WithModel(tm), agent.WithHandoffs(billing), agent.WithCheckpointer(cp))
			return agent.NewRunner(triage, sqlstore.Sessions(b.db, opts...))
		}
		tm := modeltest.New(modeltest.Reply("hello"), modeltest.CallTools(modeltest.Call{ID: "h", Name: agent.HandoffToolPrefix + "billing"}))
		bm := modeltest.New(modeltest.CallTools(modeltest.Call{ID: "s", Name: "shot"}), modeltest.Reply("billing done"))
		if _, err := newRunner(tm, bm).RunText(t.Context(), "s", "hi"); err != nil {
			t.Fatal(err)
		}
		res, err := newRunner(tm, bm).RunText(t.Context(), "s", "invoice")
		if err != nil || res.Status != loop.StatusInterrupted || res.Agent != "billing" {
			t.Fatalf("Run = %+v, %v", res, err)
		}
		r := newRunner(tm, bm)
		res, err = loop.Collect(r.Resume(t.Context(), "s", map[string]loop.Approval{"s": loop.Approve}), nil)
		if err != nil || res.Output != "billing done" || res.Agent != "billing" {
			t.Fatalf("Resume = %+v, %v", res, err)
		}
		saved, err := sqlstore.Sessions(b.db, opts...).Get(t.Context(), "s")
		if err != nil {
			t.Fatal(err)
		}
		var media []model.Part
		for _, m := range saved.Messages() {
			for _, tr := range m.ToolResults() {
				media = append(media, tr.Parts...)
			}
		}
		if len(media) != 1 || string(media[0].Data) != string(png) || media[0].MediaType != "image/png" {
			t.Fatalf("saved media = %+v", media)
		}
		if v, _ := saved.State().Get(agent.ActiveAgentKey); v != "billing" {
			t.Fatalf("active agent = %v", v)
		}
		cps, err := r.Checkpoints(t.Context(), "s")
		if err != nil || len(cps) != 2 || cps[1].Agent != "" || cps[1].Message.Text() != "invoice" {
			t.Fatalf("Checkpoints = %+v, %v", cps, err)
		}
		if _, err := r.Rewind(t.Context(), "s", cps[1].ID, agent.RewindConversation); err != nil {
			t.Fatal(err)
		}
		saved, err = sqlstore.Sessions(b.db, opts...).Get(t.Context(), "s")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := saved.State().Get(agent.ActiveAgentKey); ok || len(saved.Messages()) != 2 {
			t.Fatalf("session after rewind = %d messages, active agent present = %v", len(saved.Messages()), ok)
		}
	})
}
