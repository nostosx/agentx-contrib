package gemini

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"uuid"

	"github.com/nostosx/agentx-contrib/provider/internal/jsonschema"
	"github.com/nostosx/agentx-contrib/provider/internal/marshal"
	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

type request struct {
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	Contents          []content         `json:"contents"`
	Tools             []toolGroup       `json:"tools,omitempty"`
	ToolConfig        *toolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text             string            `json:"text,omitempty"`
	MediaResolution  *mediaResolution  `json:"mediaResolution,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
	InlineData       *blob             `json:"inlineData,omitempty"`
	FileData         *fileData         `json:"fileData,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`

	// raw is the part as received. Parts without a neutral form, such as
	// executableCode, are kept as model.PartRaw and sent back verbatim.
	raw json.RawMessage
}

type partFields part

func (p *part) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, (*partFields)(p)); err != nil {
		return err
	}
	p.raw = slices.Clone(b)
	return nil
}

func (p part) MarshalJSON() ([]byte, error) {
	if p.raw != nil {
		return p.raw, nil
	}
	return json.Marshal(partFields(p))
}

// dummySignature is the documented thoughtSignature for function calls that
// Gemini did not produce, such as calls from another provider's history.
// Gemini 3 and later reject the current turn's calls without a signature.
const dummySignature = "skip_thought_signature_validator"

type blob struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
	// DisplayName names the parts of function responses.
	DisplayName string `json:"displayName,omitempty"`
}

type fileData struct {
	MIMEType    string `json:"mimeType,omitempty"`
	FileURI     string `json:"fileUri"`
	DisplayName string `json:"displayName,omitempty"`
}

// mediaResolution is the per-part resolution of Gemini 3 and later
// (https://ai.google.dev/gemini-api/docs/generate-content/media-resolution).
type mediaResolution struct {
	Level string `json:"level"`
}

type functionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type functionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
	// Parts are the media of a multimodal function response
	// (https://ai.google.dev/gemini-api/docs/generate-content/function-calling#multimodal).
	Parts []functionResponsePart `json:"parts,omitempty"`
}

type functionResponsePart struct {
	InlineData *blob     `json:"inlineData,omitempty"`
	FileData   *fileData `json:"fileData,omitempty"`
}

type toolGroup struct {
	FunctionDeclarations []functionDecl `json:"functionDeclarations"`
}

type functionDecl struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

type toolConfig struct {
	FunctionCallingConfig functionCallingConfig `json:"functionCallingConfig"`
}

// Function calling modes. VALIDATED lets the model answer in text or call
// a function, and constrains calls to their declared schema.
const (
	modeAuto      = "AUTO"
	modeAny       = "ANY"
	modeNone      = "NONE"
	modeValidated = "VALIDATED"
)

