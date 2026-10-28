package pgvector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/nostosx/agentx/memory"
)

// MemoryStore persists long-term memories and retrieves them with exact
// database-side cosine ranking. It uses its own table, not sqlstore's table.
type MemoryStore struct{ c config }

var (
	_ memory.Store         = (*MemoryStore)(nil)
	_ memory.ScopedDeleter = (*MemoryStore)(nil)
	_ memory.Retriever     = (*MemoryStore)(nil)
)

// Memory returns a store without opening connections or creating tables.
// Pass WithEmbedder to use it as a memory.Retriever.
func Memory(db *sql.DB, opts ...Option) (*MemoryStore, error) {
	c, err := newConfig(db, opts)
	if err != nil {
		return nil, err
	}
	return &MemoryStore{c: c}, nil
}

// Migrate creates the memory table and a scope/time index. Stored document
// text preserves nanoseconds and float32 bits, including negative zero.
func (s *MemoryStore) Migrate(ctx context.Context) error {
	c := s.c
	return c.migrate(ctx,
		"CREATE TABLE IF NOT EXISTS "+c.table("memories")+` (
			namespace TEXT COLLATE "C" NOT NULL,
			id TEXT COLLATE "C" NOT NULL,
			scope TEXT COLLATE "C" NOT NULL,
			created_at TEXT COLLATE "C" NOT NULL,
			data TEXT NOT NULL,
			tags JSONB NOT NULL,
			embedding vector,
			PRIMARY KEY (namespace, id))`,
		"CREATE INDEX IF NOT EXISTS "+c.table("memories_scope")+" ON "+c.table("memories")+" (namespace, scope, created_at, id)",
	)
}

// Put inserts or replaces a complete memory. Applications generate its
// embedding before Put, normally through memory.New's WithEmbedder option.
func (s *MemoryStore) Put(ctx context.Context, e *memory.Entry) error {
	if e == nil || e.ID == "" || !validKey(e.ID) || !validKey(e.Scope) {
		return errors.New("pgvector: memory ID and scope must be UTF-8 keys of at most 255 bytes without NUL; ID must be non-empty")
	}
	embedding, err := s.c.vectorText(e.Embedding)
	if err != nil {
		return err
	}
	data, err := jsonText(e)
	if err != nil {
		return err
	}
	tags, err := jsonText(e.Tags)
	if err != nil {
		return err
	}
	_, err = s.c.db.ExecContext(ctx, "INSERT INTO "+s.c.table("memories")+
		" (namespace, id, scope, created_at, data, tags, embedding) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::vector)"+
		" ON CONFLICT (namespace, id) DO UPDATE SET scope = excluded.scope, created_at = excluded.created_at,"+
		" data = excluded.data, tags = excluded.tags, embedding = excluded.embedding",
		s.c.namespace, e.ID, e.Scope, e.CreatedAt.UTC().Format(timeLayout), data, tags, embedding)
	if err != nil {
		return fmt.Errorf("pgvector: put memory: %w", err)
	}
	return nil
}

const timeLayout = "2006-01-02T15:04:05.000000000Z"

// Get loads a memory or returns memory.ErrNotFound.
func (s *MemoryStore) Get(ctx context.Context, id string) (*memory.Entry, error) {
	if id == "" || !validKey(id) {
		return nil, memory.ErrNotFound
	}
	var data []byte
	err := s.c.db.QueryRowContext(ctx, "SELECT data FROM "+s.c.table("memories")+
		" WHERE namespace = $1 AND id = $2", s.c.namespace, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, memory.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pgvector: get memory: %w", err)
	}
	return decodeMemory(data)
}

func decodeMemory(data []byte) (*memory.Entry, error) {
	e := new(memory.Entry)
	if err := json.Unmarshal(data, e); err != nil {
		return nil, fmt.Errorf("pgvector: decode memory: %w", err)
	}
	return e, nil
}

// Delete removes a memory, or returns memory.ErrNotFound.
func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	return s.DeleteInScope(ctx, id, "")
}

