package gemini

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nostosx/kitx/dataurl"

	"github.com/nostosx/agentx/model"
)

func TestBuild(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"), WithMaxTokens(32))
	if err != nil {
		t.Fatal(err)
	}
	req := &model.Request{Model: "override", System: "rules", Messages: []*model.Message{model.SystemText("extra"), model.UserText("one"), model.UserText("two")}, Tools: []*model.ToolSpec{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}, ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceTool, Name: "lookup"}, Reasoning: &model.Reasoning{Effort: "high", Display: "omitted", BudgetTokens: 1024}, ResponseFormat: &model.ResponseFormat{Schema: json.RawMessage(`{"type":"object"}`)}}
	body, name, err := m.build(req)
	if err != nil {
		t.Fatal(err)
	}
	var got request
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if name != "override" || got.SystemInstruction.Parts[0].Text != "rules\n\nextra" || len(got.Contents) != 1 || len(got.Contents[0].Parts) != 2 || got.ToolConfig.FunctionCallingConfig.Mode != "ANY" || got.ToolConfig.FunctionCallingConfig.AllowedFunctionNames[0] != "lookup" || got.GenerationConfig.MaxOutputTokens != 32 || got.GenerationConfig.ResponseMIMEType != "application/json" || got.GenerationConfig.ThinkingConfig.IncludeThoughts || *got.GenerationConfig.ThinkingConfig.ThinkingBudget != 1024 {
		t.Fatalf("body = %s", body)
	}
}

func TestConvertMessages(t *testing.T) {
	t.Parallel()
	msgs := mustConvert(t, []*model.Message{
		model.NewMessage(model.RoleUser, model.ImagePart("image/png", []byte{1, 2}), model.ImageURLPart("https://fixture.invalid/image")),
		model.NewMessage(model.RoleAssistant, model.Part{Type: model.PartReasoning, Text: "unsigned"}, model.Part{Type: model.PartReasoning, Text: "signed", Signature: "sig", Provider: providerName}, model.Part{Type: model.PartToolCall, Signature: "call-sig", Provider: providerName, ToolCall: &model.ToolCall{ID: "c", Name: "lookup"}}),
		model.ToolResults(&model.ToolResult{CallID: "c", Name: "lookup", Content: `{"value":1}`}, &model.ToolResult{CallID: "e", Name: "lookup", Content: "failed", IsError: true}),
	})
	if len(msgs) != 3 || msgs[0].Parts[0].InlineData.Data != "AQI=" || msgs[0].Parts[1].FileData.FileURI != "https://fixture.invalid/image" || len(msgs[1].Parts) != 2 || msgs[1].Role != "model" || msgs[1].Parts[1].ThoughtSignature != "call-sig" || string(msgs[1].Parts[1].FunctionCall.Args) != `{}` || msgs[2].Parts[1].FunctionResponse.Response["error"] != "failed" {
		t.Fatalf("messages = %+v", msgs)
	}
	result, ok := msgs[2].Parts[0].FunctionResponse.Response["result"].(map[string]any)
	if !ok || result["value"] != float64(1) {
		t.Fatalf("JSON tool result = %#v", result)
	}
}

func TestResponseConversion(t *testing.T) {
	t.Parallel()
	var r response
	if err := json.Unmarshal([]byte(`{"candidates":[{"content":{"parts":[{"text":"think","thought":true,"thoughtSignature":"sig"},{"text":"answer"},{"functionCall":{"name":"lookup","args":{"q":1}},"thoughtSignature":"call-sig"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"cachedContentTokenCount":5,"candidatesTokenCount":8,"thoughtsTokenCount":3}}`), &r); err != nil {
		t.Fatal(err)
	}
	got, err := r.toModel()
	if err != nil || got.Text() != "answer" || got.FinishReason != model.FinishToolCalls || got.Usage.InputTokens != 15 || got.Usage.OutputTokens != 11 || got.Usage.ReasoningTokens != 3 || !strings.HasPrefix(got.ToolCalls()[0].ID, "call_") || got.Message.Parts[2].Signature != "call-sig" || got.Message.Parts[2].Provider != providerName || got.Message.Parts[1].Provider != "" {
		t.Fatalf("response = %+v, %v", got, err)
	}
	if _, err := (&response{}).toModel(); err == nil {
		t.Fatal("missing candidates accepted")
	}
	if err := json.Unmarshal([]byte(`{"promptFeedback":{"blockReason":"SAFETY"}}`), &r); err != nil {
		t.Fatal(err)
	}
	r.Candidates = nil
	got, err = r.toModel()
	if err != nil || got.FinishReason != model.FinishContentFilter {
		t.Fatalf("blocked response = %+v, %v", got, err)
	}
	if _, ok := toPart(part{InlineData: &blob{Data: "not base64!"}}); ok {
		t.Fatal("invalid image data accepted")
	}
	for _, tc := range []struct {
		name, input string
		want        model.FinishReason
	}{{name: "length", input: "MAX_TOKENS", want: model.FinishLength}, {name: "safety", input: "SAFETY", want: model.FinishContentFilter}, {name: "unknown", want: model.FinishUnknown}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := finishReason(tc.input, false); got != tc.want {
				t.Errorf("reason = %s", got)
			}
		})
	}
}