type functionCallingConfig struct {
	Mode                 string   `json:"mode"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type generationConfig struct {
	MaxOutputTokens    int             `json:"maxOutputTokens,omitempty"`
	Temperature        *float64        `json:"temperature,omitempty"`
	TopP               *float64        `json:"topP,omitempty"`
	Seed               *int            `json:"seed,omitempty"`
	StopSequences      []string        `json:"stopSequences,omitempty"`
	ResponseMIMEType   string          `json:"responseMimeType,omitempty"`
	ResponseJSONSchema json.RawMessage `json:"responseJsonSchema,omitempty"`
	ThinkingConfig     *thinkingConfig `json:"thinkingConfig,omitempty"`
}

type thinkingConfig struct {
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
}

func (m *Model) build(req *model.Request) ([]byte, string, error) {
	name := cmp.Or(req.Model, m.model)
	c := &converter{gemini3: !usesBudget(name), vertex: m.vertex}
	contents, err := c.messages(req.Messages)
	if err != nil {
		return nil, name, err
	}
	r := request{Contents: contents}
	system := []string{}
	if req.System != "" {
		system = append(system, req.System)
	}
	for _, msg := range req.Messages {
		if msg.Role == model.RoleSystem {
			system = append(system, msg.Text())
		}
	}
	if len(req.Tools) > 0 {
		decls := make([]functionDecl, 0, len(req.Tools))
		strict := false
		for _, t := range req.Tools {
			params := t.Parameters
			if t.Strict && len(params) > 0 {
				params = adaptSchema(params)
			}
			decls = append(decls, functionDecl{Name: t.Name, Description: t.Description, ParametersJSONSchema: params})
			strict = strict || t.Strict
		}
		r.Tools = []toolGroup{{FunctionDeclarations: decls}}
		// Gemini has no per-function strict flag; VALIDATED constrains all
		// calls to their schemas, as ANY already does.
		mode := modeAuto
		if strict {
			mode = modeValidated
		}
		cfg := functionCallingConfig{Mode: mode}
		if tc := req.ToolChoice; tc != nil {
			switch tc.Mode {
			case model.ToolChoiceNone:
				cfg.Mode = modeNone
			case model.ToolChoiceRequired:
				cfg.Mode = modeAny
			case model.ToolChoiceTool:
				cfg.Mode = modeAny
				cfg.AllowedFunctionNames = []string{tc.Name}
			}
		}
		if req.ToolChoice != nil || strict {
			r.ToolConfig = &toolConfig{FunctionCallingConfig: cfg}
		}
	}
	gc := &generationConfig{
		MaxOutputTokens: cmp.Or(req.MaxTokens, m.maxTokens),
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		Seed:            req.Seed,
		StopSequences:   req.Stop,
	}
	if f := req.ResponseFormat; f != nil {
		if len(req.Tools) == 0 || jsonWithTools(name) {
			gc.ResponseMIMEType = "application/json"
			gc.ResponseJSONSchema = adaptSchema(f.Schema)
		} else {
			// The model rejects a response schema with functions; ask for
			// the format instead.
			system = append(system, jsonschema.Instruction(f.Schema))
		}
	}
	if len(system) > 0 {
		r.SystemInstruction = &content{Parts: []part{{Text: strings.Join(system, "\n\n")}}}
	}
	if rs := req.Reasoning; rs != nil {
		tc, err := thinking(rs, name)
		if err != nil {
			return nil, name, err
		}
		gc.ThinkingConfig = tc
	}
	r.GenerationConfig = gc
	body, err := utils.EncodeBody(providerName, r, req.ProviderOptions[providerName])
	return body, name, err
}

// thinking converts reasoning options. Gemini rejects thinkingLevel and
// thinkingBudget together, and Gemini 2.5 models accept only a budget, so
// an effort is mapped to a budget for them and to a level for later models.
// See https://ai.google.dev/gemini-api/docs/generate-content/thinking.
func thinking(rs *model.Reasoning, name string) (*thinkingConfig, error) {
	tc := &thinkingConfig{IncludeThoughts: rs.Display != model.DisplayOmitted}
	effort := strings.ToLower(string(rs.Effort))
	switch {
	case rs.BudgetTokens != 0:
		tc.ThinkingBudget = new(clampBudget(name, rs.BudgetTokens))
	case effort == "":
	case usesBudget(name):
		b, ok := effortBudgets[effort]
		if !ok {
			return nil, unknownEffort(string(rs.Effort))
		}
		tc.ThinkingBudget = new(clampBudget(name, b))
	default:
		level, ok := effortLevels[effort]
		if !ok {
			return nil, unknownEffort(string(rs.Effort))
		}
		tc.ThinkingLevel = nearestLevel(name, level)
	}
	return tc, nil
}

func unknownEffort(effort string) error {
	return model.Permanent(fmt.Errorf("gemini: unsupported reasoning effort %q; use none, minimal, low, medium, high, xhigh or max", effort))
}

// usesBudget reports whether a model predates thinkingLevel.
func usesBudget(name string) bool {
	name = strings.TrimPrefix(name, "models/")
	return strings.HasPrefix(name, "gemini-2.") || strings.HasPrefix(name, "gemini-1.")
}

// effortBudgets maps efforts to thinking budgets for Gemini 2.5 models.
// clampBudget fits them to each model's range.
var effortBudgets = map[string]int{
	"none":    0,
	"minimal": 512,
	"low":     2048,
	"medium":  8192,
	"high":    24576,
	"xhigh":   24576,
	"max":     24576,
}

// budgetRange is the thinkingBudget range of a Gemini 2.5 model; off
// reports whether 0 turns thinking off.
type budgetRange struct {
	min, max int
	off      bool
}

// budgetRanges lists the thinkingBudget range of the Gemini 2.5 models.
var budgetRanges = map[string]budgetRange{
	"gemini-2.5-pro":        {min: 128, max: 32768},
	"gemini-2.5-flash":      {max: 24576, off: true},
	"gemini-2.5-flash-lite": {min: 512, max: 24576, off: true},
}

// clampBudget fits a budget to the model's range, so that Gemini 2.5 Pro,
// which cannot turn thinking off, gets its minimum instead of 0. Negative
// budgets, which select dynamic thinking, and budgets for other models are
// sent unchanged.
func clampBudget(name string, b int) int {
	r, ok := lookup(budgetRanges, name)
	if !ok || b < 0 || (b == 0 && r.off) {
		return b
	}
	return min(max(b, r.min), r.max)
}

// effortLevels maps efforts to thinkingLevel values; nearestLevel then
// picks one the model accepts. Gemini 3 and later models cannot turn
// thinking off, so none asks for the lowest level, as minimal does.
// Efforts above high become high.
var effortLevels = map[string]string{
	"none":    levelMinimal,
	"minimal": levelMinimal,
	"low":     "low",
	"medium":  "medium",
	"high":    "high",
	"xhigh":   "high",
	"max":     "high",
}

const levelMinimal = "minimal"

// levels lists thinkingLevel values from lowest to highest.
var levels = []string{levelMinimal, "low", "medium", "high"}

// modelLevels lists the thinkingLevel values each model accepts, from the
// tables at https://ai.google.dev/gemini-api/docs/generate-content/thinking
// and https://ai.google.dev/gemini-api/docs/thinking. Other models are
// assumed to accept low, medium and high.
var modelLevels = map[string][]string{
	"gemini-3.8-flash":            levels[1:],
	"gemini-3.6-flash":            levels,
	"gemini-4-argon":              levels[1:],
	"gemini-3.5-flash-lite":       levels,
	"gemini-3.1-flash-lite":       levels,
	"gemini-3.1-flash-lite-image": {levelMinimal, "high"},
	"gemini-3-flash":              levels,
	"gemini-robotics-er-2":        levels,
	"gemini-3.1-pro-preview":      levels[1:],
	"gemini-3-pro-preview":        {"low", "high"},
}

// nearestLevel returns the level closest to level that the model accepts,
// rounding ties up.
func nearestLevel(name, level string) string {
	allowed, ok := lookup(modelLevels, name)
	if !ok {
		allowed = levels[1:]
	}
	want := slices.Index(levels, level)
	best, bestDist := level, len(levels)
	for _, l := range allowed {
		if d := abs(slices.Index(levels, l) - want); d <= bestDist {
			best, bestDist = l, d
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// lookup returns the entry of table whose key is the longest prefix of the
// model name, ignoring case and a "models/" prefix.
func lookup[T any](table map[string]T, name string) (T, bool) {
	name = strings.ToLower(strings.TrimPrefix(name, "models/"))
	var v T
	found := ""
	for k, e := range table {
		if strings.HasPrefix(name, k) && len(k) > len(found) {
			v, found = e, k
		}
	}
	return v, found != ""
}

// signature returns the thought signature of p if Gemini produced it.
// Signatures from other providers mean nothing to Gemini and are rejected.
func signature(p model.Part) string {
	if p.Provider == providerName {
		return p.Signature
	}
	return ""
}

// converter converts messages to contents.
type converter struct {
	// gemini3 reports whether the model is Gemini 3 or later, which takes
	// media in function responses and per-part media resolution. Older
	// models get the media of tool results after the responses.
	gemini3 bool
	// vertex allows file URIs in function responses, which Vertex AI
	// takes and the Gemini API does not.
	vertex bool
	// names holds the display names of function response parts, which
	// must be unique.
	names map[string]bool
}

func (c *converter) messages(msgs []*model.Message) ([]content, error) {
	var out []content
	push := func(role string, parts []part) {
		if len(parts) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Parts = append(out[n-1].Parts, parts...)
			return
		}
		out = append(out, content{Role: role, Parts: parts})
	}
	for _, msg := range msgs {
		if msg.Role == model.RoleSystem {
			continue
		}
		role := "user"
		if msg.Role == model.RoleAssistant {
			role = "model"
		}
		var parts, attached []part
		signedCall := false // whether a function call in this message has a signature
		firstCall := -1     // index in parts of the message's first function call
		for _, p := range msg.Parts {
			switch p.Type {
			case model.PartText:
				if sig := signature(p); p.Text != "" || sig != "" {
					parts = append(parts, part{Text: p.Text, ThoughtSignature: sig})
				}
			case model.PartReasoning:
				// Only Gemini reasoning carrying a signature is meaningful
				// to replay.
				if sig := signature(p); sig != "" {
					thought := part{Text: p.Text, Thought: true, ThoughtSignature: sig}
					if len(p.Data) > 0 && !p.Redacted {
						// A draft image of an image model.
						thought.InlineData = &blob{MIMEType: p.MediaType, Data: base64.StdEncoding.EncodeToString(p.Data)}
					}
					parts = append(parts, thought)
				}
			case model.PartRaw:
				if p.Provider == providerName && len(p.Raw) > 0 {
					parts = append(parts, part{raw: p.Raw})
				}
			case model.PartImage, model.PartDocument, model.PartAudio, model.PartVideo:
				mp, err := c.mediaPart(p)
				if err != nil {
					return nil, err
				}
				// Media Gemini generated carry a signature to return.
				mp.ThoughtSignature = signature(p)
				parts = append(parts, mp)
			case model.PartToolCall:
				if tc := p.ToolCall; tc != nil {
					sig := signature(p)
					signedCall = signedCall || sig != ""
					if firstCall < 0 {
						firstCall = len(parts)
					}
					parts = append(parts, part{
						FunctionCall:     &functionCall{ID: tc.ID, Name: tc.Name, Args: marshal.Object(tc.Arguments)},
						ThoughtSignature: sig,
					})
				}
			case model.PartToolResult:
				if r := p.ToolResult; r != nil {
					fr, extra, err := c.functionResponse(r)
					if err != nil {
						return nil, err
					}
					parts = append(parts, part{FunctionResponse: fr})
					attached = append(attached, extra...)
				}
			}
		}
		if role == "model" && firstCall >= 0 && !signedCall {
			// Calls Gemini did not produce carry no signature; Gemini
			// requires one on the first call of each step.
			parts[firstCall].ThoughtSignature = dummySignature
		}
		// Media the function responses cannot hold follow them.
		push(role, append(parts, attached...))
	}
	return out, nil
}

// functionResponseTypes are the media types that function responses
// take.
var functionResponseTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/webp": true, "application/pdf": true, "text/plain": true,
}

// functionResponse converts a tool result. Its media go into the response
// as parts referenced from the response object, where the model and the
// media type allow it; other media are returned as parts to send after the
// responses, introduced by a text part.
func (c *converter) functionResponse(r *model.ToolResult) (*functionResponse, []part, error) {
	key := "result"
	if r.IsError {
		key = "error"
	}
	var value any = r.Content
	var obj map[string]any
	if json.Unmarshal([]byte(r.Content), &obj) == nil && !r.IsError {
		value = obj
	}
	fr := &functionResponse{ID: r.CallID, Name: r.Name, Response: map[string]any{key: value}}
	var attached []part
	n := 0
	for _, p := range r.Parts {
		if p.Type == model.PartText {
			if p.Text != "" {
				attached = append(attached, part{Text: p.Text})
			}
			continue
		}
		if !p.IsMedia() {
			continue
		}
		mp, err := c.mediaPart(p)
		if err != nil {
			return nil, nil, err
		}
		inResponse := c.gemini3 && mp.MediaResolution == nil &&
			((mp.InlineData != nil && functionResponseTypes[utils.BaseType(mp.InlineData.MIMEType)]) ||
				(mp.FileData != nil && c.vertex && functionResponseTypes[utils.BaseType(mp.FileData.MIMEType)]))
		if !inResponse {
			attached = append(attached, mp)
			continue
		}
		n++
		name := c.displayName(p)
		frp := functionResponsePart{InlineData: mp.InlineData, FileData: mp.FileData}
		if frp.InlineData != nil {
			frp.InlineData.DisplayName = name
		} else {
			frp.FileData.DisplayName = name
		}
		fr.Parts = append(fr.Parts, frp)
		fr.Response[fmt.Sprintf("part_%d", n)] = map[string]string{"$ref": name}
	}
	if len(attached) > 0 {
		note := part{Text: fmt.Sprintf("[Content returned by tool call %s (%s)]", r.CallID, r.Name)}
		attached = append([]part{note}, attached...)
	}
	return fr, attached, nil
}

// displayName returns a unique display name for a part of a function
// response: the part's file name, or one made up.
func (c *converter) displayName(p model.Part) string {
	if c.names == nil {
		c.names = map[string]bool{}
	}
	name := p.Filename
	unnamed := p
	unnamed.Filename = ""
	for i := len(c.names) + 1; name == "" || c.names[name]; i++ {
		name = fmt.Sprintf("part_%d_%s", i, utils.Filename(unnamed))
	}
	c.names[name] = true
	return name
}

// mediaPart converts an image, document, audio or video part. Gemini reads
// only files it can fetch through fileData, such as Files API URIs, Cloud
// Storage URIs and public URLs, so data: URLs are sent as inline data
// (https://ai.google.dev/gemini-api/docs/generate-content/file-input-methods).
// Files are referred to by URI; a file ID that is a URI is taken as one.
func (c *converter) mediaPart(p model.Part) (part, error) {
	if err := utils.CheckMedia(providerName, p); err != nil {
		return part{}, err
	}
	var out part
	switch mt, data, ok := utils.InlineData(p); {
	case ok:
		out.InlineData = &blob{MIMEType: mt, Data: base64.StdEncoding.EncodeToString(data)}
		if p.MediaType != "" {
			// Keep parameters, such as the rate of PCM audio.
			out.InlineData.MIMEType = p.MediaType
		}
	case p.URL != "":
		out.FileData = &fileData{MIMEType: mediaTypeOf(p), FileURI: p.URL}
	case strings.Contains(p.FileID, "://"):
		out.FileData = &fileData{MIMEType: mediaTypeOf(p), FileURI: p.FileID}
	default:
		return part{}, model.Permanent(fmt.Errorf("gemini: refer to file %s by its URI in the part's URL", p.FileID))
	}
	if d := strings.ToLower(p.Detail); d != "" && d != "auto" {
		if !c.gemini3 {
			return part{}, model.Permanent(errors.New("gemini: per-part media resolution (Part.Detail) needs Gemini 3 or later; set generationConfig.mediaResolution through Request.ProviderOptions[\"gemini\"]"))
		}
		out.MediaResolution = &mediaResolution{Level: "MEDIA_RESOLUTION_" + strings.ToUpper(d)}
	}
	return out, nil
}

// mediaTypeOf returns the media type of a part referenced by URI. YouTube
// URLs go without one, as in the documentation
// (https://ai.google.dev/gemini-api/docs/generate-content/video-understanding).
func mediaTypeOf(p model.Part) string {
	if p.MediaType != "" {
		return p.MediaType
	}
	if u := strings.ToLower(p.URL); strings.Contains(u, "youtube.com/") || strings.Contains(u, "youtu.be/") {
		return ""
	}
	return utils.MediaType(p)
}

type usageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
}

func (u *usageMetadata) toModel() model.Usage {
	if u == nil {
		return model.Usage{}
	}
	return model.Usage{
		// promptTokenCount includes cached tokens.
		InputTokens:     u.PromptTokenCount - u.CachedContentTokenCount,
		CacheReadTokens: u.CachedContentTokenCount,
		// Thinking tokens are billed as output but reported separately.
		OutputTokens:    u.CandidatesTokenCount + u.ThoughtsTokenCount,
		ReasoningTokens: u.ThoughtsTokenCount,
	}
}

type candidate struct {
	Content      *content `json:"content"`
	FinishReason string   `json:"finishReason"`
}

type response struct {
	Candidates     []candidate    `json:"candidates"`
	UsageMetadata  *usageMetadata `json:"usageMetadata"`
	ModelVersion   string         `json:"modelVersion"`
	ResponseID     string         `json:"responseId"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
}

