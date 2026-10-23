package openai

import (
	"encoding/json"
	"strings"

	"github.com/nostosx/agentx-contrib/provider/internal/jsonschema"
	"github.com/nostosx/agentx/model"
)

func (m *Model) quirks() quirks {
	if m.preset == nil {
		return quirks{}
	}
	return m.preset.quirks
}

// apply adapts a Chat Completions request to a service's quirks.
// legacyMaxTokens is applied through Model.legacyMaxTokens.
func (q quirks) apply(r *request, req *model.Request) {
	if q.noStreamOptions {
		r.StreamOptions = nil
	}
	if q.noNames {
		for i := range r.Messages {
			r.Messages[i].Name = ""
		}
	}
	if q.noToolChoice && r.ToolChoice != nil {
		if r.ToolChoice == string(model.ToolChoiceNone) {
			r.Tools = nil
		}
		r.ToolChoice = nil
	}
	if q.jsonObject && req.ResponseFormat != nil {
		r.ResponseFormat = &responseFormat{Type: "json_object"}
		instruction := jsonschema.Instruction(req.ResponseFormat.Schema)
		if len(r.Messages) > 0 && r.Messages[0].Role == "system" {
			if s, ok := r.Messages[0].Content.(string); ok {
				r.Messages[0].Content = s + "\n\n" + instruction
			}
		} else {
			r.Messages = append([]message{{Role: "system", Content: instruction}}, r.Messages...)
		}
	}
	if q.replayReasoning {
		// convertMessages turns every assistant message into exactly one
		// assistant message, in order.
		i := 0
		for _, msg := range req.Messages {
			if msg.Role != model.RoleAssistant {
				continue
			}
			for i < len(r.Messages) && r.Messages[i].Role != "assistant" {
				i++
			}
			if i == len(r.Messages) {
				break
			}
			r.Messages[i].ReasoningContent = reasoningText(msg)
			i++
		}
	}
}

func reasoningText(msg *model.Message) string {
	var texts []string
	for _, p := range msg.Parts {
		if p.Type == model.PartReasoning && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// reasoning is the reasoning field that some compatible services send
// instead of reasoning_content. Values other than strings, such as
// objects some services send, are ignored rather than failing the decode.
type reasoning string

func (r *reasoning) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*r = reasoning(s)
	}
	return nil
}
