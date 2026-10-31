package retrieval

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/nostosx/servex/storage/search"
	"github.com/nostosx/servex/storage/vector"

	"github.com/nostosx/agentx/rag"
)

type vectorFunc func(context.Context, vector.Query) ([]vector.Hit, error)

func (f vectorFunc) Search(ctx context.Context, q vector.Query) ([]vector.Hit, error) {
	return f(ctx, q)
}

type searchFunc func(context.Context, search.Query) ([]search.Hit, error)

func (f searchFunc) Search(ctx context.Context, q search.Query) ([]search.Hit, error) {
	return f(ctx, q)
}

type fixtureEmbedder struct{}

func (fixtureEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return [][]float32{{1, 0}}, nil
}

func TestServexVectorFilteringAndIdentity(t *testing.T) {
	c := &rag.Chunk{ID: "chunk", Source: "source", Content: "content", Embedding: []float32{1, 0}, Metadata: map[string]any{"kind": "doc"}}
	r, err := VectorRecord(c)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServexVector(ServexOptions{Embedder: fixtureEmbedder{}, Approximate: true, Vectors: vectorFunc(func(_ context.Context, q vector.Query) ([]vector.Hit, error) {
		if q.Metadata["agentx_source"] != "source" || q.Metadata["agentx_meta_kind"] != "doc" || !q.Approximate {
			t.Fatalf("query: %+v", q)
		}
		return []vector.Hit{{Record: r, Distance: 0.25}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := s.Retrieve(t.Context(), rag.Query{Text: "question", Limit: 2, Filter: rag.Filter{Sources: []string{"source"}, Metadata: map[string]any{"kind": "doc"}}})
	if err != nil || len(hits) != 1 || hits[0].Score != 0.75 || hits[0].Chunk.Content != "content" {
		t.Fatalf("%v: %v", hits, err)
	}
	r.ID = "tampered"
	if _, err := s.Retrieve(t.Context(), rag.Query{Text: "question", Limit: 2, Filter: rag.Filter{Sources: []string{"source"}, Metadata: map[string]any{"kind": "doc"}}}); err == nil {
		t.Fatal("accepted identity mismatch")
	}
}
func TestServexTextRejectsUnsupportedFilters(t *testing.T) {
	sentinel := errors.New("backend")
	s, err := NewServexText(ServexOptions{Text: searchFunc(func(context.Context, search.Query) ([]search.Hit, error) { return nil, sentinel })})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retrieve(t.Context(), rag.Query{Text: "x", Limit: 1}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	for _, filter := range []rag.Filter{{Sources: []string{"a", "b"}}, {SourcePrefix: "prefix"}, {Metadata: map[string]any{"number": 1}}} {
		if _, err := s.Retrieve(t.Context(), rag.Query{Text: "x", Limit: 1, Filter: filter}); err == nil || errors.Is(err, sentinel) {
			t.Fatal("unsupported filter reached backend", err)
		}
	}
}

type embeddingFunc func(context.Context, []string) ([][]float32, error)

func (f embeddingFunc) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}
func TestServexCancellationAndInvalidEmbeddings(t *testing.T) {
	for _, v := range [][][]float32{nil, {{}}, {{0, 0}}, {{float32(math.NaN())}}, {{float32(math.Inf(1))}}} {
		s, err := NewServexVector(ServexOptions{Embedder: embeddingFunc(func(context.Context, []string) ([][]float32, error) { return v, nil }), Vectors: vectorFunc(func(context.Context, vector.Query) ([]vector.Hit, error) {
			t.Fatal("invalid embedding reached backend")
			return nil, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Retrieve(t.Context(), rag.Query{Text: "query", Limit: 1}); err == nil {
			t.Fatal("invalid embedding accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s, err := NewServexText(ServexOptions{Text: searchFunc(func(context.Context, search.Query) ([]search.Hit, error) {
		t.Fatal("cancelled query reached backend")
		return nil, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retrieve(ctx, rag.Query{Text: "query", Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestServexRejectsUnboundedBackendResults(t *testing.T) {
	s, err := NewServexText(ServexOptions{Text: searchFunc(func(context.Context, search.Query) ([]search.Hit, error) { return make([]search.Hit, 2), nil })})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retrieve(t.Context(), rag.Query{Text: "query", Limit: 1}); err == nil {
		t.Fatal("backend result limit ignored")
	}
}

func TestServexChunkIdentityIsUnambiguous(t *testing.T) {
	a := &rag.Chunk{ID: "b\x00c", Source: "a"}
	b := &rag.Chunk{ID: "c", Source: "a\x00b"}
	if chunkID(a) == chunkID(b) {
		t.Fatal("distinct identities collide")
	}
	if _, err := TextDocument(&rag.Chunk{ID: "id", Source: string([]byte{0xff})}); err == nil {
		t.Fatal("invalid UTF-8 identity accepted")
	}
}
