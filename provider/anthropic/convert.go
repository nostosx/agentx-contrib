package anthropic

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nostosx/agentx-contrib/provider/internal/jsonschema"
	"github.com/nostosx/agentx-contrib/provider/internal/marshal"
	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

type request struct {
	Model string `json:"model,omitempty"`
	// AnthropicVersion is sent in the body on Vertex AI.
	AnthropicVersion string        `json:"anthropic_version,omitempty"`
	MaxTokens        int           `json:"max_tokens"`
	System           string        `json:"system,omitempty"`
	Messages         []message     `json:"messages"`
	Tools            []toolDef     `json:"tools,omitempty"`
	ToolChoice       *toolChoice   `json:"tool_choice,omitempty"`
	Temperature      *float64      `json:"temperature,omitempty"`
	TopP             *float64      `json:"top_p,omitempty"`
	StopSequences    []string      `json:"stop_sequences,omitempty"`
	Thinking         *thinking     `json:"thinking,omitempty"`
	OutputConfig     *outputConfig `json:"output_config,omitempty"`
	CacheControl     *cacheControl `json:"cache_control,omitempty"`
	Fallbacks        string        `json:"fallbacks,omitempty"`
	Stream           bool          `json:"stream,omitempty"`
}

type message struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type toolDef struct {
	Name                string          `json:"name"`
	Description         string          `json:"description,omitempty"`
	InputSchema         json.RawMessage `json:"input_schema"`
	Strict              bool            `json:"strict,omitempty"`
	EagerInputStreaming bool            `json:"eager_input_streaming,omitempty"`
}

type toolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type thinking struct {
	Type         string        `json:"type"`
	BudgetTokens int           `json:"budget_tokens,omitempty"`
	Display      string        `json:"display,omitempty"`
	BlockBinding *blockBinding `json:"block_binding,omitempty"`
}

type blockBinding struct {
	PrefixMismatchBehavior string `json:"prefix_mismatch_behavior"`
}

type outputConfig struct {
	Effort string        `json:"effort,omitempty"`
	Format *outputFormat `json:"format,omitempty"`
}

type outputFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

type cacheControl struct {
	Type string `json:"type"`
}

// Content blocks.

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// Citations are those of a response text block, sent back with it.
	Citations json.RawMessage `json:"citations,omitempty"`
}

// textAttrs are the attributes of a response text block kept in the Raw
// field of its part.
type textAttrs struct {
	Citations json.RawMessage `json:"citations,omitempty"`
}

type imageBlock struct {
	Type   string      `json:"type"`
	Source imageSource `json:"source"`
}

// imageSource is the source of an image or document block
// (https://platform.claude.com/docs/en/build-with-claude/vision and
// https://platform.claude.com/docs/en/build-with-claude/pdf-support).
type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
	FileID    string `json:"file_id,omitempty"`
}

// documentBlock is a document
// (https://platform.claude.com/docs/en/build-with-claude/citations).
type documentBlock struct {
	Type      string           `json:"type"`
	Source    imageSource      `json:"source"`
	Title     string           `json:"title,omitempty"`
	Citations *citationsConfig `json:"citations,omitempty"`
}

type citationsConfig struct {
	Enabled bool `json:"enabled"`
}

type thinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type redactedBlock struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type toolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type toolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	// Content is a string, or a list of text, image and document blocks
	// (https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls).
	Content any  `json:"content"`
	IsError bool `json:"is_error,omitempty"`
}

