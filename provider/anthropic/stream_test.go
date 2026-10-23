package anthropic

import (
	"errors"
	"strings"
	"testing"

	"github.com/nostosx/agentx/model"
)

func TestReadStreamToolAndRawBlocks(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"type":"message_start","message":{"id":"r","usage":{"input_tokens":10,"cache_read_input_tokens":3}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"c","name":"lookup","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":1}"}}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"server_tool_use","id":"s","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"go\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`,
		`{"type":"message_stop"}`,
	}
	input := "data: " + strings.Join(chunks, "\n\ndata: ") + "\n\n"
	resp, err := model.Collect(func(yield func(*model.StreamEvent, error) bool) { readStream(strings.NewReader(input), yield) })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.TotalTokens() != 17 || resp.Message.Parts[0].Signature != "sig" || resp.Message.Parts[0].Provider != providerName || string(resp.ToolCalls()[0].Arguments) != `{"q":1}` || !strings.Contains(string(resp.Message.Parts[2].Raw), `"query":"go"`) {
		t.Fatalf("response = %+v", resp)
	}
}

func TestReadStreamFailures(t *testing.T) {
	t.Parallel()
	errorEvent := func(typ string) string {
		return "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"" + typ + "\",\"message\":\"m\"}}\n\n"
	}
	for _, tc := range []struct {
		name, input string
		status      int // 0 for errors that are not API errors
		retryable   bool
	}{
		{name: "missing stop", input: "data: {\"type\":\"ping\"}\n\n", retryable: true},
		{name: "invalid json", input: "data: broken\n\n"},
		{name: "overload", input: errorEvent("overloaded_error"), status: 529, retryable: true},
		{name: "invalid request", input: errorEvent("invalid_request_error"), status: 400},
		{name: "authentication", input: errorEvent("authentication_error"), status: 401},
		{name: "billing", input: errorEvent("billing_error"), status: 402},
		{name: "permission", input: errorEvent("permission_error"), status: 403},
		{name: "not found", input: errorEvent("not_found_error"), status: 404},
		{name: "too large", input: errorEvent("request_too_large"), status: 413},
		{name: "rate limit", input: errorEvent("rate_limit_error"), status: 429, retryable: true},
		{name: "api", input: errorEvent("api_error"), status: 500, retryable: true},
		{name: "timeout", input: errorEvent("timeout_error"), status: 504, retryable: true},
		{name: "unknown", input: errorEvent("new_error"), status: 500, retryable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := model.Collect(func(yield func(*model.StreamEvent, error) bool) { readStream(strings.NewReader(tc.input), yield) })
			if err == nil {
				t.Fatal("expected stream error")
			}
			var api *model.APIError
			if errors.As(err, &api) != (tc.status != 0) || (api != nil && api.StatusCode != tc.status) || model.IsRetryable(err) != tc.retryable {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
