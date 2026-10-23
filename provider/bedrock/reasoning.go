package bedrock

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nostosx/agentx-contrib/provider/internal/jsonschema"
	"github.com/nostosx/agentx/model"
)

// defaultThinkingMaxTokens is the output limit sent with a thinking budget
// when the request sets none, since the budget must stay below it.
const defaultThinkingMaxTokens = 16000

// claudeID returns the part of a Bedrock Claude model name after
// "anthropic.claude-", such as "opus-4-6-v1" or
// "sonnet-4-5-20250929-v1:0", skipping inference profile prefixes such as
// "us." or "global." and ARN prefixes.
func claudeID(name string) (string, bool) {
	name = strings.ToLower(name)
	const marker = "anthropic.claude-"
	i := strings.Index(name, marker)
	if i < 0 {
		return "", false
	}
	return name[i+len(marker):], true
}

// version parses the "<major>-<minor>" prefix of a model version, such as
// "4-6-v1" or "4-20250514-v1:0"; a minor that looks like a date counts as
// 0.
func version(s string) (major, minor int) {
	parts := strings.SplitN(s, "-", 3)
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0
	}
	if len(parts) > 1 {
		if n, err := strconv.Atoi(parts[1]); err == nil && n < 100 {
			minor = n
		}
	}
	return major, minor
}

// offMode is how effort "none" turns a Claude model's thinking off.
type offMode int

const (
	// offOmit omits thinking, which is off by default.
	offOmit offMode = iota
	// offDisabled sends thinking type "disabled".
	offDisabled
	// offBetweenTools sends thinking type "between_tools".
	offBetweenTools
	// offLowEffort keeps thinking, which cannot be turned off, at effort
	// "low".
	offLowEffort
)

// support describes the thinking parameters a Claude model accepts.
type support struct {
	known    bool // a Claude model ID; unknown names get no thinking
	adaptive bool // accepts thinking type "adaptive"
	extended bool // accepts thinking type "enabled" with budget_tokens
	effort   bool // accepts output_config.effort
	xhigh    bool // accepts effort "xhigh"
	max      bool // accepts effort "max"
	off      offMode
}

// Thinking support of Claude models on Bedrock. Sources:
// https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-adaptive-thinking.html
// (adaptive thinking, effort levels, models that can disable thinking) and
// https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-extended-thinking.html
// (budgets). Claude Sonnet 5.5's "between_tools" is per
// https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting.
var (
	// maxEffort lists the models that accept effort "max"; xhighEffort
	// those that also accept "xhigh".
	maxEffort   = []string{"haiku-5-5", "sonnet-5-5", "opus-5-5", "opus-5-0", "sonnet-5-0", "opus-4-8", "opus-4-7", "opus-4-6", "sonnet-4-6"}
	xhighEffort = []string{"haiku-5-5", "sonnet-5-5", "opus-5-5", "opus-5-0", "sonnet-5-0", "opus-4-8", "opus-4-7"}
	// offModes lists the adaptive models that can turn thinking off.
	offModes = map[string]offMode{
		"opus-4-7": offDisabled, "opus-4-8": offDisabled, "opus-5-0": offDisabled,
		"sonnet-5-0": offDisabled, "haiku-5-5": offDisabled, "sonnet-5-5": offBetweenTools,
		"opus-4-6": offDisabled, "sonnet-4-6": offDisabled,
	}
)

// claudeSupport classifies a Claude model ID as returned by claudeID.
func claudeSupport(id string) support {
	if strings.HasPrefix(id, "3-7-sonnet") {
		return support{known: true, extended: true, off: offOmit}
	}
	family, rest, _ := strings.Cut(id, "-")
	switch family {
	case "fable", "mythos", "opus", "sonnet", "haiku":
	default:
		// Claude 3 and 3.5 models, whose IDs put the generation first,
		// cannot think.
		return support{known: true}
	}
	major, minor := version(rest)
	key := family + "-" + strconv.Itoa(major) + "-" + strconv.Itoa(minor)
	s := support{known: true, adaptive: true, effort: true, off: offLowEffort,
		xhigh: slices.Contains(xhighEffort, key), max: slices.Contains(maxEffort, key)}
	switch {
	case family == "fable" || family == "mythos" || major >= 5 || (family == "opus" && major == 4 && minor >= 7):
		// Adaptive only.
	case (family == "opus" || family == "sonnet") && major == 4 && minor == 6:
		s.extended = true
	case major == 4:
		// Claude Opus, Sonnet and Haiku 4.5 and earlier Claude 4 models
		// accept only budgets; of these, only Opus 4.5 accepts effort.
		return support{known: true, extended: true, effort: key == "opus-4-5", off: offOmit}
	default:
		return support{known: true}
	}
	if off, ok := offModes[key]; ok {
		s.off = off
	}
	return s
}

// rejectsSampling reports whether a Claude model rejects temperature and
// top_p: Claude Opus 4.7 and later, generation 5 and later, and the Fable
// and Mythos models.
func rejectsSampling(id string) bool {
	family, rest, _ := strings.Cut(id, "-")
	switch family {
	case "fable", "mythos":
		return true
	case "opus", "sonnet", "haiku":
		major, minor := version(rest)
		return major >= 5 || (family == "opus" && major == 4 && minor >= 7)
	}
	return false
}

// derivedBudget returns the thinking budget for effort, at most half of
// maxTokens so the answer has room, or 0 when that leaves less than the
// minimum budget of 1024.
func derivedBudget(effort string, maxTokens int) int {
	b := 8192
	switch effort {
	case "low", "minimal":
		b = 2048
	case "high":
		b = 16384
	case "xhigh", "max":
		b = 32000
	}
	if b = min(b, maxTokens/2); b < 1024 {
		return 0
	}
	return b
}