// build returns the request body and the headers it needs.
func (m *Model) build(req *model.Request, stream bool) ([]byte, http.Header, error) {
	name := cmp.Or(req.Model, m.model)
	fallback := defaultMaxTokens
	if stream {
		fallback = defaultStreamMaxTokens
	}
	if limit := outputLimit(name); limit > 0 {
		fallback = min(fallback, limit)
	}
	maxTokens := cmp.Or(req.MaxTokens, m.maxTokens, fallback)
	system, msgs, replayed, err := m.convertMessages(req)
	if err != nil {
		return nil, nil, err
	}
	r := request{
		Model:         name,
		MaxTokens:     maxTokens,
		System:        system,
		Messages:      msgs,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		StopSequences: req.Stop,
		Stream:        stream,
	}
	for _, t := range req.Tools {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		} else if t.Strict {
			params = adaptSchema(params)
		}
		r.Tools = append(r.Tools, toolDef{
			Name:                t.Name,
			Description:         t.Description,
			InputSchema:         params,
			Strict:              t.Strict,
			EagerInputStreaming: stream,
		})
	}
	if tc := req.ToolChoice; tc != nil && len(r.Tools) > 0 {
		switch tc.Mode {
		case model.ToolChoiceNone:
			r.ToolChoice = &toolChoice{Type: "none"}
		case model.ToolChoiceRequired:
			r.ToolChoice = &toolChoice{Type: "any"}
		case model.ToolChoiceTool:
			r.ToolChoice = &toolChoice{Type: "tool", Name: tc.Name}
		default:
			r.ToolChoice = &toolChoice{Type: "auto"}
		}
	}
	drop := m.reasoning(&r, req.Reasoning, replayed)
	if t := r.Thinking; t != nil && (t.Type == "adaptive" || t.Type == "enabled") {
		// On models that accept sampling parameters, thinking is
		// incompatible with temperature and accepts top_p only between
		// 0.95 and 1.
		r.Temperature = nil
		if r.TopP != nil && (*r.TopP < 0.95 || *r.TopP > 1) {
			r.TopP = nil
		}
	}
	if rejectsSampling(name) {
		// These models reject any temperature or top_p, with or without
		// thinking.
		r.Temperature, r.TopP = nil, nil
	}
	if f := req.ResponseFormat; f != nil {
		if jsonOutputs(name) {
			if r.OutputConfig == nil {
				r.OutputConfig = &outputConfig{}
			}
			if cites(req.Messages) {
				// https://platform.claude.com/docs/en/build-with-claude/citations
				return nil, nil, model.Permanent(errors.New("anthropic: citations cannot be combined with structured output"))
			}
			r.OutputConfig.Format = &outputFormat{Type: "json_schema", Schema: adaptSchema(f.Schema)}
		} else {
			// The model cannot constrain its answer; ask for the format.
			r.System = strings.TrimPrefix(r.System+"\n\n"+jsonschema.Instruction(f.Schema), "\n\n")
		}
	}
	if !m.noCache {
		r.CacheControl = &cacheControl{Type: "ephemeral"}
	}
	if m.useFallbacks(name) {
		r.Fallbacks = "default"
	}
	if m.vertex {
		// The model is in the URL.
		r.Model, r.AnthropicVersion = "", VertexVersion
	}
	var betas []string
	if drop {
		betas = append(betas, ThinkingBindingBeta)
	}
	body, err := utils.EncodeBody(providerName, r, req.Extra)
	return body, m.headers(name, betas...), err
}

// rejectsSampling reports whether a model rejects the sampling parameters
// temperature, top_p and top_k: Claude Opus 4.7 and later, every Claude
// model of generation 5 and later, and the Fable and Mythos models. Names
// are matched after platform prefixes such as "anthropic." or
// "us.anthropic." and regardless of date or version suffixes. Older models,
// and names that are not Claude model IDs, such as gateway aliases, are
// assumed to accept them.
func rejectsSampling(name string) bool {
	id, ok := claudeID(name)
	if !ok {
		return false
	}
	family, rest, _ := strings.Cut(id, "-")
	switch family {
	case "fable", "mythos":
		return true
	case "opus", "sonnet", "haiku":
		major, minor := version(rest)
		return major >= 5 || (family == "opus" && major == 4 && minor >= 7)
	}
	// Older IDs, such as claude-3-5-sonnet, put the generation first.
	return false
}

// outputLimit returns the most output tokens a Claude model accepts when
// that is below the default max_tokens, or 0: Claude Opus 4 and 4.1 take
// 32000, Claude 3.5 models 8192 and Claude 3 models 4096. Later models
// take at least 64000.
func outputLimit(name string) int {
	id, ok := claudeID(name)
	if !ok {
		return 0
	}
	family, rest, _ := strings.Cut(id, "-")
	switch family {
	case "opus":
		if major, minor := version(rest); major == 4 && minor < 5 {
			return 32000
		}
	case "3":
		// Older IDs, such as claude-3-5-sonnet, put the generation first.
		switch {
		case strings.HasPrefix(rest, "7-"):
		case strings.HasPrefix(rest, "5-"):
			return 8192
		default:
			return 4096
		}
	}
	return 0
}

