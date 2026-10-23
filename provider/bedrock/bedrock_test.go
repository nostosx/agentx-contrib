package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nostosx/agentx/model"
)

// clearEnv unsets the environment variables the provider reads.
func clearEnv(t *testing.T) {
	for _, k := range []string{"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "AWS_ENDPOINT_URL",
		"AWS_BEARER_TOKEN_BEDROCK", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
		t.Setenv(k, "")
	}
}

func TestNew(t *testing.T) {
	clearEnv(t)
	if _, err := New(WithRegion("us-east-1")); err == nil {
		t.Fatal("missing credentials accepted")
	}
	if _, err := New(WithCredentials(Credentials{AccessKeyID: "a", SecretAccessKey: "s"})); err == nil {
		t.Fatal("missing region accepted")
	}
	// The endpoint names the region.
	m, err := New(WithCredentials(Credentials{AccessKeyID: "a", SecretAccessKey: "s"}), WithBaseURL("https://bedrock-runtime-fips.us-gov-west-1.amazonaws.com"))
	if err != nil || m.region != "us-gov-west-1" {
		t.Fatalf("region = %q, %v", m.region, err)
	}
	// An API key needs no region with a custom endpoint.
	if _, err := New(WithAPIKey("k"), WithBaseURL("https://gateway.example")); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AWS_DEFAULT_REGION", "eu-west-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKID")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_SESSION_TOKEN", "token")
	m, err = New()
	if err != nil {
		t.Fatal(err)
	}
	creds, err := m.creds(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if m.apiKey != "" || creds != (Credentials{"AKID", "secret", "token"}) || m.baseURL != "https://bedrock-runtime.eu-west-1.amazonaws.com" || m.model != DefaultModel {
		t.Fatalf("configuration = %+v %+v", m, creds)
	}

	// The bearer token takes precedence over environment credentials, and
	// options over the environment; the last authentication option wins.
	t.Setenv("AWS_REGION", "us-west-2")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "env-key")
	t.Setenv("AWS_ENDPOINT_URL", "https://generic.example")
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "https://runtime.example")
	m, err = New()
	if err != nil || m.apiKey != "env-key" || m.creds != nil || m.region != "us-west-2" || m.baseURL != "https://runtime.example" {
		t.Fatalf("configuration = %+v, %v", m, err)
	}
	m, err = New(WithAPIKey("explicit"), WithCredentials(Credentials{AccessKeyID: "x", SecretAccessKey: "y"}))
	if err != nil || m.apiKey != "" || m.creds == nil {
		t.Fatalf("configuration = %+v, %v", m, err)
	}
	m, err = New(WithCredentials(Credentials{AccessKeyID: "x", SecretAccessKey: "y"}), WithAPIKey("explicit"), WithModel("custom"), WithMaxTokens(7))
	if err != nil || m.apiKey != "explicit" || m.creds != nil || m.model != "custom" || m.maxTokens != 7 {
		t.Fatalf("configuration = %+v, %v", m, err)
	}
}

func TestDescribe(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("k"), WithRegion("us-east-1"))
	if err != nil {
		t.Fatal(err)
	}
	want := model.Description{Provider: "aws.bedrock", Operation: "chat", Model: DefaultModel, ServerAddress: "bedrock-runtime.us-east-1.amazonaws.com", ServerPort: 443}
	if got := model.Describe(m); got != want {
		t.Fatalf("Describe = %+v, want %+v", got, want)
	}
}

var fixedTime = time.Date(2026, 10, 21, 8, 30, 0, 0, time.UTC)

// verifySignature checks the SigV4 signature of a request received by a
// test server.
func verifySignature(t *testing.T, r *http.Request, body []byte, creds Credentials, region string) {
	t.Helper()
	signed := http.Header{"Host": {r.Host}}
	for k, v := range r.Header {
		if lk := strings.ToLower(k); lk == "content-type" || strings.HasPrefix(lk, "x-amz-") {
			signed[k] = v
		}
	}
	want := signature(sigV4Input{
		creds: creds, region: region, service: "bedrock", time: fixedTime,
		method: r.Method, path: r.URL.EscapedPath(), query: r.URL.RawQuery, header: signed, payload: hashHex(body),
	})
	if got := r.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization = %s\nwant %s", got, want)
	}
	if !strings.Contains(want, "/"+region+"/bedrock/aws4_request, SignedHeaders=content-type;host;x-amz-date") {
		t.Errorf("Authorization = %s", want)
	}
}

