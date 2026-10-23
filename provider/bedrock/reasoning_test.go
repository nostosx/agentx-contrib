package bedrock

import (
	"encoding/json"
	"testing"

	"github.com/nostosx/agentx/model"
)

func TestReasoning(t *testing.T) {
	t.Parallel()
	temp, topP, wideTopP := 0.7, 0.5, 0.97
	for _, tc := range []struct {
		name      string
		model     string
		reasoning *model.Reasoning
		maxTokens int
		topP      *float64
		fields    string // additionalModelRequestFields, or "" for none
		inference string // inferenceConfig, or "" for none
	}{
		{name: "no reasoning keeps sampling", model: "anthropic.claude-sonnet-4-5-20250929-v1:0",
			inference: `{"temperature":0.7,"topP":0.5}`},
		{name: "adaptive with effort", model: "us.anthropic.claude-opus-4-6-v1", reasoning: &model.Reasoning{Effort: "max", Display: "summarized"},
			fields: `{"output_config":{"effort":"max"},"thinking":{"display":"summarized","type":"adaptive"}}`},
		{name: "4.6 budget", model: "anthropic.claude-sonnet-4-6", reasoning: &model.Reasoning{BudgetTokens: 4000}, maxTokens: 9000, topP: &wideTopP,
			fields: `{"thinking":{"budget_tokens":4000,"type":"enabled"}}`, inference: `{"maxTokens":9000,"topP":0.97}`},
		{name: "4.6 lowers xhigh", model: "anthropic.claude-sonnet-4-6", reasoning: &model.Reasoning{Effort: "xhigh"},
			fields: `{"output_config":{"effort":"high"},"thinking":{"type":"adaptive"}}`},
		{name: "extended only derives budget", model: "us.anthropic.claude-sonnet-4-5-20250929-v1:0", reasoning: &model.Reasoning{Effort: "high"},
			fields: `{"thinking":{"budget_tokens":8000,"type":"enabled"}}`, inference: `{"maxTokens":16000}`},
		{name: "extended budget within limit", model: "anthropic.claude-3-7-sonnet-20250219-v1:0", reasoning: &model.Reasoning{Effort: "low"}, maxTokens: 64000,
			fields: `{"thinking":{"budget_tokens":2048,"type":"enabled"}}`, inference: `{"maxTokens":64000}`},
		{name: "explicit budget raises default limit", model: "anthropic.claude-opus-4-1-20250805-v1:0", reasoning: &model.Reasoning{BudgetTokens: 20000},
			fields: `{"thinking":{"budget_tokens":20000,"type":"enabled"}}`, inference: `{"maxTokens":24000}`},
		{name: "no room for budget", model: "anthropic.claude-haiku-4-5-20251001-v1:0", reasoning: &model.Reasoning{Effort: "medium"}, maxTokens: 1500,
			inference: `{"maxTokens":1500,"temperature":0.7,"topP":0.5}`},
		{name: "opus 4.5 effort", model: "anthropic.claude-opus-4-5-20251101-v1:0", reasoning: &model.Reasoning{Effort: "max"}, maxTokens: 64000,
			fields: `{"output_config":{"effort":"high"},"thinking":{"budget_tokens":16384,"type":"enabled"}}`, inference: `{"maxTokens":64000}`},
		{name: "none disables", model: "us.anthropic.claude-opus-4-7", reasoning: &model.Reasoning{Effort: "none"},
			fields: `{"thinking":{"type":"disabled"}}`},
		{name: "none between tools", model: "anthropic.claude-sonnet-5-5", reasoning: &model.Reasoning{Effort: "none"},
			fields: `{"thinking":{"type":"between_tools"}}`},
		{name: "none on always-on model", model: "global.anthropic.claude-opus-5-5", reasoning: &model.Reasoning{Effort: "none"},
			fields: `{"output_config":{"effort":"low"}}`},
		{name: "none omitted on extended-only", model: "anthropic.claude-sonnet-4-20250514-v1:0", reasoning: &model.Reasoning{Effort: "none"},
			inference: `{"temperature":0.7,"topP":0.5}`},
		{name: "fable lowers xhigh", model: "anthropic.claude-fable-5-1", reasoning: &model.Reasoning{Effort: "xhigh"},
			fields: `{"output_config":{"effort":"high"},"thinking":{"type":"adaptive"}}`},
		{name: "minimal is low", model: "anthropic.claude-opus-5", reasoning: &model.Reasoning{Effort: "minimal"},
			fields: `{"output_config":{"effort":"low"},"thinking":{"type":"adaptive"}}`},
		{name: "claude 3 cannot think", model: "anthropic.claude-3-haiku-20240307-v1:0", reasoning: &model.Reasoning{Effort: "high"},
			inference: `{"temperature":0.7,"topP":0.5}`},
		{name: "nova 2", model: "us.amazon.nova-2-lite-v1:0", reasoning: &model.Reasoning{Effort: "low"},
			fields: `{"reasoningConfig":{"maxReasoningEffort":"low","type":"enabled"}}`, inference: `{"temperature":0.7,"topP":0.5}`},
		{name: "nova 2 high drops sampling and limit", model: "amazon.nova-2-lite-v1:0", reasoning: &model.Reasoning{Effort: "max"}, maxTokens: 5000,
			fields: `{"reasoningConfig":{"maxReasoningEffort":"high","type":"enabled"}}`},
		{name: "nova 2 budget is medium", model: "amazon.nova-2-lite-v1:0", reasoning: &model.Reasoning{BudgetTokens: 100},
			fields: `{"reasoningConfig":{"maxReasoningEffort":"medium","type":"enabled"}}`, inference: `{"temperature":0.7,"topP":0.5}`},
		{name: "nova 2 none", model: "amazon.nova-2-lite-v1:0", reasoning: &model.Reasoning{Effort: "none"},
			inference: `{"temperature":0.7,"topP":0.5}`},
		{name: "nova 1 ignored", model: "amazon.nova-pro-v1:0", reasoning: &model.Reasoning{Effort: "high"},
			inference: `{"temperature":0.7,"topP":0.5}`},
		{name: "other models ignored", model: "openai.gpt-oss-120b-1:0", reasoning: &model.Reasoning{Effort: "high"},
			inference: `{"temperature":0.7,"topP":0.5}`},
		{name: "sampling rejected without reasoning", model: "anthropic.claude-haiku-5-5",
			inference: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestModel(t)
			req := &model.Request{Messages: []*model.Message{model.UserText("q")}, Reasoning: tc.reasoning, MaxTokens: tc.maxTokens, Temperature: &temp, TopP: &topP}
			if tc.topP != nil {
				req.TopP = tc.topP
			}
			got := buildJSON(t, m, req, tc.model, false)
			if string(compactJSON(t, got["additionalModelRequestFields"])) != tc.fields || string(got["inferenceConfig"]) != tc.inference {
				t.Fatalf("fields = %s, inferenceConfig = %s", got["additionalModelRequestFields"], got["inferenceConfig"])
			}
		})
	}
}

