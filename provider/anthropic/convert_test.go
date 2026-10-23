package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nostosx/agentx/model"
)

func TestBuild(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"), WithModel(DefaultModel), WithBeta("custom"))
	if err != nil {
		t.Fatal(err)
	}
	req := &model.Request{System: "rules", Messages: []*model.Message{model.SystemText("extra"), model.UserText("one"), model.UserText("two")}, Tools: []*model.ToolSpec{{Name: "lookup", Strict: true}}, ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceRequired}, Reasoning: &model.Reasoning{Effort: "high"}, ResponseFormat: &model.ResponseFormat{Schema: json.RawMessage(`{"type":"object"}`)}}
	body, header, err := m.build(req, true)
	if err != nil {
		t.Fatal(err)
	}
	var got request
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != DefaultModel || got.MaxTokens != defaultStreamMaxTokens || got.System != "rules\n\nextra" || len(got.Messages) != 1 || len(got.Messages[0].Content) != 2 || got.ToolChoice.Type != "any" || !got.Tools[0].EagerInputStreaming || got.Thinking.Type != "adaptive" || got.OutputConfig.Effort != "high" || got.OutputConfig.Format.Type != "json_schema" || got.CacheControl == nil || got.Fallbacks != "default" {
		t.Fatalf("body = %s", body)
	}
	if beta := header.Get("anthropic-beta"); beta != "custom,"+FallbackBeta {
		t.Fatal("fallback beta missing")
	}
	m.noCache = true
	m.fallbacks = new(false)
	req.Reasoning.BudgetTokens = 1024
	req.MaxTokens = 4096
	body, _, err = m.build(req, false)
	if err != nil {
		t.Fatal(err)
	}
	got = request{}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	// The default model accepts only adaptive thinking.
	if got.CacheControl != nil || got.Fallbacks != "" || got.MaxTokens != 4096 || got.Thinking.Type != "adaptive" || got.Thinking.BudgetTokens != 0 || got.OutputConfig.Effort != "high" {
		t.Fatalf("body = %s", body)
	}
}

func TestBuildSamplingWithThinking(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	const older = "claude-sonnet-4-6"
	for _, tc := range []struct {
		name        string
		model       string
		reasoning   *model.Reasoning
		topP        float64
		temperature bool
		wantTopP    bool
	}{
		{name: "no thinking", model: older, topP: 0.5, temperature: true, wantTopP: true},
		{name: "thinking low top_p", model: older, reasoning: &model.Reasoning{Effort: "high"}, topP: 0.5},
		{name: "thinking high top_p", model: older, reasoning: &model.Reasoning{BudgetTokens: 1024}, topP: 0.96, wantTopP: true},
		{name: "old id", model: "claude-3-5-sonnet-20241022", topP: 0.5, temperature: true, wantTopP: true},
		{name: "opus 4.6", model: "claude-opus-4-6", topP: 0.5, temperature: true, wantTopP: true},
		{name: "alias", model: "my-gateway-model", topP: 0.5, temperature: true, wantTopP: true},
		// These models reject sampling parameters with or without thinking.
		{name: "default model", topP: 0.5},
		{name: "default model thinking top_p 1", reasoning: &model.Reasoning{Effort: "high"}, topP: 1},
		{name: "opus 4.7", model: "claude-opus-4-7", topP: 0.5},
		{name: "opus 5", model: "claude-opus-5", topP: 0.5},
		{name: "sonnet 5.5", model: "claude-sonnet-5-5", topP: 0.96},
		{name: "haiku 5.5", model: "claude-haiku-5-5", topP: 0.5},
		{name: "fable", model: "claude-fable-5-1", topP: 0.5},
		{name: "mythos", model: "claude-mythos-5-1", topP: 0.5},
		{name: "bedrock", model: "us.anthropic.claude-opus-4-8", topP: 0.5},
		{name: "vertex", model: "claude-sonnet-5@20260101", topP: 0.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			temp, topP := 0.3, tc.topP
			body, _, err := m.build(&model.Request{Model: tc.model, Messages: []*model.Message{model.UserText("hi")}, Temperature: &temp, TopP: &topP, Reasoning: tc.reasoning}, false)
			if err != nil {
				t.Fatal(err)
			}
			var got request
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if (got.Temperature != nil) != tc.temperature || (got.TopP != nil) != tc.wantTopP {
				t.Fatalf("body = %s", body)
			}
		})
	}
}

func TestConvertMessages(t *testing.T) {
	t.Parallel()
	msg := model.NewMessage(model.RoleAssistant,
		model.Part{Type: model.PartReasoning, Text: "unsigned"},
		model.Part{Type: model.PartReasoning, Text: "signed", Signature: "sig"},
		model.Part{Type: model.PartReasoning, Redacted: true, Data: []byte("opaque")},
		model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "c", Name: "lookup", Arguments: []byte(`[]`)}},
		model.Part{Type: model.PartRaw, Provider: "anthropic", Raw: json.RawMessage(`{"type":"server_tool_use","id":"s"}`)},
		model.Part{Type: model.PartRaw, Provider: "other", Raw: json.RawMessage(`{"type":"foreign"}`)})
	blocks := assistantBlocks(msg)
	if len(blocks) != 4 || strings.Contains(string(blocks[0]), "unsigned") || !strings.Contains(string(blocks[2]), `"input":{}`) {
		t.Fatalf("blocks = %s", blocks)
	}
	user := model.NewMessage(model.RoleUser, model.ImagePart("image/png", []byte{1, 2}), model.ImageURLPart("https://fixture.invalid/image"), model.Part{Type: model.PartToolResult, ToolResult: &model.ToolResult{CallID: "c", Content: "failed", IsError: true}},
		model.ImageURLPart("data:image/jpeg;base64,AQI="))
	blocks, err := (&converter{}).userBlocks(user)
	if err != nil || len(blocks) != 4 || !strings.Contains(string(blocks[0]), `"data":"AQI="`) || !strings.Contains(string(blocks[1]), `"type":"url"`) || !strings.Contains(string(blocks[2]), `"is_error":true`) {
		t.Fatalf("user blocks = %s", blocks)
	}
	// The API fetches only http and https URLs.
	if b := string(blocks[3]); b != `{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"AQI="}}` {
		t.Fatalf("data URL block = %s", b)
	}
}

