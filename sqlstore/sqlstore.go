// Package sqlstore stores sessions, run checkpoints, workflow checkpoints
// and long-term memory in a SQL database through database/sql. MCPTokens
// additionally stores authenticated-encrypted MCP OAuth credentials.
//
// The package imports no driver: open the database with the driver of
// your choice and pass the handle, a *sql.DB, *sql.Conn or *sql.Tx, to
// Sessions, Checkpoints, WorkflowCheckpoints or Memory with the matching
// Dialect. SQLite, PostgreSQL and MySQL (and compatible servers such as
// MariaDB) are supported. Create the tables with Migrate, which is
// idempotent.
//
//	db, err := sql.Open("pgx", dsn)
//	...
//	opts := []sqlstore.Option{sqlstore.WithDialect(sqlstore.PostgreSQL)}
//	if err := sqlstore.Migrate(ctx, db, opts...); err != nil { ... }
//	sessions := sqlstore.Sessions(db, opts...)
//
// # Schema
//
// Each store has one table, named with a prefix ("agentx_" by default; see
// WithPrefix): sessions, checkpoints, workflow_checkpoints, memories and
// mcp_credentials. MCP records use hashed keys and encrypted binary values.
// Values are stored as JSON documents in a text column next to the key and
// the columns needed to list them, so the schema does not change with the
// Go types. Column types are chosen to be portable and exact:
//
//   - Keys are VARCHAR on SQLite and PostgreSQL, whose comparisons are
//     exact, and VARBINARY on MySQL, where VARCHAR comparisons ignore case
//     and trailing spaces under the default collations.
//   - Timestamps are VARCHAR(30) in a fixed-width UTC form
//     (2006-01-02T15:04:05.000000000Z) that sorts chronologically,
//     preserves nanoseconds and needs no time zone settings.
//   - Documents are TEXT (LONGTEXT on MySQL); revisions and counts BIGINT.
//   - Memory embeddings are BLOB (BYTEA on PostgreSQL, LONGBLOB on MySQL)
//     holding little-endian float32 values, which round-trip bit for bit.
//     Retrieval stays in the memory package's retrievers.
//   - MySQL tables use InnoDB with utf8mb4 and a binary collation.
//
// # Concurrency
//
// Stores open no transactions. Each mutating SQL statement is atomic, but
// operations such as SessionStore.Save may execute several statements.
// Given a *sql.Tx, stores take part in the caller's transaction; the caller
// owns its commit or rollback and the database handle's lifetime.
// SessionStore detects concurrent modification with a revision column,
// updating a session only at the revision it was loaded at (compare and
// swap); see session.ErrConflict.
// With SQLite, set a busy timeout (for example the _pragma=busy_timeout
// DSN parameter of modernc.org/sqlite) so concurrent writers wait instead
// of failing.
package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DB is the part of *sql.DB, *sql.Conn and *sql.Tx the stores use.
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Dialect selects the SQL variant of a database.
type Dialect int

const (
	// SQLite uses ? placeholders and ON CONFLICT upserts. It needs SQLite
	// 3.24 or later.
	SQLite Dialect = iota
	// PostgreSQL uses $n placeholders and ON CONFLICT upserts. It needs
	// PostgreSQL 9.5 or later.
	PostgreSQL
	// MySQL uses ? placeholders and ON DUPLICATE KEY UPDATE upserts. It
	// needs MySQL 5.7 or MariaDB 10.2 or later.
	MySQL
)

// String returns the name of the dialect.
func (d Dialect) String() string {
	switch d {
	case SQLite:
		return "sqlite"
	case PostgreSQL:
		return "postgresql"
	case MySQL:
		return "mysql"
	}
	return "Dialect(" + strconv.Itoa(int(d)) + ")"
}

// Option configures a store.
type Option func(*config)

// WithDialect sets the SQL dialect. The default is SQLite.
func WithDialect(d Dialect) Option { return func(c *config) { c.dialect = d } }

// DefaultPrefix is the default table name prefix.
const DefaultPrefix = "agentx_"

// WithPrefix sets the prefix of the table and index names, which lets
// several applications share a database. It must be at most 32 ASCII
// letters, digits and underscores, not starting with a digit, and may be
// empty. The default is DefaultPrefix.
func WithPrefix(prefix string) Option { return func(c *config) { c.prefix = prefix } }

var prefixPattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]{0,31})?$`)

type config struct {
	db      DB
	dialect Dialect
	prefix  string
	// err reports an invalid configuration; every method returns it.
	err error
}

func newConfig(db DB, opts []Option) config {
	c := config{db: db, prefix: DefaultPrefix}
	for _, opt := range opts {
		opt(&c)
	}
	switch {
	case db == nil:
		c.err = fmt.Errorf("sqlstore: nil database")
	case c.dialect < SQLite || c.dialect > MySQL:
		c.err = fmt.Errorf("sqlstore: unknown dialect %v", c.dialect)
	case !prefixPattern.MatchString(c.prefix):
		c.err = fmt.Errorf("sqlstore: invalid table prefix %q", c.prefix)
	}
	return c
}

// Migrate creates the tables of all stores, using the same options as the
// stores. It is idempotent: existing tables are left as they are. Run it
// from one process at a time; concurrent first runs on PostgreSQL may fail
// and can be retried.
func Migrate(ctx context.Context, db DB, opts ...Option) error {
	c := newConfig(db, opts)
	for _, m := range []func(context.Context) error{
		(&SessionStore{c: c}).Migrate,
		(&CheckpointStore{c: c}).Migrate,
		(&WorkflowCheckpointStore{c: c}).Migrate,
		(&MemoryStore{c: c}).Migrate,
		func(ctx context.Context) error { return migrateTokens(ctx, c) },
	} {
		if err := m(ctx); err != nil {
			return err
		}
	}
	return nil
}

// table returns the name of a table.
func (c config) table(name string) string { return c.prefix + name }

// rebind rewrites ? placeholders for the dialect. Queries built by this
// package contain no other question marks.
func (c config) rebind(query string) string {
	if c.dialect != PostgreSQL {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (c config) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.db.ExecContext(ctx, c.rebind(query), args...)
}

func (c config) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return c.db.QueryContext(ctx, c.rebind(query), args...)
}

func (c config) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return c.db.QueryRowContext(ctx, c.rebind(query), args...)
}

// Column types; see the package documentation.

// keyType is the type of a key of at most n bytes.
func (c config) keyType(n int) string {
	if c.dialect == MySQL {
		return "VARBINARY(" + strconv.Itoa(n) + ")"
	}
	return "VARCHAR(" + strconv.Itoa(n) + ")"
}

const timeType = "VARCHAR(30)"

func (c config) textType() string {
	if c.dialect == MySQL {
		return "LONGTEXT"
	}
	return "TEXT"
}

func (c config) blobType() string {
	switch c.dialect {
	case PostgreSQL:
		return "BYTEA"
	case MySQL:
		return "LONGBLOB"
	}
	return "BLOB"
}

// index is a secondary index of a table.
type index struct {
	name    string
	columns string
}

// createTable returns the statements creating a table with the given
// column definitions, primary key and indexes, if they do not exist.
func (c config) createTable(name string, columns []string, key string, indexes ...index) []string {
	table := c.table(name)
	defs := append(slices.Clone(columns), "PRIMARY KEY ("+key+")")
	if c.dialect == MySQL {
		// MySQL has no CREATE INDEX IF NOT EXISTS, so indexes are created
		// with the table.
		for _, ix := range indexes {
			defs = append(defs, "INDEX "+c.table(ix.name)+" ("+ix.columns+")")
		}
	}
	stmt := "CREATE TABLE IF NOT EXISTS " + table + " (\n\t" + strings.Join(defs, ",\n\t") + "\n)"
	if c.dialect == MySQL {
		stmt += " ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin"
	}
	stmts := []string{stmt}
	if c.dialect != MySQL {
		for _, ix := range indexes {
			stmts = append(stmts, "CREATE INDEX IF NOT EXISTS "+c.table(ix.name)+" ON "+table+" ("+ix.columns+")")
		}
	}
	return stmts
}

func (c config) migrate(ctx context.Context, stmts []string) error {
	if c.err != nil {
		return c.err
	}
	for _, stmt := range stmts {
		if _, err := c.exec(ctx, stmt); err != nil {
			return fmt.Errorf("sqlstore: migrate: %w", err)
		}
	}
	return nil
}

// upsert returns a statement inserting a row or, when a row with the same
// key exists, updating its other columns.
func (c config) upsert(table string, columns []string, key string) string {
	var sets []string
	for _, col := range columns {
		if col == key {
			continue
		}
		if c.dialect == MySQL {
			sets = append(sets, col+" = VALUES("+col+")")
		} else {
			sets = append(sets, col+" = excluded."+col)
		}
	}
	stmt := "INSERT INTO " + c.table(table) + " (" + strings.Join(columns, ", ") + ") VALUES (" + placeholders(len(columns)) + ")"
	if c.dialect == MySQL {
		return stmt + " ON DUPLICATE KEY UPDATE " + strings.Join(sets, ", ")
	}
	return stmt + " ON CONFLICT (" + key + ") DO UPDATE SET " + strings.Join(sets, ", ")
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

// timeLayout is a fixed-width layout, so stored times sort as strings.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, error) { return time.Parse(timeLayout, s) }

// affected returns the number of rows a statement changed.
func affected(res sql.Result) (int64, error) {
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlstore: rows affected: %w", err)
	}
	return n, nil
}