// compactJSON re-encodes raw with sorted keys, or returns "" for nil.
func compactJSON(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	if raw == nil {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	return b
}

func TestClaudeIDAndVersion(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"anthropic.claude-opus-5-5":                                                "opus-5-5",
		"global.anthropic.claude-opus-5-5":                                         "opus-5-5",
		"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-opus-4-6-v1": "opus-4-6-v1",
		"US.Anthropic.Claude-Sonnet-4-5-20250929-v1:0":                             "sonnet-4-5-20250929-v1:0",
	} {
		if got, ok := claudeID(name); !ok || got != want {
			t.Errorf("claudeID(%q) = %q, %v", name, got, ok)
		}
	}
	if _, ok := claudeID("meta.llama3-70b-instruct-v1:0"); ok {
		t.Error("Llama classified as Claude")
	}
	for in, want := range map[string][2]int{"4-6-v1": {4, 6}, "4-20250514-v1:0": {4, 0}, "5": {5, 0}, "x": {0, 0}} {
		if major, minor := version(in); major != want[0] || minor != want[1] {
			t.Errorf("version(%q) = %d, %d", in, major, minor)
		}
	}
}

func TestAdaptSchema(t *testing.T) {
	t.Parallel()
	in := `{"type":"object","properties":{` +
		`"a":{"type":"array","minItems":1,"maxItems":3,"items":{"type":"string","format":"email"}},` +
		`"b":{"type":"array","minItems":2},"c":{"type":"string","format":"phone","maxLength":4},` +
		`"d":{"type":"number","exclusiveMaximum":9}}}`
	want := `{"type":"object","properties":{` +
		`"a":{"type":"array","minItems":1,"items":{"type":"string","format":"email"},"description":"{maxItems: 3}"},` +
		`"b":{"type":"array","description":"{minItems: 2}"},"c":{"type":"string","description":"{format: \"phone\", maxLength: 4}"},` +
		`"d":{"type":"number","description":"{exclusiveMaximum: 9}"}}}`
	if got := string(adaptSchema(json.RawMessage(in))); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}
