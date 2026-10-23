package otel

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/nostosx/agentx/middleware"
)

func TestKeyValue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		value any
		want  attribute.Value
	}{
		{"s", attribute.StringValue("s")},
		{true, attribute.BoolValue(true)},
		{3, attribute.IntValue(3)},
		{int64(4), attribute.Int64Value(4)},
		{int32(5), attribute.Int64Value(5)},
		{1.5, attribute.Float64Value(1.5)},
		{float32(0.5), attribute.Float64Value(0.5)},
		{[]string{"a"}, attribute.StringSliceValue([]string{"a"})},
		{[]bool{true}, attribute.BoolSliceValue([]bool{true})},
		{[]int{1}, attribute.IntSliceValue([]int{1})},
		{[]int64{2}, attribute.Int64SliceValue([]int64{2})},
		{[]float64{0.5}, attribute.Float64SliceValue([]float64{0.5})},
		{time.Second, attribute.StringValue("1s")},
		{errors.New("e"), attribute.StringValue("e")},
		{struct{ A int }{1}, attribute.StringValue("{1}")},
	} {
		if got := keyValue(middleware.Attr{Key: "k", Value: tc.value}); got.Value != tc.want {
			t.Errorf("%T %v: got %v, want %v", tc.value, tc.value, got.Value.String(), tc.want.String())
		}
	}
	if kvs := attributes([]middleware.Attr{{Key: "nil"}, {Key: "k", Value: 1}}); len(kvs) != 1 {
		t.Errorf("nil attribute kept: %v", kvs)
	}
}

func TestJSONValue(t *testing.T) {
	t.Parallel()
	got := jsonValue(`[{"n":1,"f":1.5,"b":true,"z":null,"s":"x"}]`)
	m := got.AsSlice()[0].AsMap()
	want := []attribute.KeyValue{
		{Key: "b", Value: attribute.BoolValue(true)},
		{Key: "f", Value: attribute.Float64Value(1.5)},
		{Key: "n", Value: attribute.Int64Value(1)},
		{Key: "s", Value: attribute.StringValue("x")},
		{Key: "z", Value: attribute.Value{}},
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("map = %v", m)
	}
	for _, bad := range []string{"not json", `{} {}`} {
		if v := jsonValue(bad); v != attribute.StringValue(bad) {
			t.Errorf("%q: %v", bad, v.String())
		}
	}
}

type wrapped struct{ msg string }

func (w *wrapped) Error() string { return w.msg }

func TestExceptionType(t *testing.T) {
	t.Parallel()
	base := &wrapped{"x"}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{base, "*otel.wrapped"},
		{fmt.Errorf("a: %w", fmt.Errorf("b: %w", base)), "*otel.wrapped"},
		{fmt.Errorf("a: %w, %w", base, errors.New("y")), "*otel.wrapped"},
		{fmt.Errorf("plain %s", "text"), "*errors.errorString"},
		{errors.Join(base), "*errors.joinError"},
	} {
		if got := exceptionType(tc.err); got != tc.want {
			t.Errorf("%v: %s, want %s", tc.err, got, tc.want)
		}
	}
}

// failingMeter fails to create instruments.
type failingMeter struct{ noop.Meter }

var errInstrument = errors.New("no instrument")

func (failingMeter) Int64Counter(string, ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return nil, errInstrument
}

func (failingMeter) Int64Histogram(string, ...metric.Int64HistogramOption) (metric.Int64Histogram, error) {
	return nil, errInstrument
}

func (failingMeter) Float64Histogram(string, ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	return nil, errInstrument
}

func TestMeterFallsBackToNoop(t *testing.T) {
	t.Parallel()
	m := meter{failingMeter{}}
	m.Int64Counter(middleware.Instrument{Name: "c"}).Add(t.Context(), 1)
	m.Int64Histogram(middleware.Instrument{Name: "h"}).Record(t.Context(), 1)
	m.Float64Histogram(middleware.Instrument{Name: "f"}).Record(t.Context(), 1)
}

func TestGlobalProviders(t *testing.T) {
	t.Parallel()
	ctx, span := Tracer(nil).Start(t.Context(), "s", middleware.SpanKindClient)
	span.RecordError(nil)
	span.End()
	if ctx == nil {
		t.Fatal("nil context")
	}
	Meter(nil).Int64Counter(middleware.Instrument{Name: "c", Unit: "1"}).Add(t.Context(), 1)
}
