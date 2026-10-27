package gemini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nostosx/agentx/model"
)

func TestNew(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_BASE_URL", "")
	if _, err := New(); err == nil {
		t.Fatal("missing credentials accepted")
	}
	t.Setenv("GOOGLE_API_KEY", "google")
	m, err := New()
	if err != nil || m.http.Header.Get("x-goog-api-key") != "google" {
		t.Fatalf("fallback key: %+v, %v", m, err)
	}
	t.Setenv("GEMINI_API_KEY", "gemini")
	m, err = New()
	if err != nil || m.http.Header.Get("x-goog-api-key") != "gemini" {
		t.Fatalf("preferred key: %+v, %v", m, err)
	}
	m, err = New(WithAPIKey("explicit"), WithModel("custom"), WithBaseURL("http://fixture.invalid"), WithMaxTokens(64), WithHeader("X-Custom", "yes"))
	if err != nil {
		t.Fatal(err)
	}
	if m.http.Header.Get("x-goog-api-key") != "explicit" || m.model != "custom" || m.maxTokens != 64 || m.http.BaseURL != "http://fixture.invalid" || m.http.Header.Get("X-Custom") != "yes" {
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
				method := "generateContent"
				if tc.stream {
					method = "streamGenerateContent"
					if r.URL.Query().Get("alt") != "sse" {
						t.Error("SSE query missing")
					}
				}
				if r.URL.Path != "/v1beta/models/custom:"+method || r.Header.Get("x-goog-api-key") != "fixture" {
					t.Errorf("request = %s %v", r.URL, r.Header)
				}
				var body request
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if len(body.Contents) != 1 || body.Contents[0].Parts[0].Text != "hi" || body.GenerationConfig.MaxOutputTokens != 42 {
					t.Errorf("body = %+v", body)
				}
				payload := `{"responseId":"r1","modelVersion":"custom","candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3}}`
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					payload = "data: " + payload + "\n\n"
				}
				if _, err := w.Write([]byte(payload)); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			m, err := New(WithAPIKey("fixture"), WithModel("custom"), WithBaseURL(srv.URL+"/v1beta"), WithHTTPClient(srv.Client()))
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

// The thinking configuration sent follows each model's accepted levels and
// budget range.
func TestThinkingRequestBody(t *testing.T) {
	t.Parallel()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			GenerationConfig struct {
				ThinkingConfig json.RawMessage `json:"thinkingConfig"`
			} `json:"generationConfig"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		got = string(body.GenerationConfig.ThinkingConfig)
		if _, err := w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	m, err := New(WithAPIKey("fixture"), WithBaseURL(srv.URL+"/v1beta"), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ model, effort, want string }{
		{model: "gemini-3.1-flash-lite-image", effort: "low", want: `{"includeThoughts":true,"thinkingLevel":"minimal"}`},
		{model: "gemini-3.1-flash-lite-image", effort: "medium", want: `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{model: "gemini-3-pro-preview", effort: "medium", want: `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{model: "gemini-3.8-flash", effort: "minimal", want: `{"includeThoughts":true,"thinkingLevel":"low"}`},
		{model: "gemini-2.5-pro", effort: "none", want: `{"includeThoughts":true,"thinkingBudget":128}`},
	} {
		if _, err := m.Generate(t.Context(), &model.Request{Model: tc.model, Messages: []*model.Message{model.UserText("hi")}, Reasoning: &model.Reasoning{Effort: model.Effort(tc.effort)}}); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s %s: thinkingConfig = %s, want %s", tc.model, tc.effort, got, tc.want)
		}
	}
}

// A model name may carry the API's "models/" prefix, as the Models API
// lists names.
func TestModelsPrefix(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"gemini-x", "models/gemini-x"} {
		if got := modelPath(name, "generateContent"); got != "/models/gemini-x:generateContent" {
			t.Errorf("modelPath(%q) = %q", name, got)
		}
	}
}