// reasoning returns the additionalModelRequestFields for rs on model name
// and adjusts ic for them. See the package documentation.
func reasoning(name string, rs *model.Reasoning, ic *inferenceConfig) map[string]any {
	if id, ok := claudeID(name); ok {
		return claudeReasoning(id, rs, ic)
	}
	if novaReasoning(name) {
		return novaReasoningFields(rs, ic)
	}
	return nil
}

// claudeReasoning returns the thinking fields for Claude model id.
func claudeReasoning(id string, rs *model.Reasoning, ic *inferenceConfig) map[string]any {
	s := claudeSupport(id)
	thinking := map[string]any(nil)
	effort := ""
	if rs != nil && s.known {
		effort = rs.Effort
		switch {
		case effort == "minimal":
			effort = "low"
		case effort == "max" && !s.max:
			effort = "xhigh"
		}
		if effort == "xhigh" && !s.xhigh {
			effort = "high"
		}
		switch {
		case effort == "none":
			effort = ""
			switch s.off {
			case offDisabled:
				thinking = map[string]any{"type": "disabled"}
			case offBetweenTools:
				thinking = map[string]any{"type": "between_tools"}
			case offLowEffort:
				effort = "low"
			}
		case s.extended && rs.BudgetTokens > 0 || s.extended && !s.adaptive:
			budget := rs.BudgetTokens
			if ic.MaxTokens == 0 {
				ic.MaxTokens = max(defaultThinkingMaxTokens, budget+defaultThinkingMaxTokens/4)
			}
			if budget == 0 {
				budget = derivedBudget(effort, ic.MaxTokens)
			}
			if budget > 0 {
				thinking = map[string]any{"type": "enabled", "budget_tokens": budget}
			}
		case s.adaptive:
			thinking = map[string]any{"type": "adaptive"}
		}
		if thinking != nil && rs.Display != "" && (thinking["type"] == "adaptive" || thinking["type"] == "enabled") {
			thinking["display"] = rs.Display
		}
	}
	if !s.effort {
		effort = ""
	}
	on := thinking != nil && (thinking["type"] == "adaptive" || thinking["type"] == "enabled")
	if on {
		// Thinking is incompatible with temperature and accepts top_p only
		// between 0.95 and 1.
		ic.Temperature = nil
		if ic.TopP != nil && (*ic.TopP < 0.95 || *ic.TopP > 1) {
			ic.TopP = nil
		}
	}
	if rejectsSampling(id) {
		ic.Temperature, ic.TopP = nil, nil
	}
	fields := map[string]any{}
	if thinking != nil {
		fields["thinking"] = thinking
	}
	if effort != "" {
		fields["output_config"] = map[string]any{"effort": effort}
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// novaVersion matches Amazon Nova model names with a generation number,
// such as "us.amazon.nova-2-lite-v1:0".
var novaVersion = regexp.MustCompile(`amazon\.nova-(\d+)-`)

// novaReasoning reports whether model name is an Amazon Nova model of
// generation 2 or later, which accepts reasoningConfig.
func novaReasoning(name string) bool {
	m := novaVersion.FindStringSubmatch(strings.ToLower(name))
	if m == nil {
		return false
	}
	n, err := strconv.Atoi(m[1])
	return err == nil && n >= 2
}

// novaReasoningFields returns the reasoningConfig for rs. Effort maps to
// maxReasoningEffort low, medium or high; a budget alone selects medium.
func novaReasoningFields(rs *model.Reasoning, ic *inferenceConfig) map[string]any {
	if rs == nil || rs.Effort == "none" {
		return nil
	}
	effort := "medium"
	switch rs.Effort {
	case "minimal", "low":
		effort = "low"
	case "high", "xhigh", "max":
		effort = "high"
	}
	if effort == "high" {
		// High effort rejects temperature, topP and maxTokens.
		ic.Temperature, ic.TopP, ic.MaxTokens = nil, nil, 0
	}
	return map[string]any{"reasoningConfig": map[string]any{"type": "enabled", "maxReasoningEffort": effort}}
}

// Structured output accepts a subset of JSON Schema
// (https://docs.aws.amazon.com/bedrock/latest/userguide/structured-output.html#structured-output-supported-schema-features).
// Schemas sent with outputConfig.textFormat or strict tools are adapted:
// unsupported constraints are removed and noted in the description, and
// the runtime validates the answer against the full schema.

// supportedFormats lists the string formats structured output accepts.
var supportedFormats = []string{"date-time", "time", "date", "duration", "email", "hostname", "uri", "ipv4", "ipv6", "uuid"}

// unsupportedKeyword reports whether structured output rejects a keyword:
// numerical and string length constraints, array constraints beyond
// minItems of 0 or 1, and string formats outside the supported list.
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
	}
	return false
}

// adaptSchema adapts a schema for structured output or strict tools.
func adaptSchema(schema json.RawMessage) json.RawMessage {
	return jsonschema.Adapt(schema, unsupportedKeyword)
}

// jsonOutputs reports whether model name uses structured output natively:
// Claude Opus, Sonnet and Haiku 4.5 and later and the Fable and Mythos
// models, which Bedrock lists as supporting it
// (https://docs.aws.amazon.com/bedrock/latest/userguide/model-cards.html).
func jsonOutputs(name string) bool {
	id, ok := claudeID(name)
	if !ok {
		return false
	}
	family, rest, _ := strings.Cut(id, "-")
	switch family {
	case "fable", "mythos":
		return true
	case "opus", "sonnet", "haiku":
		major, minor := version(rest)
		return major >= 5 || (major == 4 && minor >= 5)
	}
	return false
}