func TestResponseThoughtImages(t *testing.T) {
	t.Parallel()
	var r response
	if err := json.Unmarshal([]byte(`{"candidates":[{"content":{"parts":[`+strings.Join(thoughtImageParts, ",")+`]},"finishReason":"STOP"}]}`), &r); err != nil {
		t.Fatal(err)
	}
	got, err := r.toModel()
	if err != nil {
		t.Fatal(err)
	}
	checkThoughtImages(t, got.Message)
	// Unsigned draft images are not replayed; the final image is.
	b, err := json.Marshal(mustConvert(t, []*model.Message{got.Message}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "AQI=") || strings.Contains(string(b), "AwQ=") || !strings.Contains(string(b), `"thoughtSignature":"sig2","inlineData":{"mimeType":"image/png","data":"BQY="}`) {
		t.Fatalf("replay = %s", b)
	}
}

func TestConvertForeignHistory(t *testing.T) {
	t.Parallel()
	// History from another provider: its signatures must not reach Gemini,
	// and the first call of each step gets the documented dummy signature.
	msgs := mustConvert(t, []*model.Message{
		model.UserText("q"),
		model.NewMessage(model.RoleAssistant,
			model.Part{Type: model.PartReasoning, Text: "claude thinking", Signature: "anthropic-sig"},
			model.Part{Type: model.PartText, Text: "calling", Signature: "other"},
			model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "a", Name: "lookup"}},
			model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "b", Name: "lookup"}},
			model.Part{Type: model.PartRaw, Provider: "anthropic", Raw: json.RawMessage(`{"type":"server_tool_use"}`)},
		),
	})
	parts := msgs[1].Parts
	if len(parts) != 3 || parts[0].Text != "calling" || parts[0].ThoughtSignature != "" || parts[1].ThoughtSignature != dummySignature || parts[2].ThoughtSignature != "" {
		t.Fatalf("parts = %+v", parts)
	}
	// A Gemini call keeps its own signature and gets no dummy.
	msgs = mustConvert(t, []*model.Message{model.NewMessage(model.RoleAssistant,
		model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "a", Name: "lookup"}, Signature: "real", Provider: providerName},
		model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "b", Name: "lookup"}},
	)})
	if msgs[0].Parts[0].ThoughtSignature != "real" || msgs[0].Parts[1].ThoughtSignature != "" {
		t.Fatalf("gemini parts = %+v", msgs[0].Parts)
	}
}

func TestRawPartsRoundTrip(t *testing.T) {
	t.Parallel()
	var r response
	raw := `{"executableCode":{"language":"PYTHON","code":"print(1)"},"thoughtSignature":"s"}`
	if err := json.Unmarshal([]byte(`{"candidates":[{"content":{"parts":[`+raw+`,{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1"}},{"text":""},{"text":"done"}]},"finishReason":"STOP"}]}`), &r); err != nil {
		t.Fatal(err)
	}
	got, err := r.toModel()
	if err != nil || len(got.Message.Parts) != 3 || got.Message.Parts[0].Type != model.PartRaw || got.Message.Parts[0].Provider != providerName || got.Text() != "done" {
		t.Fatalf("response = %+v, %v", got, err)
	}
	b, err := json.Marshal(mustConvert(t, []*model.Message{got.Message}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), raw) || !strings.Contains(string(b), `"codeExecutionResult"`) {
		t.Fatalf("replay = %s", b)
	}
}

