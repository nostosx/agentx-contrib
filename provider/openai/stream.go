package openai

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/nostosx/agentx-contrib/provider/internal/marshal"
	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

type chunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          string     `json:"content"`
			Refusal          string     `json:"refusal"`
			ReasoningContent string     `json:"reasoning_content"`
			Reasoning        reasoning  `json:"reasoning"`
			ToolCalls        []toolCall `json:"tool_calls"`
			Audio            *chatAudio `json:"audio"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usage          `json:"usage"`
	Error *responsesError `json:"error"`
}

// Part indexes: reasoning, text and audio get fixed slots; tool call i
// uses toolBase+i so parts keep a stable order.
const (
	audioIndex     = -1
	reasoningIndex = 0
	textIndex      = 1
	toolBase       = 2
)

// errUnexpectedEnd reports a stream that ended before its final event, as
// when the connection is lost, so it is retryable.
var errUnexpectedEnd = fmt.Errorf("openai: stream ended unexpectedly: %w", io.ErrUnexpectedEOF)

// toolSlots assigns tool-call deltas to tool slots. Deltas carry the index
// of their call; for services that omit it, a delta with a new call ID
// starts the next slot and other deltas extend the current one.
type toolSlots struct {
	ids     map[int]string
	current int
	n       int
}

func (s *toolSlots) slot(tc toolCall) int {
	if s.ids == nil {
		s.ids = map[int]string{}
	}
	idx := s.current
	switch {
	case tc.Index != nil:
		idx = *tc.Index
	case s.n == 0 || (tc.ID != "" && s.ids[s.current] != "" && s.ids[s.current] != tc.ID):
		idx = s.n
	}
	if tc.ID != "" && s.ids[idx] == "" {
		s.ids[idx] = tc.ID
	}
	s.current, s.n = idx, max(s.n, idx+1)
	return idx
}

func readStream(r io.Reader, yield func(*model.StreamEvent, error) bool) {
	resp := &model.Response{FinishReason: model.FinishUnknown}
	var acc model.Accumulator
	refused := false
	var slots toolSlots
	var audio audioAttrs // the ID and expiry of streamed audio
	audioData := false   // whether audio data has arrived
	emit := func(index int, p *model.Part) bool {
		ev := &model.StreamEvent{Type: model.StreamDelta, Index: index, Delta: p}
		acc.Add(ev)
		return yield(ev, nil)
	}
	done := false
	for sse, err := range utils.ReadSSE(r) {
		if err != nil {
			yield(nil, fmt.Errorf("openai: read stream: %w", err))
			return
		}
		data := strings.TrimSpace(sse.Data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			done = true
			break
		}
		var c chunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			yield(nil, fmt.Errorf("openai: decode stream chunk: %w", err))
			return
		}
		if c.Error != nil {
			yield(nil, c.Error.apiError())
			return
		}
		if c.ID != "" {
			resp.ID = c.ID
		}
		if c.Model != "" {
			resp.Model = c.Model
		}
		if c.Usage != nil {
			resp.Usage = c.Usage.toModel()
		}
		for _, ch := range c.Choices {
			d := ch.Delta
			if rc := cmp.Or(d.ReasoningContent, string(d.Reasoning)); rc != "" && !emit(reasoningIndex, &model.Part{Type: model.PartReasoning, Text: rc}) {
				return
			}
			text := d.Content + d.Refusal
			if d.Refusal != "" {
				refused = true
			}
			if text != "" && !emit(textIndex, &model.Part{Type: model.PartText, Text: text}) {
				return
			}
			if a := d.Audio; a != nil {
				p, err := audioPart(a)
				if err != nil {
					yield(nil, err)
					return
				}
				// The format shows in the first data only.
				if audioData {
					p.MediaType = ""
				}
				audioData = audioData || len(p.Data) > 0
				// The ID and expiry arrive in separate chunks; Raw keeps
				// both.
				p.Raw = nil
				if a.ID != "" || a.ExpiresAt != 0 {
					audio.ID, audio.ExpiresAt = cmp.Or(a.ID, audio.ID), cmp.Or(a.ExpiresAt, audio.ExpiresAt)
					p.Raw = marshal.MustRaw(audio)
				}
				if !emit(audioIndex, &p) {
					return
				}
			}
			for _, tc := range d.ToolCalls {
				idx := slots.slot(tc)
				p := &model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{
					ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(tc.Function.Arguments),
				}}
				if !emit(toolBase+idx, p) {
					return
				}
			}
			if ch.FinishReason != "" {
				resp.FinishReason = finishReason(ch.FinishReason)
			}
		}
	}
	if !done && resp.FinishReason == model.FinishUnknown {
		yield(nil, errUnexpectedEnd)
		return
	}
	if refused {
		resp.FinishReason = model.FinishRefusal
	}
	resp.Message = utils.NormalizeToolArguments(acc.Message())
	yield(&model.StreamEvent{Type: model.StreamDone, Response: resp}, nil)
}
