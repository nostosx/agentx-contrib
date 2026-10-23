package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nostosx/agentx/model"
)

func TestBuild(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"), WithMaxTokens(32))
	if err != nil {
		t.Fatal(err)
	}
	req := &model.Request{System: "rules", Messages: []*model.Message{model.UserText("hello")}, Tools: []*model.ToolSpec{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`), Strict: true}}, ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceTool, Name: "lookup"}, Reasoning: &model.Reasoning{Effort: "high"}, ResponseFormat: &model.ResponseFormat{Schema: json.RawMessage(`{"type":"object"}`), Strict: true}}
	for _, tc := range []struct {
		name   string
		legacy bool
	}{{name: "modern"}, {name: "legacy", legacy: true}} {
		t.Run(tc.name, func(t *testing.T) {
			copyModel := *m
			copyModel.legacyMaxTokens = tc.legacy
			body, err := copyModel.build(req, true)
			if err != nil {
				t.Fatal(err)
			}
			var got request
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if got.StreamOptions == nil || !got.StreamOptions.IncludeUsage || got.ReasoningEffort != "high" || got.ResponseFormat.JSONSchema.Name != "response" || len(got.Tools) != 1 || !got.Tools[0].Function.Strict {
				t.Fatalf("request = %s", body)
			}
			if (tc.legacy && (got.MaxTokens != 32 || got.MaxCompletionTokens != 0)) || (!tc.legacy && (got.MaxCompletionTokens != 32 || got.MaxTokens != 0)) {
				t.Fatalf("token limit = %s", body)
			}
			if got.Messages[0].Role != "system" || got.Messages[1].Content != "hello" {
				t.Fatalf("messages = %+v", got.Messages)
			}
		})
	}
	temp, topP := 0.5, 0.9
	req.Temperature, req.TopP = &temp, &topP
	body, err := m.build(req, false)
	if err != nil {
		t.Fatal(err)
	}
	var sampled map[string]any
	if err := json.Unmarshal(body, &sampled); err != nil {
		t.Fatal(err)
	}
	if _, ok := sampled["temperature"]; ok || sampled["top_p"] != nil {
		t.Fatalf("sampling parameters sent with reasoning effort: %s", body)
	}
	req.Reasoning = &model.Reasoning{Effort: "none"}
	if body, err := m.build(req, false); err != nil || !strings.Contains(string(body), `"temperature":0.5`) {
		t.Fatalf("temperature dropped without reasoning: %s, %v", body, err)
	}
	req.Temperature, req.TopP, req.Reasoning = nil, nil, &model.Reasoning{Effort: "high"}
	req.Extra = map[string]any{"stream": nil, "model": "custom"}
	body, err = m.build(req, true)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["stream"]; ok || fields["model"] != "custom" {
		t.Fatalf("extra override = %s", body)
	}
}

func TestBuildSamplingOnCompatibleServices(t *testing.T) {
	t.Parallel()
	temp, topP := 0.5, 0.9
	req := &model.Request{Temperature: &temp, TopP: &topP, Reasoning: &model.Reasoning{Effort: "high"}}
	for base, sent := range map[string]bool{
		"":                                       false, // DefaultBaseURL
		"https://API.openai.com/v1/":             false,
		"https://res.openai.azure.com/openai/v1": false,
		"https://generativelanguage.googleapis.com/v1beta/openai": true,
		"https://api.groq.com/openai/v1":                          true,
		"http://localhost:8000/v1":                                true,
	} {
		m, err := New(WithAPIKey("fixture"), WithChatCompletions(), WithBaseURL(base))
		if err != nil {
			t.Fatal(err)
		}
		body, err := m.build(req, false)
		if err != nil {
			t.Fatal(err)
		}
		got := decodeBody(t, body)
		if _, ok := got["temperature"]; ok != sent || (got["top_p"] != nil) != sent || got["reasoning_effort"] != "high" {
			t.Errorf("%q: body = %s", base, body)
		}
	}
}

func TestConvertMessages(t *testing.T) {
	t.Parallel()
	req := &model.Request{Messages: []*model.Message{
		model.NewMessage(model.RoleUser, model.TextPart("image"), model.ImagePart("image/png", []byte{1, 2})),
		model.NewMessage(model.RoleAssistant, model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "c", Name: "lookup", Arguments: []byte(`null`)}}),
		model.ToolResults(&model.ToolResult{CallID: "c", Content: "failed", IsError: true}),
	}}
	msgs, err := (&Model{}).convertMessages(req)
	if err != nil || len(msgs) != 3 || msgs[1].ToolCalls[0].Function.Arguments != `{}` || msgs[2].Content != "Error: failed" || msgs[2].ToolCallID != "c" {
		t.Fatalf("messages = %+v", msgs)
	}
	parts, ok := msgs[0].Content.([]contentPart)
	if !ok || len(parts) != 2 || parts[1].ImageURL.URL != "data:image/png;base64,AQI=" {
		t.Fatalf("image content = %+v", msgs[0].Content)
	}
}

func TestResponseConversion(t *testing.T) {
	t.Parallel()
	var r response
	if err := json.Unmarshal([]byte(`{"choices":[{"message":{"reasoning_content":"think","content":"answer","refusal":"denied","tool_calls":[{"id":"c","function":{"name":"lookup","arguments":"not json"}}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":5},"completion_tokens_details":{"reasoning_tokens":3}}}`), &r); err != nil {
		t.Fatal(err)
	}
	got, err := r.toModel()
	if err != nil || got.FinishReason != model.FinishRefusal || got.Text() != "answerdenied" || got.Usage.InputTokens != 15 || got.Usage.CacheReadTokens != 5 || got.Usage.ReasoningTokens != 3 || string(got.ToolCalls()[0].Arguments) != `"not json"` {
		t.Fatalf("response = %+v, %v", got, err)
	}
	if _, err := (&response{}).toModel(); err == nil {
		t.Fatal("empty choices accepted")
	}
	for _, tc := range []struct {
		name, input string
		want        model.FinishReason
	}{{name: "length", input: "length", want: model.FinishLength}, {name: "tool", input: "tool_calls", want: model.FinishToolCalls}, {name: "filtered", input: "content_filter", want: model.FinishContentFilter}, {name: "unknown", want: model.FinishUnknown}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := finishReason(tc.input); got != tc.want {
				t.Errorf("reason = %s", got)
			}
		})
	}
}
