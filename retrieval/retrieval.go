// Package retrieval provides Qdrant and Elasticsearch retrieval and ingestion.
// Applications own index creation, ingestion, permissions and vector spaces.
package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/nostosx/servex-contrib/transport/httpx"

	"github.com/nostosx/agentx/memory"
	"github.com/nostosx/agentx/rag"
)

// Options configures a trusted endpoint and an application-owned index.
type Options struct {
	Endpoint string
	Index    string
	APIKey   string
	Client   *http.Client
	// Namespace is always enforced server-side, independently of Query.Filter.
	Namespace string
	// MaxResults defaults to 100. Unbounded queries are rejected.
	MaxResults int
	// Embedder is required by Qdrant; Elasticsearch uses BM25 when nil,
	// otherwise exact cosine script scoring over the embedding field.
	Embedder memory.Embedder
	// VectorName selects a Qdrant named vector. Empty selects its unnamed vector.
	VectorName string
}

// Retriever queries a server-managed index. It does not implement VectorStore:
// neither backend provides the source replacement transaction that it requires.
type Retriever struct {
	o        Options
	http     *http.Client
	provider string
	owned    bool
}

var segment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// NewQdrant queries a Cosine collection with payload {namespace, chunk}.
func NewQdrant(o Options) (*Retriever, error) {
	if o.Embedder == nil {
		return nil, errors.New("retrieval: Qdrant requires an embedder")
	}
	return newRetriever(o, "qdrant")
}

// NewElasticsearch queries documents {namespace, chunk, embedding}; map
// chunk.content as text, identity/metadata fields as keyword, and embedding
// as dense_vector when using an embedder.
func NewElasticsearch(o Options) (*Retriever, error) { return newRetriever(o, "elasticsearch") }

func newRetriever(o Options, provider string) (*Retriever, error) {
	u, err := url.Parse(o.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("retrieval: invalid endpoint")
	}
	if !segment.MatchString(o.Index) || o.Namespace == "" || strings.ContainsAny(o.APIKey, "\r\n") {
		return nil, errors.New("retrieval: index and namespace are required; invalid configuration")
	}
	if o.MaxResults == 0 {
		o.MaxResults = 100
	}
	if o.MaxResults < 1 || o.MaxResults > 10000 {
		return nil, errors.New("retrieval: MaxResults must be between 1 and 10000")
	}
	h, err := httpx.NewRestrictedHTTPClient(httpx.HTTPPolicy{AllowPrivateNetwork: true})
	if err != nil {
		return nil, err
	}
	if o.Client != nil {
		*h = *o.Client
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	o.Endpoint = strings.TrimRight(o.Endpoint, "/")
	return &Retriever{o: o, http: h, provider: provider, owned: o.Client == nil}, nil
}

// Close releases idle connections of an internally constructed HTTP client.
// An injected client's transport remains owned by its application.
func (r *Retriever) Close() error {
	if r.owned {
		r.http.CloseIdleConnections()
	}
	return nil
}

func (r *Retriever) vector(ctx context.Context, text string) ([]float32, error) {
	v, err := r.o.Embedder.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(v) != 1 || len(v[0]) == 0 {
		return nil, errors.New("retrieval: invalid embedding count or dimension")
	}
	var norm float64
	for _, x := range v[0] {
		if math.IsInf(float64(x), 0) || math.IsNaN(float64(x)) {
			return nil, errors.New("retrieval: nonfinite embedding")
		}
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return nil, errors.New("retrieval: zero embedding")
	}
	return v[0], nil
}

// Retrieve implements rag.Retriever. Limit must be positive and bounded;
// unsupported filters fail before an embedding or network request is made.
func (r *Retriever) Retrieve(ctx context.Context, q rag.Query) ([]rag.Hit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q.Limit <= 0 || q.Limit > r.o.MaxResults || math.IsNaN(q.MinScore) || math.IsInf(q.MinScore, 0) {
		return nil, errors.New("retrieval: positive bounded limit and finite MinScore are required")
	}
	if strings.TrimSpace(q.Text) == "" {
		return nil, nil
	}
	if r.provider == "qdrant" && q.Filter.SourcePrefix != "" {
		return nil, errors.New("retrieval: Qdrant SourcePrefix is unsupported; use exact Sources")
	}
	for key, v := range q.Filter.Metadata {
		if !segment.MatchString(key) {
			return nil, errors.New("retrieval: metadata filter key must be a simple field name")
		}
		switch v.(type) {
		case string, bool, int, int32, int64, float32, float64:
		default:
			return nil, errors.New("retrieval: only scalar metadata filters are supported")
		}
		if _, err := json.Marshal(v); err != nil {
			return nil, errors.New("retrieval: invalid metadata filter value")
		}
	}
	if r.provider == "qdrant" {
		return r.qdrant(ctx, q)
	}
	return r.elasticsearch(ctx, q)
}

func (r *Retriever) post(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("retrieval: encode request: %w", err)
	}
	return r.request(ctx, http.MethodPost, path, "application/json", b, out)
}

func (r *Retriever) request(ctx context.Context, method, path, contentType string, b []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, r.o.Endpoint+path, bytes.NewReader(b))
	if err != nil {
		return errors.New("retrieval: create request")
	}
	req.Header.Set("Content-Type", contentType)
	if r.o.APIKey != "" {
		if r.provider == "qdrant" {
			req.Header.Set("api-key", r.o.APIKey)
		} else {
			req.Header.Set("Authorization", "ApiKey "+r.o.APIKey)
		}
	}
	res, err := r.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("retrieval: transport failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("retrieval: %s HTTP %d", r.provider, res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
	if err != nil {
		return fmt.Errorf("retrieval: read response: %w", err)
	}
	if len(data) > 16<<20 {
		return errors.New("retrieval: response exceeds 16 MiB")
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("retrieval: invalid response JSON")
	}
	return nil
}

