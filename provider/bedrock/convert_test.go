package bedrock

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nostosx/agentx/model"
)

// pngBytes is the PNG signature, enough for content sniffing.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func newTestModel(t *testing.T, opts ...Option) *Model {
	t.Helper()
	m, err := New(append([]Option{WithAPIKey("k"), WithRegion("us-east-1")}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// buildJSON returns the request body for req on model name, decoded.
func buildJSON(t *testing.T, m *Model, req *model.Request, name string, stream bool) map[string]json.RawMessage {
	t.Helper()
	b, err := m.build(req, name, stream)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBuildMessages(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)
	req := &model.Request{
		System: "base",
		Messages: []*model.Message{
			model.SystemText("extra"),
			model.NewMessage(model.RoleUser, model.TextPart("look"), model.ImagePart("image/png", pngBytes),
				model.ImageURLPart("data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString([]byte{1, 2})),
				model.Part{Type: model.PartImage, URL: "s3://bucket/photo.webp"}, model.TextPart("  ")),
			model.UserText("again"),
			model.NewMessage(model.RoleAssistant,
				model.Part{Type: model.PartReasoning, Text: "think", Signature: "sig", Provider: "bedrock"},
				model.Part{Type: model.PartReasoning, Redacted: true, Data: []byte{9, 9}, Provider: "bedrock"},
				model.Part{Type: model.PartReasoning, Text: "foreign", Signature: "other", Provider: "anthropic"},
				model.Part{Type: model.PartReasoning, Text: "unsigned", Provider: "bedrock"},
				model.TextPart("calling"),
				model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "t1", Name: "lookup", Arguments: json.RawMessage(`{"q":1}`)}},
				model.Part{Type: model.PartRaw, Provider: "bedrock", Raw: json.RawMessage(`{"citationsContent":{}}`)},
				model.Part{Type: model.PartRaw, Provider: "openai", Raw: json.RawMessage(`{"x":1}`)}),
			model.ToolResults(&model.ToolResult{CallID: "t1", Content: "failed", IsError: true}),
			model.UserText("next"),
		},
		Tools: []*model.ToolSpec{{Name: "lookup", Description: "Look up.", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"integer","minimum":1}}}`)}},
	}
	got := buildJSON(t, m, req, "us.anthropic.claude-opus-4-1-20250805-v1:0", false)
	if string(got["system"]) != `[{"text":"base\n\nextra"}]` {
		t.Errorf("system = %s", got["system"])
	}
	wantMessages := `[{"role":"user","content":[{"text":"look"},` +
		`{"image":{"format":"png","source":{"bytes":"` + base64.StdEncoding.EncodeToString(pngBytes) + `"}}},` +
		`{"image":{"format":"jpeg","source":{"bytes":"AQI="}}},` +
		`{"image":{"format":"webp","source":{"s3Location":{"uri":"s3://bucket/photo.webp"}}}},{"text":"again"}]},` +
		`{"role":"assistant","content":[{"reasoningContent":{"reasoningText":{"text":"think","signature":"sig"}}},` +
		`{"reasoningContent":{"redactedContent":"CQk="}},{"text":"calling"},` +
		`{"toolUse":{"toolUseId":"t1","name":"lookup","input":{"q":1}}},{"citationsContent":{}}]},` +
		`{"role":"user","content":[{"toolResult":{"toolUseId":"t1","content":[{"text":"failed"}],"status":"error"}},{"text":"next"}]}]`
	if string(got["messages"]) != wantMessages {
		t.Errorf("messages\ngot  %s\nwant %s", got["messages"], wantMessages)
	}
	// Non-strict tools keep their schema.
	if string(got["toolConfig"]) != `{"tools":[{"toolSpec":{"name":"lookup","description":"Look up.","inputSchema":{"json":{"type":"object","properties":{"q":{"type":"integer","minimum":1}}}}}}]}` {
		t.Errorf("toolConfig = %s", got["toolConfig"])
	}
	if _, ok := got["inferenceConfig"]; ok {
		t.Errorf("inferenceConfig = %s", got["inferenceConfig"])
	}
	// Models other than Claude and Nova get no tool result status.
	got = buildJSON(t, m, req, "meta.llama3-3-70b-instruct-v1:0", false)
	if strings.Contains(string(got["messages"]), `"status"`) {
		t.Errorf("messages = %s", got["messages"])
	}
}

func TestBuildImageErrors(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)
	for _, p := range []model.Part{
		model.ImageURLPart("https://example.com/cat.png"),
		{Type: model.PartImage, URL: "s3://bucket/object"},
		model.ImagePart("image/tiff", []byte("II*\x00")),
		{Type: model.PartImage},
	} {
		_, err := m.build(&model.Request{Messages: []*model.Message{model.NewMessage(model.RoleUser, p)}}, "x", false)
		if err == nil || model.IsRetryable(err) {
			t.Errorf("part %+v: error = %v", p, err)
		}
	}
	// A known media type of an S3 object needs no extension, and bytes
	// without a media type are sniffed.
	req := &model.Request{Messages: []*model.Message{model.NewMessage(model.RoleUser,
		model.Part{Type: model.PartImage, URL: "s3://bucket/object", MediaType: "image/gif"},
		model.Part{Type: model.PartImage, URL: "s3://bucket/a.JPG"},
		model.Part{Type: model.PartImage, Data: pngBytes})}}
	got := buildJSON(t, m, req, "x", false)
	if !strings.Contains(string(got["messages"]), `"format":"gif"`) || !strings.Contains(string(got["messages"]), `"format":"png"`) ||
		!strings.Contains(string(got["messages"]), `"format":"jpeg","source":{"s3Location":{"uri":"s3://bucket/a.JPG"}}`) {
		t.Fatalf("messages = %s", got["messages"])
	}
}

func TestBuildToolChoice(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)
	history := []*model.Message{
		model.UserText("q"),
		model.NewMessage(model.RoleAssistant, model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "t1", Name: "lookup", Arguments: json.RawMessage(`{"q":1}`)}},
			model.Part{Type: model.PartRaw, Provider: "bedrock", Raw: json.RawMessage(`{"toolUse":{"toolUseId":"s","name":"search","input":{},"type":"server_tool_use"}}`)}),
		model.ToolResults(&model.ToolResult{CallID: "t1", Content: "42"}, &model.ToolResult{CallID: "t2", Content: "bad", IsError: true}),
	}
	tools := []*model.ToolSpec{{Name: "lookup"}}
	for _, tc := range []struct {
		name   string
		choice *model.ToolChoice
		want   string
	}{
		{name: "default", want: `{"tools":[{"toolSpec":{"name":"lookup","inputSchema":{"json":{"type":"object","properties":{}}}}}]}`},
		{name: "auto", choice: &model.ToolChoice{Mode: model.ToolChoiceAuto}, want: `"toolChoice":{"auto":{}}`},
		{name: "required", choice: &model.ToolChoice{Mode: model.ToolChoiceRequired}, want: `"toolChoice":{"any":{}}`},
		{name: "tool", choice: &model.ToolChoice{Mode: model.ToolChoiceTool, Name: "lookup"}, want: `"toolChoice":{"tool":{"name":"lookup"}}`},
	} {
		got := buildJSON(t, m, &model.Request{Messages: history, Tools: tools, ToolChoice: tc.choice}, "x", false)
		if !strings.Contains(string(got["toolConfig"]), tc.want) || !strings.Contains(string(got["messages"]), `"toolUse"`) {
			t.Errorf("%s: toolConfig = %s", tc.name, got["toolConfig"])
		}
	}
	// Without tools, or with ToolChoiceNone, no toolConfig is sent and the
	// tool blocks of the history become text.
	for _, req := range []*model.Request{
		{Messages: history, Tools: tools, ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceNone}},
		{Messages: history},
	} {
		got := buildJSON(t, m, req, "x", false)
		want := `[{"role":"user","content":[{"text":"q"}]},{"role":"assistant","content":[{"text":"[Tool call lookup (t1) with input: {\"q\":1}]"}]},` +
			`{"role":"user","content":[{"text":"[Tool result for call t1: 42]"},{"text":"[Tool error for call t2: bad]"}]}]`
		if _, ok := got["toolConfig"]; ok || string(got["messages"]) != want {
			t.Errorf("toolConfig = %s, messages = %s", got["toolConfig"], got["messages"])
		}
	}
}

func TestBuildStructuredOutput(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type": "object", "properties": {"n": {"type": "integer", "minimum": 0}}, "required": ["n"], "additionalProperties": false}`)
	adapted := `{"type":"object","properties":{"n":{"type":"integer","description":"{minimum: 0}"}},"required":["n"],"additionalProperties":false}`
	req := &model.Request{
		Messages:       []*model.Message{model.UserText("q")},
		ResponseFormat: &model.ResponseFormat{Name: "answer", Schema: schema},
		Tools:          []*model.ToolSpec{{Name: "f", Parameters: schema, Strict: true}},
	}
	m := newTestModel(t)
	got := buildJSON(t, m, req, "global.anthropic.claude-sonnet-4-5-20250929-v1:0", false)
	var oc struct {
		TextFormat struct {
			Type      string `json:"type"`
			Structure struct {
				JSONSchema struct {
					Schema string `json:"schema"`
					Name   string `json:"name"`
				} `json:"jsonSchema"`
			} `json:"structure"`
		} `json:"textFormat"`
	}
	if err := json.Unmarshal(got["outputConfig"], &oc); err != nil {
		t.Fatal(err)
	}
	js := oc.TextFormat.Structure.JSONSchema
	if oc.TextFormat.Type != "json_schema" || js.Schema != adapted || js.Name != "answer" || got["system"] != nil {
		t.Fatalf("outputConfig = %s, system = %s", got["outputConfig"], got["system"])
	}
	if !strings.Contains(string(got["toolConfig"]), `"strict":true`) || !strings.Contains(string(got["toolConfig"]), `{minimum: 0}`) {
		t.Fatalf("toolConfig = %s", got["toolConfig"])
	}
	// Models without known support get an instruction and plain tools.
	for _, name := range []string{"anthropic.claude-sonnet-4-20250514-v1:0", "amazon.nova-pro-v1:0"} {
		got = buildJSON(t, m, req, name, false)
		if got["outputConfig"] != nil || !strings.Contains(string(got["system"]), "JSON Schema") || strings.Contains(string(got["toolConfig"]), "strict") {
			t.Fatalf("%s: body = %v", name, got)
		}
	}
	// WithStructuredOutput overrides the choice.
	got = buildJSON(t, newTestModel(t, WithStructuredOutput(true)), req, "qwen.qwen3-32b-v1:0", false)
	if got["outputConfig"] == nil {
		t.Fatal("native structured output not forced")
	}
	got = buildJSON(t, newTestModel(t, WithStructuredOutput(false)), req, "anthropic.claude-opus-5-5", false)
	if got["outputConfig"] != nil {
		t.Fatal("native structured output not disabled")
	}
}