// DeleteInScope checks the scope and deletes in one SQL statement. An
// empty scope allows deletion in any scope within this namespace.
func (s *MemoryStore) DeleteInScope(ctx context.Context, id, scope string) error {
	if id == "" || !validKey(id) || !validKey(scope) {
		return memory.ErrNotFound
	}
	q := s.c.query()
	q.where = append(q.where, "id = "+q.arg(id))
	if scope != "" {
		q.where = append(q.where, "scope = "+q.arg(scope))
	}
	res, err := s.c.db.ExecContext(ctx, "DELETE FROM "+s.c.table("memories")+" WHERE "+q.clause(), q.args...)
	if err != nil {
		return fmt.Errorf("pgvector: delete memory: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("pgvector: deleted memory count: %w", err)
	}
	if n == 0 {
		return memory.ErrNotFound
	}
	return nil
}

func (c config) memoryQuery(f memory.Filter) (query, error) {
	q := c.query()
	if f.Scope != "" {
		q.where = append(q.where, "scope = "+q.arg(f.Scope))
	}
	if len(f.Tags) > 0 {
		tags, err := jsonText(f.Tags)
		if err != nil {
			return q, err
		}
		q.where = append(q.where, "tags @> "+q.arg(tags)+"::jsonb")
	}
	return q, nil
}

// List returns memories ordered by creation time, then ID, after filtering
// by exact scope and all tags in the database.
func (s *MemoryStore) List(ctx context.Context, f memory.Filter) ([]*memory.Entry, error) {
	q, err := s.c.memoryQuery(f)
	if err != nil {
		return nil, err
	}
	rows, err := s.c.db.QueryContext(ctx, "SELECT data FROM "+s.c.table("memories")+
		" WHERE "+q.clause()+" ORDER BY created_at, id", q.args...)
	if err != nil {
		return nil, fmt.Errorf("pgvector: list memories: %w", err)
	}
	defer rows.Close()
	var entries []*memory.Entry
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("pgvector: scan memory: %w", err)
		}
		e, err := decodeMemory(data)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Retrieve embeds q.Text, then ranks memories in PostgreSQL. As with
// memory.Vector, only positive similarities meeting MinScore are returned;
// memories without embeddings are skipped. Limit <= 0 returns all hits.
func (s *MemoryStore) Retrieve(ctx context.Context, q memory.Query) ([]memory.Hit, error) {
	if s.c.embedder == nil {
		return nil, errors.New("pgvector: memory retrieval requires WithEmbedder")
	}
	if strings.TrimSpace(q.Text) == "" || math.IsNaN(q.MinScore) || math.IsInf(q.MinScore, 1) {
		return nil, ctx.Err()
	}
	vecs, err := s.c.embedder.Embed(ctx, []string{q.Text})
	if err != nil {
		return nil, fmt.Errorf("pgvector: embed query: %w", err)
	}
	if len(vecs) != 1 {
		return nil, errors.New("pgvector: query embedder must return exactly one vector")
	}
	text, err := s.c.vectorText(vecs[0])
	if err != nil {
		return nil, err
	}
	if text == nil {
		return nil, ctx.Err()
	}
	query, err := s.c.memoryQuery(q.Filter)
	if err != nil {
		return nil, err
	}
	query.where = append(query.where, "embedding IS NOT NULL")
	score := cosineSQL(query.arg(text))
	stmt := "WITH candidates AS MATERIALIZED (SELECT data, id, " + score + " AS score FROM " + s.c.table("memories") +
		" WHERE " + query.clause() + ") SELECT data, score FROM candidates WHERE score > 0"
	if !math.IsInf(q.MinScore, -1) {
		stmt += " AND score >= " + query.arg(q.MinScore)
	}
	stmt += " ORDER BY score DESC, id"
	if q.Limit > 0 {
		stmt += " LIMIT " + query.arg(q.Limit)
	}
	rows, err := s.c.db.QueryContext(ctx, stmt, query.args...)
	if err != nil {
		return nil, fmt.Errorf("pgvector: retrieve memories: %w", err)
	}
	defer rows.Close()
	var hits []memory.Hit
	for rows.Next() {
		var data []byte
		var hit memory.Hit
		if err := rows.Scan(&data, &hit.Score); err != nil {
			return nil, fmt.Errorf("pgvector: scan memory hit: %w", err)
		}
		hit.Entry, err = decodeMemory(data)
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}
