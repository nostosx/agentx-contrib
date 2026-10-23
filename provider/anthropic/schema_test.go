package anthropic

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nostosx/agentx/model"
)

// captureBody serves one Messages call and returns the request body.
func captureBody(t *testing.T, name string, req *model.Request) map[string]any {
	t.Helper()
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		if body, err = io.ReadAll(r.Body); err != nil {
			t.Error(err)
		}
		_, _ = io.WriteString(w, `{"id":"r1","model":"m","content":[{"type":"text","text":"{}"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()
	m, err := New(WithAPIKey("fixture"), WithModel(name), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Generate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

const constrained = `{"type":"object","properties":{` +
	`"count":{"type":"integer","minimum":1,"maximum":9,"description":"How many."},` +
	`"name":{"type":"string","minLength":2,"pattern":"^\\w+$"},` +
	`"word":{"type":"string","pattern":"\\bx"},` +
	`"data":{"type":"string","format":"byte"},` +
	`"when":{"type":"string","format":"date-time"},` +
	`"pair":{"type":"array","items":{"type":"string"},"minItems":2,"maxItems":2},` +
	`"some":{"type":"array","items":{"type":"string"},"minItems":1}},` +
	`"required":["count","data","name","pair","some","when","word"],"additionalProperties":false}`

func TestStructuredOutputSchemaIsAdapted(t *testing.T) {
	t.Parallel()
	req := &model.Request{
		Messages:       []*model.Message{model.UserText("hi")},
		Tools:          []*model.ToolSpec{{Name: "strict", Parameters: json.RawMessage(constrained), Strict: true}, {Name: "loose", Parameters: json.RawMessage(constrained)}},
		ResponseFormat: &model.ResponseFormat{Name: "out", Schema: json.RawMessage(constrained), Strict: true},
	}
	body := captureBody(t, "claude-opus-5-5", req)
	format := body["output_config"].(map[string]any)["format"].(map[string]any)
	tools := body["tools"].([]any)
	for name, schema := range map[string]any{"format": format["schema"], "strict tool": tools[0].(map[string]any)["input_schema"]} {
		props := schema.(map[string]any)["properties"].(map[string]any)
		count, nm, word, data := props["count"].(map[string]any), props["name"].(map[string]any), props["word"].(map[string]any), props["data"].(map[string]any)
		pair, some, when := props["pair"].(map[string]any), props["some"].(map[string]any), props["when"].(map[string]any)
		if _, ok := count["minimum"]; ok || count["description"] != "How many.\n\n{minimum: 1, maximum: 9}" {
			t.Errorf("%s: count = %v", name, count)
		}
		if _, ok := nm["minLength"]; ok || nm["pattern"] != `^\w+$` || nm["description"] != "{minLength: 2}" {
			t.Errorf("%s: name = %v", name, nm)
		}
		if _, ok := word["pattern"]; ok || word["description"] != `{pattern: "\\bx"}` {
			t.Errorf("%s: word = %v", name, word)
		}
		if _, ok := data["format"]; ok || when["format"] != "date-time" {
			t.Errorf("%s: data = %v, when = %v", name, data, when)
		}
		if _, ok := pair["minItems"]; ok || pair["description"] != "{minItems: 2, maxItems: 2}" || some["minItems"] != 1.0 {
			t.Errorf("%s: pair = %v, some = %v", name, pair, some)
		}
	}
	// Tools without strict mode are not constrained, so their schemas stay.
	loose := tools[1].(map[string]any)["input_schema"].(map[string]any)["properties"].(map[string]any)
	if loose["count"].(map[string]any)["minimum"] != 1.0 {
		t.Errorf("loose tool = %v", loose)
	}
	if _, ok := body["system"]; ok {
		t.Errorf("system = %v", body["system"])
	}
}

func TestStructuredOutputFallbackOnOlderModels(t *testing.T) {
	t.Parallel()
	req := &model.Request{
		System:         "rules",
		Messages:       []*model.Message{model.UserText("hi")},
		ResponseFormat: &model.ResponseFormat{Name: "out", Schema: json.RawMessage(constrained), Strict: true},
	}
	body := captureBody(t, "claude-sonnet-4-20250514", req)
	if _, ok := body["output_config"]; ok {
		t.Errorf("output_config = %v", body["output_config"])
	}
	system, _ := body["system"].(string)
	// Without constrained decoding the model sees the full schema.
	if !strings.HasPrefix(system, "rules\n\n") || !strings.Contains(system, `"minimum":1`) {
		t.Errorf("system = %q", system)
	}
}

func TestJSONOutputs(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"claude-opus-5-5":                           true,
		"claude-sonnet-4-5-20250929":                true,
		"claude-haiku-4-5":                          true,
		"us.anthropic.claude-opus-4-6-v1:0":         true,
		"claude-fable-5":                            true,
		"claude-mythos-preview":                     true,
		"claude-sonnet-4-20250514":                  false,
		"claude-opus-4-1":                           false,
		"anthropic.claude-3-7-sonnet-20250219-v1:0": false,
		"claude-3-5-haiku-latest":                   false,
		"my-gateway-alias":                          true,
	} {
		if got := jsonOutputs(name); got != want {
			t.Errorf("jsonOutputs(%q) = %v, want %v", name, got, want)
		}
	}
}
