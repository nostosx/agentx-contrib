package otel_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"slices"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/nostosx/agentx-contrib/otel"
	"github.com/nostosx/agentx/agent"
	"github.com/nostosx/agentx/loop"
	"github.com/nostosx/agentx/middleware"
	"github.com/nostosx/agentx/model"
	"github.com/nostosx/agentx/model/modeltest"
	"github.com/nostosx/agentx/telemetry"
	"github.com/nostosx/agentx/tool"
)

// described adds a model.Describer to a model, as the providers have.
type described struct {
	model.Model
	d model.Description
}

func (m described) Describe() model.Description { return m.d }

var description = model.Description{Provider: "openai", Operation: "chat", Model: "gpt-fixture", ServerAddress: "api.openai.com", ServerPort: 443}

// logExporter collects log records.
type logExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *logExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range records {
		e.records = append(e.records, r.Clone())
	}
	return nil
}

func (*logExporter) Shutdown(context.Context) error   { return nil }
func (*logExporter) ForceFlush(context.Context) error { return nil }

func (e *logExporter) events(name string) []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []sdklog.Record
	for _, r := range e.records {
		if r.EventName() == name {
			out = append(out, r)
		}
	}
	return out
}

type providers struct {
	spans  *tracetest.InMemoryExporter
	reader *sdkmetric.ManualReader
	logs   *logExporter
	tp     *sdktrace.TracerProvider
	mp     *sdkmetric.MeterProvider
	lp     *sdklog.LoggerProvider
}

func newProviders(t *testing.T) *providers {
	t.Helper()
	p := &providers{spans: tracetest.NewInMemoryExporter(), reader: sdkmetric.NewManualReader(), logs: &logExporter{}}
	p.tp = sdktrace.NewTracerProvider(sdktrace.WithSyncer(p.spans))
	p.mp = sdkmetric.NewMeterProvider(sdkmetric.WithReader(p.reader))
	p.lp = sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(p.logs)))
	t.Cleanup(func() {
		ctx := context.Background()
		_ = p.tp.Shutdown(ctx)
		_ = p.mp.Shutdown(ctx)
		_ = p.lp.Shutdown(ctx)
	})
	return p
}

