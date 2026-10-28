package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nostosx/agentx/loop"
	"github.com/nostosx/agentx/memory"
	"github.com/nostosx/agentx/session"
	"github.com/nostosx/agentx/workflow"
)

// The stores are tested against real databases by the conformance suites
// in conformance_test.go. These tests cover what needs no database.

func TestRebind(t *testing.T) {
	t.Parallel()
	q := "UPDATE t SET a = ?, b = ? WHERE id = ? AND rev = ?"
	if got := newConfig(nopDB{}, nil).rebind(q); got != q {
		t.Fatalf("SQLite rebind = %q", got)
	}
	if got := newConfig(nopDB{}, []Option{WithDialect(MySQL)}).rebind(q); got != q {
		t.Fatalf("MySQL rebind = %q", got)
	}
	if got := newConfig(nopDB{}, []Option{WithDialect(PostgreSQL)}).rebind(q); got != "UPDATE t SET a = $1, b = $2 WHERE id = $3 AND rev = $4" {
		t.Fatalf("PostgreSQL rebind = %q", got)
	}
}

func TestSchema(t *testing.T) {
	t.Parallel()
	want := map[Dialect][]string{
		SQLite: {
			"CREATE TABLE IF NOT EXISTS app_memories (\n\tid VARCHAR(255) NOT NULL,\n\tscope VARCHAR(255) NOT NULL,\n\tcreated_at VARCHAR(30) NOT NULL,\n\tupdated_at VARCHAR(30) NOT NULL,\n\tdata TEXT NOT NULL,\n\tembedding BLOB,\n\tPRIMARY KEY (id)\n)",
			"CREATE INDEX IF NOT EXISTS app_memories_scope ON app_memories (scope, created_at)",
		},
		PostgreSQL: {
			"CREATE TABLE IF NOT EXISTS app_memories (\n\tid VARCHAR(255) NOT NULL,\n\tscope VARCHAR(255) NOT NULL,\n\tcreated_at VARCHAR(30) NOT NULL,\n\tupdated_at VARCHAR(30) NOT NULL,\n\tdata TEXT NOT NULL,\n\tembedding BYTEA,\n\tPRIMARY KEY (id)\n)",
			"CREATE INDEX IF NOT EXISTS app_memories_scope ON app_memories (scope, created_at)",
		},
		MySQL: {
			"CREATE TABLE IF NOT EXISTS app_memories (\n\tid VARBINARY(255) NOT NULL,\n\tscope VARBINARY(255) NOT NULL,\n\tcreated_at VARCHAR(30) NOT NULL,\n\tupdated_at VARCHAR(30) NOT NULL,\n\tdata LONGTEXT NOT NULL,\n\tembedding LONGBLOB,\n\tPRIMARY KEY (id),\n\tINDEX app_memories_scope (scope, created_at)\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin",
		},
	}
	for d, stmts := range want {
		db := &recordingDB{}
		if err := Memory(db, WithDialect(d), WithPrefix("app_")).Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(db.stmts, stmts) {
			t.Errorf("%v schema = %q\nwant %q", d, db.stmts, stmts)
		}
	}
	db := &recordingDB{}
	if err := Migrate(t.Context(), db, WithDialect(PostgreSQL), WithPrefix("")); err != nil {
		t.Fatal(err)
	}
	var created []string
	for _, stmt := range db.stmts {
		if name, ok := strings.CutPrefix(stmt, "CREATE TABLE IF NOT EXISTS "); ok {
			created = append(created, strings.Fields(name)[0])
		}
	}
	if !slices.Equal(created, []string{"sessions", "checkpoints", "workflow_checkpoints", "memories", "mcp_credentials"}) {
		t.Fatalf("Migrate created %v", created)
	}
	if !strings.Contains(db.stmts[0], "id VARCHAR(128) NOT NULL,\n\trevision BIGINT NOT NULL,") {
		t.Fatalf("sessions table = %s", db.stmts[0])
	}
}

