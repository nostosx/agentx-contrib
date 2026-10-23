package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nostosx/agentx/memory"
	"github.com/nostosx/agentx/model"
)

var _ memory.Embedder = (*Embedder)(nil)

func clearEnv(t *testing.T) {
	for _, k := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "GEMINI_BASE_URL"} {
		t.Setenv(k, "")
	}
}

func token(tok string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return tok, nil }
}

func TestVertex(t *testing.T) {
	clearEnv(t)
	t.Setenv("GEMINI_API_KEY", "gemini-secret")
	if _, err := New(WithVertex("proj", "global")); err == nil || !strings.Contains(err.Error(), "WithTokenSource") {
		t.Fatalf("Vertex without token: %v", err)
	}
	m, err := New(WithVertex("proj", "us-central1"), WithTokenSource(token("adc")))
	if err != nil {
		t.Fatal(err)
	}
	if m.http.BaseURL != "https://us-central1-aiplatform.googleapis.com/v1/projects/proj/locations/us-central1/publishers/google" || m.http.Header.Get("x-goog-api-key") != "" {
		t.Fatalf("Vertex model = %+v", m)
	}
	if d := m.Describe(); d.Provider != "gcp.vertex_ai" || d.ServerAddress != "us-central1-aiplatform.googleapis.com" {
		t.Fatalf("Describe = %+v", d)
	}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method := ":generateContent"
				if stream {
					method = ":streamGenerateContent"
				}
				if r.URL.Path != "/v1/projects/proj/locations/global/publishers/google/models/gemini-x"+method || r.Header.Get("Authorization") != "Bearer adc" || r.Header.Get("x-goog-api-key") != "" {
					t.Errorf("request = %s %v", r.URL, r.Header)
				}
				var body request
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Contents) != 1 || body.Contents[0].Role != "user" {
					t.Errorf("body = %+v, %v", body, err)
				}
				payload := `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}]}`
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					payload = "data: " + payload + "\n\n"
				}
				fmt.Fprint(w, payload)
			}))
			defer srv.Close()
			m, err := New(WithVertex("proj", "global"), WithTokenSource(token("adc")), WithModel("gemini-x"), WithHTTPClient(srv.Client()))
			if err != nil {
				t.Fatal(err)
			}
			// Point the documented URL at the test server.
			m.http.BaseURL = strings.Replace(m.http.BaseURL, "https://aiplatform.googleapis.com", srv.URL, 1)
			req := &model.Request{Messages: []*model.Message{model.UserText("hi")}}
			var resp *model.Response
			if stream {
				resp, err = model.Collect(m.Stream(t.Context(), req))
			} else {
				resp, err = m.Generate(t.Context(), req)
			}
			if err != nil || resp.Text() != "hello" {
				t.Fatalf("response = %+v, %v", resp, err)
			}
		})
	}
}

func TestTokenSourceSkipsEnvironmentKey(t *testing.T) {
	clearEnv(t)
	t.Setenv("GEMINI_API_KEY", "gemini-secret")
	m, err := New(WithTokenSource(token("t")))
	if err != nil || m.http.Header.Get("x-goog-api-key") != "" {
		t.Fatalf("model = %+v, %v", m, err)
	}
	wantErr := errors.New("no credentials")
	m, err = New(WithTokenSource(func(context.Context) (string, error) { return "", wantErr }), WithBaseURL("http://127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Generate(t.Context(), &model.Request{}); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v", err)
	}
}

func embedServer(t *testing.T) (*httptest.Server, func() []map[string]any, func() string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "k" {
			t.Errorf("key = %q", r.Header.Get("x-goog-api-key"))
		}
		var body struct {
			Requests []struct {
				Content content `json:"content"`
			} `json:"requests"`
		}
		raw := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Error(err)
			return
		}
		b, _ := json.Marshal(raw)
		if err := json.Unmarshal(b, &body); err != nil {
			t.Errorf("embedding request = %s: %v", b, err)
			return
		}
		mu.Lock()
		bodies, path = append(bodies, raw), r.URL.Path
		mu.Unlock()
		var embs []string
		for _, req := range body.Requests {
			if len(req.Content.Parts) != 1 {
				t.Errorf("embedding content = %+v, want one text part", req.Content)
				return
			}
			embs = append(embs, fmt.Sprintf(`{"values":[%d,0,0]}`, len(req.Content.Parts[0].Text)))
		}
		fmt.Fprintf(w, `{"embeddings":[%s]}`, strings.Join(embs, ","))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]any { mu.Lock(); defer mu.Unlock(); return bodies }, func() string { mu.Lock(); defer mu.Unlock(); return path }
}

