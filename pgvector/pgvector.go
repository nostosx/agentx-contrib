// Package pgvector stores RAG chunks and long-term memories in PostgreSQL
// with the vector extension. It imports no SQL driver; applications open a
// *sql.DB and retain ownership of it.
//
// Vectors implements rag.VectorStore. Memory implements memory.Store,
// memory.ScopedDeleter and memory.Retriever, executing cosine ranking in the
// database. Both use exact search, including when approximate indexes exist.
// A namespace isolates all reads and writes within the module's own tables.
// The schema is independent of sqlstore; stores never import other contrib
// modules or share their tables.
package pgvector

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nostosx/agentx/memory"
)

// DefaultPrefix is the default prefix of this module's tables.
const DefaultPrefix = "agentx_pg_"

// Option configures a store. Options do not install the vector extension.
type Option func(*config)

// WithPrefix sets a table prefix of at most 32 ASCII letters, digits and
// underscores, not starting with a digit. An empty prefix is allowed.
func WithPrefix(prefix string) Option { return func(c *config) { c.prefix = prefix } }

// WithNamespace isolates records within the tables. The default is
// "default". A namespace is not authorization: applications must select it
// from a trusted identity, not from an unchecked request parameter.
func WithNamespace(namespace string) Option {
	return func(c *config) { c.namespace = namespace }
}

// WithDimensions requires every non-empty embedding and query vector to
// have dimensions values. Zero, the default, accepts mixed dimensions.
// Cosine similarity is zero for entries of a different dimension, as in
// the agentx in-memory stores. The maximum supported dimension is 16000.
func WithDimensions(dimensions int) Option { return func(c *config) { c.dimensions = dimensions } }

// WithEmbedder sets the query embedder used by MemoryStore.Retrieve.
// It does not generate embeddings during Put; memory.New does that.
func WithEmbedder(embedder memory.Embedder) Option {
	return func(c *config) { c.embedder = embedder }
}

type config struct {
	db         *sql.DB
	prefix     string
	namespace  string
	dimensions int
	embedder   memory.Embedder
}

var prefixPattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]{0,31})?$`)

func newConfig(db *sql.DB, opts []Option) (config, error) {
	c := config{db: db, prefix: DefaultPrefix, namespace: "default"}
	for _, opt := range opts {
		if opt != nil {
			opt(&c)
		}
	}
	switch {
	case db == nil:
		return c, errors.New("pgvector: nil database")
	case !prefixPattern.MatchString(c.prefix):
		return c, fmt.Errorf("pgvector: invalid table prefix %q", c.prefix)
	case !validKey(c.namespace) || c.namespace == "":
		return c, errors.New("pgvector: namespace must be 1 to 255 bytes of UTF-8 without NUL")
	case c.dimensions < 0 || c.dimensions > 16000:
		return c, errors.New("pgvector: dimensions must be between 0 and 16000")
	}
	return c, nil
}

func (c config) table(name string) string { return `"` + c.prefix + name + `"` }

// Migrate creates both stores' tables. Enable the PostgreSQL vector
// extension separately before calling it. Run initial migrations from one
// process; callers retain responsibility for deploying schema changes.
func Migrate(ctx context.Context, db *sql.DB, opts ...Option) error {
	v, err := Vectors(db, opts...)
	if err != nil {
		return err
	}
	if err := v.Migrate(ctx); err != nil {
		return err
	}
	m, err := Memory(db, opts...)
	if err != nil {
		return err
	}
	return m.Migrate(ctx)
}

func (c config) migrate(ctx context.Context, stmts ...string) error {
	var installed bool
	if err := c.db.QueryRowContext(ctx, "SELECT to_regtype('vector') IS NOT NULL").Scan(&installed); err != nil {
		return fmt.Errorf("pgvector: check extension: %w", err)
	}
	if !installed {
		return errors.New("pgvector: enable the PostgreSQL vector extension before migrating")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pgvector: begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // A failed transaction is already being reported; success committed below.
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("pgvector: migrate: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pgvector: commit migration: %w", err)
	}
	return nil
}

func validKey(key string) bool {
	return len(key) <= 255 && utf8.ValidString(key) && !strings.ContainsRune(key, 0)
}

func hashKey(key string) []byte {
	h := sha256.Sum256([]byte(key))
	return h[:]
}

// vectorText uses the extension's text input, avoiding driver-specific
// codecs. JSON documents retain the original float32 values for reads.
func (c config) vectorText(v []float32) (any, error) {
	if len(v) == 0 {
		return nil, nil
	}
	if len(v) > 16000 || (c.dimensions > 0 && len(v) != c.dimensions) {
		return nil, fmt.Errorf("pgvector: invalid embedding dimension %d (configured %d)", len(v), c.dimensions)
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return nil, errors.New("pgvector: embeddings must contain only finite values")
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String(), nil
}

type query struct {
	where []string
	args  []any
}

func (q *query) arg(v any) string {
	q.args = append(q.args, v)
	return "$" + strconv.Itoa(len(q.args))
}

func (q *query) clause() string { return strings.Join(q.where, " AND ") }

func (c config) query() query {
	q := query{}
	q.where = append(q.where, "namespace = "+q.arg(c.namespace))
	return q
}

func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("pgvector: encode record: %w", err)
	}
	return string(b), nil
}

// cosineSQL preserves the zero-vector and mixed-dimension behavior of
// memory.Cosine. The materialized candidate relation used by callers
// prevents an externally-created approximate index from changing recall.
func cosineSQL(vector string) string {
	return "CASE WHEN vector_dims(embedding) <> vector_dims(" + vector + "::vector)" +
		" OR vector_norm(embedding) = 0 OR vector_norm(" + vector + "::vector) = 0" +
		" THEN 0::double precision ELSE 1 - (embedding <=> " + vector + "::vector) END"
}
