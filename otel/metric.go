package otel

import (
	"context"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/nostosx/agentx/telemetry"
)

// meter adapts a meter. Instrument creation errors go to the global error
// handler; the instrument then discards measurements if the provider
// returned none.
type meter struct{ m metric.Meter }

func (m meter) Int64Counter(in telemetry.Instrument) telemetry.Int64Counter {
	c, err := m.m.Int64Counter(in.Name, metric.WithDescription(in.Description), metric.WithUnit(in.Unit))
	if err != nil {
		otelapi.Handle(err)
		if c == nil {
			c = noop.Int64Counter{}
		}
	}
	return int64Counter{c}
}

func (m meter) Int64Histogram(in telemetry.Instrument) telemetry.Int64Histogram {
	opts := []metric.Int64HistogramOption{metric.WithDescription(in.Description), metric.WithUnit(in.Unit)}
	if len(in.Buckets) > 0 {
		opts = append(opts, metric.WithExplicitBucketBoundaries(in.Buckets...))
	}
	h, err := m.m.Int64Histogram(in.Name, opts...)
	if err != nil {
		otelapi.Handle(err)
		if h == nil {
			h = noop.Int64Histogram{}
		}
	}
	return int64Histogram{h}
}

func (m meter) Float64Histogram(in telemetry.Instrument) telemetry.Float64Histogram {
	opts := []metric.Float64HistogramOption{metric.WithDescription(in.Description), metric.WithUnit(in.Unit)}
	if len(in.Buckets) > 0 {
		opts = append(opts, metric.WithExplicitBucketBoundaries(in.Buckets...))
	}
	h, err := m.m.Float64Histogram(in.Name, opts...)
	if err != nil {
		otelapi.Handle(err)
		if h == nil {
			h = noop.Float64Histogram{}
		}
	}
	return float64Histogram{h}
}

type int64Counter struct{ c metric.Int64Counter }

func (c int64Counter) Add(ctx context.Context, n int64, attrs ...telemetry.Attr) {
	c.c.Add(ctx, n, metric.WithAttributes(attributes(attrs)...))
}

type int64Histogram struct{ h metric.Int64Histogram }

func (h int64Histogram) Record(ctx context.Context, v int64, attrs ...telemetry.Attr) {
	h.h.Record(ctx, v, metric.WithAttributes(attributes(attrs)...))
}

type float64Histogram struct{ h metric.Float64Histogram }

func (h float64Histogram) Record(ctx context.Context, v float64, attrs ...telemetry.Attr) {
	h.h.Record(ctx, v, metric.WithAttributes(attributes(attrs)...))
}
