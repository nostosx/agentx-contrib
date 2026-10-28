package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/nostosx/agentx/model"
)

func response(text string) *model.Response {
	return &model.Response{Message: model.AssistantText(text), FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 4, OutputTokens: 3}}
}

func TestRunGate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cfg  Config
		code string
	}{
		{name: "disabled", cfg: Config{Provider: "openai", Model: "fixture"}, code: "live_not_enabled"},
		{name: "no model", cfg: Config{Live: true, Provider: "openai"}, code: "invalid_configuration"},
		{name: "unknown check", cfg: Config{Live: true, Provider: "openai", Model: "fixture", Checks: []string{"typo"}}, code: "invalid_configuration"},
		{name: "empty check", cfg: Config{Live: true, Provider: "openai", Model: "fixture", Checks: []string{""}}, code: "invalid_configuration"},
		{name: "missing embedding model", cfg: Config{Live: true, Provider: "openai", Model: "fixture", Checks: []string{"embedding"}}, code: "invalid_configuration"},
		{name: "missing fixture", cfg: Config{Live: true, Provider: "openai", Model: "fixture", Checks: []string{"media"}}, code: "invalid_configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := Run(t.Context(), tc.cfg, func(Config) (Bundle, error) { t.Fatal("factory called before gate passed"); return Bundle{}, nil })
			if r.Status != "unverified" || r.ErrorCode != tc.code {
				t.Fatalf("report=%+v", r)
			}
		})
	}
}

func TestRunChecks(t *testing.T) {
	t.Parallel()
	var calls int
	m := model.Func(func(ctx context.Context, req *model.Request) (*model.Response, error) {
		calls++
		if req.MaxTokens != 512 {
			t.Errorf("unbounded request: %d", req.MaxTokens)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("request lacks deadline")
		}
		if req.ResponseFormat != nil {
			return response(`{"status":"agentx-ok"}`), nil
		}
		if len(req.Tools) > 0 && len(req.Messages) == 1 {
			r := response("")
			r.Message.Parts = []model.Part{{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "call-1", Name: "acceptance_echo", Arguments: json.RawMessage(`{"value":"agentx-ok"}`)}}}
			return r, nil
		}
		if len(req.Tools) > 0 && len(req.Messages[2].ToolResults()) != 1 {
			t.Error("tool result not returned")
		}
		return response("agentx-ok"), nil
	})
	r := Run(t.Context(), Config{Live: true, Provider: "openai", Model: "fixture"}, func(Config) (Bundle, error) { return Bundle{Model: m}, nil })
	if r.Status != "passed" || calls != 5 || r.Results[4].Status != "not_requested" || r.Results[5].Status != "not_requested" {
		t.Fatalf("report=%+v calls=%d", r, calls)
	}
}

type noDone struct{ model.Model }

func (m noDone) Stream(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{Type: model.StreamDelta, Delta: &model.Part{Type: model.PartText, Text: "agentx-ok"}}, nil)
	}
}

func TestRunFailureRedaction(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		m    model.Model
		code string
	}{
		{name: "missing completion", m: noDone{}, code: "check_failed"},
		{name: "provider error", m: model.Func(func(context.Context, *model.Request) (*model.Response, error) {
			return nil, &model.APIError{StatusCode: 401, Message: "secret-api-key-and-prompt"}
		}), code: "http_401"},
		{name: "deadline", m: model.Func(func(ctx context.Context, _ *model.Request) (*model.Response, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}), code: "deadline_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := Run(t.Context(), Config{Live: true, Provider: "openai", Model: "fixture", Checks: []string{"stream"}, Timeout: time.Millisecond}, func(Config) (Bundle, error) { return Bundle{Model: tc.m}, nil })
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != "failed" || r.Results[1].ErrorCode != tc.code || strings.Contains(string(data), "secret") {
				t.Fatalf("report=%s", data)
			}
		})
	}
	r := Run(t.Context(), Config{Live: true, Provider: "openai", Model: "fixture"}, func(Config) (Bundle, error) { return Bundle{}, errors.New("secret credentials") })
	if r.Status != "unverified" || r.ErrorCode != "adapter_configuration_failed" {
		t.Fatalf("report=%+v", r)
	}
}
