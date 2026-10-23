package sqlstore

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nostosx/agentx/memory"
)

// maxKeyBytes is the longest memory ID or scope stored.
const maxKeyBytes = 255

// MemoryStore is a memory.Store and memory.ScopedDeleter in a SQL table.
// IDs and scopes are UTF-8 strings of at most 255 bytes without NUL
// characters, compared exactly. Embeddings are stored as binary float32
// vectors; List filters by scope in SQL and by tags in Go, and retrieval
// stays in the memory package's retrievers.
type MemoryStore struct {
	c config
}

var (
	_ memory.Store         = (*MemoryStore)(nil)
	_ memory.ScopedDeleter = (*MemoryStore)(nil)
)

// Memory returns a memory store in db.
func Memory(db DB, opts ...Option) *MemoryStore {
	return &MemoryStore{c: newConfig(db, opts)}
}

// Migrate creates the memories table and its scope index if they do not
// exist.
func (s *MemoryStore) Migrate(ctx context.Context) error {
	c := s.c
	return c.migrate(ctx, c.createTable("memories", []string{
		"id " + c.keyType(maxKeyBytes) + " NOT NULL",
		"scope " + c.keyType(maxKeyBytes) + " NOT NULL",
		"created_at " + timeType + " NOT NULL",
		"updated_at " + timeType + " NOT NULL",
		"data " + c.textType() + " NOT NULL",
		"embedding " + c.blobType(),
	}, "id", index{name: "memories_scope", columns: "scope, created_at"}))
}

// validMemoryKey reports whether s fits the memory ID and scope encoding limits.
// An empty ID is rejected separately; an empty scope is valid.
func validMemoryKey(s string) bool {
	return len(s) <= maxKeyBytes && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// Put implements memory.Store.
func (s *MemoryStore) Put(ctx context.Context, e *memory.Entry) error {
	if s.c.err != nil {
		return s.c.err
	}
	switch {
	case e.ID == "":
		return errors.New("memory: entry has no ID")
	case !validMemoryKey(e.ID):
		return fmt.Errorf("memory: invalid entry ID %q: want at most %d bytes of UTF-8 without NUL", e.ID, maxKeyBytes)
	case !validMemoryKey(e.Scope):
		return fmt.Errorf("memory: invalid scope %q: want at most %d bytes of UTF-8 without NUL", e.Scope, maxKeyBytes)
	}
	doc := *e
	doc.Embedding = nil
	data, err := json.Marshal(&doc)
	if err != nil {
		return err
	}
	_, err = s.c.exec(ctx, s.c.upsert("memories", []string{"id", "scope", "created_at", "updated_at", "data", "embedding"}, "id"),
		e.ID, e.Scope, formatTime(e.CreatedAt), formatTime(e.UpdatedAt), string(data), encodeVector(e.Embedding))
	return err
}

// Get implements memory.Store.
func (s *MemoryStore) Get(ctx context.Context, id string) (*memory.Entry, error) {
	if s.c.err != nil {
		return nil, s.c.err
	}
	if id == "" || !validMemoryKey(id) {
		return nil, memory.ErrNotFound
	}
	var data, vec []byte
	err := s.c.queryRow(ctx, "SELECT data, embedding FROM "+s.c.table("memories")+" WHERE id = ?", id).Scan(&data, &vec)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, memory.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return decodeEntry(id, data, vec)
}

func decodeEntry(id string, data, vec []byte) (*memory.Entry, error) {
	var e memory.Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("decode memory %s: %w", id, err)
	}
	var err error
	if e.Embedding, err = decodeVector(vec); err != nil {
		return nil, fmt.Errorf("decode memory %s: %w", id, err)
	}
	e.ID = id
	return &e, nil
}

// Delete implements memory.Store.
func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	return s.DeleteInScope(ctx, id, "")
}

// DeleteInScope implements memory.ScopedDeleter: it deletes the entry
// only when its scope is scope, or any entry when scope is empty, in one
// statement.
func (s *MemoryStore) DeleteInScope(ctx context.Context, id, scope string) error {
	if s.c.err != nil {
		return s.c.err
	}
	if id == "" || !validMemoryKey(id) || !validMemoryKey(scope) {
		return memory.ErrNotFound
	}
	stmt, args := "DELETE FROM "+s.c.table("memories")+" WHERE id = ?", []any{id}
	if scope != "" {
		stmt, args = stmt+" AND scope = ?", append(args, scope)
	}
	res, err := s.c.exec(ctx, stmt, args...)
	if err != nil {
		return err
	}
	n, err := affected(res)
	if err != nil {
		return err
	}
	if n == 0 {
		return memory.ErrNotFound
	}
	return nil
}

// List implements memory.Store.
func (s *MemoryStore) List(ctx context.Context, f memory.Filter) ([]*memory.Entry, error) {
	if s.c.err != nil {
		return nil, s.c.err
	}
	if !validMemoryKey(f.Scope) {
		return nil, nil
	}
	query, args := "SELECT id, data, embedding FROM "+s.c.table("memories"), []any(nil)
	if f.Scope != "" {
		query, args = query+" WHERE scope = ?", append(args, f.Scope)
	}
	rows, err := s.c.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*memory.Entry
	for rows.Next() {
		var (
			id        string
			data, vec []byte
		)
		if err := rows.Scan(&id, &data, &vec); err != nil {
			return nil, err
		}
		e, err := decodeEntry(id, data, vec)
		if err != nil {
			return nil, err
		}
		if f.Match(e) {
			out = append(out, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b *memory.Entry) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// encodeVector encodes v as little-endian float32 values, or NULL.
func encodeVector(v []float32) any {
	if len(v) == 0 {
		return nil
	}
	b := make([]byte, 0, 4*len(v))
	for _, x := range v {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(x))
	}
	return b
}

func decodeVector(b []byte) ([]float32, error) {
	if len(b) == 0 {
		return nil, nil
	}
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("embedding of %d bytes is not a float32 vector", len(b))
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}
