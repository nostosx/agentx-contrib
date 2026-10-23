package jsonschema

import (
	"encoding/json"
	"strings"
	"testing"
)

func dropLimits(kw string, _ json.RawMessage) bool {
	return kw == "minimum" || kw == "maxLength" || kw == "maxItems"
}

func TestAdaptMovesKeywordsIntoDescriptions(t *testing.T) {
	in := `{"type":"object","properties":{"minimum":{"type":"integer","minimum":1,"description":"Count."},` +
		`"name":{"anyOf":[{"type":"string","maxLength":5},{"type":"null"}]},` +
		`"tags":{"type":"array","maxItems":3,"items":{"type":"string","maxLength":2}},` +
		`"meta":{"$ref":"#/$defs/m"}},"required":["minimum"],"additionalProperties":false,` +
		`"$defs":{"m":{"type":"number","minimum":0.5}},"enum":[{"minimum":1}]}`
	got := string(Adapt(json.RawMessage(in), dropLimits))
	want := `{"type":"object","properties":{"minimum":{"type":"integer","description":"Count.\n\n{minimum: 1}"},` +
		`"name":{"anyOf":[{"type":"string","description":"{maxLength: 5}"},{"type":"null"}]},` +
		`"tags":{"type":"array","items":{"type":"string","description":"{maxLength: 2}"},"description":"{maxItems: 3}"},` +
		`"meta":{"$ref":"#/$defs/m"}},"required":["minimum"],"additionalProperties":false,` +
		`"$defs":{"m":{"type":"number","description":"{minimum: 0.5}"}},"enum":[{"minimum":1}]}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if !json.Valid([]byte(got)) {
		t.Fatal("invalid JSON")
	}
}

func TestAdaptKeepsUnchangedSchemas(t *testing.T) {
	for _, in := range []string{
		`{"type": "object", "properties": {"b": {"type": "string"}, "a": {"type": "integer"}}}`,
		`true`,
		`{not json`,
		`{"type":"array","items":[{"type":"string"}]}`,
	} {
		if got := string(Adapt(json.RawMessage(in), dropLimits)); got != in {
			t.Errorf("Adapt(%s) = %s", in, got)
		}
	}
}

func TestAdaptTupleItemsAndCombinators(t *testing.T) {
	in := `{"type":"array","items":[{"type":"integer","minimum":2}],"allOf":[{"maxLength":1}],"not":{"minimum":3}}`
	got := string(Adapt(json.RawMessage(in), dropLimits))
	want := `{"type":"array","items":[{"type":"integer","description":"{minimum: 2}"}],"allOf":[{"description":"{maxLength: 1}"}],"not":{"description":"{minimum: 3}"}}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestAdaptKeepsRepeatedNames(t *testing.T) {
	in := `{"type":"integer","minimum":1,"type":"number","de\u0073cription":"x"}`
	got := string(Adapt(json.RawMessage(in), dropLimits))
	want := `{"type":"integer","type":"number","description":"x\n\n{minimum: 1}"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestInstruction(t *testing.T) {
	got := Instruction(json.RawMessage("{\n  \"type\": \"object\"\n}"))
	if !strings.HasSuffix(got, `{"type":"object"}`) || !strings.Contains(got, "only a JSON value") {
		t.Fatalf("Instruction = %q", got)
	}
}
