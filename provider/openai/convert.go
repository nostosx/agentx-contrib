package openai

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nostosx/kitx/dataurl"

	"github.com/nostosx/agentx-contrib/provider/internal/marshal"
	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

type request struct {
	Model               string          `json:"model"`
	Messages            []message       `json:"messages"`
	Tools               []toolDef       `json:"tools,omitempty"`
	ToolChoice          any             `json:"tool_choice,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	Seed                *int            `json:"seed,omitempty"`
	Stop                []string        `json:"stop,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	ResponseFormat      *responseFormat `json:"response_format,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
	StreamOptions       *streamOptions  `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type responseFormat struct {
	Type       string      `json:"type"`
	JSONSchema *jsonSchema `json:"json_schema,omitempty"`
}

type jsonSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict,omitempty"`
}

type message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// ReasoningContent is sent back to services that need it.
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// Audio refers to a previous audio response of the model.
	Audio *audioRef `json:"audio,omitempty"`
}

type audioRef struct {
	ID string `json:"id"`
}

// contentPart is a content part of a Chat Completions message
// (https://developers.openai.com/api/docs/guides/images-vision,
// https://developers.openai.com/api/docs/guides/file-inputs and
// https://developers.openai.com/api/docs/guides/audio-chat-completions).
type contentPart struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	ImageURL   *imageURL   `json:"image_url,omitempty"`
	File       *chatFile   `json:"file,omitempty"`
	InputAudio *inputAudio `json:"input_audio,omitempty"`
}

type imageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type chatFile struct {
	Filename string `json:"filename,omitempty"`
	FileData string `json:"file_data,omitempty"`
	FileID   string `json:"file_id,omitempty"`
}

type inputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

// imageSource returns the URL of an image part; inline data becomes a data:
// URL.
func imageSource(p model.Part) (string, error) {
	if len(p.Data) > 0 {
		encoded, err := dataurl.Encode(cmp.Or(p.MediaType, http.DetectContentType(p.Data)), p.Data)
		if err != nil {
			return "", model.Permanent(fmt.Errorf("openai: encode image: %w", err))
		}
		return encoded, nil
	}
	return p.URL, nil
}

// audioFormats maps media types to the input_audio formats of Chat
// Completions, which takes WAV and MP3.
var audioFormats = map[string]string{
	"audio/wav":      "wav",
	"audio/wave":     "wav",
	"audio/x-wav":    "wav",
	"audio/vnd.wave": "wav",
	"audio/mpeg":     "mp3",
	"audio/mp3":      "mp3",
}

// chatPart converts a media part of a user message. Text documents become
// text, since Chat Completions takes only PDF files. official reports
// whether the request goes to OpenAI's API, which rejects other formats;
// compatible services get them to decide.
func (m *Model) chatPart(p model.Part, official bool) (contentPart, error) {
	if err := utils.CheckMedia(providerName, p, m.providerName()); err != nil {
		return contentPart{}, err
	}
	switch p.Type {
	case model.PartImage:
		if p.FileID != "" {
			return contentPart{}, model.Permanent(errors.New("openai: Chat Completions does not take image file IDs; use the Responses API or the image data"))
		}
		source, err := imageSource(p)
		if err != nil {
			return contentPart{}, err
		}
		return contentPart{Type: "image_url", ImageURL: &imageURL{URL: source, Detail: p.Detail}}, nil
	case model.PartDocument:
		if p.FileID != "" {
			return contentPart{Type: "file", File: &chatFile{FileID: p.FileID}}, nil
		}
		mt, data, ok := utils.InlineData(p)
		if !ok {
			return contentPart{}, model.Permanent(errors.New("openai: Chat Completions does not take document URLs; use the Responses API or the document data"))
		}
		if strings.HasPrefix(mt, "text/") {
			return contentPart{Type: "text", Text: documentText(p, data)}, nil
		}
		if official && mt != "application/pdf" {
			return contentPart{}, model.Permanent(fmt.Errorf("openai: Chat Completions takes PDF documents, not %s; use the Responses API", mt))
		}
		encoded, err := dataurl.Encode(mt, data)
		if err != nil {
			return contentPart{}, model.Permanent(fmt.Errorf("openai: encode file: %w", err))
		}
		return contentPart{Type: "file", File: &chatFile{Filename: utils.Filename(p), FileData: encoded}}, nil
	case model.PartAudio:
		mt, data, ok := utils.InlineData(p)
		if !ok {
			return contentPart{}, model.Permanent(errors.New("openai: Chat Completions takes audio data, not URLs or file IDs"))
		}
		format := audioFormats[mt]
		if format == "" {
			if official {
				return contentPart{}, model.Permanent(fmt.Errorf("openai: Chat Completions takes WAV and MP3 audio, not %s", mt))
			}
			_, format, _ = strings.Cut(mt, "/")
		}
		return contentPart{Type: "input_audio", InputAudio: &inputAudio{Data: base64.StdEncoding.EncodeToString(data), Format: format}}, nil
	}
	return contentPart{}, model.Permanent(fmt.Errorf("openai: %s input is not supported", p.Type))
}

