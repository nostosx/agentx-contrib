// Package otel adapts agentx's tracing and metrics to OpenTelemetry.
//
// The agentx module uses only the standard library and kitx. This package is
// a separate module, github.com/nostosx/agentx-contrib/otel, so programs using
// OpenTelemetry take on its dependencies.
//
// Tracer and Meter adapt OpenTelemetry providers to telemetry.Tracer and
// telemetry.Meter, and Middleware returns the tracing and metrics
// middleware of an agent:
//
//	a := agent.MustNew("assistant",
//		agent.WithModel(m),
//		agent.WithMiddleware(otel.Middleware(tp, mp)...),
//	)
//
// Spans, metrics and events follow the OpenTelemetry GenAI semantic
// conventions (https://github.com/open-telemetry/semantic-conventions-genai);
// see telemetry.Tracer and telemetry.Meter for what is recorded.
package otel

import (
	"slices"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	agentx "github.com/nostosx/agentx"
	"github.com/nostosx/agentx/loop"
	"github.com/nostosx/agentx/middleware"
	"github.com/nostosx/agentx/telemetry"
)

// ScopeName is the instrumentation scope of the telemetry this package
// produces. Its version is agentx.Version.
const ScopeName = "github.com/nostosx/agentx-contrib/otel"

// Option configures the adapters.
type Option func(*config)

type config struct {
	logs       log.LoggerProvider
	content    bool
	traceOpts  []middleware.TraceOption
	metricOpts []middleware.MetricsOption
}

func newConfig(opts []Option) *config {
	c := &config{}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// WithLoggerProvider emits the GenAI events of the conventions as log
// records of lp: for each model call a gen_ai.client.inference.operation.details
// event (severity DEBUG) with the attributes of its span, and for each
// failed model call a gen_ai.client.operation.exception event (severity
// WARN). With RecordContent, the details event carries the content in
// structured form, while spans carry it as JSON strings.
func WithLoggerProvider(lp log.LoggerProvider) Option {
	return func(c *config) { c.logs = lp }
}

// RecordContent makes Middleware record the content of model and tool
// calls; see middleware.RecordContent. It may contain sensitive data.
func RecordContent() Option { return func(c *config) { c.content = true } }

// WithTraceOptions passes options to the tracing middleware of Middleware.
func WithTraceOptions(opts ...middleware.TraceOption) Option {
	return func(c *config) { c.traceOpts = append(c.traceOpts, opts...) }
}

// WithMetricsOptions passes options to the metrics middleware of
// Middleware, such as middleware.LegacyMetrics.
func WithMetricsOptions(opts ...middleware.MetricsOption) Option {
	return func(c *config) { c.metricOpts = append(c.metricOpts, opts...) }
}

// Middleware returns middleware.Tracing and middleware.Metrics backed by tp
// and mp, in that order. A nil provider uses the global one. Register them
// before retries, such as the retry middleware of
// github.com/nostosx/agentx-contrib/middleware, so a model span and its
// duration cover the retries, which are recorded as span events.
func Middleware(tp trace.TracerProvider, mp metric.MeterProvider, opts ...Option) []loop.Middleware {
	c := newConfig(opts)
	traceOpts := c.traceOpts
	if c.content {
		traceOpts = append(slices.Clip(traceOpts), middleware.RecordContent())
	}
	return []loop.Middleware{
		middleware.Tracing(Tracer(tp, opts...), traceOpts...),
		middleware.Metrics(Meter(mp, opts...), c.metricOpts...),
	}
}

// Tracer adapts tp to telemetry.Tracer; a nil tp uses the global provider.
// Span kinds map to OpenTelemetry's, attributes to attribute values, and a
// recorded error to an exception event and an error status. Only
// WithLoggerProvider applies.
func Tracer(tp trace.TracerProvider, opts ...Option) telemetry.Tracer {
	if tp == nil {
		tp = otelapi.GetTracerProvider()
	}
	t := &tracer{t: tp.Tracer(ScopeName, trace.WithInstrumentationVersion(agentx.Version))}
	if lp := newConfig(opts).logs; lp != nil {
		t.logger = lp.Logger(ScopeName, log.WithInstrumentationVersion(agentx.Version))
	}
	return t
}

// Meter adapts mp to telemetry.Meter; a nil mp uses the global provider.
// Histograms get the bucket boundaries the conventions advise. No option
// applies.
func Meter(mp metric.MeterProvider, _ ...Option) telemetry.Meter {
	if mp == nil {
		mp = otelapi.GetMeterProvider()
	}
	return meter{mp.Meter(ScopeName, metric.WithInstrumentationVersion(agentx.Version))}
}
