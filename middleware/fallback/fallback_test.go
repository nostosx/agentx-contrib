package fallback_test

import (
	"context"
	"errors"
	"io"
	"iter"
	"testing"

	"github.com/nostosx/agentx-contrib/middleware/fallback"
	"github.com/nostosx/agentx/model"
)

func TestGenerate(t *testing.T) {
	t.Parallel()
	failure := &model.APIError{Provider: "first", StatusCode: 503}
	var names []string
	first := model.Func(func(_ context.Context, r *model.Request) (*model.Response, error) {
		names = append(names, r.Model)
		return nil, failure
	})
	last := model.Func(func(_ context.Context, r *model.Request) (*model.Response, error) {
		names = append(names, r.Model)
		return &model.Response{Message: model.AssistantText("ok")}, nil
	})
	m, err := fallback.New([]fallback.Target{{Model: first, ModelName: "primary"}, {Model: last, ModelName: "backup"}}, fallback.Options{})
	if err != nil {
		t.Fatal(err)
	}
	req := &model.Request{Model: "original"}
	out, err := m.Generate(t.Context(), req)
	if err != nil || out.Text() != "ok" || len(names) != 2 || names[0] != "primary" || names[1] != "backup" || req.Model != "original" {
		t.Fatalf("Generate=%+v, %v, names=%v", out, err, names)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.Generate(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

type streamModel struct {
	model.Model
	events  []*model.StreamEvent
	failure error
}

func (m streamModel) Stream(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		for _, ev := range m.events {
			if !yield(ev, nil) {
				return
			}
		}
		if m.failure != nil {
			yield(nil, m.failure)
		}
	}
}

func TestStreamBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		events   []*model.StreamEvent
		failure  error
		fallback bool
	}{
		{name: "failure before output", failure: &model.APIError{StatusCode: 503}, fallback: true},
		{name: "empty stream", fallback: true},
		{name: "partial output", events: []*model.StreamEvent{{Type: model.StreamDelta, Delta: new(model.TextPart("partial"))}}, failure: io.ErrUnexpectedEOF},
		{name: "permanent failure", failure: &model.APIError{StatusCode: 401}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			backup := model.Func(func(context.Context, *model.Request) (*model.Response, error) {
				calls++
				return &model.Response{Message: model.AssistantText("backup"), FinishReason: model.FinishStop}, nil
			})
			m, err := fallback.New([]fallback.Target{{Model: streamModel{events: tc.events, failure: tc.failure}}, {Model: backup}}, fallback.Options{})
			if err != nil {
				t.Fatal(err)
			}
			var gotErr error
			done := false
			for ev, err := range m.Stream(t.Context(), &model.Request{}) {
				if err != nil {
					gotErr = err
				}
				if ev != nil && ev.Type == model.StreamDone {
					done = true
				}
			}
			if tc.fallback != (calls == 1) || tc.fallback != done || tc.fallback != (gotErr == nil) {
				t.Fatalf("backup=%d done=%v err=%v", calls, done, gotErr)
			}
		})
	}
}

func TestFailureChain(t *testing.T) {
	t.Parallel()
	a, b := errors.New("one"), errors.New("two")
	m, err := fallback.New([]fallback.Target{{Model: model.Func(func(context.Context, *model.Request) (*model.Response, error) { return nil, a })}, {Model: model.Func(func(context.Context, *model.Request) (*model.Response, error) { return nil, b })}}, fallback.Options{ShouldFallback: func(error) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Generate(t.Context(), &model.Request{})
	if !errors.Is(err, a) || !errors.Is(err, b) {
		t.Fatalf("error chain=%v", err)
	}
}

func TestFinalFailureClassification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		first, last int
		retry       bool
	}{
		{name: "permanent final failure", first: 503, last: 401},
		{name: "transient final failure", first: 401, last: 503, retry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, last := &model.APIError{StatusCode: tc.first}, &model.APIError{StatusCode: tc.last}
			m, err := fallback.New([]fallback.Target{{Model: model.Func(func(context.Context, *model.Request) (*model.Response, error) { return nil, first })}, {Model: model.Func(func(context.Context, *model.Request) (*model.Response, error) { return nil, last })}}, fallback.Options{ShouldFallback: func(error) bool { return true }})
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.Generate(t.Context(), &model.Request{})
			if !errors.Is(err, first) || !errors.Is(err, last) || model.IsRetryable(err) != tc.retry {
				t.Fatalf("final error=%v retry=%v", err, model.IsRetryable(err))
			}
		})
	}
}
