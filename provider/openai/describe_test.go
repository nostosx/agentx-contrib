package openai

import (
	"testing"

	"github.com/nostosx/agentx/model"
)

func TestDescribe(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"), WithBaseURL("https://api.openai.com/v1"))
	if err != nil {
		t.Fatal(err)
	}
	want := model.Description{Provider: "openai", Operation: "chat", Model: DefaultModel, ServerAddress: "api.openai.com", ServerPort: 443}
	if got := model.Describe(m); got != want {
		t.Fatalf("Describe = %+v, want %+v", got, want)
	}
}
