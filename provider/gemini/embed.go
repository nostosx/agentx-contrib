package gemini

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

// DefaultEmbeddingModel is the embedding model used when none is
// configured (https://ai.google.dev/gemini-api/docs/embeddings).
const DefaultEmbeddingModel = "gemini-embedding-2"

// defaultEmbedBatch is the number of texts per batchEmbedContents request.
// The API reference documents no limit; 100 keeps requests moderate.
const defaultEmbedBatch = 100

// Task types (https://ai.google.dev/api/embeddings#tasktype). Use
// TaskRetrievalDocument for an Embedder that embeds stored entries and
// TaskRetrievalQuery for one that embeds queries; see
// memory.WithQueryEmbedder.
const (
	TaskRetrievalQuery     = "RETRIEVAL_QUERY"
	TaskRetrievalDocument  = "RETRIEVAL_DOCUMENT"
	TaskSemanticSimilarity = "SEMANTIC_SIMILARITY"
	TaskClassification     = "CLASSIFICATION"
	TaskClustering         = "CLUSTERING"
	TaskQuestionAnswering  = "QUESTION_ANSWERING"
	TaskFactVerification   = "FACT_VERIFICATION"
	TaskCodeRetrievalQuery = "CODE_RETRIEVAL_QUERY"
)

// taskPrefixes are the text prefixes that replace task types for
// gemini-embedding-2 and later, which reject taskType
// (https://ai.google.dev/gemini-api/docs/embeddings).
var taskPrefixes = map[string]string{
	TaskRetrievalQuery:     "task: search result | query: ",
	TaskQuestionAnswering:  "task: question answering | query: ",
	TaskFactVerification:   "task: fact checking | query: ",
	TaskCodeRetrievalQuery: "task: code retrieval | query: ",
	TaskClassification:     "task: classification | query: ",
	TaskClustering:         "task: clustering | query: ",
	TaskSemanticSimilarity: "task: sentence similarity | query: ",
	TaskRetrievalDocument:  "title: none | text: ",
}

// WithTaskType sets the task an Embedder optimizes its vectors for, one of
// the Task constants. gemini-embedding-001 receives it as taskType; later
// models, which reject that field, get the documented text prefix instead.
func WithTaskType(task string) Option { return func(m *Model) { m.taskType = task } }

// WithDimensions sets the length of the vectors an Embedder returns
// (outputDimensionality, 128 to 3072; 768, 1536 and 3072 are
// recommended). The vectors are L2-normalized, as reduced
// gemini-embedding-001 vectors need.
func WithDimensions(n int) Option { return func(m *Model) { m.dimensions = n } }

// WithBatchSize sets how many texts an Embedder sends per
// batchEmbedContents request. Defaults to 100.
func WithBatchSize(n int) Option { return func(m *Model) { m.batchSize = n } }

// Embedder implements memory.Embedder with the Gemini API's
// batchEmbedContents method. API errors are *model.APIError and invalid
// input is permanent, so callers using model.IsRetryable, such as the
// retry.Embedder of github.com/nostosx/agentx-contrib/middleware, retry only
// what may succeed.
type Embedder struct {
	http       utils.HTTPClient
	model      string
	taskType   string
	dimensions int
	batchSize  int
}

// NewEmbedder returns an Embedder. It takes the options of New: WithModel
// selects the embedding model, DefaultEmbeddingModel by default. Vertex AI
// serves embeddings through a different API and is not supported.
func NewEmbedder(opts ...Option) (*Embedder, error) {
	m := newModel(opts)
	if m.vertex {
		return nil, errors.New("gemini: the embedder supports the Gemini API, not Vertex AI")
	}
	if err := m.resolve(); err != nil {
		return nil, err
	}
	if m.taskType != "" {
		if _, ok := taskPrefixes[m.taskType]; !ok {
			return nil, fmt.Errorf("gemini: unknown task type %q", m.taskType)
		}
	}
	e := &Embedder{http: m.http, model: cmp.Or(m.model, DefaultEmbeddingModel), taskType: m.taskType, dimensions: m.dimensions, batchSize: m.batchSize}
	if e.batchSize <= 0 {
		e.batchSize = defaultEmbedBatch
	}
	return e, nil
}

// usesTaskType reports whether a model takes the taskType field; models
// from gemini-embedding-2 on take text prefixes instead.
func usesTaskType(name string) bool {
	return strings.HasPrefix(name, "gemini-embedding-001") || strings.HasPrefix(name, "text-embedding-") || strings.HasPrefix(name, "embedding-")
}

type embedRequest struct {
	Model   string       `json:"model"`
	Content content      `json:"content"`
	Config  *embedConfig `json:"embedContentConfig,omitempty"`
}

type embedConfig struct {
	TaskType             string `json:"taskType,omitempty"`
	OutputDimensionality int    `json:"outputDimensionality,omitempty"`
}

type batchEmbedResponse struct {
	Embeddings []struct {
		Values []float32 `json:"values"`
	} `json:"embeddings"`
}

// Embed implements memory.Embedder. Texts are sent in batches; the
// vectors are returned in the order of texts.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if slices.Contains(texts, "") {
		return nil, model.Permanent(errors.New("gemini: cannot embed empty text"))
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
	var cfg *embedConfig
	prefix := ""
	if e.taskType != "" || e.dimensions > 0 {
		cfg = &embedConfig{OutputDimensionality: e.dimensions}
	}
	if e.taskType != "" {
		if usesTaskType(e.model) {
			cfg.TaskType = e.taskType
		} else {
			prefix = taskPrefixes[e.taskType]
		}
	}
	if cfg != nil && *cfg == (embedConfig{}) {
		cfg = nil
	}
	reqs := make([]embedRequest, len(texts))
	for i, t := range texts {
		reqs[i] = embedRequest{Model: "models/" + strings.TrimPrefix(e.model, "models/"), Content: content{Parts: []part{{Text: prefix + t}}}, Config: cfg}
	}
	body, err := utils.EncodeBody(providerName, map[string]any{"requests": reqs}, nil)
	if err != nil {
		return nil, err
	}
	var resp batchEmbedResponse
	if err := e.http.PostJSON(ctx, modelPath(e.model, "batchEmbedContents"), body, nil, &resp); err != nil {
		return nil, err
	}
	if len(resp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("gemini: got %d embeddings for %d inputs", len(resp.Embeddings), len(texts))
	}
	vecs := make([][]float32, len(texts))
	for i, emb := range resp.Embeddings {
		vecs[i] = emb.Values
		if e.dimensions > 0 {
			normalize(vecs[i])
		}
	}
	return vecs, nil
}

// normalize scales a nonzero vector to unit length, leaving a zero vector unchanged.
func normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	n := float32(math.Sqrt(sum))
	for i := range v {
		v[i] /= n
	}
}
