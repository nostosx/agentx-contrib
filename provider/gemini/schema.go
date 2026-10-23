package gemini

import (
	"encoding/json"

	"github.com/nostosx/agentx-contrib/provider/internal/jsonschema"
)

// Structured output supports a subset of JSON Schema and ignores other
// keywords; see
// https://ai.google.dev/gemini-api/docs/generate-content/structured-output.
// minLength, maxLength, minProperties and maxProperties are documented as
// not enforced and ignored, and pattern is not supported. Response schemas
// and the schemas of strict functions are adapted so the model still sees
// these constraints in the description; the runtime validates the answer
// against the full schema.

// ignoredKeyword reports whether constrained decoding ignores a keyword.
func ignoredKeyword(keyword string, _ json.RawMessage) bool {
	switch keyword {
	case "minLength", "maxLength", "minProperties", "maxProperties", "pattern":
		return true
	}
	return false
}

func adaptSchema(schema json.RawMessage) json.RawMessage {
	return jsonschema.Adapt(schema, ignoredKeyword)
}

// jsonWithTools reports whether a model accepts a response schema together
// with function declarations. Per the structured output documentation,
// combining structured outputs with tools "is available only to Gemini 4
// and Gemini 3 series models"; earlier models are given the schema as an
// instruction instead.
func jsonWithTools(name string) bool { return !usesBudget(name) }
