package openai

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

const (
	// DefaultEmbeddingModel is the embedding model used for OpenAI's API
	// (https://developers.openai.com/api/docs/guides/embeddings).
	DefaultEmbeddingModel = "text-embedding-3-small"
	// maxEmbeddingInputs is the most inputs one request may carry
	// (https://developers.openai.com/api/reference/resources/embeddings/methods/create).
	maxEmbeddingInputs = 2048
)

// WithDimensions sets the length of the vectors an Embedder returns. It is
// supported by text-embedding-3 and later models, and by some compatible
// services such as Ollama; Mistral receives it as output_dimension.
func WithDimensions(n int) Option { return func(m *Model) { m.dimensions = n } }

// WithBatchSize sets how many texts an Embedder sends per request.
// Defaults to 2048, the most the API accepts. The API also limits a
// request to 300,000 tokens in total, so lower it for long texts.
func WithBatchSize(n int) Option { return func(m *Model) { m.batchSize = n } }

// Embedder implements memory.Embedder with the embeddings endpoint
// (POST /embeddings) of OpenAI or a compatible service.
//
// Errors are those of the chat models: API errors are *model.APIError and
// invalid input is permanent, so callers using model.IsRetryable, such as
// the retry.Embedder of github.com/nostosx/agentx-contrib/middleware, retry
// only what may succeed.
type Embedder struct {
	http       utils.HTTPClient
	model      string
	dimensions int
	batchSize  int
	dimField   string
}

// NewEmbedder returns an Embedder. It takes the options of New: WithModel
// selects the embedding model, which defaults to DefaultEmbeddingModel for
// OpenAI and to the preset's embedding model, if any, otherwise. Presets
// configure the endpoint, for example Ollama() for a local Ollama server.
func NewEmbedder(opts ...Option) (*Embedder, error) {
	m := newModel(opts)
	if err := m.resolve(DefaultEmbeddingModel, func(p *preset) string { return p.embedModel }); err != nil {
		return nil, err
	}
	e := &Embedder{http: m.http, model: m.model, dimensions: m.dimensions, batchSize: m.batchSize, dimField: m.quirks().dimensionsField}
	if e.batchSize <= 0 || e.batchSize > maxEmbeddingInputs {
		e.batchSize = maxEmbeddingInputs
	}
	return e, nil
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

// Embed implements memory.Embedder. Texts are sent in batches; the
// vectors are returned in the order of texts.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if slices.Contains(texts, "") {
		return nil, model.Permanent(errors.New("openai: cannot embed empty text"))
	}
	out := make([][]float32, 0, len(texts))
	for batch := range slices.Chunk(texts, e.batchSize) {
		vecs, err := e.embed(ctx, batch)
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (e *Embedder) embed(ctx context.Context, texts []string) ([][]float32, error) {
	body := map[string]any{"model": e.model, "input": texts}
	if e.dimensions > 0 {
		field := "dimensions"
		if e.dimField != "" {
			field = e.dimField
		}
		body[field] = e.dimensions
	}
	b, err := utils.EncodeBody(providerName, body, nil)
	if err != nil {
		return nil, err
	}
	var resp embeddingResponse
	if err := e.http.PostJSON(ctx, "/embeddings", b, nil, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("openai: got %d embeddings for %d inputs", len(resp.Data), len(texts))
	}
	vecs := make([][]float32, len(texts))
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= len(texts) || vecs[d.Index] != nil {
			return nil, fmt.Errorf("openai: embedding index %d out of order", d.Index)
		}
		vecs[d.Index] = d.Embedding
	}
	return vecs, nil
}
