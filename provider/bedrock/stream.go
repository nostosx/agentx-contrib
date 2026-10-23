package bedrock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/nostosx/agentx-contrib/provider/internal/marshal"
	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

// ConverseStream events
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ConverseStreamOutput.html).
// Each event stream message carries one union member, named by its
// :event-type header, as its JSON payload.

type contentBlockStart struct {
	ContentBlockIndex int `json:"contentBlockIndex"`
	Start             struct {
		ToolUse *toolUse `json:"toolUse"`
		// Image starts an image
		// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ContentBlockStart.html).
		Image *struct {
			Format string `json:"format"`
		} `json:"image"`
	} `json:"start"`
}

type contentBlockDelta struct {
	ContentBlockIndex int `json:"contentBlockIndex"`
	Delta             struct {
		Text    *string `json:"text"`
		ToolUse *struct {
			Input string `json:"input"`
		} `json:"toolUse"`
		ReasoningContent *struct {
			Text            *string `json:"text"`
			Signature       string  `json:"signature"`
			RedactedContent []byte  `json:"redactedContent"`
		} `json:"reasoningContent"`
		// Image carries image data
		// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ImageBlockDelta.html).
		Image *struct {
			Source imageSource `json:"source"`
		} `json:"image"`
	} `json:"delta"`
}

type contentBlockStop struct {
	ContentBlockIndex int `json:"contentBlockIndex"`
}

type messageStop struct {
	StopReason string `json:"stopReason"`
}

type streamMetadata struct {
	Usage *usage `json:"usage"`
}

// serverToolUse accumulates a server tool use block, which is echoed back
// verbatim rather than executed.
type serverToolUse struct {
	use   toolUse
	input []byte
}

// readStream decodes a ConverseStream body into stream events. resp holds
// the response ID and model. The done event follows the metadata event,
// which comes after messageStop, or the end of the stream after
// messageStop.
func readStream(r io.Reader, resp *model.Response, yield func(*model.StreamEvent, error) bool) {
	var acc model.Accumulator
	servers := map[int]*serverToolUse{}
	stopped := false
	emit := func(index int, p *model.Part) bool {
		ev := &model.StreamEvent{Type: model.StreamDelta, Index: index, Delta: p}
		acc.Add(ev)
		return yield(ev, nil)
	}
	done := func() {
		resp.Message = utils.NormalizeToolArguments(acc.Message())
		yield(&model.StreamEvent{Type: model.StreamDone, Response: resp}, nil)
	}
	er := &eventReader{r: r}
	for {
		msg, err := er.next()
		if errors.Is(err, io.EOF) {
			if stopped {
				done()
				return
			}
			yield(nil, fmt.Errorf("bedrock: stream ended before messageStop: %w", io.ErrUnexpectedEOF))
			return
		}
		if err != nil {
			yield(nil, fmt.Errorf("bedrock: read stream: %w", err))
			return
		}
		switch msg.header(":message-type") {
		case "event":
		case "exception", "error":
			yield(nil, streamError(msg))
			return
		default:
			continue
		}
		decode := func(v any) bool {
			if err := json.Unmarshal(msg.payload, v); err != nil {
				yield(nil, fmt.Errorf("bedrock: decode %s event: %w", msg.header(":event-type"), err))
				return false
			}
			return true
		}
		switch msg.header(":event-type") {
		case "contentBlockStart":
			var ev contentBlockStart
			if !decode(&ev) {
				return
			}
			if img := ev.Start.Image; img != nil {
				if !emit(ev.ContentBlockIndex, &model.Part{Type: model.PartImage, MediaType: imageType(img.Format)}) {
					return
				}
				continue
			}
			tu := ev.Start.ToolUse
			if tu == nil {
				continue
			}
			if tu.Type != "" {
				servers[ev.ContentBlockIndex] = &serverToolUse{use: *tu}
				continue
			}
			if !emit(ev.ContentBlockIndex, &model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: tu.ToolUseID, Name: tu.Name}}) {
				return
			}
		case "contentBlockDelta":
			var ev contentBlockDelta
			if !decode(&ev) {
				return
			}
			p := deltaPart(&ev)
			if p == nil {
				continue
			}
			if s := servers[ev.ContentBlockIndex]; s != nil {
				if p.ToolCall != nil {
					s.input = append(s.input, p.ToolCall.Arguments...)
				}
				continue
			}
			if !emit(ev.ContentBlockIndex, p) {
				return
			}
		case "contentBlockStop":
			var ev contentBlockStop
			if !decode(&ev) {
				return
			}
			s := servers[ev.ContentBlockIndex]
			if s == nil {
				continue
			}
			delete(servers, ev.ContentBlockIndex)
			s.use.Input = marshal.Object(s.input)
			b, err := json.Marshal(toolUseBlock{ToolUse: s.use})
			if err != nil {
				continue
			}
			if !emit(ev.ContentBlockIndex, &model.Part{Type: model.PartRaw, Provider: providerName, Raw: b}) {
				return
			}
		case "messageStop":
			var ev messageStop
			if !decode(&ev) {
				return
			}
			resp.FinishReason = finishReason(ev.StopReason)
			stopped = true
		case "metadata":
			var ev streamMetadata
			if !decode(&ev) {
				return
			}
			if ev.Usage != nil {
				resp.Usage = ev.Usage.toModel()
			}
			if stopped {
				done()
				return
			}
		}
	}
}

// deltaPart converts a content block delta. Citation and tool result
// deltas, which have no neutral representation, are skipped.
func deltaPart(ev *contentBlockDelta) *model.Part {
	d := ev.Delta
	switch {
	case d.Text != nil:
		return &model.Part{Type: model.PartText, Text: *d.Text}
	case d.ToolUse != nil:
		return &model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{Arguments: json.RawMessage(d.ToolUse.Input)}}
	case d.Image != nil && len(d.Image.Source.Bytes) > 0:
		return &model.Part{Type: model.PartImage, Data: d.Image.Source.Bytes}
	case d.ReasoningContent != nil:
		rc := d.ReasoningContent
		p := &model.Part{Type: model.PartReasoning, Provider: providerName, Signature: rc.Signature}
		if rc.Text != nil {
			p.Text = *rc.Text
		}
		if len(rc.RedactedContent) > 0 {
			p.Redacted, p.Data = true, rc.RedactedContent
		}
		return p
	}
	return nil
}
