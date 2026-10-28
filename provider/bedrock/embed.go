package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/nostosx/agentx/memory"
	"github.com/nostosx/agentx/model"
)

// DefaultEmbeddingModel is Amazon Titan Text Embeddings V2.
const DefaultEmbeddingModel = "amazon.titan-embed-text-v2:0"

// WithDimensions sets the Titan V2 output dimension: 256, 512 or 1024.
// A zero value uses the model's default. Titan G1 accepts no dimensions option.
func WithDimensions(n int) Option { return func(m *Model) { m.dimensions = n } }

// Embedder implements memory.Embedder through Bedrock InvokeModel. It supports
// Titan Text Embeddings V2 and G1, whose request formats are documented at
// https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-titan-embed-text.html.
// Embedding other Bedrock model families needs their own request format.
type Embedder struct {
	model *Model
	v2    bool
}

var _ memory.Embedder = (*Embedder)(nil)

// NewEmbedder accepts the model's authentication, HTTP and endpoint options.
// Without WithModel it uses DefaultEmbeddingModel. Embed sends one request per
// input in order; completed inputs are discarded when a later request fails.
func NewEmbedder(opts ...Option) (*Embedder, error) {
	m, err := New(append([]Option{WithModel(DefaultEmbeddingModel)}, opts...)...)
	if err != nil {
		return nil, err
	}
	v2 := strings.HasPrefix(m.model, "amazon.titan-embed-text-v2:")
	if !v2 && m.model != "amazon.titan-embed-text-v1" {
		return nil, errors.New("bedrock: embedder requires a Titan text embedding model ID")
	}
	if m.dimensions != 0 && (!v2 || (m.dimensions != 256 && m.dimensions != 512 && m.dimensions != 1024)) {
		return nil, errors.New("bedrock: Titan V2 dimensions must be 256, 512 or 1024")
	}
	return &Embedder{model: m, v2: v2}, nil
}

// Embed returns one finite, nonempty vector per input in the original order.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	for _, text := range texts {
		if strings.TrimSpace(text) == "" {
			return nil, model.Permanent(errors.New("bedrock: cannot embed empty text"))
		}
	}
	out := make([][]float32, 0, len(texts))
	for _, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vec, err := e.embed(ctx, text)
		if err != nil {
			return nil, err
		}
		if len(out) > 0 && len(vec) != len(out[0]) {
			return nil, errors.New("bedrock: inconsistent embedding dimensions")
		}
		out = append(out, vec)
	}
	return out, nil
}

func (e *Embedder) embed(ctx context.Context, text string) ([]float32, error) {
	body := map[string]any{"inputText": text}
	if e.v2 {
		body["normalize"] = true
		body["embeddingTypes"] = []string{"float"}
		if e.model.dimensions != 0 {
			body["dimensions"] = e.model.dimensions
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("bedrock: encode embedding request: %w", err)
	}
	resp, err := e.model.post(ctx, e.model.model, "invoke", data, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var value struct {
		Embedding        []float32            `json:"embedding"`
		EmbeddingsByType map[string][]float32 `json:"embeddingsByType"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&value); err != nil {
		return nil, fmt.Errorf("bedrock: decode embedding response: %w", err)
	}
	vec := value.Embedding
	if len(vec) == 0 {
		vec = value.EmbeddingsByType["float"]
	}
	if len(vec) == 0 || (e.model.dimensions != 0 && len(vec) != e.model.dimensions) {
		return nil, errors.New("bedrock: invalid embedding dimensions")
	}
	for _, v := range vec {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, errors.New("bedrock: nonfinite embedding")
		}
	}
	return vec, nil
}