// documentText returns a text document as text, headed by its name.
func documentText(p model.Part, data []byte) string {
	if p.Filename == "" {
		return string(data)
	}
	return "Document " + p.Filename + ":\n\n" + string(data)
}

// providerName returns the name of the configured service, such as a
// preset's, which may tag the file IDs of its parts.
func (m *Model) providerName() string {
	if m.preset != nil {
		return m.preset.name
	}
	return providerName
}

// attachmentsNote introduces the parts of tool results that are sent in a
// user message, because tool messages take only text.
func attachmentsNote(r *model.ToolResult) string {
	return fmt.Sprintf("[Content returned by tool call %s (%s)]", r.CallID, r.Name)
}

type toolCall struct {
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type toolDef struct {
	Type     string      `json:"type"`
	Function functionDef `json:"function"`
}

type functionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      bool            `json:"strict,omitempty"`
}

func (m *Model) build(req *model.Request, stream bool) ([]byte, error) {
	msgs, err := m.convertMessages(req)
	if err != nil {
		return nil, err
	}
	r := request{
		Model:       cmp.Or(req.Model, m.model),
		Messages:    msgs,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Seed:        req.Seed,
		Stop:        req.Stop,
	}
	maxTokens := cmp.Or(req.MaxTokens, m.maxTokens)
	if m.legacyMaxTokens {
		r.MaxTokens = maxTokens
	} else {
		r.MaxCompletionTokens = maxTokens
	}
	for _, t := range req.Tools {
		r.Tools = append(r.Tools, toolDef{Type: "function", Function: functionDef{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  strictSchema(t.Parameters, t.Strict),
			Strict:      t.Strict,
		}})
	}
	if tc := req.ToolChoice; tc != nil && len(r.Tools) > 0 {
		switch tc.Mode {
		case model.ToolChoiceTool:
			r.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": tc.Name}}
		case model.ToolChoiceNone, model.ToolChoiceRequired, model.ToolChoiceAuto:
			r.ToolChoice = string(tc.Mode)
		}
	}
	if rs := req.Reasoning; rs != nil {
		r.ReasoningEffort = string(rs.Effort)
		if rs.Effort != "" && rs.Effort != "none" && m.officialAPI() {
			// As with Responses, OpenAI reasoning models reject sampling
			// parameters unless reasoning is off. Compatible services,
			// such as Gemini, Groq, OpenRouter or vLLM, accept both, so
			// they get what the request sets.
			r.Temperature, r.TopP = nil, nil
		}
	}
	if f := req.ResponseFormat; f != nil {
		r.ResponseFormat = &responseFormat{Type: "json_schema", JSONSchema: &jsonSchema{Name: cmp.Or(f.Name, "response"), Schema: strictSchema(f.Schema, f.Strict), Strict: f.Strict}}
	}
	if stream {
		r.Stream = true
		r.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	m.quirks().apply(&r, req)
	return utils.EncodeBody(providerName, r, req.ProviderOptions[providerName])
}

// officialAPI reports whether the base URL is OpenAI's API or an Azure
// OpenAI resource, which serve OpenAI's own models.
func (m *Model) officialAPI() bool {
	u, err := url.Parse(m.http.BaseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "api.openai.com" || strings.HasSuffix(host, ".openai.azure.com")
}

// convertMessages converts the messages of req. Tool messages take only
// text, so the other parts of tool results follow in a user message.
func (m *Model) convertMessages(req *model.Request) ([]message, error) {
	official := m.officialAPI()
	var out []message
	if req.System != "" {
		out = append(out, message{Role: "system", Content: req.System})
	}
	for _, msg := range req.Messages {
		switch msg.Role {
		case model.RoleSystem:
			out = append(out, message{Role: "system", Content: msg.Text()})
		case model.RoleAssistant:
			out = append(out, assistantMessage(msg))
		default:
			// Tool results become tool messages; other content, and the
			// parts of tool results, form a user message after them.
			var parts []contentPart
			add := func(p model.Part) error {
				switch {
				case p.Type == model.PartText:
					if p.Text != "" {
						parts = append(parts, contentPart{Type: "text", Text: p.Text})
					}
				case p.IsMedia():
					cp, err := m.chatPart(p, official)
					if err != nil {
						return err
					}
					parts = append(parts, cp)
				}
				return nil
			}
			for _, p := range msg.Parts {
				if p.Type != model.PartToolResult {
					continue
				}
				r := p.ToolResult
				if r == nil {
					continue
				}
				content := resultText(r)
				if slices.ContainsFunc(r.Parts, isContent) {
					if content == "" {
						content = "[The content follows in the next message.]"
					}
					parts = append(parts, contentPart{Type: "text", Text: attachmentsNote(r)})
					for _, rp := range r.Parts {
						if err := add(rp); err != nil {
							return nil, err
						}
					}
				}
				out = append(out, message{Role: "tool", ToolCallID: r.CallID, Content: content})
			}
			for _, p := range msg.Parts {
				if p.Type != model.PartToolResult {
					if err := add(p); err != nil {
						return nil, err
					}
				}
			}
			if len(parts) == 0 {
				continue
			}
			um := message{Role: "user", Name: msg.Name}
			if len(parts) == 1 && parts[0].Type == "text" {
				um.Content = parts[0].Text
			} else {
				um.Content = parts
			}
			out = append(out, um)
		}
	}
	return out, nil
}

// isContent reports whether a part of a tool result is sent: text or
// media.
func isContent(p model.Part) bool {
	return (p.Type == model.PartText && p.Text != "") || p.IsMedia()
}

func assistantMessage(msg *model.Message) message {
	am := message{Role: "assistant", Name: msg.Name}
	var text strings.Builder
	text.WriteString(msg.Text())
	for _, p := range msg.Parts {
		if p.Type != model.PartAudio {
			continue
		}
		// Audio the model produced is referred to by its ID until it
		// expires; after that, and for audio from elsewhere, its
		// transcript stands in.
		if id := audioID(p); id != "" {
			am.Audio = &audioRef{ID: id}
		} else {
			text.WriteString(p.Text)
		}
	}
	if text.Len() > 0 {
		am.Content = text.String()
	}
	for _, tc := range msg.ToolCalls() {
		args := string(marshal.Object(tc.Arguments))
		am.ToolCalls = append(am.ToolCalls, toolCall{ID: tc.ID, Type: "function", Function: functionCall{Name: tc.Name, Arguments: args}})
	}
	if am.Content == nil && len(am.ToolCalls) == 0 {
		am.Content = ""
	}
	return am
}

// audioAttrs identify an audio response of Chat Completions, kept in the
// Raw field of its part.
type audioAttrs struct {
	ID        string `json:"id"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

// audioID returns the ID of an audio response that can still be referred
// to, or "".
func audioID(p model.Part) string {
	if p.Provider != providerName || len(p.Raw) == 0 {
		return ""
	}
	var a audioAttrs
	if json.Unmarshal(p.Raw, &a) != nil || a.ID == "" || (a.ExpiresAt > 0 && time.Now().Unix() >= a.ExpiresAt) {
		return ""
	}
	return a.ID
}

// chatAudio is the audio of a Chat Completions response message or delta.
type chatAudio struct {
	ID         string `json:"id"`
	Data       string `json:"data"`
	ExpiresAt  int64  `json:"expires_at"`
	Transcript string `json:"transcript"`
}

// audioPart converts audio output. The format is the one requested, which
// the response does not repeat, so it is detected from the content;
// headerless PCM, which streaming returns, is reported as audio/pcm.
func audioPart(a *chatAudio) (model.Part, error) {
	data, err := base64.StdEncoding.DecodeString(a.Data)
	if err != nil {
		return model.Part{}, fmt.Errorf("openai: decode audio: %w", err)
	}
	p := model.Part{Type: model.PartAudio, Data: data, Text: a.Transcript, Provider: providerName}
	if len(data) > 0 {
		p.MediaType = audioType(data)
	}
	if a.ID != "" || a.ExpiresAt != 0 {
		p.Raw = marshal.MustRaw(audioAttrs{ID: a.ID, ExpiresAt: a.ExpiresAt})
	}
	return p, nil
}

func audioType(data []byte) string {
	if mt := utils.BaseType(http.DetectContentType(data)); strings.HasPrefix(mt, "audio/") {
		return mt
	}
	return "audio/pcm"
}

type usage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u *usage) toModel() model.Usage {
	if u == nil {
		return model.Usage{}
	}
	out := model.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
	if d := u.PromptTokensDetails; d != nil {
		// OpenAI includes cached tokens in prompt_tokens.
		out.CacheReadTokens = d.CachedTokens
		out.InputTokens -= d.CachedTokens
	}
	if d := u.CompletionTokensDetails; d != nil {
		out.ReasoningTokens = d.ReasoningTokens
	}
	return out
}

type response struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content          *string    `json:"content"`
			Refusal          *string    `json:"refusal"`
			ReasoningContent string     `json:"reasoning_content"`
			Reasoning        reasoning  `json:"reasoning"`
			ToolCalls        []toolCall `json:"tool_calls"`
			Audio            *chatAudio `json:"audio"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usage `json:"usage"`
}

func (r *response) toModel() (*model.Response, error) {
	if len(r.Choices) == 0 {
		return nil, errors.New("openai: response has no choices")
	}
	c := r.Choices[0]
	msg := &model.Message{Role: model.RoleAssistant}
	if rc := cmp.Or(c.Message.ReasoningContent, string(c.Message.Reasoning)); rc != "" {
		msg.Parts = append(msg.Parts, model.Part{Type: model.PartReasoning, Text: rc})
	}
	if c.Message.Content != nil && *c.Message.Content != "" {
		msg.Parts = append(msg.Parts, model.TextPart(*c.Message.Content))
	}
	if c.Message.Refusal != nil && *c.Message.Refusal != "" {
		msg.Parts = append(msg.Parts, model.TextPart(*c.Message.Refusal))
	}
	if a := c.Message.Audio; a != nil {
		p, err := audioPart(a)
		if err != nil {
			return nil, err
		}
		msg.Parts = append(msg.Parts, p)
	}
	for _, tc := range c.Message.ToolCalls {
		msg.Parts = append(msg.Parts, model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Arguments: marshal.Arguments([]byte(tc.Function.Arguments)),
		}})
	}
	fr := finishReason(c.FinishReason)
	if c.Message.Refusal != nil && *c.Message.Refusal != "" {
		fr = model.FinishRefusal
	}
	return &model.Response{ID: r.ID, Model: r.Model, Message: msg, FinishReason: fr, Usage: r.Usage.toModel()}, nil
}

func finishReason(s string) model.FinishReason {
	switch s {
	case "stop":
		return model.FinishStop
	case "length":
		return model.FinishLength
	case "tool_calls", "function_call":
		return model.FinishToolCalls
	case "content_filter":
		return model.FinishContentFilter
	case "":
		return model.FinishUnknown
	}
	return model.FinishReason(s)
}
