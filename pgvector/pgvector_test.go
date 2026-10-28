package pgvector

import (
	"database/sql"
	"math"
	"strings"
	"testing"

	"github.com/nostosx/agentx/rag"
)

func TestConfigurationRejectsUnsafeIdentifiers(t *testing.T) {
	for _, opts := range [][]Option{
		{WithPrefix(`x"; DROP TABLE users; --`)},
		{WithPrefix("1bad")},
		{WithPrefix(strings.Repeat("a", 33))},
		{WithNamespace("")},
		{WithNamespace("one\x00two")},
		{WithNamespace(strings.Repeat("a", 256))},
		{WithDimensions(-1)},
		{WithDimensions(16001)},
	} {
		if _, err := Vectors(new(sql.DB), opts...); err == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
	if _, err := Memory(nil); err == nil {
		t.Fatal("nil database accepted")
	}
}

func TestReplacementValidatesBeforeOpeningTransaction(t *testing.T) {
	s, err := Vectors(new(sql.DB), WithDimensions(2))
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []rag.SourceChunks{
		{},
		{Info: rag.SourceInfo{Source: "a"}, Chunks: []*rag.Chunk{nil}},
		{Info: rag.SourceInfo{Source: "a"}, Chunks: []*rag.Chunk{{ID: "x"}, {ID: "x"}}},
		{Info: rag.SourceInfo{Source: "a"}, Chunks: []*rag.Chunk{{ID: "x", Embedding: []float32{1}}}},
		{Info: rag.SourceInfo{Source: "a"}, Chunks: []*rag.Chunk{{ID: "x", Embedding: []float32{1, float32(math.NaN())}}}},
		{Info: rag.SourceInfo{Source: "a"}, Chunks: []*rag.Chunk{{ID: "x", Metadata: map[string]any{"invalid": make(chan int)}}}},
	} {
		if err := s.Replace(t.Context(), src); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
}

func TestFiltersParameterizeUntrustedValues(t *testing.T) {
	c, err := newConfig(new(sql.DB), nil)
	if err != nil {
		t.Fatal(err)
	}
	const input = `x' OR TRUE; --`
	q := c.chunkQuery(rag.Filter{Sources: []string{input}, SourcePrefix: input, Metadata: map[string]any{input: input}})
	if strings.Contains(q.clause(), input) || len(q.args) != 4 {
		t.Fatalf("filter interpolates input or loses parameters: %s, %d", q.clause(), len(q.args))
	}
}