func (p *providers) span(t *testing.T, name string) tracetest.SpanStub {
	t.Helper()
	var found []tracetest.SpanStub
	for _, s := range p.spans.GetSpans() {
		if s.Name == name {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d spans named %q", len(found), name)
	}
	return found[0]
}

func (p *providers) metrics(t *testing.T) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := p.reader.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != otel.ScopeName {
			t.Errorf("scope = %q", sm.Scope.Name)
		}
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func attrs(kvs []attribute.KeyValue) map[string]attribute.Value {
	out := map[string]attribute.Value{}
	for _, kv := range kvs {
		out[string(kv.Key)] = kv.Value
	}
	return out
}

func echoTool() tool.Tool {
	return tool.Func("echo", "Echo text.", func(_ context.Context, in struct {
		Text string `json:"text"`
	}) (string, error) {
		return in.Text, nil
	})
}

// TestMiddlewareRun checks the spans, metrics and events of a run with a
// tool call and a sub-agent.
func TestMiddlewareRun(t *testing.T) {
	t.Parallel()
	p := newProviders(t)
	mws := otel.Middleware(p.tp, p.mp, otel.RecordContent(), otel.WithLoggerProvider(p.lp))
	researcher := agent.MustNew("researcher", agent.WithDescription("Researches."),
		agent.WithModel(described{modeltest.New(modeltest.Reply("found it")), description}),
		agent.WithMiddleware(mws...))
	usage := model.Usage{InputTokens: 7, OutputTokens: 5, CacheReadTokens: 2, CacheWriteTokens: 1, ReasoningTokens: 3}
	m := modeltest.New(
		modeltest.CallTools(
			modeltest.Call{ID: "c1", Name: "echo", Args: map[string]string{"text": "hi"}},
			modeltest.Call{ID: "c2", Name: "researcher", Args: map[string]string{"prompt": "look it up"}},
		),
		modeltest.Respond(&model.Response{ID: "resp_2", Model: "gpt-fixture-2026", Message: model.AssistantText("done"), FinishReason: model.FinishStop, Usage: usage}),
	)
	a := agent.MustNew("assistant", agent.WithModel(described{m, description}), agent.WithTools(echoTool()), agent.WithSubAgents(researcher),
		agent.WithMiddleware(mws...))
	res, err := agent.NewRunner(a, nil).RunText(t.Context(), "session-1", "hi")
	if err != nil {
		t.Fatal(err)
	}

	// Span tree.
	stubs := p.spans.GetSpans()
	if len(stubs) != 7 {
		names := make([]string, len(stubs))
		for i, s := range stubs {
			names[i] = s.Name
		}
		t.Fatalf("spans = %v", names)
	}
	root := p.span(t, "invoke_agent assistant")
	echo := p.span(t, "execute_tool echo")
	call := p.span(t, "execute_tool researcher")
	sub := p.span(t, "invoke_agent researcher")
	if root.Parent.IsValid() || root.SpanKind != trace.SpanKindInternal {
		t.Fatalf("root = %+v", root)
	}
	childOf := func(s, parent tracetest.SpanStub) {
		t.Helper()
		if s.Parent.SpanID() != parent.SpanContext.SpanID() || s.SpanContext.TraceID() != root.SpanContext.TraceID() {
			t.Errorf("span %q is not a child of %q", s.Name, parent.Name)
		}
	}
	childOf(echo, root)
	childOf(call, root)
	childOf(sub, call)
	var chats []tracetest.SpanStub
	for _, s := range stubs {
		if s.Name == "chat gpt-fixture" {
			chats = append(chats, s)
			if s.SpanKind != trace.SpanKindClient {
				t.Errorf("model span kind = %v", s.SpanKind)
			}
			if attrs(s.Attributes)["gen_ai.agent.name"].AsString() == "researcher" {
				childOf(s, sub)
			} else {
				childOf(s, root)
			}
		}
	}
	if len(chats) != 3 {
		t.Fatalf("%d model spans", len(chats))
	}

	// Attributes.
	ra := attrs(root.Attributes)
	if ra["gen_ai.operation.name"].AsString() != "invoke_agent" || ra["gen_ai.agent.name"].AsString() != "assistant" ||
		ra["gen_ai.conversation.id"].AsString() != "session-1" || ra["agentx.run_id"].AsString() != res.RunID ||
		ra["gen_ai.usage.input_tokens"].AsInt64() != int64(res.Usage.PromptTokens()) || ra["agentx.run.status"].AsString() != "completed" {
		t.Errorf("agent attributes = %v", ra)
	}
	var final map[string]attribute.Value
	for _, s := range chats {
		if a := attrs(s.Attributes); a["gen_ai.response.id"].AsString() == "resp_2" {
			final = a
		}
	}
	want := map[string]any{
		"gen_ai.operation.name":                 "chat",
		"gen_ai.provider.name":                  "openai",
		"gen_ai.request.model":                  "gpt-fixture",
		"gen_ai.response.model":                 "gpt-fixture-2026",
		"server.address":                        "api.openai.com",
		"server.port":                           int64(443),
		"gen_ai.usage.input_tokens":             int64(10),
		"gen_ai.usage.output_tokens":            int64(5),
		"gen_ai.usage.cache_read.input_tokens":  int64(2),
		"gen_ai.usage.cache_write.input_tokens": int64(1),
		"gen_ai.usage.reasoning.output_tokens":  int64(3),
		"gen_ai.response.finish_reasons":        []string{"stop"},
		"agentx.turn":                           int64(2),
	}
	for k, v := range want {
		if got := final[k].AsInterface(); fmt.Sprint(got) != fmt.Sprint(v) {
			t.Errorf("%s = %v, want %v", k, got, v)
		}
	}
	if final["gen_ai.output.messages"].AsString() != `[{"role":"assistant","parts":[{"type":"text","content":"done"}]}]` {
		t.Errorf("output messages = %v", final["gen_ai.output.messages"].AsString())
	}
	ea := attrs(echo.Attributes)
	if ea["gen_ai.tool.name"].AsString() != "echo" || ea["gen_ai.tool.call.id"].AsString() != "c1" || ea["gen_ai.tool.call.result"].AsString() != "hi" {
		t.Errorf("tool attributes = %v", ea)
	}
	if root.InstrumentationScope.Name != otel.ScopeName || root.InstrumentationScope.Version == "" {
		t.Errorf("scope = %+v", root.InstrumentationScope)
	}

	// Metrics.
	ms := p.metrics(t)
	duration := ms["gen_ai.client.inference.duration"]
	if duration.Unit != "s" {
		t.Errorf("duration unit = %q", duration.Unit)
	}
	var calls uint64
	for _, dp := range duration.Data.(metricdata.Histogram[float64]).DataPoints {
		calls += dp.Count
		if dp.Bounds[0] != 0.01 || dp.Bounds[len(dp.Bounds)-1] != 81.92 {
			t.Errorf("duration bounds = %v", dp.Bounds)
		}
		if v, _ := dp.Attributes.Value("gen_ai.provider.name"); v.AsString() != "openai" {
			t.Errorf("duration attributes = %v", dp.Attributes)
		}
	}
	if calls != 3 {
		t.Errorf("%d inference durations", calls)
	}
	sum := func(name, agentName string) int64 {
		t.Helper()
		var total int64
		for _, dp := range ms[name].Data.(metricdata.Sum[int64]).DataPoints {
			if v, _ := dp.Attributes.Value("gen_ai.token.modality"); v.AsString() != "unknown" {
				t.Errorf("%s modality = %v", name, v)
			}
			if v, _ := dp.Attributes.Value("gen_ai.agent.name"); v.AsString() == agentName {
				total += dp.Value
			}
		}
		return total
	}
	// The assistant's two calls used 10+10 input tokens (the scripted tool
	// call reports 10), the researcher's one call 10.
	if got := sum("gen_ai.client.inference.usage.input_tokens", "assistant"); got != 20 {
		t.Errorf("assistant input tokens = %d", got)
	}
	if got := sum("gen_ai.client.inference.usage.input_tokens", "researcher"); got != 10 {
		t.Errorf("researcher input tokens = %d", got)
	}
	if got := sum("gen_ai.client.inference.usage.reasoning.output_tokens", "assistant"); got != 3 {
		t.Errorf("reasoning tokens = %d", got)
	}
	if got := sum("gen_ai.client.inference.usage.cache_read.input_tokens", "assistant"); got != 2 {
		t.Errorf("cache read tokens = %d", got)
	}
	opIn := ms["gen_ai.client.inference.operation.input_tokens"].Data.(metricdata.Histogram[int64])
	if dp := opIn.DataPoints[0]; dp.Bounds[len(dp.Bounds)-1] != 67108864 || ms["gen_ai.client.inference.operation.input_tokens"].Unit != "{token}" {
		t.Errorf("token histogram = %+v", ms["gen_ai.client.inference.operation.input_tokens"])
	}
	tools := map[string]uint64{}
	for _, dp := range ms["gen_ai.execute_tool.duration"].Data.(metricdata.Histogram[float64]).DataPoints {
		v, _ := dp.Attributes.Value("gen_ai.tool.name")
		tools[v.AsString()] += dp.Count
	}
	if tools["echo"] != 1 || tools["researcher"] != 1 {
		t.Errorf("tool durations = %v", tools)
	}
	runs := map[string]int64{}
	for _, dp := range ms["gen_ai.invoke_agent.tool_calls"].Data.(metricdata.Histogram[int64]).DataPoints {
		v, _ := dp.Attributes.Value("gen_ai.agent.name")
		runs[v.AsString()] = dp.Sum
	}
	if runs["assistant"] != 2 || runs["researcher"] != 0 {
		t.Errorf("tool calls per run = %v", runs)
	}
	for _, dp := range ms["gen_ai.invoke_agent.duration"].Data.(metricdata.Histogram[float64]).DataPoints {
		if v, _ := dp.Attributes.Value("agentx.run.status"); v.AsString() != "completed" || dp.Count != 1 {
			t.Errorf("run duration = %+v", dp)
		}
	}

	// Events.
	details := p.logs.events("gen_ai.client.inference.operation.details")
	if len(details) != 3 {
		t.Fatalf("%d details events", len(details))
	}
	var detail map[string]attribute.Value
	for _, r := range details {
		if r.Severity() != log.SeverityDebug || !r.TraceID().IsValid() {
			t.Errorf("event severity = %v, trace = %v", r.Severity(), r.TraceID())
		}
		got := map[string]attribute.Value{}
		r.WalkAttributes(func(kv attribute.KeyValue) bool {
			got[string(kv.Key)] = kv.Value
			return true
		})
		if got["gen_ai.response.id"].AsString() == "resp_2" {
			detail = got
		}
	}
	if detail["gen_ai.provider.name"].AsString() != "openai" || detail["gen_ai.usage.input_tokens"].AsInt64() != 10 {
		t.Errorf("details attributes = %v", detail)
	}
	out := detail["gen_ai.output.messages"]
	if out.Type() != attribute.SLICE {
		t.Fatalf("structured output = %v (%v)", out, out.Type())
	}
	msg := attrs(out.AsSlice()[0].AsMap())
	part := attrs(msg["parts"].AsSlice()[0].AsMap())
	if msg["role"].AsString() != "assistant" || part["type"].AsString() != "text" || part["content"].AsString() != "done" {
		t.Errorf("structured output = %v", msg)
	}
	if len(p.logs.events("gen_ai.client.operation.exception")) != 0 {
		t.Error("exception event for a successful run")
	}
}

// retryOnce retries a failed model call once and records the retry on the
// call's span like the retry middleware of
// github.com/nostosx/agentx-contrib/middleware, which contrib modules do
// not import.
type retryOnce struct{}

func (retryOnce) Name() string { return "retry" }

func (retryOnce) WrapModel(_ *loop.Run, next model.Model) model.Model {
	note := func(ctx context.Context, err error) {
		telemetry.AddEvent(ctx, "agentx.retry",
			telemetry.Attr{Key: "agentx.retry.attempt", Value: 1},
			telemetry.Attr{Key: "agentx.retry.delay", Value: 0.001},
			telemetry.Attr{Key: "error.type", Value: telemetry.ErrorType(err)})
	}
	return &middleware.Funcs{
		Next: next,
		GenerateFunc: func(ctx context.Context, req *model.Request) (*model.Response, error) {
			resp, err := next.Generate(ctx, req)
			if err == nil {
				return resp, nil
			}
			note(ctx, err)
			return next.Generate(ctx, req)
		},
		StreamFunc: func(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
			return func(yield func(*model.StreamEvent, error) bool) {
				var failure error
				for ev, err := range next.Stream(ctx, req) {
					if err != nil {
						failure = err
						break
					}
					if !yield(ev, nil) {
						return
					}
				}
				if failure == nil {
					return
				}
				note(ctx, failure)
				for ev, err := range next.Stream(ctx, req) {
					if !yield(ev, err) {
						return
					}
				}
			}
		},
	}
}

func TestErrorsAndRetries(t *testing.T) {
	t.Parallel()
	p := newProviders(t)
	retryable := &model.APIError{Provider: "openai", StatusCode: http.StatusServiceUnavailable, Type: "overloaded"}
	failure := fmt.Errorf("call model: %w", &model.APIError{Provider: "openai", StatusCode: http.StatusBadRequest, Type: "invalid_request_error"})
	m := modeltest.New(modeltest.Fail(retryable), modeltest.Fail(failure))
	mws := append(otel.Middleware(p.tp, p.mp, otel.WithLoggerProvider(p.lp)), retryOnce{})
	a := agent.MustNew("assistant", agent.WithModel(m), agent.WithMiddleware(mws...))
	if _, err := a.Ask(t.Context(), "q"); !errors.Is(err, failure) {
		t.Fatalf("Ask = %v", err)
	}

	span := p.span(t, "chat")
	if span.Status.Code != codes.Error || attrs(span.Attributes)["error.type"].AsString() != "invalid_request_error" {
		t.Errorf("span status = %+v, attributes = %v", span.Status, span.Attributes)
	}
	var names []string
	for _, ev := range span.Events {
		names = append(names, ev.Name)
	}
	if !slices.Equal(names, []string{"agentx.retry", "exception"}) {
		t.Fatalf("span events = %v", names)
	}
	retry := attrs(span.Events[0].Attributes)
	if retry["agentx.retry.attempt"].AsInt64() != 1 || retry["error.type"].AsString() != "overloaded" || retry["agentx.retry.delay"].AsFloat64() <= 0 {
		t.Errorf("retry event = %v", retry)
	}
	if root := p.span(t, "invoke_agent assistant"); root.Status.Code != codes.Error {
		t.Errorf("run status = %+v", root.Status)
	}

	exceptions := p.logs.events("gen_ai.client.operation.exception")
	if len(exceptions) != 1 {
		t.Fatalf("%d exception events", len(exceptions))
	}
	got := map[string]string{}
	exceptions[0].WalkAttributes(func(kv attribute.KeyValue) bool {
		got[string(kv.Key)] = kv.Value.AsString()
		return true
	})
	if exceptions[0].Severity() != log.SeverityWarn || got["exception.type"] != "*model.APIError" || got["exception.message"] != failure.Error() {
		t.Errorf("exception event = %v (severity %v)", got, exceptions[0].Severity())
	}
	if exceptions[0].SpanID() != span.SpanContext.SpanID() {
		t.Error("exception event is not correlated with its span")
	}

	ms := p.metrics(t)
	dps := ms["gen_ai.client.inference.duration"].Data.(metricdata.Histogram[float64]).DataPoints
	if len(dps) != 1 {
		t.Fatalf("duration points = %+v", dps)
	}
	if v, _ := dps[0].Attributes.Value("error.type"); v.AsString() != "invalid_request_error" {
		t.Errorf("duration error.type = %v", v)
	}
	run := ms["gen_ai.invoke_agent.duration"].Data.(metricdata.Histogram[float64]).DataPoints[0]
	if v, _ := run.Attributes.Value("agentx.run.status"); v.AsString() != "failed" {
		t.Errorf("run status = %v", v)
	}
}

func TestLegacyMetrics(t *testing.T) {
	t.Parallel()
	p := newProviders(t)
	a := agent.MustNew("assistant", agent.WithModel(modeltest.New(modeltest.Reply("x"))),
		agent.WithMiddleware(otel.Middleware(p.tp, p.mp, otel.WithMetricsOptions(middleware.LegacyMetrics()), otel.WithTraceOptions())...))
	if _, err := a.Ask(t.Context(), "q"); err != nil {
		t.Fatal(err)
	}
	ms := p.metrics(t)
	usage := map[string]int64{}
	for _, dp := range ms["gen_ai.client.token.usage"].Data.(metricdata.Histogram[int64]).DataPoints {
		v, _ := dp.Attributes.Value("gen_ai.token.type")
		usage[v.AsString()] = dp.Sum
	}
	if usage["input"] != 10 || usage["output"] != 5 {
		t.Errorf("token usage = %v", usage)
	}
	if _, ok := ms["gen_ai.client.operation.duration"]; !ok {
		t.Error("no gen_ai.client.operation.duration")
	}
	// Content is opt-in, and events need a logger provider.
	for _, s := range p.spans.GetSpans() {
		if _, ok := attrs(s.Attributes)["gen_ai.input.messages"]; ok {
			t.Errorf("span %q records content", s.Name)
		}
	}
}
