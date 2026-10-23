package openai

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nostosx/agentx/model"
)

func TestNew(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	if _, err := New(); err == nil {
		t.Fatal("missing credentials accepted")
	}
	if _, err := New(WithoutAPIKey()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "environment-key")
	t.Setenv("OPENAI_BASE_URL", "http://environment.invalid")
	m, err := New(WithAPIKey("explicit-key"), WithBaseURL("http://explicit.invalid"), WithModel("custom"), WithMaxTokens(64), WithOrganization("org"), WithHeader("X-Custom", "yes"))
	if err != nil {
		t.Fatal(err)
	}
	if m.http.Header.Get("Authorization") != "Bearer explicit-key" || m.http.BaseURL != "http://explicit.invalid" || m.model != "custom" || m.maxTokens != 64 || m.http.Header.Get("OpenAI-Organization") != "org" {
		t.Fatalf("options not applied: %+v", m)
	}
}

func TestGenerateAndStream(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		stream bool
	}{{name: "generate"}, {name: "stream", stream: true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Errorf("unexpected request %s %v", r.URL, r.Header)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["model"] != "override" || body["max_completion_tokens"] != float64(42) {
					t.Errorf("request body = %v", body)
				}
				var payload string
				if tc.stream {
					if body["stream"] != true {
						t.Error("stream flag missing")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					payload = "data: {\"id\":\"r1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n"
				} else {
					payload = `{"id":"r1","choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`
				}
				if _, err := w.Write([]byte(payload)); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			m, err := New(WithAPIKey("fixture"), WithBaseURL(srv.URL+"/v1"), WithHTTPClient(srv.Client()), WithChatCompletions())
			if err != nil {
				t.Fatal(err)
			}
			req := &model.Request{Model: "override", MaxTokens: 42, Messages: []*model.Message{model.UserText("hi")}}
			var resp *model.Response
			if tc.stream {
				resp, err = model.Collect(m.Stream(t.Context(), req))
			} else {
				resp, err = m.Generate(t.Context(), req)
			}
			if err != nil || resp.Text() != "hello" || resp.ID != "r1" || resp.FinishReason != model.FinishStop || resp.Usage.TotalTokens() != 13 {
				t.Fatalf("response = %+v, error = %v", resp, err)
			}
		})
	}
}

func TestGenerateAPIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		if _, err := w.Write([]byte(`{"error":{"type":"rate_limit","message":"slow"}}`)); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	m, err := New(WithAPIKey("fixture"), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Generate(t.Context(), &model.Request{})
	var api *model.APIError
	if !errors.As(err, &api) || api.StatusCode != http.StatusTooManyRequests || api.Provider != "openai" || api.Type != "rate_limit" {
		t.Fatalf("error = %v", err)
	}
}
