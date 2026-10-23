// Package jsonschema adapts JSON Schemas to the subsets that providers'
// constrained decoding accepts.
package jsonschema

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"strings"
)

// Unsupported reports whether a provider rejects or ignores a keyword with
// the given JSON value in a schema.
type Unsupported func(keyword string, value json.RawMessage) bool

// Adapt returns schema without the keywords unsupported reports, in every
// subschema. Each removed keyword is noted in the description of its
// schema, such as "{minimum: 1, maxLength: 10}", so the model still sees
// it, as Anthropic's SDKs do; callers validate answers against the full
// schema. Keys keep their order. A schema that needs no change is returned
// as is. Callers must supply valid JSON; Adapt does not validate the complete
// input and may discard trailing data when rewriting an object.
func Adapt(schema json.RawMessage, unsupported Unsupported) json.RawMessage {
	out, _ := adapt(schema, unsupported)
	return out
}

// Instruction asks the model to answer with JSON matching schema, for
// providers or models that cannot constrain the answer.
func Instruction(schema json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, schema) != nil {
		b.Reset()
		b.Write(schema)
	}
	return "When you give your final answer, answer with only a JSON value that matches this JSON Schema, without any other text:\n" + b.String()
}

// Keywords whose values are subschemas, maps of subschemas, or lists of
// subschemas. Other keywords, such as enum and const, hold data.
var (
	schemaValues = map[string]bool{
		"items": true, "additionalProperties": true, "additionalItems": true, "not": true,
		"contains": true, "propertyNames": true, "if": true, "then": true, "else": true,
		"unevaluatedItems": true, "unevaluatedProperties": true,
	}
	schemaMaps = map[string]bool{
		"properties": true, "patternProperties": true, "$defs": true, "definitions": true,
		"dependentSchemas": true,
	}
	schemaLists = map[string]bool{"anyOf": true, "allOf": true, "oneOf": true, "prefixItems": true}
)

type member struct {
	key string
	val json.RawMessage
}

// adapt adapts a schema and reports whether it changed.
func adapt(raw json.RawMessage, unsupported Unsupported) (json.RawMessage, bool) {
	ms, ok := decodeObject(raw)
	if !ok {
		// Boolean schemas and inputs without a decodable object stay as they are.
		return raw, false
	}
	changed := false
	var notes []string
	kept := ms[:0]
	for _, m := range ms {
		var c bool
		switch {
		case schemaMaps[m.key]:
			m.val, c = adaptMap(m.val, unsupported)
		case schemaLists[m.key]:
			m.val, c = adaptList(m.val, unsupported)
		case schemaValues[m.key]:
			if bytes.HasPrefix(bytes.TrimSpace(m.val), []byte("[")) {
				// The array form of items in older drafts.
				m.val, c = adaptList(m.val, unsupported)
			} else {
				m.val, c = adapt(m.val, unsupported)
			}
		case m.key != "description" && unsupported(m.key, m.val):
			notes = append(notes, m.key+": "+compact(m.val))
			changed = true
			continue
		}
		changed = changed || c
		kept = append(kept, m)
	}
	if !changed {
		return raw, false
	}
	if len(notes) > 0 {
		kept = describe(kept, "{"+strings.Join(notes, ", ")+"}")
	}
	return encodeObject(kept), true
}

// describe appends note to the description among ms.
func describe(ms []member, note string) []member {
	for i, m := range ms {
		if m.key != "description" {
			continue
		}
		var d string
		if json.Unmarshal(m.val, &d) == nil && d != "" {
			note = d + "\n\n" + note
		}
		ms[i].val = encodeString(note)
		return ms
	}
	return append(ms, member{key: "description", val: encodeString(note)})
}

func adaptMap(raw json.RawMessage, unsupported Unsupported) (json.RawMessage, bool) {
	ms, ok := decodeObject(raw)
	if !ok {
		return raw, false
	}
	changed := false
	for i, m := range ms {
		var c bool
		ms[i].val, c = adapt(m.val, unsupported)
		changed = changed || c
	}
	if !changed {
		return raw, false
	}
	return encodeObject(ms), true
}

func adaptList(raw json.RawMessage, unsupported Unsupported) (json.RawMessage, bool) {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return raw, false
	}
	changed := false
	for i, item := range items {
		var c bool
		items[i], c = adapt(item, unsupported)
		changed = changed || c
	}
	if !changed {
		return raw, false
	}
	var b bytes.Buffer
	b.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(item)
	}
	b.WriteByte(']')
	return b.Bytes(), true
}

// decodeObject returns the members of the first JSON object in order,
// without checking for trailing data. Like encoding/json, it accepts
// repeated names and invalid UTF-8, which schemas of other programs may contain.
func decodeObject(raw json.RawMessage) ([]member, bool) {
	dec := jsontext.NewDecoder(bytes.NewReader(raw), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	if t, err := dec.ReadToken(); err != nil || t.Kind() != '{' {
		return nil, false
	}
	var ms []member
	for dec.PeekKind() == '"' {
		name, err := dec.ReadToken()
		if err != nil {
			return nil, false
		}
		key := name.String()
		v, err := dec.ReadValue()
		if err != nil {
			return nil, false
		}
		ms = append(ms, member{key: key, val: v.Clone()})
	}
	if t, err := dec.ReadToken(); err != nil || t.Kind() != '}' {
		return nil, false
	}
	return ms, true
}

func encodeObject(ms []member) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(encodeString(m.key))
		b.WriteByte(':')
		b.Write(m.val)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func encodeString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}
