package sqlstore_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/nostosx/agentx-contrib/sqlstore"
	"github.com/nostosx/agentx/loop"
	"github.com/nostosx/agentx/loop/checkpointtest"
	"github.com/nostosx/agentx/memory"
	"github.com/nostosx/agentx/memory/memorytest"
	"github.com/nostosx/agentx/session"
	"github.com/nostosx/agentx/session/sessiontest"
	"github.com/nostosx/agentx/workflow"
	workflowcheckpointtest "github.com/nostosx/agentx/workflow/checkpointtest"
)

// backend is a database the stores are tested on.
type backend struct {
	name    string
	dialect sqlstore.Dialect
	// open returns a new handle to the same database.
	open func(t *testing.T) *sql.DB
	db   *sql.DB
}

// server is a database server the integration tests use when its DSN is
// set in the environment.
type server struct {
	name, variable, driver string
	dialect                sqlstore.Dialect
}

// backends returns SQLite and, in integration builds, the database servers
// configured in the environment.
func backends(t *testing.T) []*backend {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentx.db")
	// A busy timeout makes concurrent writers wait for each other; WAL
	// lets readers run alongside them.
	sqliteDSN := "file:" + path + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)"
	all := []*backend{{name: "sqlite", dialect: sqlstore.SQLite, open: opener("sqlite", sqliteDSN)}}
	for _, env := range servers {
		if dsn := os.Getenv(env.variable); dsn != "" {
			all = append(all, &backend{name: env.name, dialect: env.dialect, open: opener(env.driver, dsn)})
		} else {
			t.Logf("%s is not set; skipping %s", env.variable, env.name)
		}
	}
	for _, b := range all {
		b.db = b.open(t)
	}
	return all
}

func opener(driver, dsn string) func(t *testing.T) *sql.DB {
	return func(t *testing.T) *sql.DB {
		t.Helper()
		db, err := sql.Open(driver, dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := db.PingContext(t.Context()); err != nil {
			t.Fatalf("connect to %s: %v", driver, err)
		}
		return db
	}
}

var tables = []string{"sessions", "checkpoints", "workflow_checkpoints", "memories", "mcp_credentials"}

// fresh returns options with a new table prefix, whose tables are created
// now and dropped when the test ends.
func (b *backend) fresh(t *testing.T) []sqlstore.Option { return b.options(b.freshPrefix(t)) }

// freshPrefix returns the new table prefix fresh uses.
func (b *backend) freshPrefix(t *testing.T) string {
	t.Helper()
	prefix := "t" + hex.EncodeToString(randomBytes(6)) + "_"
	t.Cleanup(func() { b.drop(t, prefix) })
	if err := sqlstore.Migrate(t.Context(), b.db, b.options(prefix)...); err != nil {
		t.Fatal(err)
	}
	return prefix
}

func (b *backend) options(prefix string) []sqlstore.Option {
	return []sqlstore.Option{sqlstore.WithDialect(b.dialect), sqlstore.WithPrefix(prefix)}
}

func (b *backend) drop(t *testing.T, prefix string) {
	for _, table := range tables {
		if _, err := b.db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+prefix+table); err != nil {
			t.Errorf("drop %s: %v", prefix+table, err)
		}
	}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func forEach(t *testing.T, test func(*testing.T, *backend)) {
	t.Helper()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			test(t, b)
		})
	}
}

func TestSessionStoreConformance(t *testing.T) {
	t.Parallel()
	forEach(t, func(t *testing.T, b *backend) {
		sessiontest.Run(t, func(t *testing.T) session.Store { return sqlstore.Sessions(b.db, b.fresh(t)...) })
	})
}

func TestCheckpointStoreConformance(t *testing.T) {
	t.Parallel()
	forEach(t, func(t *testing.T, b *backend) {
		checkpointtest.Run(t, func(t *testing.T) loop.Checkpointer { return sqlstore.Checkpoints(b.db, b.fresh(t)...) })
	})
}

func TestWorkflowCheckpointStoreConformance(t *testing.T) {
	t.Parallel()
	forEach(t, func(t *testing.T, b *backend) {
		workflowcheckpointtest.Run(t, func(t *testing.T) workflow.Checkpointer {
			return sqlstore.WorkflowCheckpoints(b.db, b.fresh(t)...)
		})
	})
}

func TestMemoryStoreConformance(t *testing.T) {
	t.Parallel()
	forEach(t, func(t *testing.T, b *backend) {
		memorytest.Run(t, func(t *testing.T) memory.Store { return sqlstore.Memory(b.db, b.fresh(t)...) })
	})
}
