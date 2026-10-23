package bedrock

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nostosx/agentx-contrib/provider/internal/jsonschema"
	"github.com/nostosx/agentx-contrib/provider/internal/marshal"
	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

// The Converse request body
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html#API_runtime_Converse_RequestSyntax).
// The model ID is part of the URL.
type request struct {
	Messages                     []message        `json:"messages,omitempty"`
	System                       []any            `json:"system,omitempty"`
	InferenceConfig              *inferenceConfig `json:"inferenceConfig,omitempty"`
	ToolConfig                   *toolConfig      `json:"toolConfig,omitempty"`
	OutputConfig                 *outputConfig    `json:"outputConfig,omitempty"`
	GuardrailConfig              *guardrailConfig `json:"guardrailConfig,omitempty"`
	AdditionalModelRequestFields map[string]any   `json:"additionalModelRequestFields,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type inferenceConfig struct {
	MaxTokens     int      `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

type toolConfig struct {
	Tools      []any          `json:"tools"`
	ToolChoice map[string]any `json:"toolChoice,omitempty"`
}

type toolSpec struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema inputSchema `json:"inputSchema"`
	Strict      bool        `json:"strict,omitempty"`
}

type inputSchema struct {
	JSON json.RawMessage `json:"json"`
}

type outputConfig struct {
	TextFormat textFormat `json:"textFormat"`
}

type textFormat struct {
	Type      string          `json:"type"`
	Structure formatStructure `json:"structure"`
}

type formatStructure struct {
	JSONSchema jsonSchemaDefinition `json:"jsonSchema"`
}

// jsonSchemaDefinition carries the schema as a JSON string
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_JsonSchemaDefinition.html).
type jsonSchemaDefinition struct {
	Schema string `json:"schema"`
	Name   string `json:"name,omitempty"`
}

type guardrailConfig struct {
	GuardrailIdentifier  string `json:"guardrailIdentifier,omitempty"`
	GuardrailVersion     string `json:"guardrailVersion,omitempty"`
	Trace                string `json:"trace,omitempty"`
	StreamProcessingMode string `json:"streamProcessingMode,omitempty"`
}

// Content blocks are unions with one member set.

type textBlock struct {
	Text string `json:"text"`
}

type cachePointBlock struct {
	CachePoint cachePoint `json:"cachePoint"`
}

type cachePoint struct {
	Type string `json:"type"`
}

type imageBlock struct {
	Image image `json:"image"`
}

type image struct {
	Format string      `json:"format"`
	Source imageSource `json:"source"`
}

// imageSource is the source of an image, document, video or audio block
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ImageSource.html).
// Bytes are encoded as base64 in JSON.
type imageSource struct {
	Bytes      []byte      `json:"bytes,omitempty"`
	S3Location *s3Location `json:"s3Location,omitempty"`
}

type documentBlock struct {
	Document document `json:"document"`
}

// document is a DocumentBlock
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_DocumentBlock.html).
type document struct {
	Format    string           `json:"format"`
	Name      string           `json:"name"`
	Source    imageSource      `json:"source"`
	Citations *citationsConfig `json:"citations,omitempty"`
}

type citationsConfig struct {
	Enabled bool `json:"enabled"`
}

type videoBlock struct {
	Video media `json:"video"`
}

type audioBlock struct {
	Audio media `json:"audio"`
}

// media is a VideoBlock
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_VideoBlock.html)
// or an AudioBlock
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_AudioBlock.html).
type media struct {
	Format string      `json:"format"`
	Source imageSource `json:"source"`
}

type s3Location struct {
	URI string `json:"uri"`
}

type toolUseBlock struct {
	ToolUse toolUse `json:"toolUse"`
}

type toolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Type      string          `json:"type,omitempty"`
}

type toolResultBlock struct {
	ToolResult toolResult `json:"toolResult"`
}

// toolResult is a ToolResultBlock, whose content holds text, image,
// document and video blocks
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ToolResultContentBlock.html).
type toolResult struct {
	ToolUseID string `json:"toolUseId"`
	Content   []any  `json:"content"`
	Status    string `json:"status,omitempty"`
}