func TestUpsert(t *testing.T) {
	t.Parallel()
	cols := []string{"run_id", "updated_at", "data"}
	for d, want := range map[Dialect]string{
		SQLite:     "INSERT INTO agentx_checkpoints (run_id, updated_at, data) VALUES (?, ?, ?) ON CONFLICT (run_id) DO UPDATE SET updated_at = excluded.updated_at, data = excluded.data",
		PostgreSQL: "INSERT INTO agentx_checkpoints (run_id, updated_at, data) VALUES (?, ?, ?) ON CONFLICT (run_id) DO UPDATE SET updated_at = excluded.updated_at, data = excluded.data",
		MySQL:      "INSERT INTO agentx_checkpoints (run_id, updated_at, data) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE updated_at = VALUES(updated_at), data = VALUES(data)",
	} {
		if got := newConfig(nopDB{}, []Option{WithDialect(d)}).upsert("checkpoints", cols, "run_id"); got != want {
			t.Errorf("%v upsert = %q", d, got)
		}
	}
}

func TestInvalidConfiguration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	for name, c := range map[string]struct {
		db   DB
		opts []Option
	}{
		"nil database":     {nil, nil},
		"unknown dialect":  {nopDB{}, []Option{WithDialect(Dialect(7))}},
		"quote in prefix":  {nopDB{}, []Option{WithPrefix(`x"; DROP TABLE y; --`)}},
		"leading digit":    {nopDB{}, []Option{WithPrefix("1x_")}},
		"schema qualified": {nopDB{}, []Option{WithPrefix("public.x_")}},
		"long prefix":      {nopDB{}, []Option{WithPrefix(strings.Repeat("p", 33))}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Migrate(ctx, c.db, c.opts...); err == nil {
				t.Error("Migrate accepted an invalid configuration")
			}
			sessions := Sessions(c.db, c.opts...)
			if _, err := sessions.Get(ctx, "s"); err == nil || errors.Is(err, session.ErrNotFound) {
				t.Errorf("Sessions.Get = %v", err)
			}
			if err := sessions.Save(ctx, session.New("s")); err == nil {
				t.Error("Sessions.Save accepted an invalid configuration")
			}
			if _, err := sessions.List(ctx); err == nil {
				t.Error("Sessions.List accepted an invalid configuration")
			}
			if err := Checkpoints(c.db, c.opts...).Save(ctx, &loop.State{RunID: "r"}); err == nil {
				t.Error("Checkpoints.Save accepted an invalid configuration")
			}
			if _, err := WorkflowCheckpoints(c.db, c.opts...).IDs(ctx); err == nil {
				t.Error("WorkflowCheckpoints.IDs accepted an invalid configuration")
			}
			if _, err := Memory(c.db, c.opts...).Get(ctx, "m"); err == nil || errors.Is(err, memory.ErrNotFound) {
				t.Errorf("Memory.Get = %v", err)
			}
		})
	}
	for _, prefix := range []string{"", "_", "app_", "App2_", strings.Repeat("p", 32)} {
		if err := Migrate(ctx, &recordingDB{}, WithPrefix(prefix)); err != nil {
			t.Errorf("prefix %q rejected: %v", prefix, err)
		}
	}
	if got := Dialect(7).String(); got != "Dialect(7)" {
		t.Errorf("String = %q", got)
	}
	if MySQL.String() != "mysql" || PostgreSQL.String() != "postgresql" || SQLite.String() != "sqlite" {
		t.Error("dialect names changed")
	}
}

