package utils_test

import (
	"encoding/json"
	"testing"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

func TestEncodeBody(t *testing.T) {
	t.Parallel()
	if b, err := utils.EncodeBody("test", map[string]any{"a": 1}, map[string]any{"b": 2}); err != nil || string(b) != `{"a":1,"b":2}` {
		t.Fatalf("EncodeBody = %s, %v", b, err)
	}
	if _, err := utils.EncodeBody("test", map[string]any{}, map[string]any{"bad": make(chan int)}); err == nil || model.IsRetryable(err) {
		t.Fatalf("encoding error = %v", err)
	}
}

func TestNormalizeToolArguments(t *testing.T) {
	t.Parallel()
	msg := model.NewMessage(model.RoleAssistant, model.TextPart("x"),
		model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{Arguments: json.RawMessage(` { "q": 1 } `)}},
		model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{}})
	got := utils.NormalizeToolArguments(msg)
	if got != msg || string(msg.Parts[1].ToolCall.Arguments) != `{"q":1}` || string(msg.Parts[2].ToolCall.Arguments) != `{}` {
		t.Fatalf("parts = %+v", msg.Parts)
	}
}
