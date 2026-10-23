package openai

import (
	"encoding/json"

	"github.com/nostosx/agentx-contrib/provider/internal/marshal"
	"github.com/nostosx/agentx/model"
)

// This file maps freeform tools (model.ToolSpec.Grammar) to custom tools
// of the Responses API, whose calls carry plain text instead of JSON
// (https://developers.openai.com/api/docs/guides/function-calling#custom-tools).
// A custom_tool_call item becomes a tool call whose arguments hold the text
// as model.FreeformInput, and calls of the request's freeform tools are
// sent back as custom_tool_call items with custom_tool_call_output results.
// Chat Completions requests send freeform tools as functions.

// WithFreeformTools decides whether Responses requests send freeform tools
// as custom tools, which is the default. Disable it for compatible
// services that only implement function tools; freeform tools are then
// sent as functions with their JSON parameters.
func WithFreeformTools(enabled bool) Option {
	return func(m *Model) { m.noFreeform = !enabled }
}

type customTool struct {
	Type        string        `json:"type"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Format      *customFormat `json:"format,omitempty"`
}

type customFormat struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax"`
	Definition string `json:"definition"`
}

type customCallItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Input  string `json:"input"`
}

type customOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	// Output is a string or a list of content parts.
	Output any `json:"output"`
}

// freeformTools returns the names of the request's tools sent as custom
// tools.
func (m *Model) freeformTools(tools []*model.ToolSpec) map[string]bool {
	if m.noFreeform {
		return nil
	}
	var out map[string]bool
	for _, t := range tools {
		if t.Grammar != nil {
			if out == nil {
				out = map[string]bool{}
			}
			out[t.Name] = true
		}
	}
	return out
}

// toolDefinition returns the Responses tool for t.
func toolDefinition(t *model.ToolSpec, custom map[string]bool) any {
	if !custom[t.Name] {
		return responsesTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: strictSchema(t.Parameters, t.Strict), Strict: t.Strict}
	}
	ct := customTool{Type: "custom", Name: t.Name, Description: t.Description}
	if g := t.Grammar; g.Definition != "" {
		ct.Format = &customFormat{Type: "grammar", Syntax: g.Syntax, Definition: g.Definition}
	}
	return ct
}

// customCall returns the custom_tool_call item of a call to a custom tool,
// if the call carries its text input.
func customCall(tc *model.ToolCall, custom map[string]bool) (customCallItem, bool) {
	if !custom[tc.Name] {
		return customCallItem{}, false
	}
	var args map[string]json.RawMessage
	var input string
	if json.Unmarshal(tc.Arguments, &args) != nil || json.Unmarshal(args[model.FreeformInput], &input) != nil {
		return customCallItem{}, false
	}
	return customCallItem{Type: "custom_tool_call", CallID: tc.ID, Name: tc.Name, Input: input}, true
}

// customCallPart converts a custom_tool_call output item.
func customCallPart(raw json.RawMessage) (model.Part, error) {
	var it customCallItem
	if err := json.Unmarshal(raw, &it); err != nil {
		return model.Part{}, err
	}
	args := marshal.MustRaw(map[string]string{model.FreeformInput: it.Input})
	return model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: it.CallID, Name: it.Name, Arguments: args}}, nil
}