// claudeID returns the part of a Claude model name after "claude-", such as
// "opus-4-7" or "sonnet-5@20260101", skipping platform prefixes such as
// "anthropic." or "us.anthropic.".
func claudeID(name string) (string, bool) {
	name = strings.ToLower(name)
	if i := strings.Index(name, "claude-"); i > 0 && (name[i-1] == '.' || name[i-1] == '/') {
		name = name[i:]
	}
	return strings.CutPrefix(name, "claude-")
}

// version parses the "<major>-<minor>" prefix of a model version, such as
// "4-7" or "5-20260101"; a minor that looks like a date counts as 0.
func version(s string) (major, minor int) {
	parts := strings.SplitN(s, "-", 3)
	major, err := strconv.Atoi(strings.Split(parts[0], "@")[0])
	if err != nil {
		return 0, 0
	}
	if len(parts) > 1 {
		if n, err := strconv.Atoi(strings.Split(parts[1], "@")[0]); err == nil && n < 100 {
			minor = n
		}
	}
	return major, minor
}

// convertMessages maps neutral messages to Anthropic messages, merging
// consecutive messages with the same role as the API requires. The API
// takes a single system prompt, so system messages are joined, in order,
// after Request.System. It also reports whether any Anthropic thinking
// block is replayed.
func (m *Model) convertMessages(req *model.Request) (string, []message, bool, error) {
	system := []string{}
	if req.System != "" {
		system = append(system, req.System)
	}
	var out []message
	replayed := false
	push := func(role string, blocks []json.RawMessage) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, message{Role: role, Content: blocks})
	}
	c := &converter{vertex: m.vertex}
	for _, msg := range req.Messages {
		switch msg.Role {
		case model.RoleSystem:
			if t := msg.Text(); t != "" {
				system = append(system, t)
			}
		case model.RoleAssistant:
			push("assistant", assistantBlocks(msg))
			replayed = replayed || slices.ContainsFunc(msg.Parts, replayable)
		default:
			blocks, err := c.userBlocks(msg)
			if err != nil {
				return "", nil, false, err
			}
			push("user", blocks)
		}
	}
	if c.cited && c.uncited {
		// https://platform.claude.com/docs/en/build-with-claude/citations
		return "", nil, false, model.Permanent(errors.New("anthropic: citations must be enabled on all documents of a request or on none"))
	}
	return strings.Join(system, "\n\n"), out, replayed, nil
}

// cites reports whether msgs have a document with citations enabled.
func cites(msgs []*model.Message) bool {
	cited := func(p model.Part) bool { return p.Type == model.PartDocument && p.Citations }
	for _, m := range msgs {
		for _, p := range m.Parts {
			if cited(p) || (p.ToolResult != nil && slices.ContainsFunc(p.ToolResult.Parts, cited)) {
				return true
			}
		}
	}
	return false
}

// converter converts user content.
type converter struct {
	// vertex restricts sources to base64 data, the only ones Vertex AI
	// takes.
	vertex bool
	// cited and uncited record documents with and without citations.
	cited, uncited bool
}

func (c *converter) userBlocks(msg *model.Message) ([]json.RawMessage, error) {
	var blocks []json.RawMessage
	for _, p := range msg.Parts {
		switch {
		case p.Type == model.PartText:
			if p.Text != "" {
				blocks = append(blocks, marshal.MustRaw(textBlock{Type: "text", Text: p.Text}))
			}
		case p.IsMedia():
			b, err := c.mediaBlock(p)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, marshal.MustRaw(b))
		case p.Type == model.PartToolResult:
			if r := p.ToolResult; r != nil {
				content, err := c.resultContent(r)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, marshal.MustRaw(toolResultBlock{Type: "tool_result", ToolUseID: r.CallID, Content: content, IsError: r.IsError}))
			}
		case p.Type == model.PartRaw:
			if p.Provider == providerName && len(p.Raw) > 0 {
				blocks = append(blocks, p.Raw)
			}
		}
	}
	return blocks, nil
}

