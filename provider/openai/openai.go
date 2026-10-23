// Package openai implements model.Model for the OpenAI API using only
// net/http.
//
// Models use the Responses API by default, which OpenAI recommends for new
// projects and requires for tool calls with reasoning on recent models.
// Requests are stateless (store is false): the conversation, including the
// encrypted reasoning that stateless responses return by default, is sent
// with every request. WithChatCompletions selects the Chat Completions API
// for compatible services that do not implement Responses.
//
// Request.Reasoning.Effort is sent unchanged, and the values accepted
// depend on the model; for example, gpt-6-astra rejects "none".
//
// Both APIs accept system messages anywhere in the conversation, so system
// messages in Request.Messages are sent in place, after Request.System.
//
// # Media
//
// The Responses API takes image parts as input_image and document parts as
// input_file, from data, a URL or a file ID; documents may be PDF, text,
// Word, Excel and other types
// (https://developers.openai.com/api/docs/guides/file-inputs). Tool results
// with media send them in the function output
// (https://developers.openai.com/api/docs/guides/function-calling). It
// takes no audio or video.
//
// Chat Completions takes images by URL or data, PDF documents as data or a
// file ID, text documents as text, and WAV and MP3 audio as data
// (https://developers.openai.com/api/docs/guides/audio-chat-completions).
// Other formats are left for compatible services to judge. Tool messages
// take only text, so the media of tool results follow the results in a
// user message. Audio of audio models becomes an audio part with the
// transcript in Text, which is sent back by its ID until it expires and as
// its transcript after that.
package openai

