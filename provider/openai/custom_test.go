package openai

import (
	"encoding/json"
	"testing"

	"github.com/nostosx/agentx/model"
)

// rawEqual reports whether the JSON texts got and want are equal.
func rawEqual(t *testing.T, got json.RawMessage, want string) bool {
	t.Helper()
	var v any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatal(err)
	}
	return sameJSON(t, v, want)
}

const patchGrammar = `start: "*** Begin Patch" LF`

func freeformRequest() *model.Request {
	return &model.Request{
		Messages: []*model.Message{
			model.UserText("edit"),
			model.NewMessage(model.RoleAssistant,
				model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "call_p", Name: "apply_patch", Arguments: json.RawMessage(`{"input":"*** Begin Patch\n*** End Patch"}`)}},
				model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "call_l", Name: "lookup", Arguments: json.RawMessage(`{"q":1}`)}},
				// Arguments without the text input cannot be sent as a
				// custom call.
				model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "call_bad", Name: "apply_patch", Arguments: json.RawMessage(`{"patch":"x"}`)}},
			),
			model.ToolResults(
				&model.ToolResult{CallID: "call_p", Content: "Success."},
				&model.ToolResult{CallID: "call_l", Content: "found"},
				&model.ToolResult{CallID: "call_bad", Content: "missing input", IsError: true},
			),
		},
		Tools: []*model.ToolSpec{
			{Name: "apply_patch", Description: "patch", Parameters: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`), Grammar: &model.ToolGrammar{Syntax: "lark", Definition: patchGrammar}},
			{Name: "notes", Grammar: &model.ToolGrammar{Syntax: "regex"}},
			{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`), Strict: true},
		},
		ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceTool, Name: "apply_patch"},
	}
}

func TestResponsesFreeformTools(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := m.buildResponses(freeformRequest(), false)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tools      []json.RawMessage `json:"tools"`
		ToolChoice json.RawMessage   `json:"tool_choice"`
		Input      []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	wantTools := []string{
		`{"type":"custom","name":"apply_patch","description":"patch","format":{"type":"grammar","syntax":"lark","definition":"start: \"*** Begin Patch\" LF"}}`,
		`{"type":"custom","name":"notes"}`,
		`{"type":"function","name":"lookup","parameters":{"type":"object"},"strict":true}`,
	}
	if len(got.Tools) != len(wantTools) {
		t.Fatalf("tools = %s, want %d tools", got.Tools, len(wantTools))
	}
	for i, want := range wantTools {
		if !rawEqual(t, got.Tools[i], want) {
			t.Errorf("tool %d = %s, want %s", i, got.Tools[i], want)
		}
	}
	if !rawEqual(t, got.ToolChoice, `{"type":"custom","name":"apply_patch"}`) {
		t.Errorf("tool_choice = %s", got.ToolChoice)
	}
	wantInput := []string{
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"edit"}]}`,
		`{"type":"custom_tool_call","call_id":"call_p","name":"apply_patch","input":"*** Begin Patch\n*** End Patch"}`,
		`{"type":"function_call","call_id":"call_l","name":"lookup","arguments":"{\"q\":1}"}`,
		`{"type":"function_call","call_id":"call_bad","name":"apply_patch","arguments":"{\"patch\":\"x\"}"}`,
		`{"type":"custom_tool_call_output","call_id":"call_p","output":"Success."}`,
		`{"type":"function_call_output","call_id":"call_l","output":"found"}`,
		`{"type":"function_call_output","call_id":"call_bad","output":"Error: missing input"}`,
	}
	if len(got.Input) != len(wantInput) {
		t.Fatalf("input = %s, want %d items", got.Input, len(wantInput))
	}
	for i, want := range wantInput {
		if !rawEqual(t, got.Input[i], want) {
			t.Errorf("input %d = %s, want %s", i, got.Input[i], want)
		}
	}
}

func TestResponsesFreeformToolsDisabled(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"), WithFreeformTools(false))
	if err != nil {
		t.Fatal(err)
	}
	body, err := m.buildResponses(freeformRequest(), false)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tools      []map[string]any  `json:"tools"`
		ToolChoice map[string]any    `json:"tool_choice"`
		Input      []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 3 || len(got.Input) != 7 {
		t.Fatalf("body = %s, want 3 tools and 7 input items", body)
	}
	for i, tool := range got.Tools {
		if tool["type"] != "function" {
			t.Errorf("tool %d = %v, want function", i, tool)
		}
	}
	if got.Tools[0]["parameters"] == nil || got.ToolChoice["type"] != "function" {
		t.Fatalf("body = %s", body)
	}
	if !rawEqual(t, got.Input[1], `{"type":"function_call","call_id":"call_p","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\\n*** End Patch\"}"}`) {
		t.Fatalf("call = %s", got.Input[1])
	}
}

const customCallJSON = `{"type":"custom_tool_call","id":"ctc_1","status":"completed","call_id":"call_p","name":"apply_patch","input":"*** Begin Patch\n*** Delete File: a\n*** End Patch"}`

func checkCustomCall(t *testing.T, resp *model.Response) {
	t.Helper()
	calls := resp.Message.ToolCalls()
	if resp.FinishReason != model.FinishToolCalls || len(calls) != 1 {
		t.Fatalf("response = %+v", resp)
	}
	var args map[string]string
	if err := json.Unmarshal(calls[0].Arguments, &args); err != nil {
		t.Fatal(err)
	}
	if calls[0].ID != "call_p" || calls[0].Name != "apply_patch" || args[model.FreeformInput] != "*** Begin Patch\n*** Delete File: a\n*** End Patch" {
		t.Fatalf("call = %+v (%s)", calls[0], calls[0].Arguments)
	}
}

func TestResponsesCustomToolCall(t *testing.T) {
	t.Parallel()
	var r responsesResponse
	if err := json.Unmarshal([]byte(`{"id":"r","status":"completed","output":[`+customCallJSON+`]}`), &r); err != nil {
		t.Fatal(err)
	}
	resp, err := r.toModel()
	if err != nil {
		t.Fatal(err)
	}
	checkCustomCall(t, resp)

	resp, err = readEvents(
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","call_id":"call_p","name":"apply_patch","input":""}}`,
		`{"type":"response.custom_tool_call_input.delta","output_index":0,"delta":"*** Begin Patch\n"}`,
		`{"type":"response.custom_tool_call_input.done","output_index":0,"input":"*** Begin Patch\n*** Delete File: a\n*** End Patch"}`,
		`{"type":"response.output_item.done","output_index":0,"item":`+customCallJSON+`}`,
		`{"type":"response.completed","response":{"id":"r","status":"completed"}}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	checkCustomCall(t, resp)

	// The call goes back as it came.
	m, err := New(WithAPIKey("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	req := freeformRequest()
	req.Messages = []*model.Message{resp.Message}
	body, err := m.buildResponses(req, false)
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Input) != 1 {
		t.Fatalf("sent input = %s, want one custom tool call", sent.Input)
	}
	if !rawEqual(t, sent.Input[0], `{"type":"custom_tool_call","call_id":"call_p","name":"apply_patch","input":"*** Begin Patch\n*** Delete File: a\n*** End Patch"}`) {
		t.Fatalf("sent %s", sent.Input[0])
	}
}