func TestBuildOptionsAndExtra(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, WithGuardrail(Guardrail{Identifier: "gr", Version: "DRAFT", Trace: "enabled", StreamProcessingMode: "async"}),
		WithPromptCaching(), WithMaxTokens(100))
	temp, topP := 0.5, 0.9
	req := &model.Request{
		System:      "sys",
		Messages:    []*model.Message{model.UserText("q")},
		Tools:       []*model.ToolSpec{{Name: "f"}},
		Temperature: &temp, TopP: &topP, Stop: []string{"END"},
		Extra: map[string]any{"requestMetadata": map[string]string{"team": "a"}, "additionalModelRequestFields": map[string]int{"top_k": 5}},
	}
	got := buildJSON(t, m, req, "meta.llama3-3-70b-instruct-v1:0", false)
	for field, want := range map[string]string{
		"system":                       `[{"text":"sys"},{"cachePoint":{"type":"default"}}]`,
		"messages":                     `[{"role":"user","content":[{"text":"q"},{"cachePoint":{"type":"default"}}]}]`,
		"toolConfig":                   `{"tools":[{"toolSpec":{"name":"f","inputSchema":{"json":{"type":"object","properties":{}}}}},{"cachePoint":{"type":"default"}}]}`,
		"inferenceConfig":              `{"maxTokens":100,"temperature":0.5,"topP":0.9,"stopSequences":["END"]}`,
		"guardrailConfig":              `{"guardrailIdentifier":"gr","guardrailVersion":"DRAFT","trace":"enabled"}`,
		"requestMetadata":              `{"team":"a"}`,
		"additionalModelRequestFields": `{"top_k":5}`,
	} {
		if string(got[field]) != want {
			t.Errorf("%s = %s, want %s", field, got[field], want)
		}
	}
	got = buildJSON(t, m, req, "x", true)
	if string(got["guardrailConfig"]) != `{"guardrailIdentifier":"gr","guardrailVersion":"DRAFT","trace":"enabled","streamProcessingMode":"async"}` {
		t.Errorf("stream guardrailConfig = %s", got["guardrailConfig"])
	}
}

