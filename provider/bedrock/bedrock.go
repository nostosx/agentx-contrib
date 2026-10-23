// Package bedrock implements model.Model for the Amazon Bedrock Runtime
// Converse and ConverseStream APIs using only net/http
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html,
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ConverseStream.html).
//
// The model name is any model ID the APIs accept: a base model ID such as
// "anthropic.claude-opus-5-5", a cross-Region inference profile ID such as
// "us.anthropic.claude-opus-5-5" or "global.anthropic.claude-opus-5-5", or
// the ARN of an inference profile, a provisioned model or a custom model.
//
// # Authentication
//
// Requests are signed with AWS Signature Version 4 for the "bedrock"
// service, or carry a Bedrock API key as a bearer token
// (https://docs.aws.amazon.com/bedrock/latest/userguide/api-keys-use.html).
// The last of WithAPIKey, WithCredentials and WithCredentialsProvider
// applies. Without them, the provider uses $AWS_BEARER_TOKEN_BEDROCK, or
// else $AWS_ACCESS_KEY_ID, $AWS_SECRET_ACCESS_KEY and $AWS_SESSION_TOKEN.
// Use WithCredentialsProvider for other sources, such as profiles, IAM
// roles or SSO, and to refresh temporary credentials; it is called for
// every request.
//
// The region comes from WithRegion, $AWS_REGION or $AWS_DEFAULT_REGION,
// and the endpoint from WithBaseURL, $AWS_ENDPOINT_URL_BEDROCK_RUNTIME,
// $AWS_ENDPOINT_URL or https://bedrock-runtime.<region>.amazonaws.com. A
// region is needed for signing; with a custom endpoint whose host names
// it, such as bedrock-runtime-fips.us-east-1.amazonaws.com, it is taken
// from there.
//
// # Requests
//
// System messages anywhere in Request.Messages are joined, in order, after
// Request.System. Converse has no tool choice that forbids tool use, and it
// rejects toolUse and toolResult blocks without a toolConfig, so with
// ToolChoiceNone, or without tools, no toolConfig is sent and earlier tool
// calls and results are sent as text.
//
// Request.Extra is merged into the Converse request body, so use
// "additionalModelRequestFields" for model parameters; it replaces the
// fields the provider generates, such as Claude's thinking settings.
//
// # Media
//
// Image, document, video and audio parts of user messages become image,
// document, video and audio blocks
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ContentBlock.html).
// They must carry their bytes, a data: URL or an s3:// URL; the format
// comes from the media type, or the extension of an S3 object. Documents
// are PDF, CSV, DOC, DOCX, XLS, XLSX, HTML, Markdown or other text; their
// names are derived from Part.Filename to fit Converse's character rules,
// and Part.Citations enables citations for PDF and text documents given as
// data. A message with a document needs text as well. Media of assistant
// messages other than images are not sent. Tool results hold text,
// images, documents and videos; audio, which they cannot hold, follows
// the result in the same message. Image blocks of responses become image
// parts.
//
// # Reasoning
//
// Request.Reasoning is sent in additionalModelRequestFields for the model
// families whose parameters Bedrock documents; it is ignored for others.
//
//   - Claude models get thinking and output_config.effort as the model
//     accepts them
//     (https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-adaptive-thinking.html,
//     https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-extended-thinking.html):
//     adaptive thinking on Claude Opus 4.6 and later, Sonnet 4.6 and later,
//     generation 5 and the Fable and Mythos models, and a budget,
//     BudgetTokens or one derived from Effort, on earlier models. Effort
//     "none" turns thinking off where the model allows it. Efforts a model
//     does not accept are lowered: "max" to "xhigh" and "xhigh" to "high".
//     Sampling parameters are dropped where the model rejects them.
//   - Amazon Nova 2 models get reasoningConfig with maxReasoningEffort
//     (https://docs.aws.amazon.com/nova/latest/nova2-userguide/extended-thinking.html);
//     at effort "high", which rejects them, temperature, topP and
//     maxTokens are not sent.
//
// # Structured output
//
// Request.ResponseFormat is sent as outputConfig.textFormat
// (https://docs.aws.amazon.com/bedrock/latest/userguide/structured-output.html)
// to Claude Opus, Sonnet and Haiku 4.5 and later and the Fable and Mythos
// models, with the schema adapted to the supported subset. Other models,
// whose support varies, get an instruction in the system prompt instead;
// WithStructuredOutput overrides the choice. Strict tools are sent with
// strict only where structured output is used natively.
//
// # Errors
//
// Errors are *model.APIError values whose Type is the Bedrock exception
// name, such as ThrottlingException or ValidationException. Their status
// decides whether they are retried: throttling, ModelNotReadyException
// (429), ModelTimeoutException (408), InternalServerException and
// ServiceUnavailableException are retried; validation, access and
// not-found errors are not. ModelErrorException (424) reports the status
// the model returned, when given, so a model overload is retried.
// Exceptions inside a stream get the status the API reference gives them;
// modelStreamErrorException, which it says to retry, gets the model's
// status or 500.
package bedrock

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

