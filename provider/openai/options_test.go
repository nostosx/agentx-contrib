package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/nostosx/agentx/model"
)

func TestLegacyMaxTokens(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		opts     []Option
		path     string
		reqMax   int
		field    string
		want     float64
		absent   []string
		response string
	}{
		{
			name: "chat default", opts: []Option{WithChatCompletions()}, path: "/chat/completions", reqMax: 42,
			field: "max_completion_tokens", want: 42, absent: []string{"max_tokens"},
		},
		{
			name: "chat legacy", opts: []Option{WithChatCompletions(), WithLegacyMaxTokens()}, path: "/chat/completions", reqMax: 42,
			field: "max_tokens", want: 42, absent: []string{"max_completion_tokens"},
		},
		{
			// The model's default limit is sent in the legacy field as well.
			name: "chat legacy default limit", opts: []Option{WithChatCompletions(), WithLegacyMaxTokens(), WithMaxTokens(64)}, path: "/chat/completions",
			field: "max_tokens", want: 64, absent: []string{"max_completion_tokens"},
		},
		{
			name: "chat legacy without limit", opts: []Option{WithChatCompletions(), WithLegacyMaxTokens()}, path: "/chat/completions",
			absent: []string{"max_tokens", "max_completion_tokens"},
		},
		{
			// The option applies to Chat Completions only.
			name: "responses ignores legacy", opts: []Option{WithLegacyMaxTokens()}, path: "/responses", reqMax: 42,
			field: "max_output_tokens", want: 42, absent: []string{"max_tokens", "max_completion_tokens"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path = %s, want %s", r.URL.Path, tc.path)
				}
				var got map[string]any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				mu.Lock()
				body = got
				mu.Unlock()
				payload := `{"id":"r1","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`
				if tc.path == "/responses" {
					payload = `{"id":"r1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`
				}
				if _, err := w.Write([]byte(payload)); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			m, err := New(append([]Option{WithAPIKey("fixture"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client())}, tc.opts...)...)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := m.Generate(t.Context(), &model.Request{MaxTokens: tc.reqMax, Messages: []*model.Message{model.UserText("hi")}})
			if err != nil || resp.Text() != "ok" {
				t.Fatalf("Generate = %+v, %v", resp, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if tc.field != "" && body[tc.field] != tc.want {
				t.Errorf("%s = %v, want %v; body = %v", tc.field, body[tc.field], tc.want, body)
			}
			for _, k := range tc.absent {
				if v, ok := body[k]; ok {
					t.Errorf("%s = %v sent; body = %v", k, v, body)
				}
			}
		})
	}
}