func TestParseResponse(t *testing.T) {
	t.Parallel()
	body := `{"output":{"message":{"role":"assistant","content":[
		{"reasoningContent":{"reasoningText":{"text":"hmm","signature":"sig"}}},
		{"reasoningContent":{"redactedContent":"CQk="}},
		{"text":"answer"},
		{"toolUse":{"toolUseId":"t1","name":"lookup","input":{"q": 1}}},
		{"toolUse":{"toolUseId":"s1","name":"search","input":{},"type":"server_tool_use"}},
		{"citationsContent":{"content":[{"text":"cited"}]}}]}},
		"stopReason":"tool_use","usage":{"inputTokens":3,"outputTokens":4,"totalTokens":7}}`
	var r response
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	resp := r.toModel()
	parts := resp.Message.Parts
	if len(parts) != 6 || parts[0].Signature != "sig" || parts[0].Provider != "bedrock" || !parts[1].Redacted || string(parts[1].Data) != "\t\t" ||
		resp.Text() != "answer" || string(resp.ToolCalls()[0].Arguments) != `{"q":1}` || len(resp.ToolCalls()) != 1 ||
		parts[4].Type != model.PartRaw || parts[5].Type != model.PartRaw || resp.FinishReason != model.FinishToolCalls || resp.Usage.TotalTokens() != 7 {
		t.Fatalf("response = %+v", resp)
	}
	// Replaying the response sends the blocks back unchanged.
	m := newTestModel(t)
	got := buildJSON(t, m, &model.Request{Messages: []*model.Message{model.UserText("q"), resp.Message}, Tools: []*model.ToolSpec{{Name: "lookup"}}}, "x", false)
	for _, want := range []string{`{"reasoningText":{"text":"hmm","signature":"sig"}}`, `{"redactedContent":"CQk="}`,
		`"type":"server_tool_use"`, `{"citationsContent":{"content":[{"text":"cited"}]}}`} {
		if !strings.Contains(string(got["messages"]), want) {
			t.Errorf("messages = %s, want %s", got["messages"], want)
		}
	}
}

func TestFinishReason(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]model.FinishReason{
		"end_turn": model.FinishStop, "stop_sequence": model.FinishStop, "tool_use": model.FinishToolCalls,
		"max_tokens": model.FinishLength, "model_context_window_exceeded": model.FinishLength,
		"guardrail_intervened": model.FinishContentFilter, "content_filtered": model.FinishContentFilter,
		"malformed_tool_use": "malformed_tool_use", "": model.FinishUnknown,
	} {
		if got := finishReason(in); got != want {
			t.Errorf("finishReason(%q) = %q, want %q", in, got, want)
		}
	}
}