const (
	// DefaultModel is used when no model is configured. The global
	// inference profile serves it from every commercial Region.
	DefaultModel = "global.anthropic.claude-opus-5-5"

	providerName = "bedrock"
	// semconvProvider is the gen_ai.provider.name of Amazon Bedrock.
	semconvProvider = "aws.bedrock"
)

// CredentialsProvider returns the credentials for a request. It is called
// for every request, so it should cache credentials it fetches.
type CredentialsProvider func(ctx context.Context) (Credentials, error)

// Guardrail configures an Amazon Bedrock guardrail
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_GuardrailConfiguration.html).
type Guardrail struct {
	// Identifier is the guardrail ID or ARN.
	Identifier string
	// Version is a version number or "DRAFT".
	Version string
	// Trace is "enabled", "disabled" or "enabled_full".
	Trace string
	// StreamProcessingMode is "sync" or "async" and applies to streams.
	StreamProcessingMode string
}

// Model calls the Amazon Bedrock Converse API.
type Model struct {
	client     *http.Client
	baseURL    string
	region     string
	header     http.Header
	model      string
	maxTokens  int
	apiKey     string
	creds      CredentialsProvider
	authSet    bool
	guardrail  *Guardrail
	structured *bool
	caching    bool
	now        func() time.Time
}

// Option configures a Model.
type Option func(*Model)

// WithRegion sets the AWS Region, such as "us-east-1".
func WithRegion(region string) Option { return func(m *Model) { m.region = region } }

// WithAPIKey authenticates with a Bedrock API key instead of signing.
func WithAPIKey(key string) Option {
	return func(m *Model) { m.apiKey, m.creds, m.authSet = key, nil, true }
}

// WithCredentials signs requests with static credentials.
func WithCredentials(c Credentials) Option {
	return WithCredentialsProvider(func(context.Context) (Credentials, error) { return c, nil })
}

// WithCredentialsProvider signs requests with the credentials p returns.
func WithCredentialsProvider(p CredentialsProvider) Option {
	return func(m *Model) { m.apiKey, m.creds, m.authSet = "", p, true }
}

// WithBaseURL sets the endpoint, such as a VPC or FIPS endpoint.
func WithBaseURL(u string) Option { return func(m *Model) { m.baseURL = u } }

// WithModel sets the model ID, inference profile ID or ARN.
func WithModel(name string) Option { return func(m *Model) { m.model = name } }

// WithHTTPClient sets the HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(m *Model) { m.client = c } }

// WithHeader adds a header to every request. It is not signed.
func WithHeader(key, value string) Option { return func(m *Model) { m.header.Add(key, value) } }

// WithMaxTokens sets the default output limit. Without it, requests that
// set none use the model's default.
func WithMaxTokens(n int) Option { return func(m *Model) { m.maxTokens = n } }

// WithGuardrail applies a guardrail to every request.
func WithGuardrail(g Guardrail) Option { return func(m *Model) { m.guardrail = &g } }

// WithStructuredOutput sets whether ResponseFormat and strict tools use
// Converse structured output (true) or an instruction in the system prompt
// (false), overriding the choice by model. See the package documentation.
func WithStructuredOutput(native bool) Option {
	return func(m *Model) { m.structured = new(native) }
}

// WithPromptCaching adds cache points after the system prompt, the tools
// and the last message, for models that support explicit prompt caching
// (https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html).
func WithPromptCaching() Option { return func(m *Model) { m.caching = true } }