type reasoningBlock struct {
	ReasoningContent reasoningContent `json:"reasoningContent"`
}

type reasoningContent struct {
	ReasoningText   *reasoningText `json:"reasoningText,omitempty"`
	RedactedContent []byte         `json:"redactedContent,omitempty"`
}

type reasoningText struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

// build returns the Converse request body for model name.
func (m *Model) build(req *model.Request, name string, stream bool) ([]byte, error) {
	native := m.nativeStructured(name)
	useTools := len(req.Tools) > 0 && (req.ToolChoice == nil || req.ToolChoice.Mode != model.ToolChoiceNone)
	system, msgs, err := convertMessages(req, name, useTools)
	if err != nil {
		return nil, model.Permanent(err)
	}
	r := request{Messages: msgs}
	if f := req.ResponseFormat; f != nil {
		if native {
			r.OutputConfig = &outputConfig{TextFormat: textFormat{
				Type: "json_schema",
				Structure: formatStructure{JSONSchema: jsonSchemaDefinition{
					Schema: string(compact(adaptSchema(f.Schema))),
					Name:   f.Name,
				}},
			}}
		} else {
			// The model cannot constrain its answer; ask for the format.
			system = strings.TrimPrefix(system+"\n\n"+jsonschema.Instruction(f.Schema), "\n\n")
		}
	}
	if system != "" {
		r.System = []any{textBlock{Text: system}}
		if m.caching {
			r.System = append(r.System, cachePointBlock{CachePoint: cachePoint{Type: "default"}})
		}
	}
	if m.caching && len(r.Messages) > 0 {
		last := &r.Messages[len(r.Messages)-1]
		last.Content = append(last.Content, cachePointBlock{CachePoint: cachePoint{Type: "default"}})
	}
	if useTools {
		r.ToolConfig = m.tools(req, native)
	}
	ic := inferenceConfig{
		MaxTokens:     cmp.Or(req.MaxTokens, m.maxTokens),
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		StopSequences: req.Stop,
	}
	r.AdditionalModelRequestFields = reasoning(name, req.Reasoning, &ic)
	if ic.MaxTokens > 0 || ic.Temperature != nil || ic.TopP != nil || len(ic.StopSequences) > 0 {
		r.InferenceConfig = &ic
	}
	if g := m.guardrail; g != nil {
		r.GuardrailConfig = &guardrailConfig{
			GuardrailIdentifier: g.Identifier,
			GuardrailVersion:    g.Version,
			Trace:               g.Trace,
		}
		if stream {
			r.GuardrailConfig.StreamProcessingMode = g.StreamProcessingMode
		}
	}
	return utils.EncodeBody(providerName, r, req.Extra)
}

// tools returns the tool configuration of req.
func (m *Model) tools(req *model.Request, native bool) *toolConfig {
	tc := &toolConfig{}
	for _, t := range req.Tools {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		strict := t.Strict && native
		if strict {
			params = adaptSchema(params)
		}
		tc.Tools = append(tc.Tools, map[string]toolSpec{"toolSpec": {
			Name:        t.Name,
			Description: t.Description,
			InputSchema: inputSchema{JSON: params},
			Strict:      strict,
		}})
	}
	if m.caching {
		tc.Tools = append(tc.Tools, cachePointBlock{CachePoint: cachePoint{Type: "default"}})
	}
	if c := req.ToolChoice; c != nil {
		switch c.Mode {
		case model.ToolChoiceRequired:
			tc.ToolChoice = map[string]any{"any": struct{}{}}
		case model.ToolChoiceTool:
			tc.ToolChoice = map[string]any{"tool": map[string]string{"name": c.Name}}
		case model.ToolChoiceAuto:
			tc.ToolChoice = map[string]any{"auto": struct{}{}}
		}
	}
	return tc
}

// nativeStructured reports whether model name gets structured output
// through outputConfig and strict tools.
func (m *Model) nativeStructured(name string) bool {
	if m.structured != nil {
		return *m.structured
	}
	return jsonOutputs(name)
}

