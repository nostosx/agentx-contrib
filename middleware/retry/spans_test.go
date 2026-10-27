package retry_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nostosx/agentx-contrib/middleware/retry"
	"github.com/nostosx/agentx/middleware"
	"github.com/nostosx/agentx/model"
	"github.com/nostosx/agentx/model/modeltest"
	"github.com/nostosx/agentx/telemetry"
)

// event is a span event a recordingTracer recorded.
type event struct {
	name  string
	attrs map[string]any
}

// recordingSpan is a telemetry.EventSpan that records what it is given.
type recordingSpan struct {
	name   string
	mu     sync.Mutex
	events []event
	errors []error
}

func (s *recordingSpan) SetAttributes(...telemetry.Attr) {}

func (s *recordingSpan) RecordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors = append(s.errors, err)
}

func (s *recordingSpan) End() {}

func (s *recordingSpan) AddEvent(name string, attrs ...telemetry.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := event{name: name, attrs: map[string]any{}}
	for _, a := range attrs {
		ev.attrs[a.Key] = a.Value
	}
	s.events = append(s.events, ev)
}

// recordingTracer records the spans it starts.
type recordingTracer struct {
	mu    sync.Mutex
	spans []*recordingSpan
}

func (tr *recordingTracer) Start(ctx context.Context, name string, _ telemetry.SpanKind, _ ...telemetry.Attr) (context.Context, telemetry.Span) {
	s := &recordingSpan{name: name}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.spans = append(tr.spans, s)
	return ctx, s
}

func (tr *recordingTracer) find(t *testing.T, name string) *recordingSpan {
	t.Helper()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, s := range tr.spans {
		if s.name == name {
			return s
		}
	}
	t.Fatalf("no span %q among %d spans", name, len(tr.spans))
	return nil
}

func TestRetryEvents(t *testing.T) {
	t.Parallel()
	synctest.Test(t, testRetryEvents)
}

func testRetryEvents(t *testing.T) {
	tracer := &recordingTracer{}
	failure := &model.APIError{Provider: "openai", StatusCode: http.StatusServiceUnavailable, Type: "overloaded"}
	base := modeltest.New(modeltest.Fail(failure), modeltest.Reply("ok"))
	policy := retry.Policy{InitialBackoff: time.Millisecond, Jitter: -1}
	m := middleware.Wrap(base, middleware.TraceModel(tracer), retry.Model(policy))
	if _, err := m.Generate(t.Context(), &model.Request{Model: "x"}); err != nil {
		t.Fatal(err)
	}
	span := tracer.find(t, "chat x")
	if len(span.events) != 1 {
		t.Fatalf("events = %+v", span.events)
	}
	ev := span.events[0]
	if ev.name != "agentx.retry" || ev.attrs["agentx.retry.attempt"] != 1 || ev.attrs["error.type"] != "overloaded" || ev.attrs["agentx.retry.delay"] != 0.001 {
		t.Fatalf("event = %+v", ev)
	}
	if len(span.errors) != 0 {
		t.Fatalf("a recovered call recorded errors: %v", span.errors)
	}
}
