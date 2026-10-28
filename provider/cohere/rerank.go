// Package cohere adapts the Cohere Rerank v2 API to rag.Reranker without an SDK.
package cohere

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
	"github.com/nostosx/agentx/rag"
)

const (
	// DefaultModel is a model documented by Cohere's Rerank v2 reference.
	DefaultModel = "rerank-v5.0-pro"
	// DefaultBaseURL is the Cohere API origin.
	DefaultBaseURL = "https://api.cohere.com"
)

// Reranker calls https://docs.cohere.com/v2/reference/rerank.
// It is safe for concurrent use after construction.
type Reranker struct {
	http            utils.HTTPClient
	model           string
	topN            int
	maxTokensPerDoc int
	minScore        float64
}

// Option configures a reranker.
type Option func(*Reranker)

// WithAPIKey sets the bearer token, otherwise read from COHERE_API_KEY.
func WithAPIKey(key string) Option {
	return func(r *Reranker) {
		value := ""
		if key != "" {
			value = "Bearer " + key
		}
		r.http.Header.Set("Authorization", value)
	}
}

// WithTokenSource obtains a bearer token for each request. Environment API keys
// are not read when it is configured.
func WithTokenSource(source func(context.Context) (string, error)) Option {
	return func(r *Reranker) { r.http.Token = source }
}

// WithBaseURL sets an API origin that accepts POST /v2/rerank.
func WithBaseURL(baseURL string) Option { return func(r *Reranker) { r.http.BaseURL = baseURL } }

// WithModel sets a reranking model.
func WithModel(name string) Option { return func(r *Reranker) { r.model = name } }

// WithHTTPClient sets the caller-owned HTTP client.
func WithHTTPClient(client *http.Client) Option { return func(r *Reranker) { r.http.Client = client } }

// WithTopN limits the number of results; zero requests every candidate.
func WithTopN(n int) Option { return func(r *Reranker) { r.topN = n } }

// WithMaxTokensPerDoc sets Cohere's per-document truncation budget. Zero uses
// the API default. Documents longer than this budget are truncated by Cohere.
func WithMaxTokensPerDoc(n int) Option {
	return func(r *Reranker) { r.maxTokensPerDoc = n }
}

// WithMinScore drops results scoring below score, in the API's [0,1] scale.
func WithMinScore(score float64) Option { return func(r *Reranker) { r.minScore = score } }

// NewReranker constructs a Cohere Rerank v2 adapter. No requests are sent.
func NewReranker(opts ...Option) (*Reranker, error) {
	r := &Reranker{http: utils.HTTPClient{Provider: "cohere", BaseURL: DefaultBaseURL, Header: http.Header{}}, model: DefaultModel}
	for _, opt := range opts {
		opt(r)
	}
	if r.http.Token == nil && !utils.HasAuth(r.http.Header, "Authorization") {
		if key := os.Getenv("COHERE_API_KEY"); key != "" {
			r.http.Header.Set("Authorization", "Bearer "+key)
		}
	}
	if r.http.Token == nil && !utils.HasAuth(r.http.Header, "Authorization") {
		return nil, errors.New("cohere: no API key; set COHERE_API_KEY or use WithAPIKey")
	}
	if strings.TrimSpace(r.model) == "" || r.topN < 0 || r.maxTokensPerDoc < 0 || math.IsNaN(r.minScore) || r.minScore < 0 || r.minScore > 1 {
		return nil, errors.New("cohere: invalid reranker configuration")
	}
	return r, nil
}

// Rerank returns newly scored hits in the order supplied by the API. It never
// changes the input slice or chunks. At most 1000 candidates are accepted in one
// call; splitting calls would lose a single global ranking.
func (r *Reranker) Rerank(ctx context.Context, query string, hits []rag.Hit) ([]rag.Hit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(query) == "" || len(hits) > 1000 {
		return nil, model.Permanent(errors.New("cohere: rerank needs a query and at most 1000 candidates"))
	}
	docs := make([]string, len(hits))
	for i, hit := range hits {
		if hit.Chunk == nil || strings.TrimSpace(hit.Chunk.Text()) == "" {
			return nil, model.Permanent(fmt.Errorf("cohere: candidate %d has no text", i))
		}
		docs[i] = hit.Chunk.Text()
	}
	body := map[string]any{"model": r.model, "query": query, "documents": docs}
	if r.topN > 0 {
		body["top_n"] = min(r.topN, len(hits))
	}
	if r.maxTokensPerDoc > 0 {
		body["max_tokens_per_doc"] = r.maxTokensPerDoc
	}
	data, err := utils.EncodeBody("cohere", body, nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Results []struct {
			Index *int     `json:"index"`
			Score *float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := r.http.PostJSON(ctx, "/v2/rerank", data, nil, &response); err != nil {
		return nil, err
	}
	if response.Results == nil || (r.topN > 0 && len(response.Results) > r.topN) {
		return nil, errors.New("cohere: missing or excessive rerank results")
	}
	seen := make([]bool, len(hits))
	out := make([]rag.Hit, 0, len(response.Results))
	lastScore := math.Inf(1)
	for _, result := range response.Results {
		if result.Index == nil || result.Score == nil || *result.Index < 0 || *result.Index >= len(hits) || seen[*result.Index] || math.IsNaN(*result.Score) || *result.Score < 0 || *result.Score > 1 || *result.Score > lastScore {
			return nil, errors.New("cohere: invalid rerank result index or score")
		}
		seen[*result.Index], lastScore = true, *result.Score
		if *result.Score >= r.minScore {
			hit := hits[*result.Index]
			hit.Score = *result.Score
			out = append(out, hit)
		}
	}
	return out, nil
}

var _ rag.Reranker = (*Reranker)(nil)
