package anthropic

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nostosx/agentx/model"
)

// send sends req through m to a capturing server and returns the request
// body's thinking and output_config, compacted, and the beta flags.
func send(t *testing.T, req *model.Request, opts ...Option) (thinking, effort string, betas []string) {
	t.Helper()
	srv, last := captureRequest(t)
	m, err := New(append([]Option{WithAPIKey("fixture"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client())}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Generate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	body, beta := last()
	if v, ok := body["thinking"]; ok {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		thinking = string(b)
	}
	if oc, ok := body["output_config"].(map[string]any); ok {
		effort, _ = oc["effort"].(string)
	}
	if beta != "" {
		betas = strings.Split(beta, ",")
	}
	return thinking, effort, betas
}

func TestReasoningPerModel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		model     string
		maxTokens int
		reasoning model.Reasoning
		thinking  string
		effort    string
	}{
		// Adaptive-only models ignore budgets.
		{name: "opus 4.7 budget", model: "claude-opus-4-7", reasoning: model.Reasoning{BudgetTokens: 4096, Effort: "high"}, thinking: `{"type":"adaptive"}`, effort: "high"},
		{name: "opus 5 budget", model: "claude-opus-5", reasoning: model.Reasoning{BudgetTokens: 4096}, thinking: `{"type":"adaptive"}`},
		{name: "fable 5 budget", model: "claude-fable-5", reasoning: model.Reasoning{BudgetTokens: 4096, Display: "summarized"}, thinking: `{"display":"summarized","type":"adaptive"}`},
		{name: "bedrock opus 4.8 budget", model: "us.anthropic.claude-opus-4-8", reasoning: model.Reasoning{BudgetTokens: 4096}, thinking: `{"type":"adaptive"}`},
		{name: "vertex sonnet 5 budget", model: "claude-sonnet-5@20260101", reasoning: model.Reasoning{BudgetTokens: 4096}, thinking: `{"type":"adaptive"}`},
		// Models accepting both keep budgets.
		{name: "mythos preview budget", model: "claude-mythos-preview", reasoning: model.Reasoning{BudgetTokens: 4096}, thinking: `{"budget_tokens":4096,"type":"enabled"}`},
		{name: "sonnet 4.6 budget", model: "claude-sonnet-4-6", reasoning: model.Reasoning{BudgetTokens: 4096}, thinking: `{"budget_tokens":4096,"type":"enabled"}`},
		{name: "sonnet 4.6 effort", model: "claude-sonnet-4-6", reasoning: model.Reasoning{Effort: "medium"}, thinking: `{"type":"adaptive"}`, effort: "medium"},
		// Extended-only models derive budgets from effort.
		{name: "sonnet 4.5 high", model: "claude-sonnet-4-5", maxTokens: 64000, reasoning: model.Reasoning{Effort: "high"}, thinking: `{"budget_tokens":16384,"type":"enabled"}`},
		{name: "opus 4.5 medium keeps effort", model: "claude-opus-4-5-20251101", maxTokens: 32000, reasoning: model.Reasoning{Effort: "medium"}, thinking: `{"budget_tokens":8192,"type":"enabled"}`, effort: "medium"},
		{name: "opus 4.5 max becomes high", model: "claude-opus-4-5-20251101", maxTokens: 64000, reasoning: model.Reasoning{Effort: "max"}, thinking: `{"budget_tokens":16384,"type":"enabled"}`, effort: "high"},
		{name: "haiku 4.5 default", model: "claude-haiku-4-5", thinking: `{"budget_tokens":8000,"type":"enabled"}`},
		{name: "bedrock opus 4.1 low", model: "anthropic.claude-opus-4-1-20250805-v1:0", reasoning: model.Reasoning{Effort: "low"}, thinking: `{"budget_tokens":2048,"type":"enabled"}`},
		{name: "sonnet 4 minimal", model: "claude-sonnet-4-20250514", reasoning: model.Reasoning{Effort: "minimal"}, thinking: `{"budget_tokens":2048,"type":"enabled"}`},
		{name: "sonnet 4.5 high default max_tokens", model: "claude-sonnet-4-5", reasoning: model.Reasoning{Effort: "high"}, thinking: `{"budget_tokens":8000,"type":"enabled"}`},
		{name: "sonnet 3.7 max clamped", model: "claude-3-7-sonnet-20250219", maxTokens: 4000, reasoning: model.Reasoning{Effort: "max"}, thinking: `{"budget_tokens":2000,"type":"enabled"}`},
		{name: "no room for budget", model: "claude-sonnet-4-5", maxTokens: 1024, reasoning: model.Reasoning{Effort: "high"}},
		{name: "extended explicit budget", model: "vertex/claude-sonnet-4@20250514", reasoning: model.Reasoning{BudgetTokens: 2000, Effort: "high"}, thinking: `{"budget_tokens":2000,"type":"enabled"}`},
		// Effort "none" turns thinking off where the model allows it.
		{name: "sonnet 4.6 none", model: "claude-sonnet-4-6", reasoning: model.Reasoning{Effort: "none"}, thinking: `{"type":"disabled"}`},
		{name: "opus 4.7 none", model: "claude-opus-4-7", reasoning: model.Reasoning{Effort: "none"}, thinking: `{"type":"disabled"}`},
		{name: "opus 5 none", model: "claude-opus-5", reasoning: model.Reasoning{Effort: "none"}, thinking: `{"type":"disabled"}`},
		{name: "haiku 5.5 none", model: "claude-haiku-5-5", reasoning: model.Reasoning{Effort: "none"}, thinking: `{"type":"disabled"}`},
		{name: "sonnet 5.5 none", model: "claude-sonnet-5-5", reasoning: model.Reasoning{Effort: "none"}, thinking: `{"type":"between_tools"}`},
		{name: "opus 5.5 none", model: "claude-opus-5-5", reasoning: model.Reasoning{Effort: "none"}, effort: "low"},
		{name: "fable 5.1 none", model: "claude-fable-5-1", reasoning: model.Reasoning{Effort: "none"}, effort: "low"},
		{name: "mythos preview none", model: "claude-mythos-preview", reasoning: model.Reasoning{Effort: "none"}, effort: "low"},
		{name: "sonnet 4.5 none", model: "claude-sonnet-4-5", reasoning: model.Reasoning{Effort: "none"}},
		// Effort values a model lacks are mapped.
		{name: "opus 5 minimal", model: "claude-opus-5", reasoning: model.Reasoning{Effort: "minimal"}, thinking: `{"type":"adaptive"}`, effort: "low"},
		{name: "sonnet 4.6 xhigh", model: "claude-sonnet-4-6", reasoning: model.Reasoning{Effort: "xhigh"}, thinking: `{"type":"adaptive"}`, effort: "high"},
		{name: "opus 4.8 xhigh", model: "claude-opus-4-8", reasoning: model.Reasoning{Effort: "xhigh"}, thinking: `{"type":"adaptive"}`, effort: "xhigh"},
		{name: "opus 4.6 max", model: "claude-opus-4-6", reasoning: model.Reasoning{Effort: "max"}, thinking: `{"type":"adaptive"}`, effort: "max"},
		// Other names keep the generic behavior.
		{name: "alias", model: "my-gateway-model", reasoning: model.Reasoning{Effort: "none"}, thinking: `{"type":"adaptive"}`, effort: "none"},
		{name: "alias budget", model: "my-gateway-model", reasoning: model.Reasoning{BudgetTokens: 4096}, thinking: `{"budget_tokens":4096,"type":"enabled"}`},
		{name: "claude 3.5", model: "claude-3-5-sonnet-20241022", reasoning: model.Reasoning{Effort: "low"}, thinking: `{"type":"adaptive"}`, effort: "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			thinking, effort, _ := send(t, &model.Request{Model: tc.model, MaxTokens: tc.maxTokens, Messages: []*model.Message{model.UserText("hi")}, Reasoning: &tc.reasoning})
			if thinking != tc.thinking || effort != tc.effort {
				t.Errorf("thinking = %s, effort = %q; want %s, %q", thinking, effort, tc.thinking, tc.effort)
			}
		})
	}
}