func TestThinkingConfig(t *testing.T) {
	t.Parallel()
	budget := func(n int) *int { return &n }
	for _, tc := range []struct {
		name, model string
		in          model.Reasoning
		level       string
		budget      *int
	}{
		{name: "level", model: "gemini-3.8-flash", in: model.Reasoning{Effort: "high"}, level: "high"},
		{name: "budget wins", model: "gemini-3.8-flash", in: model.Reasoning{Effort: "high", BudgetTokens: 1024}, budget: budget(1024)},
		{name: "2.5 effort", model: "gemini-2.5-flash", in: model.Reasoning{Effort: "LOW"}, budget: budget(2048)},
		{name: "2.5 prefixed", model: "models/gemini-2.5-pro", in: model.Reasoning{Effort: "high"}, budget: budget(24576)},
		{name: "2.5 max", model: "gemini-2.5-flash", in: model.Reasoning{Effort: "max"}, budget: budget(24576)},
		// Models without minimal get their lowest level, low.
		{name: "level none", model: "gemini-3.8-flash", in: model.Reasoning{Effort: "none"}, level: "low"},
		{name: "level minimal", model: "models/gemini-3.8-flash", in: model.Reasoning{Effort: "minimal"}, level: "low"},
		{name: "argon none", model: "gemini-4-argon", in: model.Reasoning{Effort: "none"}, level: "low"},
		{name: "pro none", model: "gemini-3.1-pro-preview", in: model.Reasoning{Effort: "none"}, level: "low"},
		{name: "3 pro minimal", model: "gemini-3-pro-preview", in: model.Reasoning{Effort: "minimal"}, level: "low"},
		{name: "unlisted none", model: "gemini-3.7-flash", in: model.Reasoning{Effort: "none"}, level: "low"},
		{name: "unlisted medium", model: "gemini-5-pro", in: model.Reasoning{Effort: "medium"}, level: "medium"},
		// Models with minimal.
		{name: "3 flash none", model: "gemini-3-flash-preview", in: model.Reasoning{Effort: "none"}, level: "minimal"},
		{name: "3.6 flash minimal", model: "gemini-3.6-flash", in: model.Reasoning{Effort: "minimal"}, level: "minimal"},
		{name: "flash-lite none", model: "gemini-3.5-flash-lite", in: model.Reasoning{Effort: "none"}, level: "minimal"},
		{name: "3.1 flash-lite medium", model: "gemini-3.1-flash-lite-preview", in: model.Reasoning{Effort: "medium"}, level: "medium"},
		{name: "robotics", model: "gemini-robotics-er-2-preview", in: model.Reasoning{Effort: "none"}, level: "minimal"},
		// Other levels map to the nearest accepted one, rounding ties up.
		{name: "flash-lite image minimal", model: "gemini-3.1-flash-lite-image", in: model.Reasoning{Effort: "minimal"}, level: "minimal"},
		{name: "flash-lite image low", model: "gemini-3.1-flash-lite-image-preview", in: model.Reasoning{Effort: "low"}, level: "minimal"},
		{name: "flash-lite image medium", model: "gemini-3.1-flash-lite-image", in: model.Reasoning{Effort: "medium"}, level: "high"},
		{name: "3 pro medium", model: "gemini-3-pro-preview", in: model.Reasoning{Effort: "medium"}, level: "high"},
		{name: "3 pro low", model: "Gemini-3-Pro-Preview", in: model.Reasoning{Effort: "low"}, level: "low"},
		// Gemini 2.5 budgets fit each model's range.
		{name: "2.5 pro none", model: "gemini-2.5-pro", in: model.Reasoning{Effort: "none"}, budget: budget(128)},
		{name: "2.5 pro explicit", model: "gemini-2.5-pro", in: model.Reasoning{BudgetTokens: 64}, budget: budget(128)},
		{name: "2.5 pro dynamic", model: "gemini-2.5-pro", in: model.Reasoning{BudgetTokens: -1}, budget: budget(-1)},
		{name: "2.5 flash none", model: "gemini-2.5-flash", in: model.Reasoning{Effort: "none"}, budget: budget(0)},
		{name: "2.5 flash explicit", model: "gemini-2.5-flash", in: model.Reasoning{BudgetTokens: 40000}, budget: budget(24576)},
		{name: "2.5 flash-lite none", model: "gemini-2.5-flash-lite", in: model.Reasoning{Effort: "none"}, budget: budget(0)},
		{name: "2.5 flash-lite explicit", model: "gemini-2.5-flash-lite", in: model.Reasoning{BudgetTokens: 100}, budget: budget(512)},
		{name: "older budget unchanged", model: "gemini-2.0-flash-thinking-exp", in: model.Reasoning{BudgetTokens: 100}, budget: budget(100)},
		{name: "level xhigh", model: "gemini-3.8-flash", in: model.Reasoning{Effort: "XHigh"}, level: "high"},
		{name: "level max", model: "gemini-4-argon", in: model.Reasoning{Effort: "max"}, level: "high"},
		{name: "display only", model: "gemini-3.8-flash", in: model.Reasoning{Display: "summarized"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := thinking(&tc.in, tc.model)
			if err != nil {
				t.Fatal(err)
			}
			if got.ThinkingLevel != tc.level || (got.ThinkingBudget == nil) != (tc.budget == nil) || (tc.budget != nil && *got.ThinkingBudget != *tc.budget) {
				t.Fatalf("thinking = %+v", got)
			}
			if got.ThinkingLevel != "" && got.ThinkingBudget != nil {
				t.Fatal("level and budget sent together")
			}
		})
	}
}

