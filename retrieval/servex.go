package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/nostosx/servex/storage/search"
	"github.com/nostosx/servex/storage/vector"

	"github.com/nostosx/agentx/memory"
	"github.com/nostosx/agentx/rag"
)

// ServexOptions adapts injected servex capabilities without depending on a backend.
// Vector search requires cosine distance and an embedder in the indexed vector space.
// MaxResults defaults to 100 and is bounded to 10000. Neither constructor creates
// tables, installs extensions, generates document embeddings or owns its backend.
type ServexOptions struct {
	Vectors     vector.Searcher
	Text        search.Searcher
	Embedder    memory.Embedder
	MaxResults  int
	Approximate bool
}

// Servex is a read-only RAG retriever. Index through VectorRecord/TextDocument.
// Use core rag.Hybrid to fuse independent text and vector retrievers.
type Servex struct {
	options ServexOptions
	vectors bool
}

func NewServexVector(o ServexOptions) (*Servex, error) {
	if o.Vectors == nil || o.Embedder == nil {
		return nil, errors.New("retrieval: vector searcher and embedder required")
	}
	return newServex(o, true)
}
func NewServexText(o ServexOptions) (*Servex, error) {
	if o.Text == nil {
		return nil, errors.New("retrieval: text searcher required")
	}
	return newServex(o, false)
}
func newServex(o ServexOptions, vectors bool) (*Servex, error) {
	if o.MaxResults == 0 {
		o.MaxResults = 100
	}
	if o.MaxResults < 1 || o.MaxResults > 10000 {
		return nil, errors.New("retrieval: invalid maximum result count")
	}
	return &Servex{o, vectors}, nil
}

func chunkID(c *rag.Chunk) string {
	identity, _ := json.Marshal([2]string{c.Source, c.ID})
	sum := sha256.Sum256(identity)
	return hex.EncodeToString(sum[:])
}
func labels(c *rag.Chunk) map[string]string {
	m := map[string]string{"agentx_source": c.Source}
	for k, v := range c.Metadata {
		if value, ok := v.(string); ok {
			m["agentx_meta_"+k] = value
		}
	}
	return m
}
func encodeChunk(c *rag.Chunk) ([]byte, error) {
	if c == nil || c.ID == "" || c.Source == "" || !utf8.ValidString(c.ID) || !utf8.ValidString(c.Source) {
		return nil, errors.New("retrieval: chunk ID and source required")
	}
	return json.Marshal(c)
}

// VectorRecord encodes a chunk for a servex vector store. Source and ID jointly
// identify it; the store's trusted namespace supplies the tenant boundary.
func VectorRecord(c *rag.Chunk) (vector.Record, error) {
	payload, err := encodeChunk(c)
	if err != nil {
		return vector.Record{}, err
	}
	return vector.Record{ID: chunkID(c), Vector: append([]float32(nil), c.Embedding...), Metadata: labels(c), Payload: payload}, nil
}

// TextDocument encodes the same chunk identity for a servex document index.
func TextDocument(c *rag.Chunk) (search.Document, error) {
	payload, err := encodeChunk(c)
	if err != nil {
		return search.Document{}, err
	}
	return search.Document{ID: chunkID(c), Text: c.Text(), Metadata: labels(c), Payload: payload}, nil
}
func servexFilter(f rag.Filter) (map[string]string, error) {
	if len(f.Sources) > 1 || f.SourcePrefix != "" {
		return nil, errors.New("retrieval: servex adapters support one exact source, not source OR/prefix filters")
	}
	m := map[string]string{}
	if len(f.Sources) == 1 {
		m["agentx_source"] = f.Sources[0]
	}
	for k, v := range f.Metadata {
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("retrieval: servex metadata filters require string values")
		}
		m["agentx_meta_"+k] = s
	}
	return m, nil
}
func decodeChunk(id string, payload []byte, f rag.Filter) (*rag.Chunk, error) {
	var c rag.Chunk
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("retrieval: decode indexed chunk: %w", err)
	}
	if c.ID == "" || c.Source == "" || chunkID(&c) != id || !f.Match(&c) {
		return nil, errors.New("retrieval: inconsistent indexed chunk or filter result")
	}
	return &c, nil
}
func (s *Servex) Retrieve(ctx context.Context, q rag.Query) ([]rag.Hit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q.Limit < 1 || q.Limit > s.options.MaxResults || math.IsNaN(q.MinScore) || math.IsInf(q.MinScore, 0) {
		return nil, errors.New("retrieval: invalid limit or score")
	}
	metadata, err := servexFilter(q.Filter)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(q.Text) == "" {
		return nil, ctx.Err()
	}
	var result []rag.Hit
	if s.vectors {
		v, err := queryVector(ctx, s.options.Embedder, q.Text)
		if err != nil {
			return nil, err
		}
		hits, err := s.options.Vectors.Search(ctx, vector.Query{Vector: v, Metadata: metadata, Limit: q.Limit, Approximate: s.options.Approximate})
		if err != nil {
			return nil, err
		}
		if len(hits) > q.Limit {
			return nil, errors.New("retrieval: backend exceeded result limit")
		}
		for _, hit := range hits {
			c, err := decodeChunk(hit.Record.ID, hit.Record.Payload, q.Filter)
			if err != nil {
				return nil, err
			}
			score := 1 - hit.Distance
			if math.IsNaN(score) || math.IsInf(score, 0) {
				return nil, errors.New("retrieval: nonfinite vector score")
			}
			if score > 0 && score >= q.MinScore {
				result = append(result, rag.Hit{Chunk: c, Score: score})
			}
		}
	} else {
		hits, err := s.options.Text.Search(ctx, search.Query{Text: q.Text, Metadata: metadata, Limit: q.Limit})
		if err != nil {
			return nil, err
		}
		if len(hits) > q.Limit {
			return nil, errors.New("retrieval: backend exceeded result limit")
		}
		for _, hit := range hits {
			c, err := decodeChunk(hit.Document.ID, hit.Document.Payload, q.Filter)
			if err != nil {
				return nil, err
			}
			if math.IsNaN(hit.Score) || math.IsInf(hit.Score, 0) {
				return nil, errors.New("retrieval: nonfinite text score")
			}
			if hit.Score >= q.MinScore {
				result = append(result, rag.Hit{Chunk: c, Score: hit.Score})
			}
		}
	}
	return result, nil
}

var _ rag.Retriever = (*Servex)(nil)
