package openai

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/nostosx/kitx/dataurl"

	"github.com/nostosx/agentx-contrib/provider/internal/marshal"
	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

// This file implements the Responses API (POST /responses).
//
// Output items that must be sent back unchanged, such as reasoning items
// with their encrypted content, are kept in model.Part.Raw with Provider set
// to "openai". Assistant text keeps its message attributes, such as phase,
// in Raw too, while the text itself stays in Part.Text.

type responsesRequest struct {
	Model           string              `json:"model"`
	Instructions    string              `json:"instructions,omitempty"`
	Input           []json.RawMessage   `json:"input"`
	Tools           []any               `json:"tools,omitempty"`
	ToolChoice      any                 `json:"tool_choice,omitempty"`
	MaxOutputTokens int                 `json:"max_output_tokens,omitempty"`
	Temperature     *float64            `json:"temperature,omitempty"`
	TopP            *float64            `json:"top_p,omitempty"`
	Reasoning       *responsesReasoning `json:"reasoning,omitempty"`
	Text            *responsesText      `json:"text,omitempty"`
	// Store is false and the conversation is sent with every request;
	// this adapter does not use server-side conversation state.
	Store bool `json:"store"`
	// Include is sent only when Extra or WithEncryptedReasoning sets it.
	// Stateless requests return reasoning items with encrypted_content by
	// default.
	Include []string `json:"include,omitempty"`
	Stream  bool     `json:"stream,omitempty"`
}

// includeReasoning is the legacy include value that asks for reasoning
// items with their encrypted_content, which stateless requests now return
// by default (https://developers.openai.com/api/docs/guides/reasoning).
const includeReasoning = "reasoning.encrypted_content"

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	// Strict is always sent, because its default differs between APIs.
	Strict bool `json:"strict"`
}

type responsesReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type responsesText struct {
	Format *responsesFormat `json:"format,omitempty"`
}

type responsesFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict,omitempty"`
}

type inputMessage struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content any    `json:"content"`
	Phase   string `json:"phase,omitempty"`
}

