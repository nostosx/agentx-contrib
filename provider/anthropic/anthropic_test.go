package anthropic

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nostosx/agentx/model"
)

func TestNew(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	if _, err := New(); err == nil {
		t.Fatal("missing credentials accepted")
	}
	if _, err := New(WithHeader("Authorization", "Bearer fixture")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "environment")
	m, err := New(WithAPIKey("explicit"), WithModel("custom"), WithBaseURL("http://fixture.invalid"), WithMaxTokens(64))
	if err != nil {
		t.Fatal(err)
	}
	if m.http.Header.Get("x-api-key") != "explicit" || m.http.Header.Get("anthropic-version") != APIVersion || m.model != "custom" || m.maxTokens != 64 {
		t.Fatalf("configuration = %+v", m)
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
				if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "fixture" || r.Header.Get("anthropic-version") != APIVersion || r.Header.Get("anthropic-beta") != "custom-beta" {
					t.Errorf("request = %s %v", r.URL, r.Header)
				}
				var body request
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body.Model != "custom" || body.MaxTokens != 42 || body.Stream != tc.stream {
					t.Errorf("body = %+v", body)
				}
				payload := `{"id":"r1","model":"custom","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":3}}`
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					payload = "data: {\"type\":\"message_start\",\"message\":{\"id\":\"r1\",\"model\":\"custom\",\"usage\":{\"input_tokens\":10}}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
				}
				if _, err := w.Write([]byte(payload)); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			m, err := New(WithAPIKey("fixture"), WithModel("custom"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithBeta("custom-beta"))
			if err != nil {
				t.Fatal(err)
			}
			req := &model.Request{MaxTokens: 42, Messages: []*model.Message{model.UserText("hi")}}
			var resp *model.Response
			if tc.stream {
				resp, err = model.Collect(m.Stream(t.Context(), req))
			} else {
				resp, err = m.Generate(t.Context(), req)
			}
			if err != nil || resp.Text() != "hello" || resp.ID != "r1" || resp.FinishReason != model.FinishStop || resp.Usage.TotalTokens() != 13 {
				t.Fatalf("response = %+v, %v", resp, err)
			}
		})
	}
}