func (r *response) toModel() (*model.Response, error) {
	out := &model.Response{ID: r.ResponseID, Model: r.ModelVersion, Usage: r.UsageMetadata.toModel()}
	if len(r.Candidates) == 0 {
		if r.PromptFeedback != nil && r.PromptFeedback.BlockReason != "" {
			out.Message = &model.Message{Role: model.RoleAssistant}
			out.FinishReason = model.FinishContentFilter
			return out, nil
		}
		return nil, errors.New("gemini: response has no candidates")
	}
	c := r.Candidates[0]
	msg := &model.Message{Role: model.RoleAssistant}
	if c.Content != nil {
		for _, p := range c.Content.Parts {
			if mp, ok := toPart(p); ok {
				msg.Parts = append(msg.Parts, mp)
			}
		}
	}
	out.Message = msg
	out.FinishReason = finishReason(c.FinishReason, len(msg.ToolCalls()) > 0)
	return out, nil
}

// toPart converts a response part by its content. Thoughts, including the
// draft images of image models (inlineData with thought set), become
// reasoning parts, so only the final image is output. Parts that carry a
// thought signature are tagged with the provider, so the signature is only
// sent back to Gemini. Content without a neutral form, such as
// executableCode, becomes a raw part that is sent back verbatim.
func toPart(p part) (model.Part, bool) {
	var mp model.Part
	switch {
	case p.FunctionCall != nil:
		id := p.FunctionCall.ID
		if id == "" {
			id = "call_" + uuid.NewV7().String()
		}
		mp = model.Part{
			Type:     model.PartToolCall,
			ToolCall: &model.ToolCall{ID: id, Name: p.FunctionCall.Name, Arguments: marshal.Arguments(p.FunctionCall.Args)},
		}
	case p.InlineData != nil:
		data, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
		if err != nil {
			return model.Part{}, false
		}
		// Image models return images, and speech models audio
		// (https://ai.google.dev/gemini-api/docs/generate-content/speech-generation).
		mp = model.MediaPart(p.InlineData.MIMEType, data)
		if p.Thought {
			// Image models think in draft images; the final image comes
			// without the thought flag.
			mp.Type = model.PartReasoning
		}
	case p.Text == "" && hasUnknownContent(p.raw):
		return model.Part{Type: model.PartRaw, Provider: providerName, Raw: slices.Clone(p.raw)}, true
	case p.Thought:
		mp = model.Part{Type: model.PartReasoning, Text: p.Text}
	case p.Text != "" || p.ThoughtSignature != "":
		mp = model.TextPart(p.Text)
	default:
		return model.Part{}, false
	}
	if p.ThoughtSignature != "" {
		mp.Signature = p.ThoughtSignature
		mp.Provider = providerName
	}
	return mp, true
}

// knownFields are the part fields with a neutral representation, and the
// metadata fields that accompany any content.
var knownFields = map[string]bool{
	"text": true, "inlineData": true, "fileData": true, "functionCall": true, "functionResponse": true,
	"thought": true, "thoughtSignature": true, "partMetadata": true, "videoMetadata": true, "mediaResolution": true,
}

// hasUnknownContent reports whether a part has content without a neutral
// form, such as executableCode or codeExecutionResult.
func hasUnknownContent(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for k := range fields {
		if !knownFields[k] {
			return true
		}
	}
	return false
}

func finishReason(s string, hasCalls bool) model.FinishReason {
	if hasCalls {
		return model.FinishToolCalls
	}
	switch s {
	case "STOP":
		return model.FinishStop
	case "MAX_TOKENS":
		return model.FinishLength
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII",
		"IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION":
		return model.FinishContentFilter
	case "":
		return model.FinishUnknown
	}
	return model.FinishReason("gemini:" + strings.ToLower(s))
}