// convertMessages maps neutral messages to Converse messages, merging
// consecutive messages with the same role. System messages are joined,
// in order, after Request.System. Without tools, tool calls and results
// are sent as text, because Converse rejects toolUse and toolResult blocks
// without a toolConfig.
func convertMessages(req *model.Request, name string, tools bool) (string, []message, error) {
	var system []string
	if req.System != "" {
		system = append(system, req.System)
	}
	var out []message
	push := func(role string, blocks []any) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, message{Role: role, Content: blocks})
	}
	c := &converter{tools: tools, status: toolStatus(name)}
	for _, msg := range req.Messages {
		if msg == nil {
			continue
		}
		if msg.Role == model.RoleSystem {
			if t := msg.Text(); t != "" {
				system = append(system, t)
			}
			continue
		}
		role := "user"
		if msg.Role == model.RoleAssistant {
			role = "assistant"
		}
		var blocks []any
		for _, p := range msg.Parts {
			b, err := c.contentBlocks(p, role)
			if err != nil {
				return "", nil, err
			}
			blocks = append(blocks, b...)
		}
		push(role, blocks)
	}
	for _, m := range out {
		if hasBlock[documentBlock](m.Content) && !hasBlock[textBlock](m.Content) {
			// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Message.html
			return "", nil, errors.New("bedrock: a message with a document needs text as well")
		}
	}
	return strings.Join(system, "\n\n"), out, nil
}

// hasBlock reports whether blocks hold a block of type T.
func hasBlock[T any](blocks []any) bool {
	return slices.ContainsFunc(blocks, func(b any) bool { _, ok := b.(T); return ok })
}

// converter converts parts to content blocks.
type converter struct {
	// tools reports whether the request has tools; without them, tool
	// calls and results are sent as text.
	tools bool
	// status reports whether the model takes the status of tool results.
	status bool
	// names holds the document names in use, which are made unique.
	names map[string]bool
}

// contentBlocks converts a part, returning no blocks for parts that are
// not sent.
func (c *converter) contentBlocks(p model.Part, role string) ([]any, error) {
	switch p.Type {
	case model.PartText:
		if strings.TrimSpace(p.Text) != "" {
			return []any{textBlock{Text: p.Text}}, nil
		}
	case model.PartImage:
		b, err := c.mediaBlock(p)
		if err != nil {
			return nil, err
		}
		return []any{b}, nil
	case model.PartDocument, model.PartAudio, model.PartVideo:
		// Converse takes media in user messages only; media of other
		// providers' answers are not sent.
		if role != "user" {
			return nil, nil
		}
		b, err := c.mediaBlock(p)
		if err != nil {
			return nil, err
		}
		return []any{b}, nil
	case model.PartToolCall:
		tc := p.ToolCall
		if tc == nil {
			return nil, nil
		}
		if !c.tools {
			return []any{textBlock{Text: fmt.Sprintf("[Tool call %s (%s) with input: %s]", tc.Name, tc.ID, marshal.Object(tc.Arguments))}}, nil
		}
		return []any{toolUseBlock{ToolUse: toolUse{ToolUseID: tc.ID, Name: tc.Name, Input: marshal.Object(tc.Arguments)}}}, nil
	case model.PartToolResult:
		if p.ToolResult == nil {
			return nil, nil
		}
		return c.toolResultBlocks(p.ToolResult)
	case model.PartReasoning:
		// Reasoning is replayed only with what Bedrock can verify: a
		// signature or encrypted content it issued.
		if role != "assistant" || p.Provider != providerName {
			return nil, nil
		}
		switch {
		case p.Redacted && len(p.Data) > 0:
			return []any{reasoningBlock{ReasoningContent: reasoningContent{RedactedContent: p.Data}}}, nil
		case p.Signature != "":
			return []any{reasoningBlock{ReasoningContent: reasoningContent{ReasoningText: &reasoningText{Text: p.Text, Signature: p.Signature}}}}, nil
		}
	case model.PartRaw:
		if p.Provider == providerName && len(p.Raw) > 0 {
			if !c.tools && isToolBlock(p.Raw) {
				return nil, nil
			}
			return []any{p.Raw}, nil
		}
	}
	return nil, nil
}

