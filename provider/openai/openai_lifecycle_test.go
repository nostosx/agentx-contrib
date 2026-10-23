package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nostosx/agentx/model"
)

type lifecycleRoundTripper func(*http.Request) (*http.Response, error)

func (f lifecycleRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type lifecycleBody struct {
	chunks []string
	chunk  int
	offset int
	reads  int
	closes int
}

func (b *lifecycleBody) Read(p []byte) (int, error) {
	b.reads++
	if b.closes > 0 {
		return 0, errors.New("read after close")
	}
	if b.chunk == len(b.chunks) {
		return 0, io.EOF
	}
	n := copy(p, b.chunks[b.chunk][b.offset:])
	b.offset += n
	if b.offset == len(b.chunks[b.chunk]) {
		b.chunk++
		b.offset = 0
	}
	return n, nil
}

func (b *lifecycleBody) Close() error { b.closes++; return nil }

func lifecycleModel(t *testing.T, transport http.RoundTripper) *Model {
	t.Helper()
	m, err := New(WithAPIKey("fixture"), WithBaseURL("https://fixture.invalid/v1"), WithHTTPClient(&http.Client{Transport: transport}), WithChatCompletions())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestStreamPreservesUsageAfterFinishReason(t *testing.T) {
	t.Parallel()
	body := &lifecycleBody{chunks: []string{
		"data: " + `{"id":"response","model":"fixture","choices":[{"delta":{"content":"hello"}}]}` + "\n\n",
		"data: " + `{"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n",
		"data: " + `{"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":5},"completion_tokens_details":{"reasoning_tokens":3}}}` + "\n\n",
		"data: [DONE]\n\n",
	}}
	m := lifecycleModel(t, lifecycleRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body, Request: req}, nil
	}))
	resp, err := model.Collect(m.Stream(t.Context(), &model.Request{}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "response" || resp.Model != "fixture" || resp.Text() != "hello" || resp.FinishReason != model.FinishStop {
		t.Fatalf("response=%+v", resp)
	}
	wantUsage := model.Usage{InputTokens: 15, OutputTokens: 8, CacheReadTokens: 5, ReasoningTokens: 3}
	if resp.Usage != wantUsage {
		t.Fatalf("tail usage=%+v, want %+v", resp.Usage, wantUsage)
	}
	if body.chunk != len(body.chunks) || body.closes != 1 {
		t.Fatalf("consumed chunks=%d/%d closes=%d", body.chunk, len(body.chunks), body.closes)
	}
}

func TestHTTPRequestsPreserveCancellation(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"generate", "stream"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			m := lifecycleModel(t, lifecycleRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls++
				cancel()
				return nil, req.Context().Err()
			}))
			var err error
			if mode == "generate" {
				_, err = m.Generate(ctx, &model.Request{})
			} else {
				_, err = model.Collect(m.Stream(ctx, &model.Request{}))
			}
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("%s error=%v HTTP calls=%d", mode, err, calls)
			}
		})
	}
}

func TestStreamEarlyStopClosesBodyWithoutReadingRemainder(t *testing.T) {
	t.Parallel()
	first := "data: " + `{"choices":[{"delta":{"content":"first"}}]}` + "\n\n"
	tail := "data: " + `{"choices":[{"delta":{"content":"unread"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	body := &lifecycleBody{chunks: []string{first, tail}}
	m := lifecycleModel(t, lifecycleRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body, Request: req}, nil
	}))
	events := 0
	for ev, err := range m.Stream(t.Context(), &model.Request{}) {
		if err != nil {
			t.Fatal(err)
		}
		events++
		if ev == nil || ev.Type != model.StreamDelta || ev.Delta == nil || ev.Delta.Text != "first" {
			t.Fatalf("first event=%+v", ev)
		}
		break
	}
	if events != 1 || body.closes != 1 || body.reads != 1 || body.chunk != 1 || body.offset != 0 {
		t.Fatalf("events=%d closes=%d reads=%d consumed=%d/%d offset=%d", events, body.closes, body.reads, body.chunk, len(body.chunks), body.offset)
	}
	if !strings.Contains(body.chunks[body.chunk], "unread") {
		t.Fatal("recording body has no unread tail")
	}
}
