package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbedVertex(t *testing.T) {
	t.Parallel()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/projects/p/locations/us-central1/publishers/google/models/"+DefaultVertexEmbeddingModel+":predict" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("x-goog-api-key") != "" {
			t.Error("invalid token authentication")
		}
		var req struct {
			Instances []struct {
				Content  string `json:"content"`
				TaskType string `json:"task_type"`
			} `json:"instances"`
			Parameters map[string]any `json:"parameters"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if len(req.Instances) != 1 || req.Instances[0].TaskType != TaskRetrievalQuery || req.Parameters["outputDimensionality"] != float64(2) || req.Parameters["autoTruncate"] != false {
			t.Errorf("request=%+v", req)
		}
		fmt.Fprint(w, `{"predictions":[{"embeddings":{"values":[3,4]}}]}`)
	}))
	defer srv.Close()
	e, err := NewEmbedder(WithVertex("p", "us-central1"), WithBaseURL(srv.URL+"/v1/projects/p/locations/us-central1/publishers/google"), WithTokenSource(func(context.Context) (string, error) { return "fixture", nil }), WithHTTPClient(srv.Client()), WithTaskType(TaskRetrievalQuery), WithDimensions(2))
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := e.Embed(t.Context(), []string{"first", "second"})
	if err != nil || calls != 2 || len(vecs) != 2 || vecs[0][0] != 0.6 || vecs[0][1] != 0.8 {
		t.Fatalf("vectors=%v calls=%d err=%v", vecs, calls, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.Embed(ctx, []string{"x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestEmbedVertexErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, payload string }{
		{name: "missing vector", payload: `{"predictions":[{"embeddings":{}}]}`},
		{name: "missing prediction", payload: `{"predictions":[]}`},
		{name: "wrong dimensions", payload: `{"predictions":[{"embeddings":{"values":[1,2,3]}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, tc.payload) }))
			defer srv.Close()
			e, err := NewEmbedder(WithVertex("p", "global"), WithTokenSource(func(context.Context) (string, error) { return "fixture", nil }), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithDimensions(2))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.Embed(t.Context(), []string{"x"}); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestEmbedVertexBatchDimensionMismatch(t *testing.T) {
	t.Parallel()
	var count int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count++
		if count == 1 {
			fmt.Fprint(w, `{"predictions":[{"embeddings":{"values":[1,2]}}]}`)
		} else {
			fmt.Fprint(w, `{"predictions":[{"embeddings":{"values":[1,2,3]}}]}`)
		}
	}))
	defer srv.Close()
	e, err := NewEmbedder(WithVertex("p", "global"), WithTokenSource(func(context.Context) (string, error) { return "fixture", nil }), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Embed(t.Context(), []string{"first", "second"}); err == nil {
		t.Fatal("inconsistent dimensions accepted")
	}
}