// resultContent returns the content of a tool result: its text, or a list
// of blocks when it has images or documents.
func (c *converter) resultContent(r *model.ToolResult) (any, error) {
	if !slices.ContainsFunc(r.Parts, func(p model.Part) bool { return p.IsMedia() }) {
		return r.Content, nil
	}
	var blocks []any
	if r.Content != "" {
		blocks = append(blocks, textBlock{Type: "text", Text: r.Content})
	}
	for _, p := range r.Parts {
		switch {
		case p.Type == model.PartText && p.Text != "":
			blocks = append(blocks, textBlock{Type: "text", Text: p.Text})
		case p.IsMedia():
			b, err := c.mediaBlock(p)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, b)
		}
	}
	return blocks, nil
}

// mediaBlock converts an image or document part. Claude takes no audio or
// video (https://platform.claude.com/docs/en/api/messages/create).
func (c *converter) mediaBlock(p model.Part) (any, error) {
	if err := utils.CheckMedia(providerName, p); err != nil {
		return nil, err
	}
	switch p.Type {
	case model.PartImage:
		src, err := c.source(p)
		if err != nil {
			return nil, err
		}
		return imageBlock{Type: "image", Source: src}, nil
	case model.PartDocument:
		src, err := c.documentSource(p)
		if err != nil {
			return nil, err
		}
		b := documentBlock{Type: "document", Source: src, Title: truncateRunes(p.Filename, maxTitle)}
		if p.Citations {
			b.Citations = &citationsConfig{Enabled: true}
			c.cited = true
		} else {
			c.uncited = true
		}
		return b, nil
	}
	return nil, model.Permanent(fmt.Errorf("anthropic: %s input is not supported", p.Type))
}

// maxTitle is the longest document title the API accepts, in characters.
const maxTitle = 500

// source returns the source of an image part, or of a document by URL or
// file ID. The API fetches only http and https URLs, so data: URLs are
// sent as base64 data.
func (c *converter) source(p model.Part) (imageSource, error) {
	if mt, data, ok := utils.InlineData(p); ok {
		return imageSource{Type: "base64", MediaType: mt, Data: base64.StdEncoding.EncodeToString(data)}, nil
	}
	if c.vertex {
		return imageSource{}, model.Permanent(fmt.Errorf("anthropic: Vertex AI takes %s data, not URLs or file IDs", p.Type))
	}
	if p.FileID != "" {
		// Files API IDs need no beta header
		// (https://platform.claude.com/docs/en/build-with-claude/files).
		return imageSource{Type: "file", FileID: p.FileID}, nil
	}
	return imageSource{Type: "url", URL: p.URL}, nil
}

// documentSource returns the source of a document: PDFs and plain text
// are taken (https://platform.claude.com/docs/en/build-with-claude/pdf-support);
// text documents of other types, such as Markdown or CSV, are sent as
// plain text.
func (c *converter) documentSource(p model.Part) (imageSource, error) {
	mt, data, ok := utils.InlineData(p)
	switch {
	case !ok:
		return c.source(p)
	case strings.HasPrefix(mt, "text/"):
		return imageSource{Type: "text", MediaType: "text/plain", Data: string(data)}, nil
	case mt == "application/pdf":
		return imageSource{Type: "base64", MediaType: mt, Data: base64.StdEncoding.EncodeToString(data)}, nil
	}
	return imageSource{}, model.Permanent(fmt.Errorf("anthropic: documents must be PDF or plain text, not %s", mt))
}

// truncateRunes returns s cut to at most n runes.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// ownReasoning reports whether reasoning part p came from Anthropic. Other
// providers tag their signed parts with their name; untagged parts are
// Anthropic's own from sessions recorded before parts were tagged, and are
// kept because tool use with thinking requires the previous thinking blocks.
func ownReasoning(p model.Part) bool {
	return p.Provider == "" || p.Provider == providerName
}

// replayable reports whether part p is replayed as a thinking or
// redacted_thinking block.
func replayable(p model.Part) bool {
	return p.Type == model.PartReasoning && ownReasoning(p) && (p.Signature != "" || (p.Redacted && len(p.Data) > 0))
}