// inputContent is a content part of a Responses input message or tool
// output (https://developers.openai.com/api/docs/guides/images-vision and
// https://developers.openai.com/api/docs/guides/file-inputs).
type inputContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	FileData string `json:"file_data,omitempty"`
	FileURL  string `json:"file_url,omitempty"`
	Filename string `json:"filename,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// inputPart converts a text or media part. The Responses API takes images
// and files of many types, such as PDF, text, Word and Excel documents,
// but no audio or video
// (https://developers.openai.com/api/docs/guides/audio-chat-completions).
func (m *Model) inputPart(p model.Part) (inputContent, error) {
	if p.Type == model.PartText {
		return inputContent{Type: "input_text", Text: p.Text}, nil
	}
	if err := utils.CheckMedia(providerName, p, m.providerName()); err != nil {
		return inputContent{}, err
	}
	switch p.Type {
	case model.PartImage:
		c := inputContent{Type: "input_image", Detail: p.Detail, FileID: p.FileID}
		if p.FileID == "" {
			source, err := imageSource(p)
			if err != nil {
				return inputContent{}, err
			}
			c.ImageURL = source
		}
		return c, nil
	case model.PartDocument:
		c := inputContent{Type: "input_file", Detail: p.Detail, FileID: p.FileID}
		switch mt, data, ok := utils.InlineData(p); {
		case p.FileID != "":
		case ok:
			encoded, err := dataurl.Encode(mt, data)
			if err != nil {
				return inputContent{}, model.Permanent(fmt.Errorf("openai: encode file: %w", err))
			}
			c.FileData, c.Filename = encoded, utils.Filename(p)
		default:
			c.FileURL = p.URL
		}
		return c, nil
	case model.PartAudio:
		return inputContent{}, model.Permanent(errors.New("openai: the Responses API does not take audio input; use WithChatCompletions with an audio model"))
	}
	return inputContent{}, model.Permanent(fmt.Errorf("openai: %s input is not supported", p.Type))
}

// toolOutput returns the output of a tool result: its text, or a list of
// content parts when it has images or files
// (https://developers.openai.com/api/docs/guides/function-calling).
func (m *Model) toolOutput(r *model.ToolResult) (any, error) {
	text := resultText(r)
	if !slices.ContainsFunc(r.Parts, isContent) {
		return text, nil
	}
	var out []inputContent
	if text != "" {
		out = append(out, inputContent{Type: "input_text", Text: text})
	}
	for _, p := range r.Parts {
		if !isContent(p) {
			continue
		}
		c, err := m.inputPart(p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

type functionCallItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type functionOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	// Output is a string or a list of content parts.
	Output any `json:"output"`
}

// messageAttrs are the attributes of an assistant message item kept with
// its text part.
type messageAttrs struct {
	Phase string `json:"phase,omitempty"`
}

func (m *Model) buildResponses(req *model.Request, stream bool) ([]byte, error) {
	if len(req.Stop) > 0 {
		return nil, model.Permanent(errors.New("openai: the Responses API does not support stop sequences; use WithChatCompletions"))
	}
	custom := m.freeformTools(req.Tools)
	input, err := m.convertInput(req.Messages, custom)
	if err != nil {
		return nil, err
	}
	r := responsesRequest{
		Model:           cmp.Or(req.Model, m.model),
		Instructions:    req.System,
		Input:           input,
		MaxOutputTokens: cmp.Or(req.MaxTokens, m.maxTokens),
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		Stream:          stream,
	}
	extra := req.Extra
	r.Include, extra = m.include(extra)
	if r.Input == nil {
		r.Input = []json.RawMessage{}
	}
	for _, t := range req.Tools {
		r.Tools = append(r.Tools, toolDefinition(t, custom))
	}
	if tc := req.ToolChoice; tc != nil && len(r.Tools) > 0 {
		switch tc.Mode {
		case model.ToolChoiceTool:
			kind := "function"
			if custom[tc.Name] {
				kind = "custom"
			}
			r.ToolChoice = map[string]string{"type": kind, "name": tc.Name}
		case model.ToolChoiceNone, model.ToolChoiceRequired, model.ToolChoiceAuto:
			r.ToolChoice = string(tc.Mode)
		}
	}
	if rs := req.Reasoning; rs != nil {
		r.Reasoning = &responsesReasoning{Effort: rs.Effort}
		if rs.Display != "omitted" {
			r.Reasoning.Summary = "auto"
		}
		if rs.Effort != "" && rs.Effort != "none" {
			// Reasoning models reject sampling parameters unless reasoning
			// is off.
			r.Temperature, r.TopP = nil, nil
		}
	}
	if f := req.ResponseFormat; f != nil {
		r.Text = &responsesText{Format: &responsesFormat{Type: "json_schema", Name: cmp.Or(f.Name, "response"), Schema: strictSchema(f.Schema, f.Strict), Strict: f.Strict}}
	}
	return utils.EncodeBody(providerName, r, extra)
}

// include returns the include list and the Extra fields to send. With
// WithEncryptedReasoning(true), encrypted reasoning is added to an include
// list from Extra rather than replaced by it; an Extra include set to nil
// removes the field.
func (m *Model) include(extra map[string]any) ([]string, map[string]any) {
	var include []string
	if v, ok := extra["include"]; ok {
		values, ok := stringList(v)
		if !ok {
			// nil removes the field; other values are sent as given.
			return nil, extra
		}
		include = values
		extra = maps.Clone(extra)
		delete(extra, "include")
	}
	if m.encrypted && !slices.Contains(include, includeReasoning) {
		include = append(include, includeReasoning)
	}
	return include, extra
}

// stringList converts an Extra value holding a list of strings.
func stringList(v any) ([]string, bool) {
	switch v := v.(type) {
	case []string:
		return slices.Clone(v), true
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

// convertInput converts messages to input items. Items OpenAI produced are
// sent back verbatim; content from other providers is converted, except
// reasoning, which OpenAI cannot use. Reasoning items without encrypted
// content, such as those recorded from stored requests, are dropped:
// stateless requests cannot refer to them by ID.
func (m *Model) convertInput(msgs []*model.Message, custom map[string]bool) ([]json.RawMessage, error) {
	var out []json.RawMessage
	customCalls := map[string]bool{} // IDs of calls sent as custom tool calls
	add := func(v any) { out = append(out, marshal.MustRaw(v)) }
	for _, msg := range msgs {
		switch msg.Role {
		case model.RoleSystem:
			if t := msg.Text(); t != "" {
				add(inputMessage{Type: "message", Role: "system", Content: t})
			}
		case model.RoleAssistant:
			for _, p := range msg.Parts {
				own := p.Provider == providerName && len(p.Raw) > 0
				switch p.Type {
				case model.PartText:
					if p.Text == "" {
						continue
					}
					item := inputMessage{Type: "message", Role: "assistant", Content: p.Text}
					if own {
						var attrs messageAttrs
						if json.Unmarshal(p.Raw, &attrs) == nil {
							item.Phase = attrs.Phase
						}
					}
					add(item)
				case model.PartReasoning, model.PartRaw:
					if own && (p.Type != model.PartReasoning || hasEncryptedContent(p.Raw)) {
						out = append(out, slices.Clone(p.Raw))
					}
				case model.PartAudio:
					// Audio of Chat Completions is replayed as its
					// transcript.
					if p.Text != "" {
						add(inputMessage{Type: "message", Role: "assistant", Content: p.Text})
					}
				case model.PartToolCall:
					if tc := p.ToolCall; tc != nil {
						if item, ok := customCall(tc, custom); ok {
							customCalls[tc.ID] = true
							add(item)
							continue
						}
						add(functionCallItem{Type: "function_call", CallID: tc.ID, Name: tc.Name, Arguments: string(marshal.Object(tc.Arguments))})
					}
				}
			}
		default:
			// Tool results become function_call_output items; other
			// content forms a user message after them.
			var parts []inputContent
			for _, p := range msg.Parts {
				switch {
				case p.Type == model.PartToolResult:
					if r := p.ToolResult; r != nil {
						output, err := m.toolOutput(r)
						if err != nil {
							return nil, err
						}
						if customCalls[r.CallID] {
							add(customOutputItem{Type: "custom_tool_call_output", CallID: r.CallID, Output: output})
							continue
						}
						add(functionOutputItem{Type: "function_call_output", CallID: r.CallID, Output: output})
					}
				case isContent(p):
					c, err := m.inputPart(p)
					if err != nil {
						return nil, err
					}
					parts = append(parts, c)
				}
			}
			if len(parts) > 0 {
				add(inputMessage{Type: "message", Role: "user", Content: parts})
			}
		}
	}
	return out, nil
}

// hasEncryptedContent reports whether a reasoning item carries its
// encrypted content.
func hasEncryptedContent(raw json.RawMessage) bool {
	var item struct {
		EncryptedContent string `json:"encrypted_content"`
	}
	return json.Unmarshal(raw, &item) == nil && item.EncryptedContent != ""
}

// resultText returns a tool result's content, marking errors so the model
// can tell them apart; neither API has an error flag for tool outputs.
func resultText(r *model.ToolResult) string {
	if r.IsError && !strings.HasPrefix(r.Content, "Error") {
		return "Error: " + r.Content
	}
	return r.Content
}

type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u *responsesUsage) toModel() model.Usage {
	if u == nil {
		return model.Usage{}
	}
	out := model.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}
	if d := u.InputTokensDetails; d != nil {
		// input_tokens includes cache reads and writes.
		out.CacheReadTokens = d.CachedTokens
		out.CacheWriteTokens = d.CacheWriteTokens
		out.InputTokens -= d.CachedTokens + d.CacheWriteTokens
	}
	if d := u.OutputTokensDetails; d != nil {
		out.ReasoningTokens = d.ReasoningTokens
	}
	return out
}

type responsesError struct {
	Code    errorCode `json:"code"`
	Message string    `json:"message"`
	Type    string    `json:"type"`
}

// errorCode is an error code sent as a string, or as a number by some
// compatible services.
type errorCode string

func (c *errorCode) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*c = errorCode(s)
		return nil
	}
	// Codes of other shapes carry nothing usable.
	var n json.Number
	*c = ""
	if json.Unmarshal(b, &n) == nil {
		*c = errorCode(n.String())
	}
	return nil
}

// errorStatus maps the codes and types of errors reported inside a
// response or stream to the HTTP status of the same error.
var errorStatus = map[string]int{
	"rate_limit_exceeded":  http.StatusTooManyRequests,
	"rate_limit_error":     http.StatusTooManyRequests,
	"slow_down":            http.StatusTooManyRequests,
	"insufficient_quota":   http.StatusTooManyRequests,
	"server_error":         http.StatusInternalServerError,
	"api_error":            http.StatusInternalServerError,
	"vector_store_timeout": http.StatusInternalServerError,
	"overloaded":           http.StatusServiceUnavailable,
	"server_is_overloaded": http.StatusServiceUnavailable,
	"invalid_api_key":      http.StatusUnauthorized,
}

// apiError converts an error reported inside a response or stream, which
// carries no HTTP status, so that retry decisions follow its code, or its
// type when the code is unknown:
//
//   - A known code or type maps to the status of the same error.
//   - A numeric code in the HTTP error range, as some compatible services
//     send, is used as the status.
//   - An error without code and type is treated as a server failure (500),
//     which is retried: the request was accepted, and validation errors
//     come with a code or as an error status before the response starts.
//   - Other codes and types are treated as invalid requests (400), which
//     are not retried.
func (e *responsesError) apiError() *model.APIError {
	code := string(e.Code)
	status := http.StatusBadRequest
	if s, ok := errorStatus[code]; ok {
		status = s
	} else if s, ok := errorStatus[e.Type]; ok {
		status = s
	} else if n, err := strconv.Atoi(code); err == nil && n >= 400 && n <= 599 {
		status = n
	} else if code == "" && e.Type == "" {
		status = http.StatusInternalServerError
	}
	return &model.APIError{Provider: providerName, StatusCode: status, Type: cmp.Or(e.Type, code), Code: code, Message: e.Message}
}

type responsesResponse struct {
	ID                string             `json:"id"`
	Model             string             `json:"model"`
	Status            string             `json:"status"`
	IncompleteDetails *incompleteDetails `json:"incomplete_details"`
	Output            []json.RawMessage  `json:"output"`
	Usage             *responsesUsage    `json:"usage"`
	Error             *responsesError    `json:"error"`
}

// outputItem holds the fields of the output item types agentx converts.
type outputItem struct {
	Type string `json:"type"`
	// message
	Phase   string `json:"phase"`
	Content []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	// function_call
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	// reasoning
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

// convertItem converts an output item to a part. refused reports refusal
// content in a message.
func convertItem(raw json.RawMessage) (p model.Part, refused bool, err error) {
	var it outputItem
	if err := json.Unmarshal(raw, &it); err != nil {
		return model.Part{}, false, err
	}
	switch it.Type {
	case "message":
		var b strings.Builder
		for _, c := range it.Content {
			b.WriteString(c.Text)
			b.WriteString(c.Refusal)
			refused = refused || c.Refusal != ""
		}
		p = model.TextPart(b.String())
		if it.Phase != "" {
			p.Provider, p.Raw = providerName, marshal.MustRaw(messageAttrs{Phase: it.Phase})
		}
		return p, refused, nil
	case "function_call":
		return model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{
			ID: it.CallID, Name: it.Name, Arguments: marshal.Arguments([]byte(it.Arguments)),
		}}, false, nil
	case "custom_tool_call":
		p, err := customCallPart(raw)
		return p, false, err
	case "reasoning":
		return model.Part{Type: model.PartReasoning, Text: summaryText(it), Provider: providerName, Raw: slices.Clone(raw)}, false, nil
	}
	// Other items, such as hosted tool calls requested through Extra, are
	// kept so they can be sent back.
	return model.Part{Type: model.PartRaw, Provider: providerName, Raw: slices.Clone(raw)}, false, nil
}

func summaryText(it outputItem) string {
	texts := make([]string, 0, len(it.Summary))
	for _, s := range it.Summary {
		texts = append(texts, s.Text)
	}
	return strings.Join(texts, "\n\n")
}

func (r *responsesResponse) toModel() (*model.Response, error) {
	if r.Status == "failed" {
		if r.Error != nil {
			return nil, r.Error.apiError()
		}
		return nil, (&responsesError{Message: "response failed"}).apiError()
	}
	msg := &model.Message{Role: model.RoleAssistant}
	refused := false
	for _, raw := range r.Output {
		p, ref, err := convertItem(raw)
		if err != nil {
			return nil, err
		}
		refused = refused || ref
		msg.Parts = append(msg.Parts, p)
	}
	fr := responsesFinish(r.Status, r.IncompleteDetails, len(msg.ToolCalls()) > 0, refused)
	return &model.Response{ID: r.ID, Model: r.Model, Message: msg, FinishReason: fr, Usage: r.Usage.toModel()}, nil
}

type incompleteDetails struct {
	Reason string `json:"reason"`
}

func responsesFinish(status string, incomplete *incompleteDetails, hasCalls, refused bool) model.FinishReason {
	switch {
	case refused:
		return model.FinishRefusal
	case status == "incomplete":
		reason := ""
		if incomplete != nil {
			reason = incomplete.Reason
		}
		switch reason {
		case "max_output_tokens":
			return model.FinishLength
		case "content_filter":
			return model.FinishContentFilter
		}
		return model.FinishReason("openai:" + cmp.Or(reason, "incomplete"))
	case hasCalls:
		return model.FinishToolCalls
	case status == "completed":
		return model.FinishStop
	}
	return model.FinishUnknown
}