func TestGenerateAndStream(t *testing.T) {
	t.Parallel()
	creds := Credentials{AccessKeyID: "AKID", SecretAccessKey: "secret", SessionToken: "session"}
	const name = "us.anthropic.claude-sonnet-4-5-20250929-v1:0"
	for _, tc := range []struct {
		name   string
		stream bool
	}{{name: "generate"}, {name: "stream", stream: true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				op := "converse"
				if tc.stream {
					op = "converse-stream"
				}
				if r.URL.EscapedPath() != "/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/"+op || r.Header.Get("X-Custom") != "1" ||
					r.Header.Get("X-Amz-Security-Token") != "session" || r.Header.Get("X-Amz-Date") != "20261021T083000Z" {
					t.Errorf("request = %s %v", r.URL.EscapedPath(), r.Header)
				}
				body, _ := io.ReadAll(r.Body)
				verifySignature(t, r, body, creds, "us-east-1")
				var req map[string]json.RawMessage
				if err := json.Unmarshal(body, &req); err != nil {
					t.Error(err)
				}
				if string(req["inferenceConfig"]) != `{"maxTokens":42}` || string(req["messages"]) != `[{"role":"user","content":[{"text":"hi"}]}]` {
					t.Errorf("body = %s", body)
				}
				w.Header().Set("X-Amzn-Requestid", "req-1")
				if !tc.stream {
					_, _ = io.WriteString(w, `{"output":{"message":{"role":"assistant","content":[{"text":"hello"}]}},"stopReason":"end_turn",`+
						`"usage":{"inputTokens":10,"outputTokens":3,"totalTokens":13,"cacheReadInputTokens":4,"cacheWriteInputTokens":5},"metrics":{"latencyMs":12}}`)
					return
				}
				w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				for _, ev := range [][]byte{
					event("messageStart", `{"role":"assistant","p":"abc"}`),
					event("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"hel"}}`),
					event("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"lo"}}`),
					event("contentBlockStop", `{"contentBlockIndex":0}`),
					event("messageStop", `{"stopReason":"end_turn"}`),
					event("metadata", `{"usage":{"inputTokens":10,"outputTokens":3,"totalTokens":13,"cacheReadInputTokens":4,"cacheWriteInputTokens":5},"metrics":{"latencyMs":12}}`),
				} {
					_, _ = w.Write(ev)
				}
			}))
			defer srv.Close()
			m, err := New(WithCredentials(creds), WithRegion("us-east-1"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()),
				WithModel(name), WithHeader("X-Custom", "1"))
			if err != nil {
				t.Fatal(err)
			}
			m.now = func() time.Time { return fixedTime }
			req := &model.Request{MaxTokens: 42, Messages: []*model.Message{model.UserText("hi")}}
			var resp *model.Response
			if tc.stream {
				resp, err = model.Collect(m.Stream(t.Context(), req))
			} else {
				resp, err = m.Generate(t.Context(), req)
			}
			want := model.Usage{InputTokens: 10, OutputTokens: 3, CacheReadTokens: 4, CacheWriteTokens: 5}
			if err != nil || resp.Text() != "hello" || resp.ID != "req-1" || resp.Model != name || resp.FinishReason != model.FinishStop || resp.Usage != want {
				t.Fatalf("response = %+v, %v", resp, err)
			}
		})
	}
}