func assistantBlocks(msg *model.Message) []json.RawMessage {
	var blocks []json.RawMessage
	for _, p := range msg.Parts {
		switch p.Type {
		case model.PartText:
			if p.Text != "" {
				blocks = append(blocks, marshal.MustRaw(textBlock{Type: "text", Text: p.Text, Citations: citations(p)}))
			}
		case model.PartReasoning:
			// Reasoning can only be replayed with its signature, and only
			// Anthropic can verify it.
			switch {
			case !replayable(p):
			case p.Redacted && len(p.Data) > 0:
				blocks = append(blocks, marshal.MustRaw(redactedBlock{Type: "redacted_thinking", Data: string(p.Data)}))
			default:
				blocks = append(blocks, marshal.MustRaw(thinkingBlock{Type: "thinking", Thinking: p.Text, Signature: p.Signature}))
			}
		case model.PartToolCall:
			if tc := p.ToolCall; tc != nil {
				blocks = append(blocks, marshal.MustRaw(toolUseBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: marshal.Object(tc.Arguments)}))
			}
		case model.PartRaw:
			if p.Provider == providerName && len(p.Raw) > 0 {
				blocks = append(blocks, p.Raw)
			}
		}
	}
	return blocks
}

// citations returns the citations of a text part from a response.
func citations(p model.Part) json.RawMessage {
	if p.Provider != providerName || len(p.Raw) == 0 {
		return nil
	}
	var attrs textAttrs
	if json.Unmarshal(p.Raw, &attrs) != nil {
		return nil
	}
	return attrs.Citations
}

// textPart returns the part of a text block, keeping its citations
// (https://platform.claude.com/docs/en/build-with-claude/citations) in
// Raw.
func textPart(text string, cites json.RawMessage) model.Part {
	p := model.TextPart(text)
	if c := string(cites); c != "" && c != "null" && c != "[]" {
		p.Provider, p.Raw = providerName, marshal.MustRaw(textAttrs{Citations: cites})
	}
	return p
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func (u usage) toModel() model.Usage {
	return model.Usage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}
}

type response struct {
	ID         string            `json:"id"`
	Model      string            `json:"model"`
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      usage             `json:"usage"`
}

func (r *response) toModel() *model.Response {
	msg := &model.Message{Role: model.RoleAssistant}
	for _, raw := range r.Content {
		if p, ok := parseBlock(raw); ok {
			msg.Parts = append(msg.Parts, p)
		}
	}
	return &model.Response{
		ID:           r.ID,
		Model:        r.Model,
		Message:      msg,
		FinishReason: finishReason(r.StopReason),
		Usage:        r.Usage.toModel(),
	}
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Citations json.RawMessage `json:"citations"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	Data      string          `json:"data"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

func parseBlock(raw json.RawMessage) (model.Part, bool) {
	var b block
	if err := json.Unmarshal(raw, &b); err != nil {
		return model.Part{}, false
	}
	switch b.Type {
	case "text":
		return textPart(b.Text, b.Citations), true
	case "thinking":
		return model.Part{Type: model.PartReasoning, Text: b.Thinking, Signature: b.Signature, Provider: providerName}, true
	case "redacted_thinking":
		return model.Part{Type: model.PartReasoning, Redacted: true, Data: []byte(b.Data), Provider: providerName}, true
	case "tool_use":
		return model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: b.ID, Name: b.Name, Arguments: marshal.Arguments(b.Input)}}, true
	}
	// Server tool blocks and other types are echoed back verbatim.
	return model.Part{Type: model.PartRaw, Provider: providerName, Raw: slices.Clone(raw)}, true
}

func finishReason(s string) model.FinishReason {
	switch s {
	case "end_turn", "stop_sequence":
		return model.FinishStop
	case "max_tokens", "model_context_window_exceeded":
		return model.FinishLength
	case "tool_use":
		return model.FinishToolCalls
	case "pause_turn":
		return model.FinishPause
	case "refusal":
		return model.FinishRefusal
	case "":
		return model.FinishUnknown
	}
	return model.FinishReason(s)
}