import (
	"cmp"
	"context"
	"fmt"
	"iter"
	"net/http"
	"os"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

const (
	// DefaultModel is used when no model is configured.
	DefaultModel = "gpt-6-astra"
	// DefaultBaseURL is the OpenAI API endpoint.
	DefaultBaseURL = "https://api.openai.com/v1"

	providerName = "openai"
)

// Model calls the Responses or Chat Completions API.
type Model struct {
	http      utils.HTTPClient
	model     string
	maxTokens int
	// chat selects the Chat Completions API instead of Responses.
	chat bool
	// legacyMaxTokens sends max_tokens instead of max_completion_tokens for
	// compatible services that predate the newer field.
	legacyMaxTokens bool
	noKey           bool
	// encrypted asks for encrypted reasoning with the legacy include value.
	encrypted bool
	// apiKey is the key set with WithAPIKey, sent once New knows the
	// service's credential header.
	apiKey string
	// preset describes the service; nil for OpenAI's API.
	preset *preset
	// azure sends API keys in the api-key header of Azure OpenAI.
	azure bool
	// dimensions and batchSize configure an Embedder.
	dimensions int
	batchSize  int
	// noFreeform sends freeform tools as functions; see WithFreeformTools.
	noFreeform bool
}

// Option configures a Model.
type Option func(*Model)

// WithAPIKey sets the API key. Defaults to $OPENAI_API_KEY, or to the
// variable of a preset such as DeepSeek. It is sent as a bearer token, or
// in the api-key header with WithAzure.
func WithAPIKey(key string) Option { return func(m *Model) { m.apiKey = key } }

// WithTokenSource calls fn for every request and sends the token it
// returns in an "Authorization: Bearer" header, for example a Microsoft
// Entra ID token for Azure OpenAI. API keys from the environment are then
// not used. Acquiring and caching tokens is fn's job; errors it returns
// keep their chain, so retries follow model.IsRetryable.
func WithTokenSource(fn func(context.Context) (string, error)) Option {
	return func(m *Model) { m.http.Token = fn }
}

// WithBaseURL sets the API base URL, for example of an OpenAI-compatible
// service. Defaults to $OPENAI_BASE_URL or DefaultBaseURL, or to a
// preset's URL.
func WithBaseURL(u string) Option { return func(m *Model) { m.http.BaseURL = u } }

// WithModel sets the model name.
func WithModel(name string) Option { return func(m *Model) { m.model = name } }

// WithHTTPClient sets the HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(m *Model) { m.http.Client = c } }

// WithHeader adds a header to every request.
func WithHeader(key, value string) Option { return func(m *Model) { m.http.Header.Add(key, value) } }

// WithOrganization sets the OpenAI-Organization header.
func WithOrganization(org string) Option {
	return func(m *Model) { m.http.Header.Set("OpenAI-Organization", org) }
}

// WithMaxTokens sets the default output limit.
func WithMaxTokens(n int) Option { return func(m *Model) { m.maxTokens = n } }

// WithLegacyMaxTokens sends max_tokens instead of max_completion_tokens. It
// applies to the Chat Completions API.
func WithLegacyMaxTokens() Option { return func(m *Model) { m.legacyMaxTokens = true } }

// WithChatCompletions uses the Chat Completions API instead of Responses,
// for OpenAI-compatible services that only implement Chat Completions.
// Recent OpenAI models do not support tool calls with reasoning there.
//
// With a reasoning effort other than none, temperature and top_p are
// dropped for the OpenAI API and Azure OpenAI, whose reasoning models
// reject them, and sent to other services, which accept both. Set them
// through Request.Extra to send them regardless.
func WithChatCompletions() Option { return func(m *Model) { m.chat = true } }

// WithEncryptedReasoning decides whether Responses requests send the
// legacy include value "reasoning.encrypted_content". Stateless requests
// return reasoning items with encrypted content by default, and OpenAI
// still accepts the value for compatibility
// (https://developers.openai.com/api/docs/guides/reasoning), so it is not
// sent unless enabled, for example for a compatible service that needs it.
func WithEncryptedReasoning(enabled bool) Option {
	return func(m *Model) { m.encrypted = enabled }
}

// WithoutAPIKey allows requests without credentials, for local servers.
func WithoutAPIKey() Option { return func(m *Model) { m.noKey = true } }

// New returns a Model.
func New(opts ...Option) (*Model, error) {
	m := newModel(opts)
	if err := m.resolve(DefaultModel, func(p *preset) string { return p.model }); err != nil {
		return nil, err
	}
	return m, nil
}

func newModel(opts []Option) *Model {
	m := &Model{http: utils.HTTPClient{Provider: providerName, Header: http.Header{}}}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// resolve fills in what the options left unset: the base URL, the API key
// from the environment, and the model, taken from the preset or the given
// default. Environment variables apply only when the options set no value,
// so a preset never receives OpenAI's key.
func (m *Model) resolve(fallback string, presetModel func(*preset) string) error {
	p := m.preset
	if p == nil {
		p = &preset{name: providerName, keyEnv: "OPENAI_API_KEY", baseURLEnv: "OPENAI_BASE_URL", baseURL: DefaultBaseURL}
	}
	if m.http.BaseURL == "" {
		m.http.BaseURL = p.baseURL
		if p.baseURLEnv != "" {
			m.http.BaseURL = cmp.Or(os.Getenv(p.baseURLEnv), p.baseURL)
		}
	}
	key := m.apiKey
	if key == "" && m.http.Token == nil && !utils.HasAuth(m.http.Header, "api-key") && p.keyEnv != "" {
		key = os.Getenv(p.keyEnv)
	}
	if key != "" {
		if m.azure {
			m.http.Header.Set("api-key", key)
		} else {
			m.http.Header.Set("Authorization", "Bearer "+key)
		}
	}
	if m.model == "" {
		m.model = presetModel(p)
		if m.preset == nil {
			m.model = fallback
		}
	}
	if m.model == "" {
		return fmt.Errorf("openai: %s needs a model name; use WithModel", p.title())
	}
	if !m.noKey && !p.keyOptional && m.http.Token == nil && !utils.HasAuth(m.http.Header, "api-key") {
		if p.keyEnv == "" {
			return fmt.Errorf("openai: no API key for %s; use WithAPIKey", p.title())
		}
		return fmt.Errorf("openai: no API key; set %s or use WithAPIKey", p.keyEnv)
	}
	return nil
}

// Describe implements model.Describer.
func (m *Model) Describe() model.Description {
	host, port := utils.ServerOf(m.http.BaseURL)
	provider := providerName
	if m.preset != nil {
		provider = m.preset.name
	}
	return model.Description{Provider: provider, Operation: "chat", Model: m.model, ServerAddress: host, ServerPort: port}
}

// Generate implements model.Model.
func (m *Model) Generate(ctx context.Context, req *model.Request) (*model.Response, error) {
	if !m.chat {
		body, err := m.buildResponses(req, false)
		if err != nil {
			return nil, err
		}
		var out responsesResponse
		if err := m.http.PostJSON(ctx, "/responses", body, nil, &out); err != nil {
			return nil, err
		}
		return out.toModel()
	}
	body, err := m.build(req, false)
	if err != nil {
		return nil, err
	}
	var out response
	if err := m.http.PostJSON(ctx, "/chat/completions", body, nil, &out); err != nil {
		return nil, err
	}
	return out.toModel()
}

// Stream implements model.Model.
func (m *Model) Stream(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		path, build, read := "/chat/completions", m.build, readStream
		if !m.chat {
			path, build, read = "/responses", m.buildResponses, readResponsesStream
		}
		body, err := build(req, true)
		if err != nil {
			yield(nil, err)
			return
		}
		resp, err := m.http.PostStream(ctx, path, body, nil)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()
		read(resp.Body, yield)
	}
}
