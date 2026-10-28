package pgvector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nostosx/agentx/rag"
)

// VectorStore stores RAG sources and chunks. Every Replace call is atomic,
// including concurrent replacements of the same source by other instances.
// It deliberately does not implement rag.Revisioner: a process-local
// revision would hide changes made by another database client.
type VectorStore struct{ c config }

var _ rag.VectorStore = (*VectorStore)(nil)

// Vectors returns a store without opening connections or creating tables.
func Vectors(db *sql.DB, opts ...Option) (*VectorStore, error) {
	c, err := newConfig(db, opts)
	if err != nil {
		return nil, err
	}
	return &VectorStore{c: c}, nil
}

// Migrate creates the source and chunk tables without approximate indexes.
func (s *VectorStore) Migrate(ctx context.Context) error {
	c := s.c
	return c.migrate(ctx,
		"CREATE TABLE IF NOT EXISTS "+c.table("sources")+` (
			namespace TEXT COLLATE "C" NOT NULL,
			source_key BYTEA NOT NULL,
			source TEXT COLLATE "C" NOT NULL,
			data TEXT NOT NULL,
			PRIMARY KEY (namespace, source_key))`,
		"CREATE TABLE IF NOT EXISTS "+c.table("chunks")+` (
			namespace TEXT COLLATE "C" NOT NULL,
			source_key BYTEA NOT NULL,
			chunk_key BYTEA NOT NULL,
			source TEXT COLLATE "C" NOT NULL,
			document_id TEXT COLLATE "C" NOT NULL,
			position BIGINT NOT NULL,
			data TEXT NOT NULL,
			metadata JSONB NOT NULL,
			embedding vector,
			PRIMARY KEY (namespace, source_key, chunk_key),
			FOREIGN KEY (namespace, source_key) REFERENCES `+c.table("sources")+` (namespace, source_key) ON DELETE CASCADE)`,
	)
}

type preparedChunk struct {
	key, sourceKey []byte
	document       string
	position       int
	data, metadata string
	embedding      any
}

type preparedSource struct {
	name, data string
	key        []byte
	chunks     []preparedChunk
}

func (c config) prepareSource(src rag.SourceChunks) (preparedSource, error) {
	if src.Info.Source == "" || !postgresText(src.Info.Source) {
		return preparedSource{}, errors.New("pgvector: source must be non-empty UTF-8 without NUL")
	}
	p := preparedSource{name: src.Info.Source, key: hashKey(src.Info.Source)}
	seen := make(map[string]bool, len(src.Chunks))
	for _, ch := range src.Chunks {
		if ch == nil || ch.ID == "" || !postgresText(ch.ID) || !postgresText(ch.DocumentID) {
			return p, errors.New("pgvector: chunk must have an ID and valid UTF-8 identifiers without NUL")
		}
		if seen[ch.ID] {
			return p, fmt.Errorf("pgvector: duplicate chunk ID %q", ch.ID)
		}
		seen[ch.ID] = true
		embedding, err := c.vectorText(ch.Embedding)
		if err != nil {
			return p, err
		}
		cp := *ch
		cp.Source = src.Info.Source
		data, err := jsonText(&cp)
		if err != nil {
			return p, err
		}
		metadata, err := jsonText(ch.Metadata)
		if err != nil {
			return p, err
		}
		p.chunks = append(p.chunks, preparedChunk{key: hashKey(ch.ID), sourceKey: p.key,
			document: ch.DocumentID, position: ch.Index, data: data, metadata: metadata, embedding: embedding})
	}
	info := src.Info
	info.Chunks = len(src.Chunks)
	var err error
	p.data, err = jsonText(info)
	return p, err
}

func postgresText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

// Replace atomically replaces the sources. All input is validated before
// the transaction begins. Source rows are locked in a stable order to
// serialize concurrent writers without deleting another writer's chunks.
func (s *VectorStore) Replace(ctx context.Context, sources ...rag.SourceChunks) error {
	prepared := make([]preparedSource, 0, len(sources))
	for _, src := range sources {
		p, err := s.c.prepareSource(src)
		if err != nil {
			return err
		}
		prepared = append(prepared, p)
	}
	if len(prepared) == 0 {
		return ctx.Err()
	}
	sort.SliceStable(prepared, func(i, j int) bool { return prepared[i].name < prepared[j].name })
	tx, err := s.c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pgvector: begin replacement: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // Preserve the operation error; a successful transaction is committed below.
	for _, p := range prepared {
		if err := s.replace(ctx, tx, p); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pgvector: commit replacement: %w", err)
	}
	return nil
}