func TestAPIKeyAndCredentialsProvider(t *testing.T) {
	t.Parallel()
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		if r.URL.EscapedPath() != "/model/arn%3Aaws%3Abedrock%3Aus-east-1%3A123456789012%3Aapplication-inference-profile%2Fabc/converse" {
			t.Errorf("path = %s", r.URL.EscapedPath())
		}
		_, _ = io.WriteString(w, `{"output":{"message":{"role":"assistant","content":[]}},"stopReason":"end_turn","usage":{}}`)
	}))
	defer srv.Close()
	const arn = "arn:aws:bedrock:us-east-1:123456789012:application-inference-profile/abc"
	m, err := New(WithAPIKey("bedrock-key"), WithBaseURL(srv.URL), WithModel(arn))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Generate(t.Context(), &model.Request{}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	m, err = New(WithRegion("us-east-1"), WithBaseURL(srv.URL), WithModel(arn), WithCredentialsProvider(func(context.Context) (Credentials, error) {
		calls++
		return Credentials{AccessKeyID: "rotated", SecretAccessKey: "s"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := m.Generate(t.Context(), &model.Request{}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 || auth[0] != "Bearer bedrock-key" || !strings.HasPrefix(auth[1], "AWS4-HMAC-SHA256 Credential=rotated/") {
		t.Fatalf("calls = %d, auth = %q", calls, auth)
	}

	// Credential failures are reported without sending a request.
	sentinel := errors.New("no profile")
	m, err = New(WithRegion("us-east-1"), WithBaseURL(srv.URL), WithCredentialsProvider(func(context.Context) (Credentials, error) {
		return Credentials{}, sentinel
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Generate(t.Context(), &model.Request{}); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v", err)
	}
	m, err = New(WithRegion("us-east-1"), WithBaseURL(srv.URL), WithCredentials(Credentials{AccessKeyID: "only"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Generate(t.Context(), &model.Request{}); err == nil || model.IsRetryable(err) {
		t.Fatalf("incomplete credentials = %v", err)
	}
	m, err = New(WithAPIKey("k"), WithBaseURL("ftp://host"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Generate(t.Context(), &model.Request{}); err == nil || model.IsRetryable(err) {
		t.Fatalf("invalid URL = %v", err)
	}
	if len(auth) != 3 {
		t.Fatalf("requests = %d", len(auth))
	}
}

func TestHTTPErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, errorType, body string
		status                int
		header                map[string]string
		wantType              string
		wantStatus            int
		retryable             bool
		retryAfter            time.Duration
	}{
		{name: "throttling", status: 429, errorType: "ThrottlingException:http://internal.amazon.com/coral/com.amazon.bedrock/",
			body: `{"message":"Too many requests, please wait before trying again."}`, header: map[string]string{"Retry-After": "3"},
			wantType: "ThrottlingException", wantStatus: 429, retryable: true, retryAfter: 3 * time.Second},
		{name: "model not ready", status: 429, errorType: "ModelNotReadyException", body: `{"message":"not ready"}`,
			wantType: "ModelNotReadyException", wantStatus: 429, retryable: true},
		{name: "validation", status: 400, body: `{"__type":"com.amazon.bedrock#ValidationException","message":"bad"}`,
			wantType: "ValidationException", wantStatus: 400},
		{name: "access denied", status: 403, body: `{"code":"AccessDeniedException","Message":"denied"}`,
			wantType: "AccessDeniedException", wantStatus: 403},
		{name: "not found", status: 404, errorType: "ResourceNotFoundException", body: `{"message":"no model"}`,
			wantType: "ResourceNotFoundException", wantStatus: 404},
		{name: "timeout", status: 408, errorType: "ModelTimeoutException", body: `{"message":"slow"}`,
			wantType: "ModelTimeoutException", wantStatus: 408, retryable: true},
		{name: "internal", status: 500, errorType: "InternalServerException", body: `{"message":"oops"}`,
			wantType: "InternalServerException", wantStatus: 500, retryable: true},
		{name: "unavailable", status: 503, errorType: "ServiceUnavailableException", body: `{"message":"down"}`,
			wantType: "ServiceUnavailableException", wantStatus: 503, retryable: true},
		{name: "model error", status: 424, errorType: "ModelErrorException", body: `{"message":"model failed"}`,
			wantType: "ModelErrorException", wantStatus: 424},
		{name: "model overloaded", status: 424, errorType: "ModelErrorException",
			body:     `{"message":"model failed","originalStatusCode":529,"originalMessage":"Overloaded","resourceName":"x"}`,
			wantType: "ModelErrorException", wantStatus: 529, retryable: true},
		{name: "plain text", status: 502, body: "Bad Gateway", wantStatus: 502, retryable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.errorType != "" {
					w.Header().Set("X-Amzn-ErrorType", tc.errorType)
				}
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			m, err := New(WithAPIKey("k"), WithBaseURL(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.Generate(t.Context(), &model.Request{})
			var apiErr *model.APIError
			if !errors.As(err, &apiErr) || apiErr.Type != tc.wantType || apiErr.StatusCode != tc.wantStatus || apiErr.Provider != "bedrock" ||
				model.IsRetryable(err) != tc.retryable || apiErr.RetryAfter != tc.retryAfter || apiErr.Message == "" {
				t.Fatalf("error = %#v", apiErr)
			}
			if tc.name == "model overloaded" && (apiErr.Code != "424" || !strings.Contains(apiErr.Message, "Overloaded")) {
				t.Fatalf("error = %#v", apiErr)
			}
			// Streams fail the same way before the event stream starts.
			if _, err := model.Collect(m.Stream(t.Context(), &model.Request{})); !errors.As(err, &apiErr) || apiErr.StatusCode != tc.wantStatus {
				t.Fatalf("stream error = %v", err)
			}
		})
	}
}

func TestStreamBodyReadErrorIsRetryable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write(event("messageStart", `{"role":"assistant"}`))
	}))
	defer srv.Close()
	m, err := New(WithAPIKey("k"), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Collect(m.Stream(t.Context(), &model.Request{})); err == nil || !model.IsRetryable(err) {
		t.Fatalf("error = %v", err)
	}
}

func TestRedirectDoesNotForwardSignedOrBearerRequest(t *testing.T) {
	t.Parallel()
	for _, auth := range []string{"sigv4", "bearer"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", auth, stream), func(t *testing.T) {
				t.Parallel()
				var received atomic.Int32
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					received.Add(1)
					_, _ = io.WriteString(w, `{}`)
				}))
				defer target.Close()
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") == "" {
						t.Error("initial request has no authentication")
					}
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				}))
				defer origin.Close()
				options := []Option{WithBaseURL(origin.URL), WithRegion("us-east-1")}
				if auth == "sigv4" {
					options = append(options, WithCredentials(Credentials{AccessKeyID: "test-id", SecretAccessKey: "test-secret", SessionToken: "test-session"}))
				} else {
					options = append(options, WithAPIKey("test-key"))
				}
				m, err := New(options...)
				if err != nil {
					t.Fatal(err)
				}
				req := &model.Request{Messages: []*model.Message{model.UserText("private prompt")}}
				if stream {
					_, err = model.Collect(m.Stream(t.Context(), req))
				} else {
					_, err = m.Generate(t.Context(), req)
				}
				if _, ok := errors.AsType[*model.PermanentError](err); !ok || received.Load() != 0 {
					t.Fatalf("redirect: received = %d, error = %v", received.Load(), err)
				}
			})
		}
	}
}
