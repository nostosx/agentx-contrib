package openai

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

// responsesEvent holds the fields of the streaming events agentx uses.
type responsesEvent struct {
	Type         string             `json:"type"`
	Response     *responsesResponse `json:"response"`
	OutputIndex  int                `json:"output_index"`
	SummaryIndex int                `json:"summary_index"`
	Item         json.RawMessage    `json:"item"`
	Delta        string             `json:"delta"`
	// error events, which carry the error at the top level or, from some
	// servers, in an error object
	Code    errorCode       `json:"code"`
	Message string          `json:"message"`
	Error   *responsesError `json:"error"`
}

// streamError returns the error reported by an error event.
func (ev *responsesEvent) streamError() *model.APIError {
	e := responsesError{Code: ev.Code, Message: ev.Message}
	if n := ev.Error; n != nil {
		e.Code, e.Type, e.Message = cmp.Or(e.Code, n.Code), n.Type, cmp.Or(e.Message, n.Message)
	}
	return e.apiError()
}

// readResponsesStream converts Responses streaming events. Each output item
// becomes the part at its output index. Text, refusal, argument and summary
// deltas are streamed; the completed item from response.output_item.done
// supplies what cannot be streamed, such as encrypted reasoning.
func readResponsesStream(r io.Reader, yield func(*model.StreamEvent, error) bool) {
	var acc model.Accumulator
	started := map[int]bool{} // output indexes with a part
	args := map[int]bool{}    // function calls whose arguments were streamed
	summary := map[int]int{}  // last summary index seen per reasoning item
	refused := false
	emit := func(index int, p *model.Part) bool {
		started[index] = true
		ev := &model.StreamEvent{Type: model.StreamDelta, Index: index, Delta: p}
		acc.Add(ev)
		return yield(ev, nil)
	}
	for sse, err := range utils.ReadSSE(r) {
		if err != nil {
			yield(nil, fmt.Errorf("openai: read stream: %w", err))
			return
		}
		data := strings.TrimSpace(sse.Data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev responsesEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			yield(nil, fmt.Errorf("openai: decode stream event: %w", err))
			return
		}
		i := ev.OutputIndex
		ok := true
		switch ev.Type {
		case "response.output_item.added":
			var it outputItem
			if json.Unmarshal(ev.Item, &it) == nil && it.Type == "function_call" {
				ok = emit(i, &model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: it.CallID, Name: it.Name}})
			}
		case "response.output_text.delta", "response.refusal.delta":
			refused = refused || ev.Type == "response.refusal.delta"
			ok = emit(i, &model.Part{Type: model.PartText, Text: ev.Delta})
		case "response.function_call_arguments.delta":
			args[i] = true
			ok = emit(i, &model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{Arguments: json.RawMessage(ev.Delta)}})
		case "response.reasoning_summary_text.delta":
			text := ev.Delta
			if last, seen := summary[i]; seen && ev.SummaryIndex != last {
				text = "\n\n" + text
			}
			summary[i] = ev.SummaryIndex
			ok = emit(i, &model.Part{Type: model.PartReasoning, Text: text})
		case "response.output_item.done":
			ok = finishItem(i, ev.Item, started[i], args[i], emit, &refused)
		case "response.completed", "response.incomplete":
			if ev.Response == nil {
				yield(nil, errors.New("openai: stream event without response"))
				return
			}
			resp := &model.Response{ID: ev.Response.ID, Model: ev.Response.Model, Usage: ev.Response.Usage.toModel()}
			msg := utils.NormalizeToolArguments(acc.Message())
			resp.Message = msg
			resp.FinishReason = responsesFinish(ev.Response.Status, ev.Response.IncompleteDetails, len(msg.ToolCalls()) > 0, refused)
			yield(&model.StreamEvent{Type: model.StreamDone, Response: resp}, nil)
			return
		case "response.failed":
			if ev.Response != nil && ev.Response.Error != nil {
				yield(nil, ev.Response.Error.apiError())
			} else {
				yield(nil, (&responsesError{Message: "response failed"}).apiError())
			}
			return
		case "error":
			yield(nil, ev.streamError())
			return
		}
		if !ok {
			return
		}
	}
	yield(nil, errUnexpectedEnd)
}

// finishItem handles a completed output item. Streamed parts get what the
// deltas did not carry; items that were not streamed are emitted whole.
func finishItem(i int, raw json.RawMessage, started, streamedArgs bool, emit func(int, *model.Part) bool, refused *bool) bool {
	p, ref, err := convertItem(raw)
	if err != nil {
		// A malformed item cannot be sent back; the streamed deltas are
		// kept.
		return true
	}
	*refused = *refused || ref
	if !started {
		return emit(i, &p)
	}
	switch p.Type {
	case model.PartReasoning, model.PartText:
		if len(p.Raw) == 0 {
			return true
		}
		return emit(i, &model.Part{Type: p.Type, Provider: p.Provider, Raw: p.Raw})
	case model.PartToolCall:
		if !streamedArgs {
			return emit(i, &model.Part{Type: p.Type, ToolCall: &model.ToolCall{Arguments: p.ToolCall.Arguments}})
		}
	}
	return true
}