func TestValidationBeforeQueries(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// nopDB fails every query, so these pass only if no query is made.
	sessions := Sessions(nopDB{})
	if err := sessions.Save(ctx, session.New("../escape")); err == nil || !strings.Contains(err.Error(), "invalid session id") {
		t.Errorf("Save = %v", err)
	}
	if err := Checkpoints(nopDB{}).Delete(ctx, "dot.name"); err == nil || !strings.Contains(err.Error(), "invalid checkpoint id") {
		t.Errorf("Delete = %v", err)
	}
	if _, err := WorkflowCheckpoints(nopDB{}).Load(ctx, strings.Repeat("w", 65)); err == nil || !strings.Contains(err.Error(), "invalid workflow checkpoint id") {
		t.Errorf("Load = %v", err)
	}
	if err := WorkflowCheckpoints(nopDB{}).Save(ctx, &workflow.Checkpoint{RunID: "w"}); !errors.Is(err, errNop) {
		t.Errorf("Save of a valid checkpoint = %v, want the database error", err)
	}
	mem := Memory(nopDB{})
	for _, e := range []*memory.Entry{{}, {ID: strings.Repeat("x", 256)}, {ID: "\xff"}, {ID: "a\x00b"}, {ID: "ok", Scope: strings.Repeat("s", 256)}} {
		if err := mem.Put(ctx, e); err == nil || errors.Is(err, errNop) {
			t.Errorf("Put %q in %q = %v", e.ID, e.Scope, err)
		}
	}
	for _, id := range []string{"", strings.Repeat("x", 256), "\xff"} {
		if _, err := mem.Get(ctx, id); !errors.Is(err, memory.ErrNotFound) {
			t.Errorf("Get %q = %v", id, err)
		}
		if err := mem.Delete(ctx, id); !errors.Is(err, memory.ErrNotFound) {
			t.Errorf("Delete %q = %v", id, err)
		}
	}
	if es, err := mem.List(ctx, memory.Filter{Scope: "\xff"}); err != nil || len(es) != 0 {
		t.Errorf("List of an unstorable scope = %v, %v", es, err)
	}
}

func TestVectorEncoding(t *testing.T) {
	t.Parallel()
	if encodeVector(nil) != nil || encodeVector([]float32{}) != nil {
		t.Fatal("empty vector not stored as NULL")
	}
	v := []float32{0, float32(math.Copysign(0, -1)), 1, -1, math.MaxFloat32, math.SmallestNonzeroFloat32, float32(math.NaN()), float32(math.Inf(-1))}
	got, err := decodeVector(encodeVector(v).([]byte))
	if err != nil || len(got) != len(v) {
		t.Fatalf("decode = %v, %v", got, err)
	}
	for i := range v {
		if math.Float32bits(got[i]) != math.Float32bits(v[i]) {
			t.Fatalf("element %d = %v, want %v", i, got[i], v[i])
		}
	}
	if b := encodeVector([]float32{1}).([]byte); !slices.Equal(b, []byte{0, 0, 0x80, 0x3f}) {
		t.Fatalf("encoding is not little-endian float32: %x", b)
	}
	if _, err := decodeVector([]byte{1, 2, 3}); err == nil {
		t.Fatal("decoded a truncated vector")
	}
	if got, err := decodeVector(nil); got != nil || err != nil {
		t.Fatalf("decode NULL = %v, %v", got, err)
	}
}

func TestTimeEncoding(t *testing.T) {
	t.Parallel()
	times := []time.Time{
		{},
		time.Date(1969, 12, 31, 23, 59, 59, 999999999, time.UTC),
		time.Date(2024, 1, 2, 3, 4, 5, 6, time.FixedZone("UTC+9", 9*3600)),
		time.Date(2024, 1, 1, 18, 4, 5, 7, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
	}
	var formatted []string
	for _, tm := range times {
		s := formatTime(tm)
		if len(s) != 30 {
			t.Fatalf("formatTime(%v) = %q is not 30 bytes", tm, s)
		}
		back, err := parseTime(s)
		if err != nil || !back.Equal(tm) {
			t.Fatalf("parseTime(%q) = %v, %v; want %v", s, back, err, tm)
		}
		formatted = append(formatted, s)
	}
	if !slices.IsSorted(formatted) {
		t.Fatalf("formatted times do not sort chronologically: %q", formatted)
	}
}

var errNop = errors.New("no database")

// nopDB fails every statement.
type nopDB struct{}

func (nopDB) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errNop
}

func (nopDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errNop
}

func (nopDB) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }

// recordingDB records the statements it executes.
type recordingDB struct {
	nopDB
	stmts []string
}

func (db *recordingDB) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	db.stmts = append(db.stmts, query)
	return nil, nil
}