// New returns a Model.
func New(opts ...Option) (*Model, error) {
	m := &Model{
		header: http.Header{},
		model:  DefaultModel,
		region: cmp.Or(os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION")),
		baseURL: cmp.Or(os.Getenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME"),
			os.Getenv("AWS_ENDPOINT_URL")),
		now: time.Now,
	}
	for _, opt := range opts {
		opt(m)
	}
	if !m.authSet {
		if key := os.Getenv("AWS_BEARER_TOKEN_BEDROCK"); key != "" {
			m.apiKey = key
		} else if c := envCredentials(); c.AccessKeyID != "" {
			m.creds = func(context.Context) (Credentials, error) { return c, nil }
		}
	}
	if m.apiKey == "" && m.creds == nil {
		return nil, errors.New("bedrock: no credentials; set AWS_BEARER_TOKEN_BEDROCK or AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, or use WithAPIKey, WithCredentials or WithCredentialsProvider")
	}
	if m.region == "" {
		m.region = regionOf(m.baseURL)
	}
	if m.baseURL == "" {
		if m.region == "" {
			return nil, errors.New("bedrock: no region; set AWS_REGION or use WithRegion")
		}
		m.baseURL = "https://bedrock-runtime." + m.region + ".amazonaws.com"
	}
	if m.creds != nil && m.region == "" {
		return nil, errors.New("bedrock: signing needs a region; set AWS_REGION or use WithRegion")
	}
	return m, nil
}

// envCredentials reads credentials from the standard environment
// variables.
func envCredentials() Credentials {
	c := Credentials{
		AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
	}
	if c.SecretAccessKey == "" {
		return Credentials{}
	}
	return c
}

// endpointRegion matches the Region in bedrock-runtime host names.
var endpointRegion = regexp.MustCompile(`^bedrock-runtime(?:-fips)?\.([a-z0-9-]+)\.amazonaws\.com(?:\.cn)?$`)

// regionOf returns the Region named by a bedrock-runtime endpoint URL, or
// "".
func regionOf(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	if m := endpointRegion.FindStringSubmatch(u.Hostname()); m != nil {
		return m[1]
	}
	return ""
}

// Describe implements model.Describer.
func (m *Model) Describe() model.Description {
	host, port := utils.ServerOf(m.baseURL)
	return model.Description{Provider: semconvProvider, Operation: "chat", Model: m.model, ServerAddress: host, ServerPort: port}
}

// Generate implements model.Model.
func (m *Model) Generate(ctx context.Context, req *model.Request) (*model.Response, error) {
	name := cmp.Or(req.Model, m.model)
	body, err := m.build(req, name, false)
	if err != nil {
		return nil, err
	}
	resp, err := m.post(ctx, name, "converse", body, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("bedrock: decode response: %w", err)
	}
	r := out.toModel()
	r.ID, r.Model = requestID(resp.Header), name
	return r, nil
}

// Stream implements model.Model.
func (m *Model) Stream(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		name := cmp.Or(req.Model, m.model)
		body, err := m.build(req, name, true)
		if err != nil {
			yield(nil, err)
			return
		}
		resp, err := m.post(ctx, name, "converse-stream", body, true)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()
		readStream(resp.Body, &model.Response{ID: requestID(resp.Header), Model: name, FinishReason: model.FinishUnknown}, yield)
	}
}

func requestID(h http.Header) string { return h.Get("X-Amzn-Requestid") }

