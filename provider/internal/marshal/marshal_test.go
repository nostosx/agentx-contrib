package marshal_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nostosx/agentx-contrib/provider/internal/marshal"
)

func TestWithExtra(t *testing.T) {
	t.Parallel()
	body := map[string]any{"model": "default", "stream": true, "keep": 1}
	got, err := marshal.WithExtra(body, map[string]any{"model": "custom", "stream": nil, "new": []int{2}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"model": "custom", "keep": float64(1), "new": []any{float64(2)}}
	if !reflect.DeepEqual(decoded, want) || body["stream"] != true {
		t.Fatalf("overlay = %s; original = %v", got, body)
	}
	for _, tc := range []struct {
		name  string
		body  any
		extra map[string]any
	}{
		{name: "unencodable body", body: make(chan int)},
		{name: "non-object body", body: []string{"x"}, extra: map[string]any{"a": 1}},
		{name: "unencodable extra", body: body, extra: map[string]any{"a": make(chan int)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := marshal.WithExtra(tc.body, tc.extra); err == nil {
				t.Fatal("expected encoding error")
			}
		})
	}
}

func TestArgumentsAndObject(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, input, args, object string }{
		{name: "empty", input: "  ", args: `{}`, object: `{}`},
		{name: "object", input: ` { "q": 1 } `, args: `{"q":1}`, object: `{ "q": 1 }`},
		{name: "array", input: `[1]`, args: `[1]`, object: `{}`},
		{name: "invalid", input: `{broken`, args: `"{broken"`, object: `{}`},
		{name: "null", input: `null`, args: `null`, object: `{}`},
		{name: "duplicate name", input: `{"q": 1, "q": 2}`, args: `"{\"q\": 1, \"q\": 2}"`, object: `{"q": 1, "q": 2}`},
		{name: "invalid UTF-8", input: "{\"q\":\"\xff\"}", args: "\"{\\\"q\\\":\\\"\ufffd\\\"}\"", object: "{\"q\":\"\xff\"}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(marshal.Arguments([]byte(tc.input))); got != tc.args {
				t.Errorf("Arguments = %s, want %s", got, tc.args)
			}
			if got := string(marshal.Object([]byte(tc.input))); got != tc.object {
				t.Errorf("Object = %s, want %s", got, tc.object)
			}
		})
	}
}
