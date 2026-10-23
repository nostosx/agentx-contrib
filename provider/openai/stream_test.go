package openai

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/nostosx/agentx/model"
)

func TestReadStreamToolArguments(t *testing.T) {
	t.Parallel()
	input := "data: " + `{"choices":[{"delta":{"reasoning_content":"think","tool_calls":[{"index":0,"id":"c","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}` + "\n\ndata: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"go\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
	resp, err := model.Collect(func(yield func(*model.StreamEvent, error) bool) { readStream(strings.NewReader(input), yield) })
	if err != nil || resp.FinishReason != model.FinishToolCalls || len(resp.ToolCalls()) != 1 || string(resp.ToolCalls()[0].Arguments) != `{"q":"go"}` || resp.Message.Parts[0].Text != "think" {
		t.Fatalf("response = %+v, %v", resp, err)
	}
}

func TestReadStreamToolCallsWithoutIndex(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"choices":[{"delta":{"tool_calls":[{"id":"a","function":{"name":"first","arguments":"{\"n\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"1}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"id":"b","function":{"name":"second","arguments":"{}"}},{"id":"c","function":{"name":"third","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	input := "data: " + strings.Join(chunks, "\n\ndata: ") + "\n\ndata: [DONE]\n\n"
	resp, err := model.Collect(func(yield func(*model.StreamEvent, error) bool) { readStream(strings.NewReader(input), yield) })
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tc := range resp.ToolCalls() {
		got = append(got, tc.ID+":"+tc.Name+":"+string(tc.Arguments))
	}
	if strings.Join(got, ",") != `a:first:{"n":1},b:second:{},c:third:{}` {
		t.Fatalf("tool calls = %v", got)
	}
}

func TestReadStreamErrorChunks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, chunk string
		status      int
	}{
		{name: "message only", chunk: `{"error":{"message":"upstream failed"}}`, status: http.StatusInternalServerError},
		{name: "numeric code", chunk: `{"error":{"code":429,"message":"slow"}}`, status: http.StatusTooManyRequests},
		{name: "numeric client code", chunk: `{"error":{"code":400,"type":"BadRequestError","message":"bad"}}`, status: http.StatusBadRequest},
		{name: "invalid request", chunk: `{"error":{"type":"invalid_request_error","message":"bad"}}`, status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := model.Collect(func(yield func(*model.StreamEvent, error) bool) {
				readStream(strings.NewReader("data: "+tc.chunk+"\n\n"), yield)
			})
			api, ok := errors.AsType[*model.APIError](err)
			if !ok || api.StatusCode != tc.status || model.IsRetryable(err) != api.Retryable() {
				t.Fatalf("error = %v, want status %d", err, tc.status)
			}
		})
	}
}

func TestReadStreamFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, data string
		api        bool
	}{
		{name: "empty"}, {name: "invalid json", data: "data: broken\n\n"},
		{name: "truncated", data: "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"},
		{name: "api error", data: "data: {\"error\":{\"type\":\"overloaded\",\"message\":\"busy\"}}\n\n", api: true},
		{name: "unterminated", data: "data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":\"stop\"}]}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := model.Collect(func(yield func(*model.StreamEvent, error) bool) { readStream(strings.NewReader(tc.data), yield) })
			if err == nil {
				t.Fatal("expected stream error")
			}
			var api *model.APIError
			if errors.As(err, &api) != tc.api {
				t.Fatalf("error = %T %v", err, err)
			}
		})
	}
}
