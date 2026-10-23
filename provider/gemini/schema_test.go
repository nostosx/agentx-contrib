package gemini

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nostosx/agentx/model"
)

const constrained = `{"type":"object","properties":{` +
	`"name":{"type":"string","minLength":2,"pattern":"^\\w+$","description":"Name."},` +
	`"count":{"type":"integer","minimum":1,"maximum":9},"pair":{"type":"array","items":{"type":"string"},"minItems":2,"maxItems":2}},` +
	`"required":["count","name","pair"],"additionalProperties":false}`

// captureBody serves one generateContent call and returns the request body.
func captureBody(t *testing.T, name string, req *model.Request) map[string]any {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = io.WriteString(w, `{"responseId":"r1","candidates":[{"content":{"parts":[{"text":"{}"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`)
	}))
	defer srv.Close()
	m, err := New(WithAPIKey("fixture"), WithModel(name), WithBaseURL(srv.URL+"/v1beta"), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Generate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	return body
}

func checkAdapted(t *testing.T, what string, schema any) {
	t.Helper()
	props := schema.(map[string]any)["properties"].(map[string]any)
	name, count, pair := props["name"].(map[string]any), props["count"].(map[string]any), props["pair"].(map[string]any)
	if _, ok := name["minLength"]; ok || name["description"] != "Name.\n\n{minLength: 2, pattern: \"^\\\\w+$\"}" {
		t.Errorf("%s: name = %v", what, name)
	}
	// Numeric and array constraints are supported.
	if count["minimum"] != 1.0 || count["maximum"] != 9.0 || pair["minItems"] != 2.0 || pair["maxItems"] != 2.0 {
		t.Errorf("%s: count = %v, pair = %v", what, count, pair)
	}
}

func TestResponseSchemaIsAdapted(t *testing.T) {
	t.Parallel()
	req := &model.Request{
		Messages:       []*model.Message{model.UserText("hi")},
		Tools:          []*model.ToolSpec{{Name: "strict", Parameters: json.RawMessage(constrained), Strict: true}, {Name: "loose", Parameters: json.RawMessage(constrained)}},
		ResponseFormat: &model.ResponseFormat{Name: "out", Schema: json.RawMessage(constrained), Strict: true},
	}
	body := captureBody(t, "gemini-3.8-flash", req)
	gc := body["generationConfig"].(map[string]any)
	if gc["responseMimeType"] != "application/json" {
		t.Fatalf("generationConfig = %v", gc)
	}
	checkAdapted(t, "response schema", gc["responseJsonSchema"])
	decls := body["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)
	checkAdapted(t, "strict function", decls[0].(map[string]any)["parametersJsonSchema"])
	loose := decls[1].(map[string]any)["parametersJsonSchema"].(map[string]any)["properties"].(map[string]any)["name"].(map[string]any)
	if loose["minLength"] != 2.0 {
		t.Errorf("loose function = %v", loose)
	}
	if _, ok := body["systemInstruction"]; ok {
		t.Errorf("systemInstruction = %v", body["systemInstruction"])
	}
}

// Gemini 2.5 cannot combine a response schema with functions, so the
// schema becomes an instruction; without functions it is sent as usual.
func TestResponseSchemaWithToolsOnGemini25(t *testing.T) {
	t.Parallel()
	req := &model.Request{
		System:         "rules",
		Messages:       []*model.Message{model.UserText("hi")},
		Tools:          []*model.ToolSpec{{Name: "lookup"}},
		ResponseFormat: &model.ResponseFormat{Name: "out", Schema: json.RawMessage(constrained), Strict: true},
	}
	body := captureBody(t, "gemini-2.5-flash", req)
	gc := body["generationConfig"].(map[string]any)
	if _, ok := gc["responseJsonSchema"]; ok || gc["responseMimeType"] != nil {
		t.Errorf("generationConfig = %v", gc)
	}
	text := body["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "rules\n\n") || !strings.Contains(text, `"minLength":2`) {
		t.Errorf("system instruction = %q", text)
	}

	req.Tools = nil
	body = captureBody(t, "models/gemini-2.5-flash", req)
	gc = body["generationConfig"].(map[string]any)
	if gc["responseMimeType"] != "application/json" || gc["responseJsonSchema"] == nil {
		t.Errorf("generationConfig = %v", gc)
	}
}
