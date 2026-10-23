// Package marshal contains JSON helpers shared by providers.
package marshal

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
)

// WithExtra encodes body and overlays the top-level fields in extra, which
// lets callers pass provider options agentx does not model. A nil value in
// extra deletes the field.
func WithExtra(body any, extra map[string]any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil || len(extra) == 0 {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range extra {
		if v == nil {
			delete(m, k)
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		m[k] = raw
	}
	return json.Marshal(m)
}

// Arguments normalizes tool-call arguments produced by a model. Empty input
// becomes {}, valid JSON is compacted, and invalid JSON is preserved as a
// JSON string so it still serializes and fails schema validation with a
// clear error. Valid means an I-JSON message (RFC 7493): JSON that repeats
// a member name or holds invalid UTF-8 is preserved as a string too, so
// middleware, hooks and tools never see an object that parsers read
// differently.
func Arguments(raw []byte) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	if v := jsontext.Value(bytes.Clone(raw)); v.IsValid() && v.Compact() == nil {
		return v
	}
	s, _ := json.Marshal(string(raw))
	return s
}

// Object returns args when it is a JSON object, or {} otherwise. Providers
// that require object inputs use it when replaying calls.
func Object(args json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed) {
		return trimmed
	}
	return json.RawMessage("{}")
}

// MustRaw returns v encoded as JSON, panicking on failure. Use it only for
// values that always encode.
func MustRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