// toolResultBlocks converts a tool result. Its text, images, documents
// and videos go into the result; audio, which results cannot hold, and
// all media of results sent as text without tools follow it in the same
// message.
func (c *converter) toolResultBlocks(r *model.ToolResult) ([]any, error) {
	var content, after []any
	if r.Content != "" || len(r.Parts) == 0 {
		content = append(content, textBlock{Text: r.Content})
	}
	for _, p := range r.Parts {
		switch {
		case p.Type == model.PartText && p.Text != "":
			content = append(content, textBlock{Text: p.Text})
		case p.IsMedia():
			b, err := c.mediaBlock(p)
			if err != nil {
				return nil, err
			}
			if _, audio := b.(audioBlock); audio || !c.tools {
				after = append(after, b)
			} else {
				content = append(content, b)
			}
		}
	}
	if len(after) > 0 {
		after = append([]any{textBlock{Text: fmt.Sprintf("[Content returned by tool call %s (%s)]", r.CallID, r.Name)}}, after...)
	}
	if !c.tools {
		kind := "result"
		if r.IsError {
			kind = "error"
		}
		var text []string
		for _, b := range content {
			if t, ok := b.(textBlock); ok {
				text = append(text, t.Text)
			}
		}
		return append([]any{textBlock{Text: fmt.Sprintf("[Tool %s for call %s: %s]", kind, r.CallID, strings.Join(text, "\n"))}}, after...), nil
	}
	if len(content) == 0 {
		content = append(content, textBlock{Text: ""})
	}
	tr := toolResult{ToolUseID: r.CallID, Content: content}
	if r.IsError && c.status {
		tr.Status = "error"
	}
	return append([]any{toolResultBlock{ToolResult: tr}}, after...), nil
}

// mediaBlock converts an image, document, audio or video part. Converse
// takes bytes or an Amazon S3 location, so data: URLs are decoded and
// other URLs and file IDs are rejected.
func (c *converter) mediaBlock(p model.Part) (any, error) {
	if err := utils.CheckMedia(providerName, p); err != nil {
		return nil, err
	}
	if p.FileID != "" && len(p.Data) == 0 && p.URL == "" {
		return nil, model.Permanent(fmt.Errorf("bedrock: %s file IDs are not supported; use data or an s3:// URL", p.Type))
	}
	src, mediaType, err := source(p)
	if err != nil {
		return nil, err
	}
	switch p.Type {
	case model.PartImage:
		format := imageFormats[mediaType]
		if format == "" && len(src.Bytes) > 0 {
			// Detect the format of bytes without a known media type.
			format = imageFormats[http.DetectContentType(src.Bytes)]
		}
		if format == "" {
			return nil, unsupported(p, mediaType, "PNG, JPEG, GIF and WebP")
		}
		return imageBlock{Image: image{Format: format, Source: src}}, nil
	case model.PartDocument:
		format := documentFormats[mediaType]
		if format == "" && strings.HasPrefix(mediaType, "text/") {
			format = "txt"
		}
		if format == "" {
			return nil, unsupported(p, mediaType, "PDF, CSV, DOC, DOCX, XLS, XLSX, HTML, TXT and Markdown")
		}
		d := document{Format: format, Name: c.documentName(p), Source: src}
		if p.Citations {
			// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_CitationsConfig.html
			if (format != "pdf" && format != "txt") || src.S3Location != nil {
				return nil, model.Permanent(errors.New("bedrock: citations need a PDF or text document given as data"))
			}
			d.Citations = &citationsConfig{Enabled: true}
		}
		return documentBlock{Document: d}, nil
	case model.PartVideo:
		format := videoFormats[mediaType]
		if format == "" {
			return nil, unsupported(p, mediaType, "MKV, MOV, MP4, WebM, FLV, MPEG, WMV and 3GP")
		}
		return videoBlock{Video: media{Format: format, Source: src}}, nil
	case model.PartAudio:
		format := audioFormats[mediaType]
		if format == "" {
			return nil, unsupported(p, mediaType, "MP3, Opus, WAV, AAC, FLAC, MP4, OGG, Matroska, M4A, MPEG, PCM and WebM")
		}
		return audioBlock{Audio: media{Format: format, Source: src}}, nil
	}
	return nil, model.Permanent(fmt.Errorf("bedrock: %s input is not supported", p.Type))
}

