package anthropic

import (
	"testing"

	"github.com/nostosx/agentx/model"
)

func TestDescribe(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"), WithBaseURL("http://localhost:8080"))
	if err != nil {
		t.Fatal(err)
	}
	want := model.Description{Provider: "anthropic", Operation: "chat", Model: DefaultModel, ServerAddress: "localhost", ServerPort: 8080}
	if got := model.Describe(m); got != want {
		t.Fatalf("Describe = %+v, want %+v", got, want)
	}
}
