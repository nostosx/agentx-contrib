package bedrock

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/nostosx/agentx/model"
)

// collect runs readStream over the concatenated frames.
func collect(frames ...[]byte) (*model.Response, []*model.StreamEvent, error) {
	var events []*model.StreamEvent
	resp, err := model.Collect(func(yield func(*model.StreamEvent, error) bool) {
		readStream(bytes.NewReader(bytes.Join(frames, nil)), &model.Response{ID: "id", Model: "m", FinishReason: model.FinishUnknown},
			func(ev *model.StreamEvent, err error) bool {
				if ev != nil {
					events = append(events, ev)
				}
				return yield(ev, err)
			})
	})
	return resp, events, err
}

func TestReadStream(t *testing.T) {
	t.Parallel()
	resp, events, err := collect(
		event("messageStart", `{"role":"assistant"}`),
		event("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"reasoningContent":{"text":"let me "}}}`),
		event("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"reasoningContent":{"text":"think"}}}`),
		event("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"reasoningContent":{"signature":"sig"}}}`),
		event("contentBlockStop", `{"contentBlockIndex":0}`),
		event("contentBlockDelta", `{"contentBlockIndex":1,"delta":{"reasoningContent":{"redactedContent":"CQk="}}}`),
		event("contentBlockStop", `{"contentBlockIndex":1}`),
		event("contentBlockDelta", `{"contentBlockIndex":2,"delta":{"text":"Checking."}}`),
		event("contentBlockStop", `{"contentBlockIndex":2}`),
		event("contentBlockStart", `{"contentBlockIndex":3,"start":{"toolUse":{"toolUseId":"t1","name":"lookup"}}}`),
		event("contentBlockDelta", `{"contentBlockIndex":3,"delta":{"toolUse":{"input":"{\"q\":"}}}`),
		event("contentBlockDelta", `{"contentBlockIndex":3,"delta":{"toolUse":{"input":" 1}"}}}`),
		event("contentBlockStop", `{"contentBlockIndex":3}`),
		event("contentBlockStart", `{"contentBlockIndex":4,"start":{"toolUse":{"toolUseId":"s1","name":"search","type":"server_tool_use"}}}`),
		event("contentBlockDelta", `{"contentBlockIndex":4,"delta":{"toolUse":{"input":"{\"query\":\"go\"}"}}}`),
		event("contentBlockStop", `{"contentBlockIndex":4}`),
		event("contentBlockStart", `{"contentBlockIndex":5,"start":{"toolUse":{"toolUseId":"t2","name":"noargs"}}}`),
		event("contentBlockDelta", `{"contentBlockIndex":6,"delta":{"citation":{"title":"x"}}}`),
		frame([]byte("ignored"), stringHeader(":message-type", "ping")),
		event("unknownEvent", `{}`),
		event("messageStop", `{"stopReason":"tool_use","additionalModelResponseFields":{"x":1}}`),
		event("metadata", `{"usage":{"inputTokens":5,"outputTokens":6,"cacheReadInputTokens":7},"metrics":{"latencyMs":1},"trace":{}}`),
		// Anything after the metadata event is not read.
		[]byte("garbage"),
	)
	if err != nil {
		t.Fatal(err)
	}
	parts := resp.Message.Parts
	calls := resp.ToolCalls()
	if len(parts) != 6 || parts[0].Text != "let me think" || parts[0].Signature != "sig" || parts[0].Provider != "bedrock" ||
		!parts[1].Redacted || string(parts[1].Data) != "\t\t" || resp.Text() != "Checking." ||
		len(calls) != 2 || calls[0].ID != "t1" || calls[0].Name != "lookup" || string(calls[0].Arguments) != `{"q":1}` ||
		string(calls[1].Arguments) != `{}` ||
		parts[4].Type != model.PartRaw || string(parts[4].Raw) != `{"toolUse":{"toolUseId":"s1","name":"search","input":{"query":"go"},"type":"server_tool_use"}}` ||
		resp.FinishReason != model.FinishToolCalls || resp.ID != "id" || resp.Model != "m" ||
		resp.Usage != (model.Usage{InputTokens: 5, OutputTokens: 6, CacheReadTokens: 7}) {
		t.Fatalf("response = %+v", resp)
	}
	// Deltas carry their block's index.
	if events[0].Index != 0 || events[len(events)-2].Index != 5 || events[len(events)-1].Type != model.StreamDone {
		t.Fatalf("events = %+v", events)
	}
}

func TestReadStreamEndsAfterMessageStop(t *testing.T) {
	t.Parallel()
	resp, _, err := collect(
		event("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"hi"}}`),
		event("messageStop", `{"stopReason":"max_tokens"}`),
	)
	if err != nil || resp.Text() != "hi" || resp.FinishReason != model.FinishLength {
		t.Fatalf("response = %+v, %v", resp, err)
	}
}

func TestReadStreamFailures(t *testing.T) {
	t.Parallel()
	exception := func(typ, payload string) []byte {
		return frame([]byte(payload), stringHeader(":exception-type", typ), stringHeader(":content-type", "application/json"),
			stringHeader(":message-type", "exception"))
	}
	start := event("messageStart", `{"role":"assistant"}`)
	for _, tc := range []struct {
		name      string
		frames    [][]byte
		status    int // 0 for errors that are not API errors
		errType   string
		retryable bool
	}{
		{name: "missing stop", frames: [][]byte{start}, retryable: true},
		{name: "metadata before stop", frames: [][]byte{start, event("metadata", `{"usage":{}}`)}, retryable: true},
		{name: "invalid json", frames: [][]byte{event("contentBlockDelta", `{broken`)}},
		{name: "invalid stop", frames: [][]byte{event("messageStop", `[]`)}},
		{name: "invalid start", frames: [][]byte{event("contentBlockStart", `[]`)}},
		{name: "invalid block stop", frames: [][]byte{event("contentBlockStop", `[]`)}},
		{name: "invalid metadata", frames: [][]byte{event("metadata", `[]`)}},
		{name: "corrupt frame", frames: [][]byte{start[:len(start)-1], {0}}},
		{name: "throttling", frames: [][]byte{start, exception("throttlingException", `{"message":"slow down"}`)},
			status: 429, errType: "ThrottlingException", retryable: true},
		{name: "validation", frames: [][]byte{exception("validationException", `{"message":"bad"}`)},
			status: 400, errType: "ValidationException"},
		{name: "internal", frames: [][]byte{exception("internalServerException", `{"message":"oops"}`)},
			status: 500, errType: "InternalServerException", retryable: true},
		{name: "unavailable", frames: [][]byte{exception("serviceUnavailableException", `{"message":"down"}`)},
			status: 503, errType: "ServiceUnavailableException", retryable: true},
		{name: "model stream error", frames: [][]byte{exception("modelStreamErrorException", `{"message":"broken"}`)},
			status: 500, errType: "ModelStreamErrorException", retryable: true},
		{name: "model stream error with model status", frames: [][]byte{exception("modelStreamErrorException",
			`{"message":"broken","originalStatusCode":400,"originalMessage":"prompt too long"}`)},
			status: 400, errType: "ModelStreamErrorException"},
		{name: "unknown exception", frames: [][]byte{exception("newException", `not json`)},
			status: 500, errType: "NewException", retryable: true},
		{name: "error message", frames: [][]byte{frame(nil, stringHeader(":message-type", "error"),
			stringHeader(":error-code", "InternalFailure"), stringHeader(":error-message", "failed"))},
			status: 500, errType: "InternalFailure", retryable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := collect(tc.frames...)
			if err == nil {
				t.Fatal("expected stream error")
			}
			var api *model.APIError
			if errors.As(err, &api) != (tc.status != 0) || model.IsRetryable(err) != tc.retryable {
				t.Fatalf("error = %v", err)
			}
			if api != nil && (api.StatusCode != tc.status || api.Type != tc.errType || api.Message == "" || api.Provider != "bedrock") {
				t.Fatalf("error = %#v", api)
			}
		})
	}
	_, _, err := collect(exception("modelStreamErrorException", `{"message":"broken","originalStatusCode":400,"originalMessage":"prompt too long"}`))
	if !strings.Contains(err.Error(), "broken: prompt too long") {
		t.Fatalf("error = %v", err)
	}
	if _, _, err := collect(start); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v", err)
	}
}

func TestReadStreamStopsWhenConsumerStops(t *testing.T) {
	t.Parallel()
	frames := bytes.Join([][]byte{
		event("contentBlockStart", `{"contentBlockIndex":0,"start":{"toolUse":{"toolUseId":"t","name":"f"}}}`),
		event("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"toolUse":{"input":"{}"}}}`),
		event("contentBlockStart", `{"contentBlockIndex":1,"start":{"toolUse":{"toolUseId":"s","name":"f","type":"server_tool_use"}}}`),
		event("contentBlockStop", `{"contentBlockIndex":1}`),
		event("messageStop", `{"stopReason":"end_turn"}`),
	}, nil)
	for limit := 1; limit <= 3; limit++ {
		n := 0
		readStream(bytes.NewReader(frames), &model.Response{}, func(*model.StreamEvent, error) bool {
			n++
			return n < limit
		})
		if n != limit {
			t.Fatalf("limit %d: %d events", limit, n)
		}
	}
}