func (s *VectorStore) replace(ctx context.Context, tx *sql.Tx, p preparedSource) error {
	c := s.c
	_, err := tx.ExecContext(ctx, "INSERT INTO "+c.table("sources")+
		" (namespace, source_key, source, data) VALUES ($1, $2, $3, $4)"+
		" ON CONFLICT (namespace, source_key) DO UPDATE SET data = excluded.data", c.namespace, p.key, p.name, p.data)
	if err != nil {
		return fmt.Errorf("pgvector: replace source: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+c.table("chunks")+" WHERE namespace = $1 AND source_key = $2", c.namespace, p.key); err != nil {
		return fmt.Errorf("pgvector: remove old chunks: %w", err)
	}
	for _, ch := range p.chunks {
		_, err := tx.ExecContext(ctx, "INSERT INTO "+c.table("chunks")+
			" (namespace, source_key, chunk_key, source, document_id, position, data, metadata, embedding)"+
			" VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::vector)",
			c.namespace, ch.sourceKey, ch.key, p.name, ch.document, ch.position, ch.data, ch.metadata, ch.embedding)
		if err != nil {
			return fmt.Errorf("pgvector: insert chunk: %w", err)
		}
	}
	return nil
}

// Delete removes the sources and their chunks. Unknown sources are ignored.
func (s *VectorStore) Delete(ctx context.Context, sources ...string) error {
	if len(sources) == 0 {
		return ctx.Err()
	}
	q := s.c.query()
	params := make([]string, 0, len(sources))
	for _, source := range sources {
		params = append(params, q.arg(hashKey(source)))
	}
	q.where = append(q.where, "source_key IN ("+strings.Join(params, ", ")+")")
	if _, err := s.c.db.ExecContext(ctx, "DELETE FROM "+s.c.table("sources")+" WHERE "+q.clause(), q.args...); err != nil {
		return fmt.Errorf("pgvector: delete sources: %w", err)
	}
	return nil
}

// Sources lists the sources ordered by name.
func (s *VectorStore) Sources(ctx context.Context) ([]rag.SourceInfo, error) {
	rows, err := s.c.db.QueryContext(ctx, "SELECT data FROM "+s.c.table("sources")+
		" WHERE namespace = $1 ORDER BY source", s.c.namespace)
	if err != nil {
		return nil, fmt.Errorf("pgvector: list sources: %w", err)
	}
	defer rows.Close()
	var infos []rag.SourceInfo
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("pgvector: scan source: %w", err)
		}
		var info rag.SourceInfo
		if err := json.Unmarshal(data, &info); err != nil {
			return nil, fmt.Errorf("pgvector: decode source: %w", err)
		}
		infos = append(infos, info)
	}
	return infos, rows.Err()
}

func (c config) chunkQuery(f rag.Filter) query {
	q := c.query()
	if len(f.Sources) > 0 {
		params := make([]string, 0, len(f.Sources))
		for _, source := range f.Sources {
			params = append(params, q.arg(source))
		}
		q.where = append(q.where, "source IN ("+strings.Join(params, ", ")+")")
	}
	if f.SourcePrefix != "" {
		q.where = append(q.where, "starts_with(source, "+q.arg(f.SourcePrefix)+")")
	}
	keys := make([]string, 0, len(f.Metadata))
	for k := range f.Metadata {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		// JSONB equality differs from Filter.Match for large numbers and
		// nested Go types. Key existence is a safe SQL prefilter; values
		// are checked with the core matcher after decoding each chunk.
		q.where = append(q.where, "metadata ? "+q.arg(k))
	}
	return q
}

// List returns matching chunks ordered by source, document and position.
func (s *VectorStore) List(ctx context.Context, f rag.Filter) ([]*rag.Chunk, error) {
	q := s.c.chunkQuery(f)
	rows, err := s.c.db.QueryContext(ctx, "SELECT data FROM "+s.c.table("chunks")+
		" WHERE "+q.clause()+" ORDER BY source, document_id, position", q.args...)
	if err != nil {
		return nil, fmt.Errorf("pgvector: list chunks: %w", err)
	}
	defer rows.Close()
	var chunks []*rag.Chunk
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("pgvector: scan chunk: %w", err)
		}
		var ch rag.Chunk
		if err := json.Unmarshal(data, &ch); err != nil {
			return nil, fmt.Errorf("pgvector: decode chunk: %w", err)
		}
		if f.Match(&ch) {
			chunks = append(chunks, &ch)
		}
	}
	return chunks, rows.Err()
}

// Search ranks every matching embedded chunk by cosine similarity. A
// non-positive limit returns all hits. An empty query vector gives zero
// similarity, matching the in-memory store.
func (s *VectorStore) Search(ctx context.Context, vector []float32, limit int, f rag.Filter) ([]rag.Hit, error) {
	text, err := s.c.vectorText(vector)
	if err != nil {
		return nil, err
	}
	q := s.c.chunkQuery(f)
	q.where = append(q.where, "embedding IS NOT NULL")
	score := "0::double precision"
	if text != nil {
		score = cosineSQL(q.arg(text))
	}
	stmt := "WITH candidates AS MATERIALIZED (SELECT data, source, document_id, position, " + score +
		" AS score FROM " + s.c.table("chunks") + " WHERE " + q.clause() + ")" +
		" SELECT data, score FROM candidates ORDER BY score DESC, source, document_id, position"
	if limit > 0 && len(f.Metadata) == 0 {
		stmt += " LIMIT " + q.arg(limit)
	}
	rows, err := s.c.db.QueryContext(ctx, stmt, q.args...)
	if err != nil {
		return nil, fmt.Errorf("pgvector: search chunks: %w", err)
	}
	defer rows.Close()
	var hits []rag.Hit
	for rows.Next() {
		var data []byte
		var hit rag.Hit
		if err := rows.Scan(&data, &hit.Score); err != nil {
			return nil, fmt.Errorf("pgvector: scan hit: %w", err)
		}
		hit.Chunk = new(rag.Chunk)
		if err := json.Unmarshal(data, hit.Chunk); err != nil {
			return nil, fmt.Errorf("pgvector: decode hit: %w", err)
		}
		if !f.Match(hit.Chunk) {
			continue
		}
		hits = append(hits, hit)
		if limit > 0 && len(hits) == limit {
			break
		}
	}
	return hits, rows.Err()
}
