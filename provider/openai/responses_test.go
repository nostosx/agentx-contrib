package openai

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nostosx/agentx/model"
)

const reasoningItem = `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"plan"},{"type":"summary_text","text":"check"}],"encrypted_content":"ENC"}`

func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestBuildResponses(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"), WithMaxTokens(32))
	if err != nil {
		t.Fatal(err)
	}
	temp := 0.2
	req := &model.Request{
		System: "rules",
		Messages: []*model.Message{
			model.SystemText("extra"),
			model.NewMessage(model.RoleUser, model.TextPart("look"), model.ImagePart("image/png", []byte{1})),
			model.NewMessage(model.RoleAssistant,
				model.Part{Type: model.PartReasoning, Text: "plan", Provider: providerName, Raw: json.RawMessage(reasoningItem)},
				// Stateless requests cannot refer to reasoning without its
				// encrypted content.
				model.Part{Type: model.PartReasoning, Text: "old", Provider: providerName, Raw: json.RawMessage(`{"type":"reasoning","id":"rs_0","summary":[]}`)},
				model.Part{Type: model.PartReasoning, Text: "claude", Signature: "anthropic-sig"},
				model.Part{Type: model.PartRaw, Provider: "anthropic", Raw: json.RawMessage(`{"type":"server_tool_use"}`)},
				model.Part{Type: model.PartText, Text: "checking", Provider: providerName, Raw: json.RawMessage(`{"phase":"commentary"}`)},
				model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{"q":1}`)}},
			),
			model.ToolResults(&model.ToolResult{CallID: "call_1", Content: "boom", IsError: true}),
		},
		Tools:           []*model.ToolSpec{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice:      &model.ToolChoice{Mode: model.ToolChoiceTool, Name: "lookup"},
		Temperature:     &temp,
		Reasoning:       &model.Reasoning{Effort: "high"},
		ResponseFormat:  &model.ResponseFormat{Schema: json.RawMessage(`{"type":"object"}`), Strict: true},
		ProviderOptions: map[string]map[string]any{"openai": {"parallel_tool_calls": false}},
	}
	body, err := m.buildResponses(req, true)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeBody(t, body)
	if got["model"] != DefaultModel || got["instructions"] != "rules" || got["max_output_tokens"] != float64(32) || got["store"] != false || got["stream"] != true || got["parallel_tool_calls"] != false {
		t.Fatalf("body = %s", body)
	}
	if _, ok := got["temperature"]; ok {
		t.Error("temperature sent with reasoning effort")
	}
	// Stateless responses carry encrypted reasoning without include.
	if inc, ok := got["include"]; ok {
		t.Errorf("include = %v", inc)
	}
	if r := got["reasoning"].(map[string]any); r["effort"] != "high" || r["summary"] != "auto" {
		t.Errorf("reasoning = %v", r)
	}
	if tool := got["tools"].([]any)[0].(map[string]any); tool["type"] != "function" || tool["name"] != "lookup" || tool["strict"] != false {
		t.Errorf("tool = %v", tool)
	}
	if tc := got["tool_choice"].(map[string]any); tc["type"] != "function" || tc["name"] != "lookup" {
		t.Errorf("tool_choice = %v", tc)
	}
	if f := got["text"].(map[string]any)["format"].(map[string]any); f["type"] != "json_schema" || f["name"] != "response" || f["strict"] != true {
		t.Errorf("format = %v", f)
	}

	input := got["input"].([]any)
	var types []string
	for _, it := range input {
		item := it.(map[string]any)
		types = append(types, item["type"].(string)+"/"+str(item["role"]))
	}
	want := []string{"message/system", "message/user", "reasoning/", "message/assistant", "function_call/", "function_call_output/"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("input items = %v", types)
	}
	user := input[1].(map[string]any)["content"].([]any)
	if user[0].(map[string]any)["type"] != "input_text" || !strings.HasPrefix(user[1].(map[string]any)["image_url"].(string), "data:image/png;base64,") {
		t.Errorf("user content = %v", user)
	}
	if !sameJSON(t, input[2], reasoningItem) {
		t.Errorf("reasoning item not sent verbatim: %v", input[2])
	}
	if a := input[3].(map[string]any); a["content"] != "checking" || a["phase"] != "commentary" {
		t.Errorf("assistant = %v", a)
	}
	if fc := input[4].(map[string]any); fc["call_id"] != "call_1" || fc["arguments"] != `{"q":1}` {
		t.Errorf("function call = %v", fc)
	}
	if out := input[5].(map[string]any); out["call_id"] != "call_1" || out["output"] != "Error: boom" {
		t.Errorf("function output = %v", out)
	}

	if _, err := m.buildResponses(&model.Request{Stop: []string{"x"}}, false); err == nil {
		t.Error("stop sequences accepted")
	}
	body, err = m.buildResponses(&model.Request{Temperature: &temp, Reasoning: &model.Reasoning{Effort: "none", Display: "omitted"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeBody(t, body); got["temperature"] != 0.2 || got["reasoning"].(map[string]any)["summary"] != nil || len(got["input"].([]any)) != 0 {
		t.Errorf("effort none body = %s", body)
	}
}

func TestResponsesInclude(t *testing.T) {
	t.Parallel()
	reasoning := &model.Reasoning{Effort: "low"}
	for _, tc := range []struct {
		name      string
		opts      []Option
		model     string
		reasoning *model.Reasoning
		extra     map[string]any
		want      []any // nil: no include field
	}{
		// Stateless responses include encrypted reasoning by default.
		{name: "default model"},
		{name: "default model with reasoning", reasoning: reasoning},
		{name: "gpt-4o", model: "gpt-4o"},
		{name: "deployment with reasoning", model: "my-deployment", reasoning: reasoning},
		{name: "disabled", opts: []Option{WithEncryptedReasoning(false)}, reasoning: reasoning},
		// The legacy include value is still accepted when enabled.
		{name: "enabled", opts: []Option{WithEncryptedReasoning(true)}, model: "my-deployment", want: []any{includeReasoning}},
		// An include from Extra is merged, not replaced.
		{name: "extra merged", opts: []Option{WithEncryptedReasoning(true)}, extra: map[string]any{"include": []string{"message.output_text.logprobs"}}, want: []any{"message.output_text.logprobs", includeReasoning}},
		{name: "extra any list", opts: []Option{WithEncryptedReasoning(true)}, extra: map[string]any{"include": []any{"file_search_call.results", includeReasoning}}, want: []any{"file_search_call.results", includeReasoning}},
		{name: "extra kept", model: "gpt-4o", extra: map[string]any{"include": []string{"file_search_call.results"}}, want: []any{"file_search_call.results"}},
		{name: "extra removed when enabled", opts: []Option{WithEncryptedReasoning(true)}, extra: map[string]any{"include": nil}},
		{name: "extra removed", extra: map[string]any{"include": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := New(append([]Option{WithAPIKey("fixture")}, tc.opts...)...)
			if err != nil {
				t.Fatal(err)
			}
			req := &model.Request{Model: tc.model, Reasoning: tc.reasoning, ProviderOptions: map[string]map[string]any{"openai": tc.extra}}
			extra := maps.Clone(tc.extra)
			body, err := m.buildResponses(req, false)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := decodeBody(t, body)["include"]
			if (tc.want == nil && ok) || (tc.want != nil && !reflect.DeepEqual(got, tc.want)) {
				t.Fatalf("include = %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(req.ProviderOptions["openai"], extra) {
				t.Errorf("request provider options modified: %v", req.ProviderOptions)
			}
		})
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// sameJSON reports whether v, a decoded JSON value, equals the JSON text want.
func sameJSON(t *testing.T, v any, want string) bool {
	t.Helper()
	var w any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(v, w)
}

const responseOutput = `{"id":"resp_1","model":"gpt-fixture","status":"completed","output":[` +
	reasoningItem + `,` +
	`{"type":"message","id":"msg_1","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"checking","annotations":[]}]},` +
	`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"q\": 1}"},` +
	`{"type":"web_search_call","id":"ws_1","status":"completed"}` +
	`],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":60},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":12}}}`

func checkResponse(t *testing.T, resp *model.Response) {
	t.Helper()
	parts := resp.Message.Parts
	if resp.ID != "resp_1" || resp.Model != "gpt-fixture" || resp.FinishReason != model.FinishToolCalls || len(parts) != 4 {
		t.Fatalf("response = %+v", resp)
	}
	if parts[0].Type != model.PartReasoning || parts[0].Text != "plan\n\ncheck" || parts[0].Provider != providerName || !strings.Contains(string(parts[0].Raw), `"encrypted_content":"ENC"`) {
		t.Errorf("reasoning = %+v", parts[0])
	}
	if parts[1].Text != "checking" || string(parts[1].Raw) != `{"phase":"commentary"}` {
		t.Errorf("text = %+v", parts[1])
	}
	if tc := parts[2].ToolCall; tc == nil || tc.ID != "call_1" || tc.Name != "lookup" || string(tc.Arguments) != `{"q":1}` {
		t.Errorf("call = %+v", parts[2].ToolCall)
	}
	if parts[3].Type != model.PartRaw || !strings.Contains(string(parts[3].Raw), "web_search_call") {
		t.Errorf("raw = %+v", parts[3])
	}
	if u := resp.Usage; u.InputTokens != 40 || u.CacheReadTokens != 60 || u.OutputTokens != 20 || u.ReasoningTokens != 12 || u.PromptTokens() != 100 {
		t.Errorf("usage = %+v", u)
	}
	// The response converts back to the items OpenAI produced.
	input, err := (&Model{}).convertInput([]*model.Message{resp.Message}, nil)
	if err != nil || len(input) != 4 || string(input[0]) != reasoningItem || !strings.Contains(string(input[1]), `"phase":"commentary"`) || !strings.Contains(string(input[3]), "ws_1") {
		t.Errorf("replay = %s", input)
	}
}

func TestResponsesGenerateAndStream(t *testing.T) {
	t.Parallel()
	events := []string{
		`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"pl"}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"an"}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":1,"delta":"check"}`,
		`{"type":"response.output_item.done","output_index":0,"item":` + reasoningItem + `}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","phase":"commentary","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"check"}`,
		`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"ing"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"checking"}]}}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"q\": "}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"1}"}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"q\": 1}"}}`,
		`{"type":"response.output_item.done","output_index":3,"item":{"type":"web_search_call","id":"ws_1","status":"completed"}}`,
		`{"type":"response.completed","response":` + responseOutput + `}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Errorf("unexpected request %s %v", r.URL, r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		// Like OpenAI, return encrypted reasoning in stateless mode,
		// without include; checkResponse then fails without it.
		if _, ok := body["include"]; ok {
			t.Errorf("include = %v", body["include"])
		}
		encrypted := body["store"] == false
		reply := func(s string) []byte {
			if !encrypted {
				s = strings.ReplaceAll(s, `,"encrypted_content":"ENC"`, "")
			}
			return []byte(s)
		}
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, ev := range events {
				var typ struct {
					Type string `json:"type"`
				}
				if err := json.Unmarshal([]byte(ev), &typ); err != nil {
					t.Error(err)
					return
				}
				if _, err := w.Write(reply("event: " + typ.Type + "\ndata: " + ev + "\n\n")); err != nil {
					t.Error(err)
				}
			}
			return
		}
		if _, err := w.Write(reply(responseOutput)); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	m, err := New(WithAPIKey("fixture"), WithBaseURL(srv.URL+"/v1"), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	req := &model.Request{Messages: []*model.Message{model.UserText("hi")}}
	resp, err := m.Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	checkResponse(t, resp)

	var deltas strings.Builder
	resp, err = model.Collect(func(yield func(*model.StreamEvent, error) bool) {
		for ev, err := range m.Stream(t.Context(), req) {
			if ev != nil && ev.Delta != nil && ev.Delta.Type == model.PartText {
				deltas.WriteString(ev.Delta.Text)
			}
			if !yield(ev, err) {
				return
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	checkResponse(t, resp)
	if deltas.String() != "checking" {
		t.Errorf("text deltas = %q", deltas.String())
	}
}

func readEvents(events ...string) (*model.Response, error) {
	var b strings.Builder
	for _, ev := range events {
		b.WriteString("data: " + ev + "\n\n")
	}
	return model.Collect(func(yield func(*model.StreamEvent, error) bool) {
		readResponsesStream(strings.NewReader(b.String()), yield)
	})
}

func TestResponsesStreamOutcomes(t *testing.T) {
	t.Parallel()
	resp, err := readEvents(
		`{"type":"response.output_text.delta","output_index":0,"delta":"partial"}`,
		`{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`,
	)
	if err != nil || resp.FinishReason != model.FinishLength || resp.Text() != "partial" {
		t.Fatalf("incomplete = %+v, %v", resp, err)
	}
	resp, err = readEvents(
		`{"type":"response.refusal.delta","output_index":0,"delta":"no"}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)
	if err != nil || resp.FinishReason != model.FinishRefusal || resp.Text() != "no" {
		t.Fatalf("refusal = %+v, %v", resp, err)
	}
	if _, err := readEvents(`{"type":"response.output_text.delta","output_index":0,"delta":"cut"}`); err == nil {
		t.Fatal("truncated stream accepted")
	}

	if _, err := readEvents(`{"type":"response.output_text.delta","output_index":0,"delta":"cut"}`); !model.IsRetryable(err) {
		t.Fatalf("truncated stream not retryable: %v", err)
	}

	_, err = readEvents(`{"type":"error","code":"rate_limit_exceeded","message":"slow down"}`)
	var api *model.APIError
	if !errors.As(err, &api) || api.StatusCode != http.StatusTooManyRequests || !model.IsRetryable(err) {
		t.Fatalf("rate limit error = %v", err)
	}
	for _, tc := range []struct {
		name, event, message string
		status               int
	}{
		{name: "nested", event: `{"type":"error","error":{"type":"invalid_request_error","code":"insufficient_quota","message":"pay"}}`, message: "pay", status: http.StatusTooManyRequests},
		{name: "nested server", event: `{"type":"error","error":{"type":"server_error","code":null,"message":"oops"}}`, message: "oops", status: http.StatusInternalServerError},
		{name: "unknown code", event: `{"type":"error","code":"ERR_SOMETHING","message":"odd","sequence_number":3}`, message: "odd", status: http.StatusBadRequest},
		// Without code and type the failure is most likely the server's.
		{name: "empty", event: `{"type":"error"}`, status: http.StatusInternalServerError},
		{name: "message only", event: `{"type":"error","error":{"message":"oops"}}`, message: "oops", status: http.StatusInternalServerError},
		// Compatible services may send the HTTP status as a numeric code.
		{name: "numeric code", event: `{"type":"error","error":{"code":502,"message":"upstream"}}`, message: "upstream", status: http.StatusBadGateway},
		{name: "numeric client code", event: `{"type":"error","code":400,"message":"bad"}`, message: "bad", status: http.StatusBadRequest},
		{name: "failed without error", event: `{"type":"response.failed","response":{"status":"failed"}}`, message: "response failed", status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readEvents(tc.event)
			var api *model.APIError
			if !errors.As(err, &api) || api.StatusCode != tc.status || api.Message != tc.message || model.IsRetryable(err) != (tc.status >= 500) {
				t.Fatalf("error = %+v", err)
			}
		})
	}
	_, err = readEvents(`{"type":"response.failed","response":{"status":"failed","error":{"code":"invalid_prompt","message":"bad"}}}`)
	if !errors.As(err, &api) || api.StatusCode != http.StatusBadRequest || model.IsRetryable(err) || api.Type != "invalid_prompt" {
		t.Fatalf("failed response = %v", err)
	}
	_, err = readEvents(`{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"oops"}}}`)
	if !model.IsRetryable(err) {
		t.Fatalf("server error not retryable: %v", err)
	}
}

func TestQuotaErrorsAreNotRetried(t *testing.T) {
	t.Parallel()
	for typ, want := range map[string]bool{"insufficient_quota": false, "credit_balance_exhausted": false, "rate_limit_exceeded": true, "": true} {
		err := &model.APIError{Provider: providerName, StatusCode: http.StatusTooManyRequests, Type: typ}
		if got := err.Retryable(); got != want {
			t.Errorf("%q: Retryable = %v, want %v", typ, got, want)
		}
	}
}

func TestResponsesFailedGenerate(t *testing.T) {
	t.Parallel()
	var r responsesResponse
	if err := json.Unmarshal([]byte(`{"status":"failed","error":{"code":"server_error","message":"x"}}`), &r); err != nil {
		t.Fatal(err)
	}
	if _, err := r.toModel(); !model.IsRetryable(err) {
		t.Fatalf("error = %v", err)
	}
	r = responsesResponse{}
	if err := json.Unmarshal([]byte(`{"status":"failed","error":null}`), &r); err != nil {
		t.Fatal(err)
	}
	if _, err := r.toModel(); !model.IsRetryable(err) {
		t.Fatalf("failed without details = %v, want retryable", err)
	}
	if err := json.Unmarshal([]byte(`{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}`), &r); err != nil {
		t.Fatal(err)
	}
	if resp, err := r.toModel(); err != nil || resp.FinishReason != model.FinishContentFilter {
		t.Fatalf("content filter = %+v, %v", resp, err)
	}
}