func unsupported(p model.Part, mediaType, accepted string) error {
	if mediaType == "" {
		return model.Permanent(fmt.Errorf("bedrock: unknown %s format; set the part's media type", p.Type))
	}
	return model.Permanent(fmt.Errorf("bedrock: unsupported %s type %q; Converse accepts %s", p.Type, mediaType, accepted))
}

// source returns the source of a media part and its media type.
func source(p model.Part) (imageSource, string, error) {
	if mt, data, ok := utils.InlineData(p); ok {
		return imageSource{Bytes: data}, mt, nil
	}
	if strings.HasPrefix(p.URL, "s3://") {
		return imageSource{S3Location: &s3Location{URI: p.URL}}, utils.MediaType(p), nil
	}
	return imageSource{}, "", model.Permanent(fmt.Errorf("bedrock: %s URLs must be data: or s3:// URLs", p.Type))
}

// documentName returns the name of a document. Converse requires one of
// alphanumeric characters, single spaces, hyphens, parentheses and square
// brackets, up to 200 characters
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_DocumentBlock.html),
// so the file name loses its extension and other characters become
// hyphens. Names are made unique within a request.
func (c *converter) documentName(p model.Part) string {
	name := strings.TrimSuffix(p.Filename, path.Ext(p.Filename))
	var b strings.Builder
	space := false
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-()[]", r):
			b.WriteRune(r)
			space = false
		case unicode.IsSpace(r):
			if !space {
				b.WriteByte(' ')
			}
			space = true
		default:
			b.WriteByte('-')
			space = false
		}
	}
	name = strings.TrimSpace(b.String())
	if name == "" {
		name = "document"
	}
	name = truncateRunes(name, maxNameLength-8)
	if c.names == nil {
		c.names = map[string]bool{}
	}
	unique := name
	for i := 2; c.names[unique]; i++ {
		unique = fmt.Sprintf("%s (%d)", name, i)
	}
	c.names[unique] = true
	return unique
}

// maxNameLength is the longest document name Converse accepts.
const maxNameLength = 200

// truncateRunes returns s cut to at most n runes.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n]))
}

// isToolBlock reports whether a raw block is a toolUse or toolResult block.
func isToolBlock(raw json.RawMessage) bool {
	var b map[string]json.RawMessage
	if json.Unmarshal(raw, &b) != nil {
		return false
	}
	_, use := b["toolUse"]
	_, result := b["toolResult"]
	return use || result
}

// imageFormats maps media types to the image formats Converse accepts
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ImageBlock.html).
var imageFormats = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpeg",
	"image/jpg":  "jpeg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// documentFormats maps media types to the document formats Converse
// accepts. Other text types are sent as txt.
var documentFormats = map[string]string{
	"application/pdf":    "pdf",
	"text/csv":           "csv",
	"application/msword": "doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "docx",
	"application/vnd.ms-excel": "xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": "xlsx",
	"text/html":     "html",
	"text/plain":    "txt",
	"text/markdown": "md",
}

// videoFormats maps media types to the video formats Converse accepts.
var videoFormats = map[string]string{
	"video/x-matroska": "mkv",
	"video/quicktime":  "mov",
	"video/mp4":        "mp4",
	"video/webm":       "webm",
	"video/x-flv":      "flv",
	"video/mpeg":       "mpeg",
	"video/mpg":        "mpg",
	"video/x-ms-wmv":   "wmv",
	"video/3gpp":       "three_gp",
}

// audioFormats maps media types to the audio formats Converse accepts.
var audioFormats = map[string]string{
	"audio/mpeg":       "mp3",
	"audio/mp3":        "mp3",
	"audio/opus":       "opus",
	"audio/wav":        "wav",
	"audio/wave":       "wav",
	"audio/x-wav":      "wav",
	"audio/vnd.wave":   "wav",
	"audio/aac":        "aac",
	"audio/x-aac":      "x-aac",
	"audio/flac":       "flac",
	"audio/x-flac":     "flac",
	"audio/mp4":        "mp4",
	"audio/x-m4a":      "m4a",
	"audio/m4a":        "m4a",
	"audio/ogg":        "ogg",
	"audio/x-matroska": "mka",
	"audio/webm":       "webm",
	"audio/pcm":        "pcm",
	"audio/l16":        "pcm",
}

