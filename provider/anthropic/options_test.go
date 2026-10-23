package anthropic

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nostosx/agentx/model"
)

// captureRequest returns a server answering every request with a short
// message, and a function returning the last request's body and beta
// header.
func captureRequest(t *testing.T) (*httptest.Server, func() (map[string]any, string)) {
	t.Helper()
	var mu sync.Mutex
	var body map[string]any
	var beta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		mu.Lock()
		body, beta = got, r.Header.Get("anthropic-beta")
		mu.Unlock()
		if _, err := w.Write([]byte(`{"id":"r1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() (map[string]any, string) {
		mu.Lock()
		defer mu.Unlock()
		return body, beta
	}
}

func TestRequestOptions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		model     string
		opts      []Option
		cache     bool
		fallbacks bool
		beta      string
	}{
		{name: "defaults", model: DefaultModel, cache: true, fallbacks: true, beta: FallbackBeta},
		{name: "without prompt caching", model: DefaultModel, opts: []Option{WithoutPromptCaching()}, fallbacks: true, beta: FallbackBeta},
		{name: "fallbacks disabled", model: DefaultModel, opts: []Option{WithFallbacks(false)}, cache: true},
		{name: "fallbacks disabled keeps other betas", model: DefaultModel, opts: []Option{WithFallbacks(false), WithBeta("custom")}, cache: true, beta: "custom"},
		{name: "unsupported model", model: "claude-custom", cache: true},
		{name: "fallbacks forced", model: "claude-custom", opts: []Option{WithFallbacks(true), WithBeta("custom")}, cache: true, fallbacks: true, beta: "custom," + FallbackBeta},
		{name: "both", model: "claude-custom", opts: []Option{WithoutPromptCaching(), WithFallbacks(true)}, fallbacks: true, beta: FallbackBeta},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, last := captureRequest(t)
			opts := append([]Option{WithAPIKey("fixture"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithModel(tc.model)}, tc.opts...)
			m, err := New(opts...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.Generate(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("hi")}}); err != nil {
				t.Fatal(err)
			}
			body, beta := last()
			cache, hasCache := body["cache_control"].(map[string]any)
			if hasCache != tc.cache || (tc.cache && cache["type"] != "ephemeral") {
				t.Errorf("cache_control = %v", body["cache_control"])
			}
			if fb, ok := body["fallbacks"]; ok != tc.fallbacks || (ok && fb != "default") {
				t.Errorf("fallbacks = %v", body["fallbacks"])
			}
			if beta != tc.beta {
				t.Errorf("anthropic-beta = %q, want %q", beta, tc.beta)
			}
			if body["model"] != tc.model {
				t.Errorf("model = %v", body["model"])
			}
		})
	}
}

// A request may name another model; fallbacks follow that model unless
// they were configured explicitly.
func TestFallbacksFollowRequestModel(t *testing.T) {
	t.Parallel()
	srv, last := captureRequest(t)
	m, err := New(WithAPIKey("fixture"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithModel("claude-custom"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Generate(t.Context(), &model.Request{Model: DefaultModel, Messages: []*model.Message{model.UserText("hi")}}); err != nil {
		t.Fatal(err)
	}
	if body, beta := last(); body["fallbacks"] != "default" || !strings.Contains(beta, FallbackBeta) {
		t.Fatalf("body = %v, beta = %q", body, beta)
	}
	WithFallbacks(false)(m)
	if _, err := m.Generate(t.Context(), &model.Request{Model: DefaultModel, Messages: []*model.Message{model.UserText("hi")}}); err != nil {
		t.Fatal(err)
	}
	if body, beta := last(); body["fallbacks"] != nil || beta != "" {
		t.Fatalf("disabled: body = %v, beta = %q", body, beta)
	}
}
