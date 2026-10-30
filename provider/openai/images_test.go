package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nostosx/agentx/imagegen"
	"github.com/nostosx/agentx/model"
)

const imagePNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l9sAAAAASUVORK5CYII="

func newImageFixture(t *testing.T, fn http.HandlerFunc, opts ...Option) *ImageGenerator {
	t.Helper()
	srv := httptest.NewServer(fn)
	t.Cleanup(srv.Close)
	options := []Option{WithAPIKey("image-key"), WithBaseURL(srv.URL + "/v1"), WithHTTPClient(srv.Client())}
	g, err := NewImageGenerator(append(options, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestImageGenerator(t *testing.T) {
	t.Parallel()
	for _, modelName := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare-2026-09-08"} {
		t.Run(modelName, func(t *testing.T) {
			t.Parallel()
			compression := 0
			g := newImageFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/images/generations" || r.Header.Get("Authorization") != "Bearer image-key" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Image-Test") != "yes" {
					t.Errorf("request path/headers = %s, %v", r.URL.Path, r.Header)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				want := map[string]any{"model": modelName, "prompt": "draw a cat", "n": float64(2), "size": "1536x864", "quality": "max", "background": "transparent", "output_format": "webp", "output_compression": float64(0), "safety_identifier": "hashed", "moderation": "auto"}
				if !reflect.DeepEqual(body, want) {
					t.Errorf("body = %#v, want %#v", body, want)
				}
				w.Header().Set("X-Request-ID", "image-request")
				fmt.Fprintf(w, `{"created":1793404800,"model":"resolved-model","data":[{"b64_json":%q},{"b64_json":%q,"revised_prompt":"revised"}],"output_format":"png","quality":"high","size":"1024x1024","background":"opaque","usage":{"input_tokens":12,"output_tokens":30,"total_tokens":42,"input_tokens_details":{"image_tokens":2,"text_tokens":10},"output_tokens_details":{"image_tokens":29,"text_tokens":1}}}`, imagePNG, imagePNG)
			}, WithHeader("X-Image-Test", "yes"))
			if g.model != DefaultImageModel {
				t.Fatalf("default model = %s", g.model)
			}
			req := &imagegen.Request{Model: modelName, Prompt: "draw a cat", N: 2, Size: "1536x864", Quality: "max", Background: "transparent", OutputFormat: "webp", OutputCompression: &compression, SafetyIdentifier: "hashed", ProviderOptions: map[string]map[string]any{"openai": {"moderation": "auto"}, "other": {"ignored": true}}}
			resp, err := g.Generate(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := base64.StdEncoding.DecodeString(imagePNG)
			if resp.RequestID != "image-request" || resp.Model != "resolved-model" || resp.Provider != "openai" || len(resp.Images) != 2 || !reflect.DeepEqual(resp.Images[0].Part.Data, data) || resp.Images[0].Part.MediaType != "image/png" || resp.Images[1].RevisedPrompt != "revised" || resp.Size != "1024x1024" || resp.Created.Unix() != 1793404800 || resp.Usage == nil || resp.Usage.TotalTokens != 42 || resp.Usage.InputImageTokens != 2 || resp.Usage.OutputImageTokens != 29 || resp.Usage.OutputTextTokens != 1 {
				t.Fatalf("response = %+v", resp)
			}
			if req.ProviderOptions["openai"]["moderation"] != "auto" {
				t.Fatal("request mutated")
			}
		})
	}
}

func TestImageEdit(t *testing.T) {
	t.Parallel()
	data, _ := base64.StdEncoding.DecodeString(imagePNG)
	for _, tc := range []struct {
		name         string
		mask, markup *model.Part
	}{
		{name: "mask", mask: new(model.ImagePart("image/png", data))},
		{name: "markup", markup: new(model.FilePart(model.PartImage, "openai", "file-markup", ""))},
		{name: "no overlay"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newImageFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/images/edits" {
					t.Errorf("path = %s", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				images := body["images"].([]any)
				if len(images) != 3 || images[0].(map[string]any)["image_url"] != "data:image/png;base64,"+imagePNG || images[1].(map[string]any)["image_url"] != "https://example.com/input.webp" || images[2].(map[string]any)["file_id"] != "file-source" || body["model"] != "custom-image-model" || body["input_fidelity"] != "high" {
					t.Errorf("body = %#v", body)
				}
				if tc.mask != nil && body["mask"].(map[string]any)["image_url"] != "data:image/png;base64,"+imagePNG {
					t.Error("missing mask")
				}
				if tc.markup != nil && body["markup"].(map[string]any)["file_id"] != "file-markup" {
					t.Error("missing markup")
				}
				fmt.Fprintf(w, `{"data":[{"b64_json":%q}]}`, imagePNG)
			}, WithModel("custom-image-model"))
			resp, err := g.Edit(t.Context(), &imagegen.EditRequest{Prompt: "replace sky", ProviderOptions: map[string]map[string]any{"openai": {"input_fidelity": "high"}}, Images: []model.Part{model.ImagePart("image/png", data), model.ImageURLPart("https://example.com/input.webp"), model.FilePart(model.PartImage, "openai", "file-source", "")}, Mask: tc.mask, Markup: tc.markup})
			if err != nil || resp == nil || resp.Model != "custom-image-model" || resp.Usage != nil {
				t.Fatalf("Edit = %+v, %v", resp, err)
			}
		})
	}
}