// toolStatus reports whether a model accepts the status of tool results,
// which the API reference limits to Amazon Nova and Claude models
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ToolResultBlock.html).
func toolStatus(name string) bool {
	_, claude := claudeID(name)
	return claude || strings.Contains(strings.ToLower(name), "amazon.nova-")
}

// compact returns raw without insignificant whitespace.
func compact(raw json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return raw
	}
	return b.Bytes()
}

// Response.

type response struct {
	Output struct {
		Message *struct {
			Content []json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      usage  `json:"usage"`
}

type usage struct {
	InputTokens           int `json:"inputTokens"`
	OutputTokens          int `json:"outputTokens"`
	CacheReadInputTokens  int `json:"cacheReadInputTokens"`
	CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
}

func (u usage) toModel() model.Usage {
	return model.Usage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheWriteInputTokens,
	}
}

func (r *response) toModel() *model.Response {
	msg := &model.Message{Role: model.RoleAssistant}
	if r.Output.Message != nil {
		for _, raw := range r.Output.Message.Content {
			if p, ok := parseBlock(raw); ok {
				msg.Parts = append(msg.Parts, p)
			}
		}
	}
	return &model.Response{
		Message:      msg,
		FinishReason: finishReason(r.StopReason),
		Usage:        r.Usage.toModel(),
	}
}

// block is a content block of a response.
type block struct {
	Text             *string           `json:"text"`
	ToolUse          *toolUse          `json:"toolUse"`
	ReasoningContent *reasoningContent `json:"reasoningContent"`
	Image            *image            `json:"image"`
}

func parseBlock(raw json.RawMessage) (model.Part, bool) {
	var b block
	if err := json.Unmarshal(raw, &b); err != nil {
		return model.Part{}, false
	}
	switch {
	case b.Text != nil:
		return model.TextPart(*b.Text), true
	case b.ToolUse != nil && b.ToolUse.Type == "":
		tu := b.ToolUse
		return model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: tu.ToolUseID, Name: tu.Name, Arguments: marshal.Arguments(tu.Input)}}, true
	case b.ReasoningContent != nil && len(b.ReasoningContent.RedactedContent) > 0:
		return model.Part{Type: model.PartReasoning, Redacted: true, Data: b.ReasoningContent.RedactedContent, Provider: providerName}, true
	case b.Image != nil && len(b.Image.Source.Bytes) > 0:
		// Image output
		// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ContentBlock.html).
		return model.ImagePart(imageType(b.Image.Format), b.Image.Source.Bytes), true
	case b.ReasoningContent != nil && b.ReasoningContent.ReasoningText != nil:
		rt := b.ReasoningContent.ReasoningText
		return model.Part{Type: model.PartReasoning, Text: rt.Text, Signature: rt.Signature, Provider: providerName}, true
	}
	// Server tool use, citations and other blocks are echoed back verbatim.
	return model.Part{Type: model.PartRaw, Provider: providerName, Raw: slices.Clone(raw)}, true
}

// finishReason maps a Converse stop reason
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html#bedrock-runtime_Converse-response-stopReason).
func finishReason(s string) model.FinishReason {
	switch s {
	case "end_turn", "stop_sequence":
		return model.FinishStop
	case "max_tokens", "model_context_window_exceeded":
		return model.FinishLength
	case "tool_use":
		return model.FinishToolCalls
	case "guardrail_intervened", "content_filtered":
		return model.FinishContentFilter
	case "":
		return model.FinishUnknown
	}
	// malformed_model_output and malformed_tool_use have no neutral
	// equivalent.
	return model.FinishReason(s)
}

// imageType returns the media type of a Converse image format.
func imageType(format string) string {
	if format == "" {
		return ""
	}
	return "image/" + format
}