// post sends body to the operation op of model name and returns the
// response. Error statuses are converted to *model.APIError.
func (m *Model) post(ctx context.Context, name, op string, body []byte, stream bool) (*http.Response, error) {
	target := strings.TrimRight(m.baseURL, "/") + "/model/" + uriEncode(name, true) + "/" + op
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err == nil && ((req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.Host == "") {
		err = &url.Error{Op: "parse", URL: req.URL.Redacted(), Err: errors.New("base URL needs an http or https scheme and a host")}
	}
	if err != nil {
		// An invalid URL fails the same way on every attempt.
		return nil, model.Permanent(fmt.Errorf("bedrock: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	utils.AddHeaders(req.Header, m.header)
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	} else {
		creds, err := m.creds(ctx)
		if err != nil {
			return nil, fmt.Errorf("bedrock: credentials: %w", err)
		}
		if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
			return nil, model.Permanent(errors.New("bedrock: credentials have no access key ID or secret access key"))
		}
		signRequest(req, body, creds, m.region, signingName, m.now())
	}
	resp, err := utils.Do(m.client, req)
	if err != nil {
		return nil, fmt.Errorf("bedrock: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, parseError(resp, b)
	}
	resp.Body = utils.URLErrorBody(resp.Body, req.URL.Redacted())
	return resp, nil
}

// errorBody holds the members of Bedrock error responses
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/CommonErrors.html).
type errorBody struct {
	Code               string `json:"code"`
	Type               string `json:"__type"`
	Message            string `json:"message"`
	OriginalStatusCode int    `json:"originalStatusCode"`
	OriginalMessage    string `json:"originalMessage"`
}

// parseError converts an error response. The exception name comes from
// the X-Amzn-ErrorType header or the code or __type member, as in the AWS
// SDKs' REST-JSON protocol.
func parseError(resp *http.Response, body []byte) *model.APIError {
	e := utils.ParseAPIError(providerName, resp, body)
	var b errorBody
	_ = json.Unmarshal(body, &b)
	if t := sanitizeErrorType(cmp.Or(resp.Header.Get("X-Amzn-Errortype"), b.Code, b.Type)); t != "" {
		e.Type = t
	}
	if b.OriginalStatusCode >= http.StatusBadRequest {
		// ModelErrorException wraps the model's own failure.
		e.Code = strconv.Itoa(e.StatusCode)
		e.StatusCode = b.OriginalStatusCode
	}
	if b.OriginalMessage != "" && e.Message != b.OriginalMessage {
		e.Message = strings.TrimSpace(e.Message + ": " + b.OriginalMessage)
	}
	return e
}

// sanitizeErrorType keeps the part of an error type before ':' and after
// '#', as the AWS SDKs do: "ValidationException:http://..." and
// "com.amazon#ValidationException" both become "ValidationException".
func sanitizeErrorType(s string) string {
	s, _, _ = strings.Cut(s, ":")
	if _, after, ok := strings.Cut(s, "#"); ok {
		s = after
	}
	return strings.TrimSpace(s)
}

// streamErrorStatus is the HTTP status of each exception event of a
// ConverseStream response
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ConverseStreamOutput.html),
// keyed by lower-cased name. modelStreamErrorException is documented as
// 424 but also as worth retrying, so it gets 500 unless it reports the
// model's status.
var streamErrorStatus = map[string]int{
	"internalserverexception":     http.StatusInternalServerError,
	"modelstreamerrorexception":   http.StatusInternalServerError,
	"serviceunavailableexception": http.StatusServiceUnavailable,
	"throttlingexception":         http.StatusTooManyRequests,
	"validationexception":         http.StatusBadRequest,
	"accessdeniedexception":       http.StatusForbidden,
	"resourcenotfoundexception":   http.StatusNotFound,
	"modeltimeoutexception":       http.StatusRequestTimeout,
	"modelnotreadyexception":      http.StatusTooManyRequests,
}

// streamError converts an exception or error message of an event stream.
func streamError(msg *eventMessage) *model.APIError {
	typ := msg.header(":exception-type")
	var b errorBody
	if typ == "" {
		// An error message carries its code and message in headers.
		typ, b.Message = msg.header(":error-code"), msg.header(":error-message")
	} else {
		_ = json.Unmarshal(msg.payload, &b)
	}
	e := &model.APIError{Provider: providerName, StatusCode: http.StatusInternalServerError, Message: b.Message}
	if typ != "" {
		// Union members are lower camel case; report the exception name.
		e.Type = strings.ToUpper(typ[:1]) + typ[1:]
		if s, ok := streamErrorStatus[strings.ToLower(typ)]; ok {
			e.StatusCode = s
		}
	}
	if b.OriginalStatusCode >= http.StatusBadRequest {
		e.StatusCode = b.OriginalStatusCode
	}
	if b.OriginalMessage != "" {
		e.Message = strings.TrimPrefix(e.Message+": "+b.OriginalMessage, ": ")
	}
	if e.Message == "" {
		e.Message = cmp.Or(typ, "stream error")
	}
	return e
}
