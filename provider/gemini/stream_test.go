package gemini

import (
	"errors"
	"strings"
	"testing"

	"github.com/nostosx/agentx/model"
)

func TestReadStreamPreservesSignedBoundaries(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"text":"one"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"two","thoughtSignature":"sig"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"three"},{"functionCall":{"id":"c","name":"lookup","args":{}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3}}`,
	}
	resp, err := model.Collect(func(yield func(*model.StreamEvent, error) bool) {
		readStream(strings.NewReader("data: "+strings.Join(chunks, "\n\ndata: ")+"\n\n"), yield)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.Parts) != 3 || resp.Message.Parts[0].Text != "onetwo" || resp.Message.Parts[0].Signature != "sig" || resp.Message.Parts[1].Text != "three" || resp.FinishReason != model.FinishToolCalls || resp.Usage.TotalTokens() != 13 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestReadStreamErrorsAndBlocked(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, data   string
		wantErr, api bool
	}{
		{name: "empty", wantErr: true}, {name: "malformed", data: "data: broken\n\n", wantErr: true},
		{name: "api", data: "data: {\"error\":{\"code\":429,\"status\":\"RESOURCE_EXHAUSTED\",\"message\":\"busy\"}}\n\n", wantErr: true, api: true},
		{name: "blocked", data: "data: {\"promptFeedback\":{\"blockReason\":\"SAFETY\"}}\n\n"},
		{name: "truncated", data: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := model.Collect(func(yield func(*model.StreamEvent, error) bool) { readStream(strings.NewReader(tc.data), yield) })
			if (err != nil) != tc.wantErr {
				t.Fatalf("response = %+v, %v", resp, err)
			}
			var api *model.APIError
			if errors.As(err, &api) != tc.api {
				t.Fatalf("error = %v", err)
			}
			if !tc.wantErr && resp.FinishReason != model.FinishContentFilter {
				t.Fatalf("blocked response = %+v", resp)
			}
		})
	}
}

// thoughtImageParts are the parts of an image model's response: draft images
// marked as thoughts, then the final image.
var thoughtImageParts = []string{
	`{"text":"planning","thought":true}`,
	`{"inlineData":{"mimeType":"image/png","data":"AQI="},"thought":true}`,
	`{"inlineData":{"mimeType":"image/png","data":"AwQ="},"thought":true}`,
	`{"text":"Here it is.","thoughtSignature":"sig"}`,
	`{"inlineData":{"mimeType":"image/png","data":"BQY="},"thoughtSignature":"sig2"}`,
}

func checkThoughtImages(t *testing.T, msg *model.Message) {
	t.Helper()
	var got []string
	for _, p := range msg.Parts {
		got = append(got, string(p.Type)+":"+p.Text+":"+string(p.Data))
	}
	want := []string{"reasoning:planning:", "reasoning::\x01\x02", "reasoning::\x03\x04", "text:Here it is.:", "image::\x05\x06"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parts = %q", got)
	}
}

func TestReadStreamThoughtImages(t *testing.T) {
	t.Parallel()
	var chunks []string
	for _, p := range thoughtImageParts {
		chunks = append(chunks, `{"candidates":[{"content":{"parts":[`+p+`]}}]}`)
	}
	chunks = append(chunks, `{"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}]}`)
	resp, err := model.Collect(func(yield func(*model.StreamEvent, error) bool) {
		readStream(strings.NewReader("data: "+strings.Join(chunks, "\n\ndata: ")+"\n\n"), yield)
	})
	if err != nil {
		t.Fatal(err)
	}
	checkThoughtImages(t, resp.Message)
}
