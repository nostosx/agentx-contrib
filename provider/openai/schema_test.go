package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nostosx/agentx/model"
)

const constrained = `{"type":"object","properties":{` +
	`"name":{"type":"string","minLength":2,"maxLength":8,"pattern":"^\\w+$","description":"Name."},` +
	`"data":{"type":"string","format":"byte"},"when":{"type":"string","format":"date-time"},` +
	`"count":{"type":"integer","minimum":1,"maximum":9},"pair":{"type":"array","items":{"type":"string"},"minItems":2,"maxItems":2}},` +
	`"required":["count","data","name","pair","when"],"additionalProperties":false}`

func TestStrictSchemasAreAdapted(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		opts []Option
		// schemas returns the response format schema and the tool schemas.
		schemas func(body map[string]any) (format any, tools []any)
		reply   string
	}{
		{
			name: "chat completions",
			opts: []Option{WithChatCompletions()},
			schemas: func(body map[string]any) (any, []any) {
				var tools []any
				for _, tool := range body["tools"].([]any) {
					tools = append(tools, tool.(map[string]any)["function"].(map[string]any)["parameters"])
				}
				return body["response_format"].(map[string]any)["json_schema"].(map[string]any)["schema"], tools
			},
			reply: `{"id":"r1","choices":[{"message":{"content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		},
		{
			name: "responses",
			schemas: func(body map[string]any) (any, []any) {
				var tools []any
				for _, tool := range body["tools"].([]any) {
					tools = append(tools, tool.(map[string]any)["parameters"])
				}
				return body["text"].(map[string]any)["format"].(map[string]any)["schema"], tools
			},
			reply: `{"id":"resp_1","model":"gpt-fixture","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"{}","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				_, _ = io.WriteString(w, tc.reply)
			}))
			defer srv.Close()
			m, err := New(append([]Option{WithAPIKey("fixture"), WithBaseURL(srv.URL + "/v1"), WithHTTPClient(srv.Client())}, tc.opts...)...)
			if err != nil {
				t.Fatal(err)
			}
			req := &model.Request{
				Messages:       []*model.Message{model.UserText("hi")},
				Tools:          []*model.ToolSpec{{Name: "strict", Parameters: json.RawMessage(constrained), Strict: true}, {Name: "loose", Parameters: json.RawMessage(constrained)}},
				ResponseFormat: &model.ResponseFormat{Name: "out", Schema: json.RawMessage(constrained), Strict: true},
			}
			if _, err := m.Generate(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			format, tools := tc.schemas(body)
			for i, schema := range []any{format, tools[0]} {
				props := schema.(map[string]any)["properties"].(map[string]any)
				name, data, when := props["name"].(map[string]any), props["data"].(map[string]any), props["when"].(map[string]any)
				count, pair := props["count"].(map[string]any), props["pair"].(map[string]any)
				// String length is supported outside fine-tuned models.
				if name["minLength"] != 2.0 || name["maxLength"] != 8.0 || name["pattern"] != `^\w+$` || name["description"] != "Name." {
					t.Errorf("schema %d: name = %v", i, name)
				}
				if _, ok := data["format"]; ok || data["description"] != `{format: "byte"}` || when["format"] != "date-time" {
					t.Errorf("schema %d: data = %v, when = %v", i, data, when)
				}
				// Numeric and array constraints are supported.
				if count["minimum"] != 1.0 || count["maximum"] != 9.0 || pair["minItems"] != 2.0 || pair["maxItems"] != 2.0 {
					t.Errorf("schema %d: count = %v, pair = %v", i, count, pair)
				}
			}
			loose := tools[1].(map[string]any)["properties"].(map[string]any)["name"].(map[string]any)
			if loose["minLength"] != 2.0 {
				t.Errorf("loose tool = %v", loose)
			}
		})
	}
}

func TestNonStrictResponseFormatIsUnchanged(t *testing.T) {
	t.Parallel()
	if got := string(strictSchema(json.RawMessage(constrained), false)); got != constrained {
		t.Fatalf("schema = %s", got)
	}
}
