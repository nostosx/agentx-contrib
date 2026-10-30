package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nostosx/agentx/imagegen"
	"github.com/nostosx/agentx/model"
)

func TestImageStream(t *testing.T) {
	t.Parallel()
	for _, edit := range []bool{false, true} {
		t.Run(fmt.Sprint("edit=", edit), func(t *testing.T) {
			t.Parallel()
			kind, path := "image_generation", "/v1/images/generations"
			if edit {
				kind, path = "image_edit", "/v1/images/edits"
			}
			g := newImageFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != path {
					t.Errorf("path = %s", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["stream"] != true || body["partial_images"] != float64(2) {
					t.Errorf("body = %v", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("X-Request-ID", "stream-request")
				fmt.Fprintf(w, "event: %s.partial_image\ndata: {\"b64_json\":%q,\"partial_image_index\":1}\n\n", kind, imagePNG)
				fmt.Fprintf(w, "data: {\"type\":%q,\"b64_json\":%q,\"output_format\":\"png\",\"size\":\"1536x864\",\"usage\":{\"input_tokens\":3,\"output_tokens\":4,\"total_tokens\":7}}\n\n", kind+".completed", imagePNG)
			})
			req := &imagegen.Request{Prompt: "x", PartialImages: 2}
			stream := g.Stream(t.Context(), req)
			if edit {
				stream = g.StreamEdit(t.Context(), &imagegen.EditRequest{Request: *req, Images: []model.Part{model.ImageURLPart("https://example.com/source.png")}})
			}
			var events []*imagegen.StreamEvent
			for ev, err := range stream {
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, ev)
			}
			if len(events) != 2 || events[0].Image == nil || events[0].Response != nil || events[0].PartialIndex != 1 || events[1].Image != nil || events[1].Response == nil {
				t.Fatalf("events = %+v", events)
			}
			out := events[1].Response
			if len(out.Images) != 1 || out.RequestID != "stream-request" || out.Size != "1536x864" || out.Usage.TotalTokens != 7 {
				t.Fatalf("response = %+v", out)
			}
		})
	}
}

func TestImageStreamFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, contentType string
		retry                   bool
		target                  error
	}{
		{name: "empty", target: io.ErrUnexpectedEOF, retry: true},
		{name: "truncated event", body: "data: {\"type\":\"image_generation.completed\"}", target: io.ErrUnexpectedEOF, retry: true},
		{name: "preview only", body: fmt.Sprintf("data: {\"type\":\"image_generation.partial_image\",\"b64_json\":%q}\n\n", imagePNG), target: io.ErrUnexpectedEOF, retry: true},
		{name: "malformed", body: "data: {\n\n"},
		{name: "invalid image", body: "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"!\"}\n\n"},
		{name: "error", body: "data: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\",\"message\":\"slow\"}\n\n", retry: true},
		{name: "nested error", body: "data: {\"type\":\"error\",\"error\":{\"code\":\"invalid_request_error\",\"message\":\"bad\"}}\n\n"},
		{name: "failed", body: "data: {\"type\":\"image_generation.failed\"}\n\n"},
		{name: "json instead", body: `{"error":{"message":"gateway failure"}}`, contentType: "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newImageFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				fmt.Fprint(w, tc.body)
			})
			var got error
			for ev, err := range g.Stream(t.Context(), &imagegen.Request{Prompt: "x"}) {
				if ev != nil && ev.Response != nil {
					t.Fatal("failed stream reported success")
				}
				if err != nil {
					got = err
				}
			}
			if got == nil || model.IsRetryable(got) != tc.retry {
				t.Fatalf("error = %v, retryable = %v", got, model.IsRetryable(got))
			}
			if tc.target != nil && !errors.Is(got, tc.target) {
				t.Fatalf("error chain = %v", got)
			}
		})
	}
}

func TestImageStreamStopAndCancel(t *testing.T) {
	t.Parallel()
	for _, stop := range []bool{true, false} {
		t.Run(fmt.Sprint("stop=", stop), func(t *testing.T) {
			t.Parallel()
			closed := make(chan struct{})
			g := newImageFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"type\":\"image_generation.partial_image\",\"b64_json\":%q}\n\n", imagePNG)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(closed)
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var got error
			for ev, err := range g.Stream(ctx, &imagegen.Request{Prompt: "x"}) {
				if err != nil {
					got = err
					break
				}
				if ev.Image == nil {
					t.Fatal("expected preview")
				}
				if stop {
					break
				}
				cancel()
			}
			if !stop && !errors.Is(got, context.Canceled) {
				t.Fatalf("cancel error = %v", got)
			}
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("request body was not closed/cancelled")
			}
		})
	}
}

func TestImageStreamValidation(t *testing.T) {
	t.Parallel()
	g := newImageFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid request sent") })
	for _, req := range []*imagegen.Request{nil, {Prompt: "x", N: 2}, {Prompt: "x", PartialImages: -1}, {Prompt: "x", PartialImages: 4}} {
		var got error
		for _, err := range g.Stream(t.Context(), req) {
			got = err
		}
		if got == nil || model.IsRetryable(got) {
			t.Fatalf("error = %v", got)
		}
	}
	var got error
	for _, err := range g.StreamEdit(t.Context(), nil) {
		got = err
	}
	if got == nil || !strings.Contains(got.Error(), "required") {
		t.Fatalf("nil edit error = %v", got)
	}
}
