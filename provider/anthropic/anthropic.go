// Package anthropic implements model.Model for the Anthropic Messages API
// using only net/http.
//
// The API takes a single system prompt, so system messages anywhere in
// Request.Messages are joined, in order, after Request.System.
//
// # Media
//
// Image parts become image blocks and document parts document blocks
// (https://platform.claude.com/docs/en/build-with-claude/pdf-support), from
// base64 data, an http or https URL or a Files API file ID; Vertex AI takes
// data only. Documents must be PDF or text; text of any text/* type is
// sent as plain text, titled with Part.Filename. Part.Citations enables
// citations, which must be on for all documents of a request or none and
// exclude native structured output
// (https://platform.claude.com/docs/en/build-with-claude/citations); the
// citations of response text are kept in Part.Raw and sent back with it.
// Tool results with media hold text, image and document blocks. Claude
// takes no audio or video.
//
// # Reasoning
//
// Request.Reasoning maps to the thinking and output_config.effort
// parameters according to what each Claude model accepts
// (https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting):
//
//   - Adaptive-only models (Claude Opus 4.7 and later, generation 5 and
//     later, and Fable and Mythos 5 and later) always get adaptive thinking;
//     BudgetTokens is ignored and Effort is kept.
//   - Extended-only models (Claude Opus, Sonnet and Haiku 4.5, earlier
//     Claude 4 models and Claude Sonnet 3.7) get thinking with a budget:
//     BudgetTokens, or one derived from Effort (low 2048, medium 8192,
//     high 16384, xhigh and max 32000, 8192 otherwise) and kept below
//     max_tokens. A derived budget that leaves no room for the minimum of
//     1024 is not sent, so thinking stays off rather than max_tokens being
//     raised. Effort is sent only to Claude Opus 4.5.
//   - Other models get adaptive thinking, or thinking with a budget when
//     BudgetTokens is set.
//
// Effort "minimal" is sent as "low", and "xhigh" as "high" on models that
// lack it. Effort "none" turns thinking off: with type "disabled" where
// accepted, "between_tools" on Claude Sonnet 5.5, and by omitting thinking
// on extended-only models; models whose thinking is always on (Fable,
// Mythos and Claude Opus 5.5) get effort "low" instead. Names that are not
// Claude model IDs, such as gateway aliases, get adaptive thinking, or
// thinking with BudgetTokens, and Effort unchanged.
//
// Claude Fable 5.1, Opus 5.5, Sonnet 5.5 and Haiku 5.5 check that replayed
// thinking blocks were produced in the same conversation prefix
// (https://platform.claude.com/docs/en/build-with-claude/preserved-thinking).
// Editing earlier history, as context compaction does, makes them fail
// that check. When a request to these models replays thinking with
// adaptive thinking, the provider asks the API to drop the failing blocks
// and every thinking block after them (thinking.block_binding with the
// ThinkingBindingBeta flag), so the request succeeds and the model thinks
// again; dropped blocks are not billed, but the new thinking is. Use
// WithoutThinkingBlockDrop to receive the error instead.
//
// Forced tool use (ToolChoiceRequired or ToolChoiceTool) is rejected with
// a 400 error on every request by Claude Opus 5.5, Sonnet 5.5, Fable 5.1
// and Mythos 5.1, and thinking with a budget supports only the auto and
// none tool choices; the provider sends the tool choice unchanged.
package anthropic