// Turning thinking off keeps the sampling parameters older models accept.
func TestReasoningNoneKeepsSampling(t *testing.T) {
	t.Parallel()
	srv, last := captureRequest(t)
	m, err := New(WithAPIKey("fixture"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	temp := 0.3
	for _, name := range []string{"claude-sonnet-4-6", "claude-sonnet-4-5"} {
		if _, err := m.Generate(t.Context(), &model.Request{Model: name, Messages: []*model.Message{model.UserText("hi")}, Temperature: &temp, Reasoning: &model.Reasoning{Effort: "none"}}); err != nil {
			t.Fatal(err)
		}
		if body, _ := last(); body["temperature"] != temp {
			t.Errorf("%s: body = %v", name, body)
		}
	}
}

func TestThinkingBlockDrop(t *testing.T) {
	t.Parallel()
	signed := model.NewMessage(model.RoleAssistant, model.Part{Type: model.PartReasoning, Text: "t", Signature: "sig", Provider: providerName}, model.TextPart("answer"))
	redacted := model.NewMessage(model.RoleAssistant, model.Part{Type: model.PartReasoning, Redacted: true, Data: []byte("opaque")}, model.TextPart("answer"))
	foreign := model.NewMessage(model.RoleAssistant, model.Part{Type: model.PartReasoning, Text: "t", Signature: "sig", Provider: "gemini"}, model.TextPart("answer"))
	const drop = `"block_binding":{"prefix_mismatch_behavior":"drop_block"}`
	for _, tc := range []struct {
		name      string
		model     string
		history   *model.Message
		reasoning *model.Reasoning
		opts      []Option
		thinking  string
		bound     bool
	}{
		{name: "default reasoning", model: "claude-opus-5-5", history: signed, thinking: `{` + drop + `,"type":"adaptive"}`, bound: true},
		{name: "redacted", model: "claude-fable-5-1", history: redacted, thinking: `{` + drop + `,"type":"adaptive"}`, bound: true},
		{name: "adaptive with effort", model: "claude-sonnet-5-5", history: signed, reasoning: &model.Reasoning{Effort: "high", Display: "omitted"}, thinking: `{` + drop + `,"display":"omitted","type":"adaptive"}`, bound: true},
		{name: "always on with none", model: "claude-opus-5-5", history: signed, reasoning: &model.Reasoning{Effort: "none"}, thinking: `{` + drop + `,"type":"adaptive"}`, bound: true},
		{name: "already configured beta", model: "claude-haiku-5-5", history: signed, opts: []Option{WithBeta(ThinkingBindingBeta)}, thinking: `{` + drop + `,"type":"adaptive"}`, bound: true},
		{name: "between tools", model: "claude-sonnet-5-5", history: signed, reasoning: &model.Reasoning{Effort: "none"}, thinking: `{"type":"between_tools"}`},
		{name: "disabled", model: "claude-haiku-5-5", history: signed, reasoning: &model.Reasoning{Effort: "none"}, thinking: `{"type":"disabled"}`},
		{name: "no replay", model: "claude-opus-5-5", history: model.NewMessage(model.RoleAssistant, model.TextPart("answer"))},
		{name: "foreign reasoning", model: "claude-opus-5-5", history: foreign},
		{name: "no prefix check", model: "claude-opus-5", history: signed, reasoning: &model.Reasoning{}, thinking: `{"type":"adaptive"}`},
		{name: "opted out", model: "claude-opus-5-5", history: signed, opts: []Option{WithoutThinkingBlockDrop()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := &model.Request{Model: tc.model, Messages: []*model.Message{model.UserText("q"), tc.history, model.UserText("next")}, Reasoning: tc.reasoning}
			thinking, _, betas := send(t, req, tc.opts...)
			if thinking != tc.thinking {
				t.Errorf("thinking = %s, want %s", thinking, tc.thinking)
			}
			n := 0
			for _, b := range betas {
				if b == ThinkingBindingBeta {
					n++
				}
			}
			if want := map[bool]int{true: 1}[tc.bound]; n != want {
				t.Errorf("betas = %q", betas)
			}
			if !slices.Contains(betas, FallbackBeta) && slices.Contains(fallbackModels, tc.model) {
				t.Errorf("fallback beta lost: %q", betas)
			}
		})
	}
}