func TestResponseConversion(t *testing.T) {
	t.Parallel()
	var r response
	if err := json.Unmarshal([]byte(`{"id":"r","content":[{"type":"thinking","thinking":"think","signature":"sig"},{"type":"text","text":"answer"},{"type":"tool_use","id":"c","name":"lookup","input":{"q":1}},{"type":"server_tool_result","content":"opaque"}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}`), &r); err != nil {
		t.Fatal(err)
	}
	got := r.toModel()
	if got.Text() != "answer" || len(got.Message.Parts) != 4 || got.Message.Parts[0].Signature != "sig" || got.Message.Parts[3].Provider != "anthropic" || got.Usage.TotalTokens() != 20 || string(got.ToolCalls()[0].Arguments) != `{"q":1}` || got.FinishReason != model.FinishToolCalls {
		t.Fatalf("response = %+v", got)
	}
	for _, tc := range []struct {
		name, input string
		want        model.FinishReason
	}{{name: "stop", input: "end_turn", want: model.FinishStop}, {name: "length", input: "max_tokens", want: model.FinishLength}, {name: "pause", input: "pause_turn", want: model.FinishPause}, {name: "refusal", input: "refusal", want: model.FinishRefusal}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := finishReason(tc.input); got != tc.want {
				t.Errorf("reason = %s", got)
			}
		})
	}
}

func TestForeignReasoningIsNotReplayed(t *testing.T) {
	t.Parallel()
	msg := model.NewMessage(model.RoleAssistant,
		model.Part{Type: model.PartReasoning, Text: "mine", Signature: "anthropic-sig", Provider: providerName},
		model.Part{Type: model.PartReasoning, Text: "legacy", Signature: "untagged-sig"},
		model.Part{Type: model.PartReasoning, Text: "gemini thought", Signature: "gemini-sig", Provider: "gemini"},
		model.Part{Type: model.PartReasoning, Redacted: true, Data: []byte("foreign"), Provider: "other"},
		model.Part{Type: model.PartReasoning, Text: "openai summary", Provider: "openai", Raw: json.RawMessage(`{"type":"reasoning"}`)},
		model.TextPart("answer"))
	got := string(marshalBlocks(t, assistantBlocks(msg)))
	for _, want := range []string{"anthropic-sig", "untagged-sig", "answer"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	for _, unwanted := range []string{"gemini", "foreign", "openai"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("foreign reasoning %q replayed: %s", unwanted, got)
		}
	}
}

func TestParsedReasoningIsTagged(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"type":"thinking","thinking":"t","signature":"s"}`, `{"type":"redacted_thinking","data":"d"}`} {
		p, ok := parseBlock(json.RawMessage(raw))
		if !ok || p.Provider != providerName {
			t.Errorf("%s: part = %+v", raw, p)
		}
	}
}

func marshalBlocks(t *testing.T, blocks []json.RawMessage) []byte {
	t.Helper()
	b, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The default max_tokens stays within what older models accept; an
// explicit one is sent as given.
func TestDefaultMaxTokensLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		model  string
		stream bool
		set    int
		want   int
	}{
		{"claude-opus-4-1-20250805", true, 0, 32000},
		{"claude-opus-4-20250514", false, 0, 16000},
		{"us.anthropic.claude-opus-4-1-20250805-v1:0", true, 0, 32000},
		{"claude-opus-4-5", true, 0, defaultStreamMaxTokens},
		{"claude-3-5-haiku-20241022", true, 0, 8192},
		{"claude-3-haiku-20240307", false, 0, 4096},
		{"claude-3-7-sonnet-20250219", true, 0, defaultStreamMaxTokens},
		{"claude-sonnet-5-5", true, 0, defaultStreamMaxTokens},
		{"gateway-alias", true, 0, defaultStreamMaxTokens},
		{"claude-opus-4-1", true, 50000, 50000},
	} {
		m, err := New(WithAPIKey("fixture"), WithModel(tc.model))
		if err != nil {
			t.Fatal(err)
		}
		req := &model.Request{MaxTokens: tc.set, Messages: []*model.Message{model.UserText("hi")}}
		body, _, err := m.build(req, tc.stream)
		if err != nil {
			t.Fatal(err)
		}
		var got request
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.MaxTokens != tc.want {
			t.Errorf("%s (stream %v): max_tokens = %d, want %d", tc.model, tc.stream, got.MaxTokens, tc.want)
		}
	}
}
