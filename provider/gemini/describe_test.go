package gemini

import (
	"testing"

	"github.com/nostosx/agentx/model"
)

func TestDescribe(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"), WithBaseURL("https://generativelanguage.googleapis.com/v1beta"))
	if err != nil {
		t.Fatal(err)
	}
	want := model.Description{Provider: "gcp.gemini", Operation: "generate_content", Model: DefaultModel, ServerAddress: "generativelanguage.googleapis.com", ServerPort: 443}
	if got := model.Describe(m); got != want {
		t.Fatalf("Describe = %+v, want %+v", got, want)
	}
}
