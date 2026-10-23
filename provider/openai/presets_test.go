package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nostosx/agentx/model"
)

// presetEnv lists every variable presets read, cleared for each test.
var presetEnv = []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "OLLAMA_API_KEY", "DEEPSEEK_API_KEY", "OPENROUTER_API_KEY", "GROQ_API_KEY", "XAI_API_KEY", "MISTRAL_API_KEY", "TOGETHER_API_KEY", "FIREWORKS_API_KEY", "VLLM_API_KEY", "AZURE_OPENAI_API_KEY"}

func clearEnv(t *testing.T) {
	for _, k := range presetEnv {
		t.Setenv(k, "")
	}
}

func TestPresetDefaults(t *testing.T) {
	clearEnv(t)
	// OpenAI's key must never reach another service.
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("OPENAI_BASE_URL", "http://openai.invalid")
	for _, tc := range []struct {
		name, env, baseURL, model, provider string
		opt                                 Option
		keyOptional                         bool
	}{
		{name: "ollama", opt: Ollama(), env: "OLLAMA_API_KEY", baseURL: "http://localhost:11434/v1", model: "gemma4:e2b", provider: "ollama", keyOptional: true},
		{name: "deepseek", opt: DeepSeek(), env: "DEEPSEEK_API_KEY", baseURL: "https://api.deepseek.com", model: "deepseek-flash", provider: "deepseek"},
		{name: "openrouter", opt: OpenRouter(), env: "OPENROUTER_API_KEY", baseURL: "https://openrouter.ai/api/v1", model: "openrouter/auto", provider: "openrouter"},
		{name: "groq", opt: Groq(), env: "GROQ_API_KEY", baseURL: "https://api.groq.com/openai/v1", model: "openai/gpt-oss-120b", provider: "groq"},
		{name: "xai", opt: XAI(), env: "XAI_API_KEY", baseURL: "https://api.x.ai/v1", model: "grok-4.7", provider: "x_ai"},
		{name: "mistral", opt: Mistral(), env: "MISTRAL_API_KEY", baseURL: "https://api.mistral.ai/v1", model: "mistral-large-latest", provider: "mistral_ai"},
		{name: "together", opt: Together(), env: "TOGETHER_API_KEY", baseURL: "https://api.together.ai/v1", model: "openai/gpt-oss-120b", provider: "together"},
		{name: "fireworks", opt: Fireworks(), env: "FIREWORKS_API_KEY", baseURL: "https://api.fireworks.ai/inference/v1", model: "accounts/fireworks/models/kimi-k2-instruct-0905", provider: "fireworks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := New(tc.opt)
			if tc.keyOptional {
				if err != nil || m.http.Header.Get("Authorization") != "" {
					t.Fatalf("keyless preset: %+v, %v", m, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.env) {
				t.Fatalf("missing key accepted or unnamed: %v", err)
			}
			t.Setenv(tc.env, "preset-key")
			m, err = New(tc.opt)
			if err != nil {
				t.Fatal(err)
			}
			if m.http.BaseURL != tc.baseURL || m.model != tc.model || !m.chat || m.http.Header.Get("Authorization") != "Bearer preset-key" || m.Describe().Provider != tc.provider {
				t.Fatalf("preset = %+v, auth %q, describe %+v", m, m.http.Header.Get("Authorization"), m.Describe())
			}
			// Explicit options win regardless of order.
			m, err = New(WithModel("custom"), WithBaseURL("http://custom.invalid/v1"), WithAPIKey("explicit"), tc.opt)
			if err != nil || m.model != "custom" || m.http.BaseURL != "http://custom.invalid/v1" || m.http.Header.Get("Authorization") != "Bearer explicit" {
				t.Fatalf("overrides = %+v, %v", m, err)
			}
		})
	}
}

func TestLocalPresetsNeedModel(t *testing.T) {
	clearEnv(t)
	for name, opt := range map[string]Option{"vllm": VLLM(), "lmstudio": LMStudio()} {
		if _, err := New(opt); err == nil || !strings.Contains(err.Error(), "WithModel") {
			t.Errorf("%s without model: %v", name, err)
		}
		m, err := New(opt, WithModel("served"))
		if err != nil || m.http.Header.Get("Authorization") != "" {
			t.Errorf("%s: %+v, %v", name, m, err)
		}
	}
	t.Setenv("VLLM_API_KEY", "server-key")
	m, err := New(VLLM(), WithModel("served"))
	if err != nil || m.http.BaseURL != "http://localhost:8000/v1" || m.http.Header.Get("Authorization") != "Bearer server-key" {
		t.Fatalf("vllm = %+v, %v", m, err)
	}
	m, err = New(LMStudio(), WithModel("served"))
	if err != nil || m.http.BaseURL != "http://localhost:1234/v1" || !m.legacyMaxTokens {
		t.Fatalf("lmstudio = %+v, %v", m, err)
	}
}

// capture serves Chat Completions and records request bodies.
func capture(t *testing.T, wantPath string, check func(*http.Request)) (*httptest.Server, func() map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
		}
		if check != nil {
			check(r)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		last = body
		mu.Unlock()
		payload := `{"id":"r1","choices":[{"message":{"content":"ok","reasoning":"thought"},"finish_reason":"stop"}]}`
		if strings.HasSuffix(wantPath, "/responses") {
			payload = `{"id":"r1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`
		}
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			payload = "data: {\"choices\":[{\"delta\":{\"reasoning\":\"thought\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"
		}
		if _, err := w.Write([]byte(payload)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

func quirkRequest() *model.Request {
	assistant := &model.Message{Role: model.RoleAssistant, Parts: []model.Part{
		{Type: model.PartReasoning, Text: "earlier thinking"},
		{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "c1", Name: "lookup", Arguments: json.RawMessage(`{}`)}},
	}}
	user := model.UserText("hi")
	user.Name = "alice"
	return &model.Request{
		System:         "be brief",
		MaxTokens:      42,
		Messages:       []*model.Message{user, assistant, {Role: model.RoleUser, Parts: []model.Part{{Type: model.PartToolResult, ToolResult: &model.ToolResult{CallID: "c1", Content: "done"}}}}},
		Tools:          []*model.ToolSpec{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice:     &model.ToolChoice{Mode: model.ToolChoiceNone},
		ResponseFormat: &model.ResponseFormat{Name: "answer", Schema: json.RawMessage(`{"type":"object"}`), Strict: true},
	}
}

func TestPresetQuirks(t *testing.T) {
	clearEnv(t)
	for _, tc := range []struct {
		name   string
		opt    Option
		check  func(t *testing.T, body map[string]any)
		stream bool
	}{
		{name: "openai chat sends everything", opt: WithChatCompletions(), check: func(t *testing.T, b map[string]any) {
			msgs := b["messages"].([]any)
			if b["max_completion_tokens"] != float64(42) || b["tool_choice"] != "none" || b["tools"] == nil || msgs[1].(map[string]any)["name"] != "alice" {
				t.Errorf("body = %v", b)
			}
			if _, ok := msgs[2].(map[string]any)["reasoning_content"]; ok {
				t.Errorf("reasoning replayed: %v", msgs[2])
			}
			if rf := b["response_format"].(map[string]any); rf["type"] != "json_schema" {
				t.Errorf("response_format = %v", rf)
			}
		}},
		{name: "deepseek", opt: DeepSeek(), check: func(t *testing.T, b map[string]any) {
			msgs := b["messages"].([]any)
			sys := msgs[0].(map[string]any)["content"].(string)
			if b["max_tokens"] != float64(42) || b["max_completion_tokens"] != nil || !strings.HasPrefix(sys, "be brief\n\n") || !strings.Contains(sys, "JSON Schema") {
				t.Errorf("body = %v", b)
			}
			if rf := b["response_format"].(map[string]any); rf["type"] != "json_object" || rf["json_schema"] != nil {
				t.Errorf("response_format = %v", rf)
			}
			if msgs[2].(map[string]any)["reasoning_content"] != "earlier thinking" {
				t.Errorf("assistant = %v", msgs[2])
			}
		}},
		{name: "groq", opt: Groq(), check: func(t *testing.T, b map[string]any) {
			if b["max_completion_tokens"] != float64(42) || b["messages"].([]any)[1].(map[string]any)["name"] != nil {
				t.Errorf("body = %v", b)
			}
		}},
		{name: "mistral", opt: Mistral(), stream: true, check: func(t *testing.T, b map[string]any) {
			if b["max_tokens"] != float64(42) || b["stream_options"] != nil || b["max_completion_tokens"] != nil || b["messages"].([]any)[1].(map[string]any)["name"] != nil {
				t.Errorf("body = %v", b)
			}
			if rf := b["response_format"].(map[string]any)["json_schema"].(map[string]any); rf["strict"] != true {
				t.Errorf("response_format = %v", rf)
			}
		}},
		{name: "together", opt: Together(), stream: true, check: func(t *testing.T, b map[string]any) {
			if b["max_tokens"] != float64(42) || b["stream_options"] != nil {
				t.Errorf("body = %v", b)
			}
		}},
		{name: "fireworks streams usage", opt: Fireworks(), stream: true, check: func(t *testing.T, b map[string]any) {
			if so, _ := b["stream_options"].(map[string]any); so["include_usage"] != true {
				t.Errorf("body = %v", b)
			}
		}},
		{name: "ollama drops tools for none", opt: Ollama(), check: func(t *testing.T, b map[string]any) {
			if b["tools"] != nil || b["tool_choice"] != nil || b["max_tokens"] != float64(42) {
				t.Errorf("body = %v", b)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, body := capture(t, "/v1/chat/completions", nil)
			m, err := New(tc.opt, WithAPIKey("k"), WithBaseURL(srv.URL+"/v1"), WithHTTPClient(srv.Client()))
			if err != nil {
				t.Fatal(err)
			}
			var resp *model.Response
			if tc.stream {
				resp, err = model.Collect(m.Stream(t.Context(), quirkRequest()))
			} else {
				resp, err = m.Generate(t.Context(), quirkRequest())
			}
			if err != nil || resp.Text() != "ok" {
				t.Fatalf("response = %+v, %v", resp, err)
			}
			// The reasoning field some services send is read too.
			if len(resp.Message.Parts) == 0 || resp.Message.Parts[0].Type != model.PartReasoning || resp.Message.Parts[0].Text != "thought" {
				t.Errorf("parts = %+v", resp.Message.Parts)
			}
			tc.check(t, body())
		})
	}
}

func TestOllamaKeepsOtherToolChoices(t *testing.T) {
	clearEnv(t)
	srv, body := capture(t, "/v1/chat/completions", nil)
	m, err := New(Ollama(), WithBaseURL(srv.URL+"/v1"), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	req := quirkRequest()
	req.ToolChoice = &model.ToolChoice{Mode: model.ToolChoiceRequired}
	if _, err := m.Generate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if b := body(); b["tools"] == nil || b["tool_choice"] != nil {
		t.Fatalf("body = %v", b)
	}
}

func TestReasoningField(t *testing.T) {
	t.Parallel()
	var r struct {
		R reasoning `json:"r"`
	}
	for in, want := range map[string]string{`{"r":"text"}`: "text", `{"r":{"effort":"high"}}`: "", `{"r":null}`: ""} {
		r.R = ""
		if err := json.Unmarshal([]byte(in), &r); err != nil || string(r.R) != want {
			t.Errorf("%s: %q, %v", in, r.R, err)
		}
	}
}

func TestAzure(t *testing.T) {
	clearEnv(t)
	if _, err := New(WithAzure("https://res.openai.azure.com"), WithAPIKey("k")); err == nil || !strings.Contains(err.Error(), "WithModel") {
		t.Fatalf("deployment not required: %v", err)
	}
	if _, err := New(WithAzure("https://res.openai.azure.com"), WithModel("dep")); err == nil || !strings.Contains(err.Error(), "AZURE_OPENAI_API_KEY") {
		t.Fatalf("missing key accepted: %v", err)
	}
	t.Run("v1 api key", func(t *testing.T) {
		srv, body := capture(t, "/openai/v1/responses", func(r *http.Request) {
			if r.Header.Get("api-key") != "azure-key" || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
				t.Errorf("request = %s %v", r.URL, r.Header)
			}
		})
		t.Setenv("AZURE_OPENAI_API_KEY", "azure-key")
		m, err := New(WithAzure(srv.URL+"/"), WithModel("my-deployment"), WithHTTPClient(srv.Client()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Generate(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("hi")}}); err != nil {
			t.Fatal(err)
		}
		if b := body(); b["model"] != "my-deployment" {
			t.Errorf("body = %v", b)
		}
		if m.Describe().Provider != "azure.ai.openai" {
			t.Errorf("describe = %+v", m.Describe())
		}
	})
	t.Run("deployment with entra token", func(t *testing.T) {
		t.Setenv("AZURE_OPENAI_API_KEY", "unused")
		srv, _ := capture(t, "/openai/deployments/dep/chat/completions", func(r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer entra" || r.Header.Get("api-key") != "" || r.URL.Query().Get("api-version") != "2024-10-21" {
				t.Errorf("request = %s %v", r.URL, r.Header)
			}
		})
		m, err := New(WithAzureDeployment(srv.URL, "dep", "2024-10-21"), WithHTTPClient(srv.Client()),
			WithTokenSource(func(context.Context) (string, error) { return "entra", nil }))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := model.Collect(m.Stream(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("hi")}})); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTokenSource(t *testing.T) {
	clearEnv(t)
	t.Setenv("OPENAI_API_KEY", "env-key")
	n := 0
	srv, _ := capture(t, "/chat/completions", func(r *http.Request) {
		n++
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
	})
	m, err := New(WithChatCompletions(), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithTokenSource(func(context.Context) (string, error) { return "tok", nil }))
	if err != nil {
		t.Fatal(err)
	}
	if m.http.Header.Get("Authorization") != "" {
		t.Fatal("environment key used with a token source")
	}
	for range 2 {
		if _, err := m.Generate(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("hi")}}); err != nil {
			t.Fatal(err)
		}
	}
	wantErr := errors.New("no credentials")
	m, err = New(WithBaseURL(srv.URL), WithTokenSource(func(context.Context) (string, error) { return "", wantErr }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Generate(t.Context(), &model.Request{}); !errors.Is(err, wantErr) || n != 2 {
		t.Fatalf("error = %v after %d requests", err, n)
	}
}
