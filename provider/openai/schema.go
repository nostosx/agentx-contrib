package openai

import (
	"encoding/json"
	"slices"

	"github.com/nostosx/agentx-contrib/provider/internal/jsonschema"
)

// Structured Outputs in strict mode accept a subset of JSON Schema; see
// https://developers.openai.com/api/docs/guides/structured-outputs#supported-schemas.
// Strict response formats and strict function schemas are adapted for
// both APIs: keywords outside the supported properties are removed and
// noted in the description, and the runtime validates the answer against
// the full schema. Schemas that are not strict are sent unchanged.

// supportedFormats lists the string formats strict mode accepts.
var supportedFormats = []string{"date-time", "time", "date", "duration", "email", "hostname", "ipv4", "ipv6", "uuid"}

// unsupportedKeyword reports whether strict mode rejects a keyword. The
// guide lists pattern and format for strings, multipleOf, minimum, maximum
// and their exclusive forms for numbers, and minItems and maxItems for
// arrays. It also lists minLength and maxLength among the keywords
// fine-tuned models "additionally do not support", next to pattern and
// format, so other models accept them and they are kept. Object size
// constraints and uniqueItems are not mentioned and are moved.
func unsupportedKeyword(keyword string, value json.RawMessage) bool {
	switch keyword {
	case "minProperties", "maxProperties", "uniqueItems":
		return true
	case "format":
		var f string
		return json.Unmarshal(value, &f) != nil || !slices.Contains(supportedFormats, f)
	}
	return false
}

// strictSchema adapts a schema if strict mode applies to it.
func strictSchema(schema json.RawMessage, strict bool) json.RawMessage {
	if !strict || len(schema) == 0 {
		return schema
	}
	return jsonschema.Adapt(schema, unsupportedKeyword)
}
