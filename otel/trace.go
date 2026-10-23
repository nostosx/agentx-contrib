package otel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"

	"github.com/nostosx/agentx/middleware"
)

type tracer struct {
	t      trace.Tracer
	logger log.Logger
}

func spanKind(k middleware.SpanKind) trace.SpanKind {
	if k == middleware.SpanKindClient {
		return trace.SpanKindClient
	}
	return trace.SpanKindInternal
}

// Start implements middleware.Tracer. The attributes are given at span
// creation, so samplers see them.
func (t *tracer) Start(ctx context.Context, name string, kind middleware.SpanKind, attrs ...middleware.Attr) (context.Context, middleware.Span) {
	ctx, s := t.t.Start(ctx, name, trace.WithSpanKind(spanKind(kind)), trace.WithAttributes(attributes(attrs)...))
	sp := &span{s: s}
	if t.logger != nil && kind == middleware.SpanKindClient {
		sp.events = &events{logger: t.logger, ctx: ctx, attrs: slices.Clone(attrs)}
	}
	return ctx, sp
}

// span adapts a span; it implements middleware.EventSpan.
type span struct {
	s trace.Span
	// events is set for the model call spans of a tracer with a logger.
	events *events
}

func (s *span) SetAttributes(attrs ...middleware.Attr) {
	s.s.SetAttributes(attributes(attrs)...)
	if s.events != nil {
		s.events.add(attrs)
	}
}

func (s *span) RecordError(err error) {
	if err == nil {
		return
	}
	s.s.RecordError(err)
	s.s.SetStatus(codes.Error, err.Error())
	if s.events != nil {
		s.events.exception(err)
	}
}

func (s *span) AddEvent(name string, attrs ...middleware.Attr) {
	s.s.AddEvent(name, trace.WithAttributes(attributes(attrs)...))
}

func (s *span) End() {
	if s.events != nil {
		s.events.details()
	}
	s.s.End()
}

// The GenAI events of model calls.
const (
	detailsEvent   = "gen_ai.client.inference.operation.details"
	exceptionEvent = "gen_ai.client.operation.exception"
)

// events emits the log-based events of a model call span.
type events struct {
	logger log.Logger
	ctx    context.Context
	mu     sync.Mutex
	attrs  []middleware.Attr
}

func (e *events) add(attrs []middleware.Attr) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.attrs = append(e.attrs, attrs...)
}

// contentKeys are the attributes holding JSON content, which events carry
// in structured form.
var contentKeys = map[string]bool{
	"gen_ai.system_instructions": true,
	"gen_ai.input.messages":      true,
	"gen_ai.output.messages":     true,
	"gen_ai.tool.definitions":    true,
}

// details emits the gen_ai.client.inference.operation.details event with
// the span's attributes; for a key set more than once the last value wins.
func (e *events) details() {
	e.mu.Lock()
	attrs := e.attrs
	e.mu.Unlock()
	var rec log.Record
	rec.SetEventName(detailsEvent)
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(log.SeverityDebug)
	rec.SetSeverityText("DEBUG")
	seen := map[string]bool{}
	kvs := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range slices.Backward(attrs) {
		if seen[a.Key] {
			continue
		}
		seen[a.Key] = true
		if s, ok := a.Value.(string); ok && contentKeys[a.Key] {
			kvs = append(kvs, attribute.KeyValue{Key: attribute.Key(a.Key), Value: jsonValue(s)})
			continue
		}
		if kv := keyValue(a); kv.Valid() {
			kvs = append(kvs, kv)
		}
	}
	slices.Reverse(kvs)
	rec.AddAttributes(kvs...)
	e.logger.Emit(e.ctx, rec)
}

// exception emits the gen_ai.client.operation.exception event for err.
func (e *events) exception(err error) {
	var rec log.Record
	rec.SetEventName(exceptionEvent)
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(log.SeverityWarn)
	rec.SetSeverityText("WARN")
	rec.AddAttributes(
		attribute.String("exception.type", exceptionType(err)),
		attribute.String("exception.message", err.Error()),
	)
	e.logger.Emit(e.ctx, rec)
}

// exceptionType returns the Go type of err, unwrapping the wrappers of
// fmt.Errorf, which do not classify failures, as the conventions allow.
func exceptionType(err error) string {
	for {
		t := fmt.Sprintf("%T", err)
		if t != "*fmt.wrapError" && t != "*fmt.wrapErrors" {
			return t
		}
		inner := errors.Unwrap(err)
		if inner == nil {
			if u, ok := err.(interface{ Unwrap() []error }); ok && len(u.Unwrap()) > 0 {
				inner = u.Unwrap()[0]
			}
		}
		if inner == nil {
			return t
		}
		err = inner
	}
}