func TestThinkingRejectsUnknownEffort(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gemini-2.5-flash", "gemini-3.8-flash"} {
		_, _, err := m.build(&model.Request{Model: name, Reasoning: &model.Reasoning{Effort: "turbo"}})
		if err == nil || !strings.Contains(err.Error(), `"turbo"`) || model.IsRetryable(err) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
}

func TestToPartClassifiesByContent(t *testing.T) {
	t.Parallel()
	var r response
	// Metadata outside the neutral form must not turn calls or text into
	// raw parts.
	body := `{"candidates":[{"content":{"parts":[` +
		`{"functionCall":{"id":"c","name":"lookup","args":{"q":1}},"thoughtSignature":"s","partMetadata":{"k":"v"},"newField":1},` +
		`{"text":"hi","videoMetadata":{}},` +
		`{"toolCall":{"id":"t"}}` +
		`]},"finishReason":"STOP"}]}`
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	got, err := r.toModel()
	if err != nil {
		t.Fatal(err)
	}
	parts := got.Message.Parts
	if len(parts) != 3 || got.FinishReason != model.FinishToolCalls {
		t.Fatalf("parts = %+v", parts)
	}
	if tc := parts[0].ToolCall; parts[0].Type != model.PartToolCall || tc.ID != "c" || string(tc.Arguments) != `{"q":1}` || parts[0].Signature != "s" {
		t.Errorf("call = %+v", parts[0])
	}
	if parts[1].Type != model.PartText || parts[1].Text != "hi" {
		t.Errorf("text = %+v", parts[1])
	}
	if parts[2].Type != model.PartRaw || string(parts[2].Raw) != `{"toolCall":{"id":"t"}}` {
		t.Errorf("raw = %+v", parts[2])
	}
}

func TestImageDataURL(t *testing.T) {
	t.Parallel()
	msgs := mustConvert(t, []*model.Message{model.NewMessage(model.RoleUser,
		model.ImageURLPart("data:image/png;base64,AQI="), model.ImageURLPart("gs://bucket/a.png"))})
	b, err := json.Marshal(msgs)
	if err != nil {
		t.Fatal(err)
	}
	// Gemini fetches only files it can access, so data: URLs go inline;
	// the media type of a file comes from its extension.
	want := `[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"AQI="}},{"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/a.png"}}]}]`
	if string(b) != want {
		t.Fatalf("parts = %s", b)
	}
}

func TestInvalidDataURLWithFileID(t *testing.T) {
	t.Parallel()
	p := model.ImageURLPart("data:invalid,private-payload")
	p.FileID = "gs://bucket/fallback.png"
	_, err := (&converter{}).mediaPart(p)
	var permanent *model.PermanentError
	if !errors.Is(err, dataurl.ErrInvalid) || !errors.As(err, &permanent) || model.IsRetryable(err) {
		t.Fatalf("error = %v; want permanent invalid-input error", err)
	}
	if strings.Contains(err.Error(), "private-payload") {
		t.Fatalf("error exposes input: %v", err)
	}
}

func TestStrictToolsUseValidatedMode(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	tools := []*model.ToolSpec{{Name: "a", Strict: true}, {Name: "b"}}
	for _, tc := range []struct {
		name   string
		choice *model.ToolChoice
		tools  []*model.ToolSpec
		want   string
	}{
		{name: "strict", tools: tools, want: modeValidated},
		{name: "strict required", tools: tools, choice: &model.ToolChoice{Mode: model.ToolChoiceRequired}, want: modeAny},
		{name: "plain", tools: tools[1:], want: ""},
		{name: "plain auto", tools: tools[1:], choice: &model.ToolChoice{Mode: model.ToolChoiceAuto}, want: modeAuto},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _, err := m.build(&model.Request{Tools: tc.tools, ToolChoice: tc.choice})
			if err != nil {
				t.Fatal(err)
			}
			var got request
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			mode := ""
			if got.ToolConfig != nil {
				mode = got.ToolConfig.FunctionCallingConfig.Mode
			}
			if mode != tc.want {
				t.Fatalf("mode = %q, body %s", mode, body)
			}
		})
	}
}

// mustConvert converts messages for a Gemini 3 model.
func mustConvert(t *testing.T, msgs []*model.Message) []content {
	t.Helper()
	out, err := (&converter{gemini3: true}).messages(msgs)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
