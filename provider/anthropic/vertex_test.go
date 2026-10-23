package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nostosx/agentx/model"
)

func token(tok string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return tok, nil }
}

func TestVertex(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	if _, err := New(WithVertex("proj", "global")); err == nil || !strings.Contains(err.Error(), "WithTokenSource") {
		t.Fatalf("Vertex without token: %v", err)
	}
	m, err := New(WithVertex("proj", "us"), WithTokenSource(token("adc")))
	if err != nil {
		t.Fatal(err)
	}
	if m.http.BaseURL != "https://aiplatform.us.rep.googleapis.com/v1/projects/proj/locations/us/publishers/anthropic" || m.http.Header.Get("x-api-key") != "" || m.http.Header.Get("anthropic-version") != "" {
		t.Fatalf("Vertex model = %+v", m)
	}
	if d := m.Describe(); d.Provider != "gcp.vertex_ai" {
		t.Fatalf("Describe = %+v", d)
	}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method := ":rawPredict"
				if stream {
					method = ":streamRawPredict"
				}
				if r.URL.Path != "/v1/projects/proj/locations/global/publishers/anthropic/models/claude-opus-4-5@20251101"+method ||
					r.Header.Get("Authorization") != "Bearer adc" || r.Header.Get("x-api-key") != "" || r.Header.Get("anthropic-version") != "" {
					t.Errorf("request = %s %v", r.URL, r.Header)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if _, ok := body["model"]; ok || body["anthropic_version"] != VertexVersion || body["fallbacks"] != nil || (body["stream"] == true) != stream {
					t.Errorf("body = %v", body)
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"claude\",\"usage\":{\"input_tokens\":1}}}\n\n"+
						"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
						"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"+
						"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
						"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
						"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					return
				}
				fmt.Fprint(w, `{"id":"m1","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			defer srv.Close()
			// Claude Opus 5.5 would get server-side fallbacks on the
			// Claude API; Vertex AI does not support them.
			m, err := New(WithVertex("proj", "global"), WithTokenSource(token("adc")), WithModel("claude-opus-4-5@20251101"), WithHTTPClient(srv.Client()))
			if err != nil {
				t.Fatal(err)
			}
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

func TestVertexFallbacks(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	m, err := New(WithVertex("proj", "global"), WithTokenSource(token("t")))
	if err != nil {
		t.Fatal(err)
	}
	if m.useFallbacks(DefaultModel) {
		t.Fatal("fallbacks enabled on Vertex AI")
	}
	m, err = New(WithVertex("proj", "global"), WithTokenSource(token("t")), WithFallbacks(true))
	if err != nil || !m.useFallbacks(DefaultModel) {
		t.Fatalf("explicit fallbacks ignored: %v", err)
	}
	m, err = New(WithAPIKey("k"))
	if err != nil || !m.useFallbacks(DefaultModel) || m.http.Header.Get("anthropic-version") != APIVersion {
		t.Fatalf("Claude API defaults changed: %+v, %v", m, err)
	}
}

func TestTokenSource(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	wantErr := errors.New("no credentials")
	m, err := New(WithBaseURL("http://127.0.0.1:1"), WithTokenSource(func(context.Context) (string, error) { return "", wantErr }))
	if err != nil || m.http.Header.Get("x-api-key") != "" {
		t.Fatalf("model = %+v, %v", m, err)
	}
	if _, err := m.Generate(t.Context(), &model.Request{}); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v", err)
	}
}
