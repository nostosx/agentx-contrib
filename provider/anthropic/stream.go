package anthropic

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/nostosx/agentx-contrib/provider/internal/marshal"
	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage usage  `json:"usage"`
	} `json:"message"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        *struct {
		Type        string          `json:"type"`
		Text        string          `json:"text"`
		Thinking    string          `json:"thinking"`
		Signature   string          `json:"signature"`
		PartialJSON string          `json:"partial_json"`
		StopReason  string          `json:"stop_reason"`
		Citation    json.RawMessage `json:"citation"`
	} `json:"delta"`
	Usage *usage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// rawBlock accumulates a block that is passed through verbatim.
type rawBlock struct {
	block map[string]json.RawMessage
	input []byte
}

func readStream(r io.Reader, yield func(*model.StreamEvent, error) bool) {
	resp := &model.Response{FinishReason: model.FinishUnknown}
	var acc model.Accumulator
	raws := map[int]*rawBlock{}
	cites := map[int][]json.RawMessage{} // citations of text blocks
	emit := func(index int, p *model.Part) bool {
		ev := &model.StreamEvent{Type: model.StreamDelta, Index: index, Delta: p}
		acc.Add(ev)
		return yield(ev, nil)
	}
	for sse, err := range utils.ReadSSE(r) {
		if err != nil {
			yield(nil, fmt.Errorf("anthropic: read stream: %w", err))
			return
		}
		if sse.Data == "" {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(sse.Data), &ev); err != nil {
			yield(nil, fmt.Errorf("anthropic: decode stream event: %w", err))
			return
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				resp.ID, resp.Model = ev.Message.ID, ev.Message.Model
				resp.Usage = ev.Message.Usage.toModel()
			}
		case "content_block_start":
			p, ok := startPart(ev.ContentBlock)
			if !ok {
				continue
			}
			if p.Type == model.PartRaw {
				var m map[string]json.RawMessage
				if json.Unmarshal(ev.ContentBlock, &m) == nil {
					raws[ev.Index] = &rawBlock{block: m}
				}
			}
			if !emit(ev.Index, &p) {
				return
			}
		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			var p *model.Part
			switch ev.Delta.Type {
			case "text_delta":
				p = &model.Part{Type: model.PartText, Text: ev.Delta.Text}
			case "thinking_delta":
				p = &model.Part{Type: model.PartReasoning, Text: ev.Delta.Thinking}
			case "signature_delta":
				p = &model.Part{Type: model.PartReasoning, Signature: ev.Delta.Signature}
			case "citations_delta":
				// Each delta adds a citation; the part keeps them all.
				if len(ev.Delta.Citation) == 0 {
					continue
				}
				cites[ev.Index] = append(cites[ev.Index], ev.Delta.Citation)
				tp := textPart("", marshal.MustRaw(cites[ev.Index]))
				p = &tp
			case "input_json_delta":
				if rb := raws[ev.Index]; rb != nil {
					rb.input = append(rb.input, ev.Delta.PartialJSON...)
					continue
				}
				p = &model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{Arguments: json.RawMessage(ev.Delta.PartialJSON)}}
			}
			if p != nil && !emit(ev.Index, p) {
				return
			}
		case "content_block_stop":
			rb := raws[ev.Index]
			if rb == nil {
				continue
			}
			delete(raws, ev.Index)
			if len(rb.input) > 0 {
				rb.block["input"] = marshal.Object(rb.input)
			}
			b, err := json.Marshal(rb.block)
			if err != nil {
				continue
			}
			if !emit(ev.Index, &model.Part{Type: model.PartRaw, Raw: b}) {
				return
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				resp.FinishReason = finishReason(ev.Delta.StopReason)
			}
			if ev.Usage != nil {
				// message_delta usage is cumulative.
				resp.Usage.OutputTokens = ev.Usage.OutputTokens
				if ev.Usage.InputTokens > 0 {
					resp.Usage.InputTokens = ev.Usage.InputTokens
				}
				if ev.Usage.CacheReadInputTokens > 0 {
					resp.Usage.CacheReadTokens = ev.Usage.CacheReadInputTokens
				}
				if ev.Usage.CacheCreationInputTokens > 0 {
					resp.Usage.CacheWriteTokens = ev.Usage.CacheCreationInputTokens
				}
			}
		case "message_stop":
			resp.Message = utils.NormalizeToolArguments(acc.Message())
			yield(&model.StreamEvent{Type: model.StreamDone, Response: resp}, nil)
			return
		case "error":
			apiErr := &model.APIError{Provider: providerName, StatusCode: http.StatusInternalServerError, Message: "stream error"}
			if ev.Error != nil {
				apiErr.Type, apiErr.Message = ev.Error.Type, ev.Error.Message
				if status, ok := errorStatus[ev.Error.Type]; ok {
					apiErr.StatusCode = status
				}
			}
			yield(nil, apiErr)
			return
		}
	}
	yield(nil, fmt.Errorf("anthropic: stream ended before message_stop: %w", io.ErrUnexpectedEOF))
}

// errorStatus maps the error types of stream error events to the HTTP
// status the API returns for them outside a stream. Unknown types are
// treated as server errors (500).
var errorStatus = map[string]int{
	"invalid_request_error": http.StatusBadRequest,
	"authentication_error":  http.StatusUnauthorized,
	"billing_error":         http.StatusPaymentRequired,
	"permission_error":      http.StatusForbidden,
	"not_found_error":       http.StatusNotFound,
	"conflict_error":        http.StatusConflict,
	"request_too_large":     http.StatusRequestEntityTooLarge,
	"rate_limit_error":      http.StatusTooManyRequests,
	"api_error":             http.StatusInternalServerError,
	"timeout_error":         http.StatusGatewayTimeout,
	"overloaded_error":      529,
}

// startPart converts the block of a content_block_start event.
func startPart(raw json.RawMessage) (model.Part, bool) {
	p, ok := parseBlock(raw)
	if ok && p.Type == model.PartToolCall {
		// Input arrives through input_json_delta events.
		p.ToolCall.Arguments = nil
	}
	return p, ok
}