func TestEmbedder(t *testing.T) {
	clearEnv(t)
	srv, bodies, path := embedServer(t)
	e, err := NewEmbedder(WithAPIKey("k"), WithBaseURL(srv.URL+"/v1beta"), WithHTTPClient(srv.Client()), WithBatchSize(2))
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := e.Embed(t.Context(), []string{"a", "bb", "ccc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 || vecs[0][0] != 1 || vecs[1][0] != 2 || vecs[2][0] != 3 {
		t.Fatalf("vectors = %v", vecs)
	}
	bs := bodies()
	if len(bs) != 2 || path() != "/v1beta/models/"+DefaultEmbeddingModel+":batchEmbedContents" {
		t.Fatalf("bodies = %v, path = %s", bs, path())
	}
	first := bs[0]["requests"].([]any)[0].(map[string]any)
	if first["model"] != "models/"+DefaultEmbeddingModel || first["embedContentConfig"] != nil {
		t.Fatalf("request = %v", first)
	}
	if _, err := e.Embed(t.Context(), []string{""}); err == nil || model.IsRetryable(err) {
		t.Fatalf("empty text = %v", err)
	}
}

func TestEmbedderTaskTypes(t *testing.T) {
	clearEnv(t)
	for _, tc := range []struct {
		name, model, task, wantText, wantTask string
	}{
		{name: "prefix query", model: "gemini-embedding-2", task: TaskRetrievalQuery, wantText: "task: search result | query: hi"},
		{name: "prefix document", model: "gemini-embedding-2", task: TaskRetrievalDocument, wantText: "title: none | text: hi"},
		{name: "task type field", model: "gemini-embedding-001", task: TaskRetrievalQuery, wantText: "hi", wantTask: TaskRetrievalQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, bodies, _ := embedServer(t)
			e, err := NewEmbedder(WithAPIKey("k"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithModel(tc.model), WithTaskType(tc.task), WithDimensions(3))
			if err != nil {
				t.Fatal(err)
			}
			vecs, err := e.Embed(t.Context(), []string{"hi"})
			if err != nil {
				t.Fatal(err)
			}
			// Reduced vectors are normalized.
			if math.Abs(float64(vecs[0][0])-1) > 1e-6 {
				t.Fatalf("vector = %v", vecs[0])
			}
			req := bodies()[0]["requests"].([]any)[0].(map[string]any)
			text := req["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]
			cfg := req["embedContentConfig"].(map[string]any)
			if text != tc.wantText || cfg["outputDimensionality"] != float64(3) || (tc.wantTask == "") != (cfg["taskType"] == nil) || (tc.wantTask != "" && cfg["taskType"] != tc.wantTask) {
				t.Fatalf("request = %v", req)
			}
		})
	}
	if _, err := NewEmbedder(WithAPIKey("k"), WithTaskType("SEARCH")); err == nil {
		t.Fatal("unknown task type accepted")
	}
	if _, err := NewEmbedder(WithVertex("p", "global"), WithTokenSource(token("t"))); err == nil {
		t.Fatal("Vertex embedder accepted")
	}
}

func TestEmbedderErrors(t *testing.T) {
	clearEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"code":503,"status":"UNAVAILABLE","message":"busy"}}`)
	}))
	defer srv.Close()
	e, err := NewEmbedder(WithAPIKey("k"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	var api *model.APIError
	if _, err := e.Embed(t.Context(), []string{"x"}); !errors.As(err, &api) || !model.IsRetryable(err) {
		t.Fatalf("error = %v", err)
	}
}
