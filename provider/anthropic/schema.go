package anthropic

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nostosx/agentx-contrib/provider/internal/jsonschema"
)

// Structured outputs and strict tools accept a subset of JSON Schema; see
// https://platform.claude.com/docs/en/build-with-claude/structured-outputs#json-schema-limitations.
// Schemas sent with output_config.format or strict tools are adapted as the
// SDK helpers do: unsupported constraints are removed and noted in the
// description, and the runtime validates the answer against the full schema.

// supportedFormats lists the string formats the API accepts.
var supportedFormats = []string{"date-time", "time", "date", "duration", "email", "hostname", "uri", "ipv4", "ipv6", "uuid"}

// unsupportedPattern matches the regex features the API rejects:
// backreferences, lookaround assertions and word boundaries.
var unsupportedPattern = regexp.MustCompile(`\\[1-9bB]|\(\?<?[=!]`)

// unsupportedKeyword reports whether the API rejects a keyword: numerical
// constraints, string length constraints, array constraints beyond minItems
// of 0 or 1, string formats outside the supported list and patterns using
// unsupported regex features.
func unsupportedKeyword(keyword string, value json.RawMessage) bool {
	switch keyword {
	case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
		"minLength", "maxLength", "maxItems", "uniqueItems", "minContains", "maxContains",
		"minProperties", "maxProperties":
		return true
	case "minItems":
		var n float64
		return json.Unmarshal(value, &n) != nil || (n != 0 && n != 1)
	case "format":
		var f string
		return json.Unmarshal(value, &f) != nil || !slices.Contains(supportedFormats, f)
	case "pattern":
		var p string
		return json.Unmarshal(value, &p) != nil || unsupportedPattern.MatchString(p)
	}
	return false
}

// adaptSchema adapts a schema for structured outputs or strict tools.
func adaptSchema(schema json.RawMessage) json.RawMessage {
	return jsonschema.Adapt(schema, unsupportedKeyword)
}

// jsonOutputs reports whether a model accepts output_config.format: per
// the models listed at
// https://platform.claude.com/docs/en/build-with-claude/structured-outputs,
// Claude Opus, Sonnet and Haiku 4.5 and later and the Fable and Mythos
// models. Names that are not Claude model IDs, such as gateway aliases, are
// assumed to accept it.
func jsonOutputs(name string) bool {
	id, ok := claudeID(name)
	if !ok {
		return true
	}
	family, rest, _ := strings.Cut(id, "-")
	switch family {
	case "fable", "mythos":
		return true
	case "opus", "sonnet", "haiku":
		major, minor := version(rest)
		return major >= 5 || (major == 4 && minor >= 5)
	}
	// Older IDs, such as claude-3-7-sonnet, put the generation first.
	_, err := strconv.Atoi(family)
	return err != nil
}