func (r *Retriever) qdrant(ctx context.Context, q rag.Query) ([]rag.Hit, error) {
	v, err := r.vector(ctx, q.Text)
	if err != nil {
		return nil, err
	}
	must := []any{map[string]any{"key": "namespace", "match": map[string]any{"value": r.o.Namespace}}}
	if len(q.Filter.Sources) > 0 {
		must = append(must, map[string]any{"key": "chunk.source", "match": map[string]any{"any": q.Filter.Sources}})
	}
	for k, v := range q.Filter.Metadata {
		condition := map[string]any{"key": "chunk.metadata." + k}
		switch v.(type) {
		case string, bool:
			condition["match"] = map[string]any{"value": v}
		default:
			condition["range"] = map[string]any{"gte": v, "lte": v}
		}
		must = append(must, condition)
	}
	body := map[string]any{"query": v, "limit": q.Limit, "with_payload": true, "filter": map[string]any{"must": must, "must_not": []any{map[string]any{"key": "agentx_record", "match": map[string]any{"value": "manifest"}}}}, "score_threshold": q.MinScore}
	if r.o.VectorName != "" {
		body["using"] = r.o.VectorName
	}
	var wire struct {
		Result *struct {
			Points []struct {
				Score   float64 `json:"score"`
				Payload struct {
					Namespace string     `json:"namespace"`
					Chunk     *rag.Chunk `json:"chunk"`
				} `json:"payload"`
			} `json:"points"`
		} `json:"result"`
	}
	if err := r.post(ctx, "/collections/"+r.o.Index+"/points/query", body, &wire); err != nil {
		return nil, err
	}
	if wire.Result == nil || wire.Result.Points == nil {
		return nil, errors.New("retrieval: missing Qdrant query result")
	}
	out := make([]rag.Hit, 0, len(wire.Result.Points))
	for _, p := range wire.Result.Points {
		if err := r.checkChunk(p.Payload.Namespace, p.Payload.Chunk, p.Score, q); err != nil {
			return nil, err
		}
		out = append(out, rag.Hit{Chunk: p.Payload.Chunk, Score: p.Score})
	}
	return bounded(out, q.Limit), nil
}

func (r *Retriever) elasticsearch(ctx context.Context, q rag.Query) ([]rag.Hit, error) {
	filters := []any{map[string]any{"term": map[string]any{"namespace": r.o.Namespace}}}
	if len(q.Filter.Sources) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"chunk.source": q.Filter.Sources}})
	}
	if q.Filter.SourcePrefix != "" {
		filters = append(filters, map[string]any{"prefix": map[string]any{"chunk.source": q.Filter.SourcePrefix}})
	}
	for k, v := range q.Filter.Metadata {
		filters = append(filters, map[string]any{"term": map[string]any{"chunk.metadata." + k: v}})
	}
	exclude := []any{map[string]any{"term": map[string]any{"agentx_record": "manifest"}}}
	var query any = map[string]any{"bool": map[string]any{"filter": filters, "must_not": exclude, "must": map[string]any{"match": map[string]any{"chunk.content": q.Text}}}}
	minScore := q.MinScore
	if r.o.Embedder != nil {
		v, err := r.vector(ctx, q.Text)
		if err != nil {
			return nil, err
		}
		filters = append(filters, map[string]any{"exists": map[string]any{"field": "embedding"}})
		query = map[string]any{"script_score": map[string]any{"query": map[string]any{"bool": map[string]any{"filter": filters, "must_not": exclude}}, "script": map[string]any{"source": "cosineSimilarity(params.vector, 'embedding') + 1.0", "params": map[string]any{"vector": v}}}}
		minScore++
	}
	minScore = math.Max(0, minScore)
	body := map[string]any{"size": q.Limit, "query": query, "min_score": minScore, "_source": []string{"namespace", "chunk"}}
	var wire struct {
		TimedOut bool `json:"timed_out"`
		Shards   struct {
			Failed int `json:"failed"`
		} `json:"_shards"`
		Hits *struct {
			Hits []struct {
				Score  float64 `json:"_score"`
				Source struct {
					Namespace string     `json:"namespace"`
					Chunk     *rag.Chunk `json:"chunk"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := r.post(ctx, "/"+r.o.Index+"/_search", body, &wire); err != nil {
		return nil, err
	}
	if wire.TimedOut || wire.Shards.Failed > 0 || wire.Hits == nil || wire.Hits.Hits == nil {
		return nil, errors.New("retrieval: incomplete Elasticsearch search")
	}
	out := make([]rag.Hit, 0, len(wire.Hits.Hits))
	for _, p := range wire.Hits.Hits {
		if r.o.Embedder != nil {
			p.Score--
		}
		if err := r.checkChunk(p.Source.Namespace, p.Source.Chunk, p.Score, q); err != nil {
			return nil, err
		}
		out = append(out, rag.Hit{Chunk: p.Source.Chunk, Score: p.Score})
	}
	return bounded(out, q.Limit), nil
}

func (r *Retriever) checkChunk(namespace string, c *rag.Chunk, score float64, q rag.Query) error {
	if namespace != r.o.Namespace || c == nil || c.ID == "" || c.Source == "" || !q.Filter.Match(c) || math.IsNaN(score) || math.IsInf(score, 0) || score < q.MinScore-1e-6 {
		return errors.New("retrieval: invalid or out-of-scope result")
	}
	return nil
}

func bounded(h []rag.Hit, n int) []rag.Hit {
	if len(h) > n {
		return h[:n]
	}
	return h
}

var _ rag.Retriever = (*Retriever)(nil)
