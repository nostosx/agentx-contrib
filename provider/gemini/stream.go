package gemini

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

func readStream(r io.Reader, yield func(*model.StreamEvent, error) bool) {
	resp := &model.Response{FinishReason: model.FinishUnknown}
	var acc model.Accumulator
	index := -1
	var lastType model.PartType
	lastMedia := "" // the media type of the last part
	finish := ""
	blocked := false
	received := false
	emit := func(p *model.Part, extend bool) bool {
		if !extend {
			index++
		}
		lastType = p.Type
		ev := &model.StreamEvent{Type: model.StreamDelta, Index: index, Delta: p}
		acc.Add(ev)
		return yield(ev, nil)
	}
	for sse, err := range utils.ReadSSE(r) {
		if err != nil {
			yield(nil, fmt.Errorf("gemini: read stream: %w", err))
			return
		}
		if sse.Data == "" {
			continue
		}
		var c struct {
			response
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(sse.Data), &c); err != nil {
			yield(nil, fmt.Errorf("gemini: decode stream chunk: %w", err))
			return
		}
		if c.Error != nil {
			yield(nil, &model.APIError{Provider: providerName, StatusCode: c.Error.Code, Type: c.Error.Status, Message: c.Error.Message})
			return
		}
		received = true
		if c.ResponseID != "" {
			resp.ID = c.ResponseID
		}
		if c.ModelVersion != "" {
			resp.Model = c.ModelVersion
		}
		if c.UsageMetadata != nil {
			resp.Usage = c.UsageMetadata.toModel()
		}
		if c.PromptFeedback != nil && c.PromptFeedback.BlockReason != "" {
			blocked = true
		}
		if len(c.Candidates) == 0 {
			continue
		}
		cand := c.Candidates[0]
		if cand.FinishReason != "" {
			finish = cand.FinishReason
		}
		if cand.Content == nil {
			continue
		}
		for _, p := range cand.Content.Parts {
			mp, ok := toPart(p)
			if !ok {
				continue
			}
			// Consecutive text or reasoning chunks extend the same part;
			// a signature closes it. Images, including the draft images
			// in reasoning, are parts of their own. Chunks of headerless
			// PCM audio, which speech models stream, form one part.
			pcm := mp.Type == model.PartAudio && isPCM(mp.MediaType)
			media := len(mp.Data) > 0 && !pcm
			extend := index >= 0 && !media && mp.Type == lastType && (mp.Type == model.PartText || mp.Type == model.PartReasoning || (pcm && mp.MediaType == lastMedia))
			if !emit(&mp, extend) {
				return
			}
			lastMedia = mp.MediaType
			if mp.Signature != "" || media {
				lastType = ""
			}
		}
	}
	if !received {
		yield(nil, fmt.Errorf("gemini: stream ended without data: %w", io.ErrUnexpectedEOF))
		return
	}
	resp.Message = acc.Message()
	switch {
	case blocked && len(resp.Message.Parts) == 0:
		resp.FinishReason = model.FinishContentFilter
	case finish == "":
		// The final chunk carries the finish reason; without it the
		// response was cut off.
		yield(nil, fmt.Errorf("gemini: stream ended without a finish reason: %w", io.ErrUnexpectedEOF))
		return
	default:
		resp.FinishReason = finishReason(finish, len(resp.Message.ToolCalls()) > 0)
	}
	yield(&model.StreamEvent{Type: model.StreamDone, Response: resp}, nil)
}

// isPCM reports whether audio of mediaType is headerless PCM, such as the
// audio/L16 that speech models stream, whose chunks can be joined.
func isPCM(mediaType string) bool {
	mt := utils.BaseType(mediaType)
	return mt == "audio/l16" || mt == "audio/pcm"
}
