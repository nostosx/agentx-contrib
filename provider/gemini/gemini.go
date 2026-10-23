// Package gemini implements model.Model for the Google Gemini API using only
// net/http.
//
// The API takes a single system instruction, so system messages anywhere in
// Request.Messages are joined, in order, after Request.System.
//
// # Media
//
// Image, document, audio and video parts are sent as inlineData, or as
// fileData for URLs such as Files API URIs, Cloud Storage URIs, public
// URLs and YouTube videos
// (https://ai.google.dev/gemini-api/docs/generate-content/file-input-methods);
// data: URLs go inline. Part.Detail becomes the per-part mediaResolution
// of Gemini 3 and later models
// (https://ai.google.dev/gemini-api/docs/generate-content/media-resolution).
// Gemini 3 and later models take PNG, JPEG and WebP images, PDFs and plain
// text in function responses, which refer to them by display name
// (https://ai.google.dev/gemini-api/docs/generate-content/function-calling#multimodal);
// Vertex AI takes file URIs there too. Other media of tool results, and
// all of them for older models, follow the function responses in the same
// turn. Media in responses, such as the images of image models and the
// audio of speech models, become parts of the matching type; chunks of
// streamed PCM audio form one part.
package gemini

import (
	"cmp"
	"context"
	"errors"
	"iter"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

const (
	// DefaultModel is used when no model is configured.
	DefaultModel = "gemini-3.8-flash"
	// DefaultBaseURL is the Gemini API endpoint.
	DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

	providerName = "gemini"
)

// Model calls the Gemini generateContent API, which Google still fully
// supports alongside the newer Interactions API.
type Model struct {
	http      utils.HTTPClient
	model     string
	maxTokens int
	// vertex is set by WithVertex.
	vertex bool
	// taskType, dimensions and batchSize configure an Embedder.
	taskType   string
	dimensions int
	batchSize  int
}

// Option configures a Model.
type Option func(*Model)

// WithAPIKey sets the API key. Defaults to $GEMINI_API_KEY or
// $GOOGLE_API_KEY.
func WithAPIKey(key string) Option {
	return func(m *Model) { m.http.Header.Set("x-goog-api-key", key) }
}

// WithTokenSource calls fn for every request and sends the token it
// returns in an "Authorization: Bearer" header, as Vertex AI requires.
// API keys from the environment are then not used. Acquiring and caching
// tokens is fn's job, for example with Application Default Credentials
// through golang.org/x/oauth2/google in your own module; errors it returns
// keep their chain, so retries follow model.IsRetryable.
func WithTokenSource(fn func(context.Context) (string, error)) Option {
	return func(m *Model) { m.http.Token = fn }
}

// WithVertex sends requests to Gemini on Vertex AI in a Google Cloud
// project and location, such as "global", "us", "eu" or "us-central1":
// https://{host}/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:generateContent
// (https://docs.cloud.google.com/vertex-ai/generative-ai/docs/learn/locations).
// Vertex AI needs an OAuth access token, set with WithTokenSource; API
// keys from the environment are not sent.
func WithVertex(project, location string) Option {
	return func(m *Model) {
		m.http.BaseURL = utils.VertexBaseURL(project, location, "google")
		m.vertex = true
	}
}

// WithBaseURL sets the API base URL. Defaults to $GEMINI_BASE_URL or
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

// New returns a Model.
func New(opts ...Option) (*Model, error) {
	m := newModel(opts)
	if m.model == "" {
		m.model = DefaultModel
	}
	if err := m.resolve(); err != nil {
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

// resolve applies the environment where the options set nothing; API keys
// are not read with a token source or for Vertex AI.
func (m *Model) resolve() error {
	if m.http.BaseURL == "" {
		m.http.BaseURL = cmp.Or(os.Getenv("GEMINI_BASE_URL"), DefaultBaseURL)
	}
	if !m.vertex && m.http.Token == nil && !utils.HasAuth(m.http.Header, "x-goog-api-key") {
		if key := cmp.Or(os.Getenv("GEMINI_API_KEY"), os.Getenv("GOOGLE_API_KEY")); key != "" {
			m.http.Header.Set("x-goog-api-key", key)
		}
	}
	if m.http.Token == nil && !utils.HasAuth(m.http.Header, "x-goog-api-key") {
		if m.vertex {
			return errors.New("gemini: Vertex AI needs an access token; use WithTokenSource")
		}
		return errors.New("gemini: no API key; set GEMINI_API_KEY or use WithAPIKey")
	}
	return nil
}

// path returns the URL path of method for the model name, which may be
// written with the API's "models/" prefix.
func (m *Model) path(name, method string) string {
	return modelPath(name, method)
}

func modelPath(name, method string) string {
	return "/models/" + url.PathEscape(strings.TrimPrefix(name, "models/")) + ":" + method
}

// Describe implements model.Describer.
func (m *Model) Describe() model.Description {
	host, port := utils.ServerOf(m.http.BaseURL)
	provider := "gcp.gemini"
	if m.vertex {
		provider = "gcp.vertex_ai"
	}
	return model.Description{Provider: provider, Operation: "generate_content", Model: m.model, ServerAddress: host, ServerPort: port}
}

// Generate implements model.Model.
func (m *Model) Generate(ctx context.Context, req *model.Request) (*model.Response, error) {
	body, name, err := m.build(req)
	if err != nil {
		return nil, err
	}
	var out response
	if err := m.http.PostJSON(ctx, m.path(name, "generateContent"), body, nil, &out); err != nil {
		return nil, err
	}
	return out.toModel()
}

// Stream implements model.Model.
func (m *Model) Stream(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		body, name, err := m.build(req)
		if err != nil {
			yield(nil, err)
			return
		}
		resp, err := m.http.PostStream(ctx, m.path(name, "streamGenerateContent")+"?alt=sse", body, nil)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()
		readStream(resp.Body, yield)
	}
}
