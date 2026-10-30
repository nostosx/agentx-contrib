package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nostosx/agentx/imagegen"
	"github.com/nostosx/agentx/model"
)

const generatedPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l9sAAAAASUVORK5CYII="

func imageClient(t *testing.T, handler http.HandlerFunc, opts ...Option) *ImageGenerator {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	options := []Option{WithAPIKey("fixture"), WithBaseURL(srv.URL + "/v1beta"), WithHTTPClient(srv.Client())}
	g, err := NewImageGenerator(append(options, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestImageGenerationAndEdit(t *testing.T) {
	t.Parallel()
	for _, edit := range []bool{false, true} {
		t.Run(fmt.Sprint("edit=", edit), func(t *testing.T) {
			t.Parallel()
			g := imageClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("x-goog-api-key") != "fixture" || r.URL.Path != "/v1beta/models/image-model:generateContent" {
					t.Errorf("request=%s %v", r.URL, r.Header)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				cfg := body["generationConfig"].(map[string]any)
				images := cfg["imageConfig"].(map[string]any)
				if images["imageSize"] != "2K" || images["aspectRatio"] != "16:9" || cfg["responseModalities"] == nil {
					t.Errorf("config=%v", cfg)
				}
				parts := body["contents"].([]any)[0].(map[string]any)["parts"].([]any)
				if edit && len(parts) != 2 {
					t.Errorf("edit parts=%v", parts)
				}
				w.Header().Set("X-Goog-Request-ID", "request-1")
				fmt.Fprintf(w, `{"modelVersion":"resolved","candidates":[{"content":{"parts":[{"text":"done"},{"inlineData":{"mimeType":"image/png","data":%q},"thought":true},{"inlineData":{"mimeType":"image/png","data":%q},"thoughtSignature":"signature"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":9,"thoughtsTokenCount":2,"totalTokenCount":16,"promptTokensDetails":[{"modality":"TEXT","tokenCount":3},{"modality":"IMAGE","tokenCount":2}],"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":9}]}}`, generatedPNG, generatedPNG)
			})
			req := &imagegen.Request{Model: "image-model", Prompt: "draw", Size: "2K", AspectRatio: "16:9"}
			var out *imagegen.Response
			var err error
			if edit {
				out, err = g.Edit(t.Context(), &imagegen.EditRequest{Request: *req, Images: []model.Part{model.ImageURLPart("https://example.com/input.png")}})
			} else {
				out, err = g.Generate(t.Context(), req)
			}
			if err != nil || len(out.Images) != 1 || out.Model != "resolved" || out.RequestID != "request-1" || out.Usage.OutputImageTokens != 9 || out.Usage.InputImageTokens != 2 || out.Usage.OutputTokens != 11 || out.Images[0].Part.Signature != "signature" {
				t.Fatalf("result=%+v err=%v", out, err)
			}
		})
	}
}

func TestImageStream(t *testing.T) {
	t.Parallel()
	g := imageClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ":streamGenerateContent") || r.URL.Query().Get("alt") != "sse" {
			t.Error(r.URL)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"thought\":true,\"inlineData\":{\"mimeType\":\"image/png\",\"data\":%q}}]}}]}\n\n", generatedPNG)
		fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":%q}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"totalTokenCount\":10}}\n\n", generatedPNG)
	})
	previews, complete := 0, 0
	for ev, err := range g.Stream(t.Context(), &imagegen.Request{Prompt: "x", PartialImages: 1}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Image != nil {
			previews++
		}
		if ev.Response != nil {
			complete++
			if len(ev.Response.Images) != 1 || ev.Response.Usage.TotalTokens != 10 {
				t.Fatal("final response lost")
			}
		}
	}
	if previews != 1 || complete != 1 {
		t.Fatalf("previews=%d completions=%d", previews, complete)
	}
}

func TestImageStreamErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		target     error
	}{
		{name: "empty", target: io.ErrUnexpectedEOF},
		{name: "partial only", body: fmt.Sprintf("data: {\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":%q}}]}}]}\n\n", generatedPNG), target: io.ErrUnexpectedEOF},
		{name: "blocked", body: "data: {\"promptFeedback\":{\"blockReason\":\"SAFETY\"}}\n\n"},
		{name: "invalid content", body: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"!\"}}]},\"finishReason\":\"STOP\"}]}\n\n"},
		{name: "api error", body: "data: {\"error\":{\"code\":429,\"status\":\"RESOURCE_EXHAUSTED\",\"message\":\"limit\"}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := imageClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.body)
			})
			var failure error
			for ev, err := range g.Stream(t.Context(), &imagegen.Request{Prompt: "x"}) {
				if ev != nil && ev.Response != nil {
					t.Fatal("false completion")
				}
				if err != nil {
					failure = err
				}
			}
			if failure == nil || tc.target != nil && !errors.Is(failure, tc.target) {
				t.Fatalf("error=%v", failure)
			}
		})
	}
}

func TestImageValidationAndCancellation(t *testing.T) {
	t.Parallel()
	g := imageClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid request sent") })
	for _, req := range []*imagegen.Request{nil, {Prompt: "x", N: 2}, {Prompt: "x", Quality: "high"}, {Prompt: "x", Size: "1024x1024"}, {Prompt: "x", Background: "transparent"}, {Prompt: "x", OutputFormat: "jpeg"}, {Prompt: "x", PartialImages: 4}} {
		if _, err := g.Generate(t.Context(), req); err == nil || model.IsRetryable(err) {
			t.Fatalf("invalid=%v", err)
		}
	}
	if _, err := g.Edit(t.Context(), &imagegen.EditRequest{Prompt: "x", Images: []model.Part{model.ImageURLPart("https://example.com/a.png")}, Mask: new(model.ImageURLPart("https://example.com/mask.png"))}); err == nil {
		t.Fatal("mask silently ignored")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := g.Generate(ctx, &imagegen.Request{Prompt: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}