import (
	"cmp"
	"context"
	"errors"
	"iter"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

const (
	// DefaultModel is used when no model is configured.
	DefaultModel = "claude-opus-5-5"
	// DefaultBaseURL is the Anthropic API endpoint.
	DefaultBaseURL = "https://api.anthropic.com"
	// APIVersion is sent in the anthropic-version header.
	APIVersion = "2023-06-01"
	// FallbackBeta enables server-side fallbacks for refused requests.
	FallbackBeta = "server-side-fallback-2026-07-01"
	// ThinkingBindingBeta enables thinking.block_binding, which drops
	// replayed thinking blocks that fail the prefix check.
	ThinkingBindingBeta = "thinking-binding-controls-2026-08-01"

	defaultMaxTokens       = 16000
	defaultStreamMaxTokens = 64000
	providerName           = "anthropic"
)

// fallbackModels support server-side refusal fallbacks.
var fallbackModels = []string{"claude-opus-5-5", "claude-fable-5-1", "claude-opus-5", "claude-sonnet-5-5"}

// Model calls the Anthropic Messages API.
type Model struct {
	http      utils.HTTPClient
	model     string
	maxTokens int
	noCache   bool
	fallbacks *bool
	betas     []string
	keepBlock bool
	// vertex is set by WithVertex.
	vertex bool
}

// Option configures a Model.
type Option func(*Model)

// WithAPIKey sets the API key. Defaults to $ANTHROPIC_API_KEY.
func WithAPIKey(key string) Option { return func(m *Model) { m.http.Header.Set("x-api-key", key) } }

// WithTokenSource calls fn for every request and sends the token it
// returns in an "Authorization: Bearer" header, as Vertex AI requires.
// $ANTHROPIC_API_KEY is then not used. Acquiring and caching tokens is
// fn's job, for example with Application Default Credentials through
// golang.org/x/oauth2/google in your own module; errors it returns keep
// their chain, so retries follow model.IsRetryable.
func WithTokenSource(fn func(context.Context) (string, error)) Option {
	return func(m *Model) { m.http.Token = fn }
}

// VertexVersion is the anthropic_version sent in request bodies on Vertex
// AI.
const VertexVersion = "vertex-2023-10-16"

// WithVertex sends requests to Claude on Google Cloud's Vertex AI in a
// project and region, such as "global", "us", "eu" or "us-east5"
// (https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai):
// the model goes in the URL, .../publishers/anthropic/models/{model}:rawPredict
// or :streamRawPredict, instead of the body, and the body carries
// anthropic_version VertexVersion instead of the anthropic-version header.
// Server-side fallbacks, which Vertex AI does not support, are off unless
// WithFallbacks enables them. Vertex AI needs an OAuth access token, set
// with WithTokenSource; $ANTHROPIC_API_KEY is not sent.
func WithVertex(project, region string) Option {
	return func(m *Model) {
		m.http.BaseURL = utils.VertexBaseURL(project, region, "anthropic")
		m.vertex = true
	}
}

// WithBaseURL sets the API base URL. Defaults to $ANTHROPIC_BASE_URL or
// DefaultBaseURL.
func WithBaseURL(u string) Option { return func(m *Model) { m.http.BaseURL = u } }

// WithModel sets the model name.
func WithModel(name string) Option { return func(m *Model) { m.model = name } }

// WithHTTPClient sets the HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(m *Model) { m.http.Client = c } }

// WithHeader adds a header to every request.
func WithHeader(key, value string) Option { return func(m *Model) { m.http.Header.Add(key, value) } }

// WithMaxTokens sets the default output limit.
func WithMaxTokens(n int) Option { return func(m *Model) { m.maxTokens = n } }

// WithoutPromptCaching disables automatic prompt caching.
func WithoutPromptCaching() Option { return func(m *Model) { m.noCache = true } }

// WithFallbacks enables or disables server-side refusal fallbacks. They are
// enabled by default for models that support them.
func WithFallbacks(enabled bool) Option { return func(m *Model) { m.fallbacks = new(enabled) } }

// WithBeta adds an anthropic-beta flag.
func WithBeta(flag string) Option { return func(m *Model) { m.betas = append(m.betas, flag) } }

// WithoutThinkingBlockDrop stops asking the API to drop replayed thinking
// blocks that fail the prefix check of Claude Fable 5.1, Opus 5.5, Sonnet
// 5.5 and Haiku 5.5. Requests whose earlier history was edited then fail
// with a 400 error instead of the model thinking again.
func WithoutThinkingBlockDrop() Option { return func(m *Model) { m.keepBlock = true } }

// New returns a Model.
func New(opts ...Option) (*Model, error) {
	m := &Model{
		http: utils.HTTPClient{
			Provider: providerName,
			Header:   http.Header{},
		},
		model: DefaultModel,
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.http.BaseURL == "" {
		m.http.BaseURL = cmp.Or(os.Getenv("ANTHROPIC_BASE_URL"), DefaultBaseURL)
	}
	if !m.vertex && m.http.Token == nil && !utils.HasAuth(m.http.Header, "x-api-key") {
		if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
			m.http.Header.Set("x-api-key", key)
		}
	}
	if !m.vertex && m.http.Header.Get("anthropic-version") == "" {
		m.http.Header.Set("anthropic-version", APIVersion)
	}
	if m.http.Token == nil && !utils.HasAuth(m.http.Header, "x-api-key") {
		if m.vertex {
			return nil, errors.New("anthropic: Vertex AI needs an access token; use WithTokenSource")
		}
		return nil, errors.New("anthropic: no API key; set ANTHROPIC_API_KEY or use WithAPIKey")
	}
	return m, nil
}

// path returns the request path: the Messages API, or the model's method
// on Vertex AI.
func (m *Model) path(name string, stream bool) string {
	if !m.vertex {
		return "/v1/messages"
	}
	method := ":rawPredict"
	if stream {
		method = ":streamRawPredict"
	}
	return "/models/" + url.PathEscape(name) + method
}

// headers returns the per-request headers, adding the beta flags in extra
// that are not already configured.
func (m *Model) headers(name string, extra ...string) http.Header {
	betas := slices.Clone(m.betas)
	if m.useFallbacks(name) {
		extra = append(extra, FallbackBeta)
	}
	for _, b := range extra {
		if !slices.Contains(betas, b) {
			betas = append(betas, b)
		}
	}
	h := http.Header{}
	if len(betas) > 0 {
		h.Set("anthropic-beta", strings.Join(betas, ","))
	}
	return h
}

func (m *Model) useFallbacks(name string) bool {
	if m.fallbacks != nil {
		return *m.fallbacks
	}
	return !m.vertex && slices.Contains(fallbackModels, name)
}

// Describe implements model.Describer.
func (m *Model) Describe() model.Description {
	host, port := utils.ServerOf(m.http.BaseURL)
	provider := providerName
	if m.vertex {
		provider = "gcp.vertex_ai"
	}
	return model.Description{Provider: provider, Operation: "chat", Model: m.model, ServerAddress: host, ServerPort: port}
}

// Generate implements model.Model.
func (m *Model) Generate(ctx context.Context, req *model.Request) (*model.Response, error) {
	body, header, err := m.build(req, false)
	if err != nil {
		return nil, err
	}
	var out response
	if err := m.http.PostJSON(ctx, m.path(cmp.Or(req.Model, m.model), false), body, header, &out); err != nil {
		return nil, err
	}
	return out.toModel(), nil
}

// Stream implements model.Model.
func (m *Model) Stream(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		body, header, err := m.build(req, true)
		if err != nil {
			yield(nil, err)
			return
		}
		resp, err := m.http.PostStream(ctx, m.path(cmp.Or(req.Model, m.model), true), body, header)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()
		readStream(resp.Body, yield)
	}
}
