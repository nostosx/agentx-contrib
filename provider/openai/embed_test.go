package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nostosx/agentx/memory"
	"github.com/nostosx/agentx/model"
)

var _ memory.Embedder = (*Embedder)(nil)

// embedServer answers embedding requests with vectors {len(text), i} in
// reverse order, and records the request bodies.
func embedServer(t *testing.T, check func(*http.Request)) (*httptest.Server, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/embeddings") {
			t.Errorf("path = %s", r.URL.Path)
		}
		if check != nil {
			check(r)
		}
		var body struct {
			Input []string `json:"input"`
		}
		raw := map[string]any{}
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&raw); err != nil {
			t.Error(err)
			return
		}
		b, _ := json.Marshal(raw)
		if err := json.Unmarshal(b, &body); err != nil {
			t.Errorf("embedding request = %s: %v", b, err)
			return
		}
		mu.Lock()
		bodies = append(bodies, raw)
		mu.Unlock()
		var data []string
		for i, in := range slices.Backward(body.Input) {
			data = append(data, fmt.Sprintf(`{"object":"embedding","index":%d,"embedding":[%d,%d]}`, i, len(in), i))
		}
		fmt.Fprintf(w, `{"object":"list","data":[%s],"usage":{"prompt_tokens":1,"total_tokens":1}}`, strings.Join(data, ","))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return bodies
	}
}

func TestEmbedder(t *testing.T) {
	clearEnv(t)
	srv, bodies := embedServer(t, func(r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
	})
	e, err := NewEmbedder(WithAPIKey("k"), WithBaseURL(srv.URL+"/v1"), WithHTTPClient(srv.Client()), WithDimensions(256), WithBatchSize(2))
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Embed(t.Context(), []string{"a", "bb", "ccc"})
	if err != nil {
		t.Fatal(err)
	}
	// Batches keep their order and indexes are honored.
	if want := [][]float32{{1, 0}, {2, 1}, {3, 0}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("vectors = %v, want %v", got, want)
	}
	bs := bodies()
	if len(bs) != 2 || bs[0]["model"] != DefaultEmbeddingModel || bs[0]["dimensions"] != float64(256) || len(bs[0]["input"].([]any)) != 2 || len(bs[1]["input"].([]any)) != 1 {
		t.Fatalf("bodies = %v", bs)
	}
	if got, err := e.Embed(t.Context(), nil); err != nil || len(got) != 0 || len(bodies()) != 2 {
		t.Fatalf("empty input = %v, %v", got, err)
	}
	if _, err := e.Embed(t.Context(), []string{"a", ""}); err == nil || model.IsRetryable(err) {
		t.Fatalf("empty text = %v", err)
	}
}

func TestEmbedderPresets(t *testing.T) {
	clearEnv(t)
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	srv, bodies := embedServer(t, func(r *http.Request) {
		if a := r.Header.Get("Authorization"); strings.Contains(a, "openai-secret") {
			t.Errorf("OpenAI key sent to a preset: %q", a)
		}
	})
	e, err := NewEmbedder(Ollama(), WithBaseURL(srv.URL+"/v1"), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Embed(t.Context(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MISTRAL_API_KEY", "m")
	e, err = NewEmbedder(Mistral(), WithBaseURL(srv.URL+"/v1"), WithHTTPClient(srv.Client()), WithDimensions(64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Embed(t.Context(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	bs := bodies()
	if bs[0]["model"] != "embeddinggemma" || bs[1]["model"] != "mistral-embed" || bs[1]["output_dimension"] != float64(64) || bs[1]["dimensions"] != nil {
		t.Fatalf("bodies = %v", bs)
	}
	t.Setenv("GROQ_API_KEY", "g")
	if _, err := NewEmbedder(Groq()); err == nil || !strings.Contains(err.Error(), "WithModel") {
		t.Fatalf("preset without embedding model: %v", err)
	}
}

func TestEmbedderErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, payload string
		status        int
		retryable     bool
	}{
		{name: "rate limit", status: http.StatusTooManyRequests, payload: `{"error":{"type":"rate_limit","message":"slow"}}`, retryable: true},
		{name: "bad request", status: http.StatusBadRequest, payload: `{"error":{"type":"invalid_request_error","message":"too long"}}`},
		{name: "count mismatch", status: http.StatusOK, payload: `{"data":[]}`},
		{name: "duplicate index", status: http.StatusOK, payload: `{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[2]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.payload)
			}))
			defer srv.Close()
			e, err := NewEmbedder(WithAPIKey("k"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
			if err != nil {
				t.Fatal(err)
			}
			texts := []string{"a"}
			if tc.name == "duplicate index" {
				texts = []string{"a", "b"}
			}
			_, err = e.Embed(t.Context(), texts)
			var api *model.APIError
			if err == nil || (tc.status != http.StatusOK && !errors.As(err, &api)) || model.IsRetryable(err) != tc.retryable {
				t.Fatalf("error = %v, retryable = %v", err, model.IsRetryable(err))
			}
		})
	}
}