func TestImageValidation(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	g := newImageFixture(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	for _, tc := range []struct {
		name string
		req  *imagegen.Request
	}{
		{name: "nil"},
		{name: "empty", req: &imagegen.Request{}},
		{name: "whitespace", req: &imagegen.Request{Prompt: " \n "}},
		{name: "long prompt", req: &imagegen.Request{Prompt: strings.Repeat("字", 32001)}},
		{name: "negative count", req: &imagegen.Request{Prompt: "x", N: -1}},
		{name: "too many", req: &imagegen.Request{Prompt: "x", N: 11}},
		{name: "quality", req: &imagegen.Request{Prompt: "x", Quality: "low medium"}},
		{name: "format", req: &imagegen.Request{Prompt: "x", OutputFormat: "gif"}},
		{name: "background", req: &imagegen.Request{Prompt: "x", Background: "clear"}},
		{name: "transparent jpeg", req: &imagegen.Request{Prompt: "x", OutputFormat: "jpeg", Background: "transparent"}},
		{name: "negative compression", req: &imagegen.Request{Prompt: "x", OutputFormat: "webp", OutputCompression: new(-1)}},
		{name: "high compression", req: &imagegen.Request{Prompt: "x", OutputFormat: "jpeg", OutputCompression: new(101)}},
		{name: "png compression", req: &imagegen.Request{Prompt: "x", OutputCompression: new(10)}},
		{name: "safety identifier", req: &imagegen.Request{Prompt: "x", SafetyIdentifier: strings.Repeat("a", 129)}},
		{name: "nonstream previews", req: &imagegen.Request{Prompt: "x", PartialImages: 1}},
		{name: "override stream", req: &imagegen.Request{Prompt: "x", ProviderOptions: map[string]map[string]any{"openai": {"stream": true}}}},
		{name: "override count", req: &imagegen.Request{Prompt: "x", ProviderOptions: map[string]map[string]any{"openai": {"n": 10}}}},
		{name: "unencodable option", req: &imagegen.Request{Prompt: "x", ProviderOptions: map[string]map[string]any{"openai": {"bad": make(chan int)}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := g.Generate(t.Context(), tc.req)
			if err == nil || model.IsRetryable(err) {
				t.Fatalf("invalid request = %v", err)
			}
			if _, ok := errors.AsType[*model.PermanentError](err); !ok {
				t.Fatalf("not permanent: %v", err)
			}
		})
	}
	png := model.ImagePart("image/png", []byte("png"))
	for _, tc := range []struct {
		name string
		req  *imagegen.EditRequest
	}{
		{name: "nil"},
		{name: "no images", req: &imagegen.EditRequest{Prompt: "x"}},
		{name: "too many", req: &imagegen.EditRequest{Prompt: "x", Images: make([]model.Part, 17)}},
		{name: "mask and markup", req: &imagegen.EditRequest{Prompt: "x", Images: []model.Part{png}, Mask: &png, Markup: &png}},
		{name: "text part", req: &imagegen.EditRequest{Prompt: "x", Images: []model.Part{model.TextPart("x")}}},
		{name: "empty part", req: &imagegen.EditRequest{Prompt: "x", Images: []model.Part{{Type: model.PartImage}}}},
		{name: "foreign file", req: &imagegen.EditRequest{Prompt: "x", Images: []model.Part{model.FilePart(model.PartImage, "anthropic", "file-foreign", "")}}},
		{name: "ambiguous", req: &imagegen.EditRequest{Prompt: "x", Images: []model.Part{{Type: model.PartImage, Data: []byte("x"), URL: "https://example.com/x"}}}},
		{name: "bad data url", req: &imagegen.EditRequest{Prompt: "x", Images: []model.Part{model.ImageURLPart("data:image/png;base64,!")}}},
		{name: "bad url", req: &imagegen.EditRequest{Prompt: "x", Images: []model.Part{model.ImageURLPart("file:///tmp/x")}}},
		{name: "bad mime", req: &imagegen.EditRequest{Prompt: "x", Images: []model.Part{model.ImagePart("image/gif", []byte("GIF89a"))}}},
		{name: "mask mime", req: &imagegen.EditRequest{Prompt: "x", Images: []model.Part{png}, Mask: new(model.ImagePart("image/jpeg", []byte("jpeg")))}},
	} {
		t.Run("edit/"+tc.name, func(t *testing.T) {
			_, err := g.Edit(t.Context(), tc.req)
			if err == nil || model.IsRetryable(err) {
				t.Fatalf("invalid edit = %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("sent %d invalid requests", calls.Load())
	}
}

func TestImageResponseErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		status     int
		retry      bool
	}{
		{name: "no images", body: `{"data":[]}`},
		{name: "empty image", body: `{"data":[{}]}`},
		{name: "invalid base64", body: `{"data":[{"b64_json":"!"}]}`},
		{name: "nonimage bytes", body: `{"data":[{"b64_json":"aGVsbG8="}]}`},
		{name: "invalid url", body: `{"data":[{"url":"file:///tmp/x"}]}`},
		{name: "bad json", body: `{`},
		{name: "wrong count", body: `{"data":[{},{}]}`},
		{name: "embedded error", body: `{"error":{"code":"invalid_request_error","message":"rejected"}}`},
		{name: "unauthorized", status: 401, body: `{"error":{"message":"unauthorized"}}`},
		{name: "rate limit", status: 429, body: `{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`, retry: true},
		{name: "quota", status: 429, body: `{"error":{"code":"insufficient_quota"}}`},
		{name: "server", status: 503, body: `{"error":{"message":"unavailable"}}`, retry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newImageFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "2")
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				fmt.Fprint(w, tc.body)
			})
			_, err := g.Generate(t.Context(), &imagegen.Request{Prompt: "x"})
			if err == nil || model.IsRetryable(err) != tc.retry {
				t.Fatalf("response error = %v, retryable = %v", err, model.IsRetryable(err))
			}
			if tc.status != 0 {
				api, ok := errors.AsType[*model.APIError](err)
				if !ok || api.StatusCode != tc.status || api.RetryAfter != 2*time.Second {
					t.Fatalf("API error = %v", err)
				}
			}
		})
	}
}

func TestImageCredentialsAndCancellation(t *testing.T) {
	clearEnv(t)
	if _, err := NewImageGenerator(); err == nil {
		t.Fatal("missing key accepted")
	}
	t.Setenv("OPENAI_API_KEY", "environment-key")
	var calls atomic.Int32
	g := newImageFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer refreshed-token" {
			t.Error("token source not applied")
		}
		calls.Add(1)
		fmt.Fprintf(w, `{"data":[{"url":"https://example.com/image.png"}]}`)
	}, WithTokenSource(func(context.Context) (string, error) { return "refreshed-token", nil }))
	resp, err := g.Generate(t.Context(), &imagegen.Request{Prompt: "x"})
	if err != nil || resp.Images[0].Part.URL != "https://example.com/image.png" {
		t.Fatalf("Generate = %+v, %v", resp, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := g.Generate(ctx, &imagegen.Request{Prompt: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected network request")
	}
}
