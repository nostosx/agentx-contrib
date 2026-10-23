package openai_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nostosx/agentx-contrib/provider/openai"
	"github.com/nostosx/agentx/model"
)

// A gateway that answers a stream request with a JSON error and status 200
// reports that error, which retrying would not fix.
func TestStreamNonEventResponse(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error":{"message":"model not deployed","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(srv.Close)
	m, err := openai.New(openai.WithAPIKey("sk-test"), openai.WithBaseURL(srv.URL+"/v1"), openai.WithHTTPClient(srv.Client()),
		openai.WithChatCompletions(), openai.WithModel("gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range m.Stream(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("hi")}}) {
		if err == nil || !strings.Contains(err.Error(), "model not deployed") || model.IsRetryable(err) {
			t.Fatalf("Stream error = %v, retryable %v", err, model.IsRetryable(err))
		}
		return
	}
	t.Fatal("Stream yielded nothing")
}
