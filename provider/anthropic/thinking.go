package anthropic

import (
	"strconv"
	"strings"

	"github.com/nostosx/agentx/model"
)

// offMode is how effort "none" turns a model's thinking off.
type offMode int

const (
	// offOmit omits thinking, which is off by default.
	offOmit offMode = iota
	// offDisabled sends thinking type "disabled".
	offDisabled
	// offBetweenTools sends thinking type "between_tools", which turns off
	// thinking before the first tool call.
	offBetweenTools
	// offLowEffort omits thinking, which cannot be turned off, and sends
	// effort "low".
	offLowEffort
)

// support describes the thinking and effort parameters a Claude model
// accepts.
type support struct {
	known       bool // a Claude model ID; others keep the generic behavior
	adaptive    bool // accepts thinking type "adaptive"
	extended    bool // accepts thinking type "enabled" with budget_tokens
	effort      bool // accepts output_config.effort
	xhigh       bool // accepts effort "xhigh"
	max         bool // accepts effort "max"
	off         offMode
	prefixCheck bool // checks the prefix of replayed thinking blocks
}

// Exceptions among adaptive-only models, keyed by family and version.
// Sources:
// https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting
// https://platform.claude.com/docs/en/build-with-claude/preserved-thinking
var (
	// offModes lists the models that can turn thinking off. Claude Opus 5
	// and Haiku 5.5 accept "disabled" only at effort high or below, which
	// holds when no effort is sent.
	offModes = map[string]offMode{
		"opus-4-7": offDisabled, "opus-4-8": offDisabled, "opus-5-0": offDisabled,
		"sonnet-5-0": offDisabled, "haiku-5-5": offDisabled, "sonnet-5-5": offBetweenTools,
	}
	// prefixCheckModels enforce the prefix check of replayed thinking.
	prefixCheckModels = map[string]bool{"fable-5-1": true, "opus-5-5": true, "sonnet-5-5": true, "haiku-5-5": true}
)

// supportOf classifies a model name, matched like rejectsSampling. Claude
// Opus 4.7 and later, generation 5 and later, and Fable and Mythos 5 and
// later accept only adaptive thinking; unlisted ones are assumed to keep
// thinking on. Effort is per
// https://platform.claude.com/docs/en/build-with-claude/effort.
func supportOf(name string) support {
	id, ok := claudeID(name)
	if !ok {
		return support{}
	}
	if strings.HasPrefix(id, "3-7-sonnet") {
		return support{known: true, extended: true, off: offOmit}
	}
	family, rest, _ := strings.Cut(id, "-")
	if family != "fable" && family != "mythos" && family != "opus" && family != "sonnet" && family != "haiku" {
		return support{}
	}
	major, minor := version(rest)
	key := family + "-" + strconv.Itoa(major) + "-" + strconv.Itoa(minor)
	s := support{known: true, adaptive: true, effort: true, xhigh: true, max: true, off: offLowEffort}
	switch {
	case family == "mythos" && strings.HasPrefix(rest, "preview"):
		// Mythos Preview also accepts budgets but not effort "xhigh".
		s.extended, s.xhigh = true, false
	case family == "fable" || family == "mythos" || major >= 5 || (family == "opus" && major == 4 && minor >= 7):
		if off, ok := offModes[key]; ok {
			s.off = off
		}
		s.prefixCheck = prefixCheckModels[key]
	case (family == "opus" || family == "sonnet") && major == 4 && minor == 6:
		s.extended, s.xhigh, s.off = true, false, offDisabled
	case major == 4:
		// Claude Opus, Sonnet and Haiku 4.5 and earlier Claude 4 models
		// accept only budgets; of these, only Opus 4.5 accepts effort, and
		// neither "xhigh" nor "max".
		return support{known: true, extended: true, effort: key == "opus-4-5", off: offOmit}
	default:
		return support{}
	}
	return s
}

// derivedBudget returns the thinking budget for effort on a model that
// accepts only budgets, at most half of maxTokens so the answer has room,
// or 0 when that leaves less than the minimum budget of 1024.
func derivedBudget(effort string, maxTokens int) int {
	b := 8192
	switch effort {
	case "low":
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

// reasoning sets the thinking and effort parameters of r for rs and
// reports whether replayed thinking blocks that fail the prefix check are
// dropped, which needs ThinkingBindingBeta. See the package documentation.
func (m *Model) reasoning(r *request, rs *model.Reasoning, replayed bool) bool {
	s := supportOf(r.Model)
	var effort string
	if rs != nil {
		effort = rs.Effort
	}
	if s.known {
		switch {
		case effort == "minimal":
			effort = "low"
		case effort == "max" && !s.max:
			effort = "xhigh"
		}
		if effort == "xhigh" && !s.xhigh {
			effort = "high"
		}
	}
	var t *thinking
	switch {
	case rs == nil:
	case !s.known:
		t = &thinking{Type: "adaptive", Display: rs.Display}
		if rs.BudgetTokens > 0 {
			t = &thinking{Type: "enabled", BudgetTokens: rs.BudgetTokens, Display: rs.Display}
		}
	case effort == "none":
		effort = ""
		switch s.off {
		case offDisabled:
			t = &thinking{Type: "disabled"}
		case offBetweenTools:
			t = &thinking{Type: "between_tools"}
		case offLowEffort:
			effort = "low"
		}
	case s.extended && rs.BudgetTokens > 0:
		t = &thinking{Type: "enabled", BudgetTokens: rs.BudgetTokens, Display: rs.Display}
	case s.adaptive:
		t = &thinking{Type: "adaptive", Display: rs.Display}
	default:
		if b := derivedBudget(effort, r.MaxTokens); b > 0 {
			t = &thinking{Type: "enabled", BudgetTokens: b, Display: rs.Display}
		}
	}
	if s.known && !s.effort {
		effort = ""
	}
	if effort != "" {
		r.OutputConfig = &outputConfig{Effort: effort}
	}
	drop := s.prefixCheck && replayed && !m.keepBlock
	if drop && t == nil {
		// Thinking is on by default on these models.
		t = &thinking{Type: "adaptive"}
	}
	drop = drop && (t.Type == "adaptive" || t.Type == "enabled")
	if drop {
		t.BlockBinding = &blockBinding{PrefixMismatchBehavior: "drop_block"}
	}
	r.Thinking = t
	return drop
}
