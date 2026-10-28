package gemini

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
)

// embedVertex uses the publisher-model predict protocol, whose instance field
// names differ from the Gemini API: https://cloud.google.com/vertex-ai/generative-ai/docs/embeddings/get-text-embeddings.
func (e *Embedder) embedVertex(ctx context.Context, texts []string) ([][]float32, error) {
	type instance struct {
		Content  string `json:"content"`
		TaskType string `json:"task_type,omitempty"`
	}
	instances := make([]instance, len(texts))
	for i, text := range texts {
		instances[i] = instance{Content: text, TaskType: e.taskType}
	}
	parameters := map[string]any{"autoTruncate": false}
	if e.dimensions > 0 {
		parameters["outputDimensionality"] = e.dimensions
	}
	body, err := utils.EncodeBody(providerName, map[string]any{"instances": instances, "parameters": parameters}, nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Predictions []struct {
			Embeddings struct {
				Values []float32 `json:"values"`
			} `json:"embeddings"`
		} `json:"predictions"`
	}
	if err := e.http.PostJSON(ctx, modelPath(e.model, "predict"), body, nil, &response); err != nil {
		return nil, err
	}
	if len(response.Predictions) != len(texts) {
		return nil, fmt.Errorf("gemini: got %d predictions for %d inputs", len(response.Predictions), len(texts))
	}
	out := make([][]float32, len(texts))
	for i, pred := range response.Predictions {
		vec := pred.Embeddings.Values
		if len(vec) == 0 || (e.dimensions > 0 && len(vec) != e.dimensions) || (i > 0 && len(vec) != len(out[0])) {
			return nil, errors.New("gemini: invalid Vertex embedding dimensions")
		}
		for _, v := range vec {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return nil, errors.New("gemini: nonfinite Vertex embedding")
			}
		}
		if e.dimensions > 0 {
			normalize(vec)
		}
		out[i] = vec
	}
	return out, nil
}
