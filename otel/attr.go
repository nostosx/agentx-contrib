package otel

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.opentelemetry.io/otel/attribute"

	"github.com/nostosx/agentx/telemetry"
)

// attributes converts attributes, dropping those without a value.
func attributes(attrs []telemetry.Attr) []attribute.KeyValue {
	kvs := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		if kv := keyValue(a); kv.Valid() {
			kvs = append(kvs, kv)
		}
	}
	return kvs
}

// keyValue converts an attribute. Values of other types than those of
// OpenTelemetry attributes are recorded as their string form.
func keyValue(a telemetry.Attr) attribute.KeyValue {
	switch v := a.Value.(type) {
	case nil:
		return attribute.KeyValue{}
	case string:
		return attribute.String(a.Key, v)
	case bool:
		return attribute.Bool(a.Key, v)
	case int:
		return attribute.Int(a.Key, v)
	case int64:
		return attribute.Int64(a.Key, v)
	case int32:
		return attribute.Int64(a.Key, int64(v))
	case float64:
		return attribute.Float64(a.Key, v)
	case float32:
		return attribute.Float64(a.Key, float64(v))
	case []string:
		return attribute.StringSlice(a.Key, v)
	case []bool:
		return attribute.BoolSlice(a.Key, v)
	case []int:
		return attribute.IntSlice(a.Key, v)
	case []int64:
		return attribute.Int64Slice(a.Key, v)
	case []float64:
		return attribute.Float64Slice(a.Key, v)
	case fmt.Stringer:
		return attribute.String(a.Key, v.String())
	case error:
		return attribute.String(a.Key, v.Error())
	default:
		return attribute.String(a.Key, fmt.Sprint(v))
	}
}

// jsonValue returns the structured form of JSON content, or the content
// as a string if it is not valid JSON.
func jsonValue(s string) attribute.Value {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil || d.More() {
		return attribute.StringValue(s)
	}
	return anyValue(v)
}

func anyValue(v any) attribute.Value {
	switch v := v.(type) {
	case string:
		return attribute.StringValue(v)
	case bool:
		return attribute.BoolValue(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return attribute.Int64Value(n)
		}
		f, _ := v.Float64()
		return attribute.Float64Value(f)
	case []any:
		vals := make([]attribute.Value, len(v))
		for i, e := range v {
			vals[i] = anyValue(e)
		}
		return attribute.SliceValue(vals...)
	case map[string]any:
		kvs := make([]attribute.KeyValue, 0, len(v))
		for _, k := range slices.Sorted(maps.Keys(v)) {
			kvs = append(kvs, attribute.KeyValue{Key: attribute.Key(k), Value: anyValue(v[k])})
		}
		return attribute.MapValue(kvs...)
	default:
		// JSON null.
		return attribute.Value{}
	}
}
