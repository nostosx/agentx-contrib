package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nostosx/agentx/model"
)

func TestEmbedder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, model string
		signed      bool
	}{
		{name: "titan v2 bearer", model: DefaultEmbeddingModel},
		{name: "titan g1 signed", model: "amazon.titan-embed-text-v1", signed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var texts []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/model/"+tc.model+"/invoke" {
					t.Errorf("path = %s", r.URL.Path)
				}
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				if tc.signed {
					verifySignature(t, r, data, Credentials{AccessKeyID: "key", SecretAccessKey: "secret"}, "us-east-1")
				} else if r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("missing bearer")
				}
				var body map[string]any
				if err := json.Unmarshal(data, &body); err != nil {
					t.Error(err)
				}
				text, _ := body["inputText"].(string)
				texts = append(texts, text)
				if tc.model == DefaultEmbeddingModel && body["normalize"] != true {
					t.Error("missing normalization")
				}
				if tc.model != DefaultEmbeddingModel && len(body) != 1 {
					t.Error("G1 received unsupported parameters")
				}
				fmt.Fprintf(w, `{"embedding":[%d,1]}`, len(text))
			}))
			defer srv.Close()
			opts := []Option{WithModel(tc.model), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithRegion("us-east-1"), WithAPIKey("fixture")}
			if tc.signed {
				opts = append(opts, WithCredentials(Credentials{AccessKeyID: "key", SecretAccessKey: "secret"}), func(m *Model) { m.now = func() time.Time { return fixedTime } })
			}
			e, err := NewEmbedder(opts...)
			if err != nil {
				t.Fatal(err)
			}
			vecs, err := e.Embed(t.Context(), []string{"a", "bb"})
			if err != nil || len(vecs) != 2 || !slices.Equal(texts, []string{"a", "bb"}) || vecs[1][0] != 2 {
				t.Fatalf("vectors=%v texts=%v err=%v", vecs, texts, err)
			}
			if _, err := e.Embed(t.Context(), []string{"ok", " "}); err == nil || model.IsRetryable(err) {
				t.Fatalf("empty text error=%v", err)
			}
			if len(texts) != 2 {
				t.Fatal("partially sent invalid input")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := e.Embed(ctx, []string{"x"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel error=%v", err)
			}
		})
	}
}

func TestEmbedderErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, payload string
		status        int
		retryable     bool
	}{
		{name: "no vector", payload: `{}`, status: http.StatusOK},
		{name: "invalid vector", payload: `{"embedding":["bad"]}`, status: http.StatusOK},
		{name: "dimension mismatch", payload: `{"embedding":[1,2]}`, status: http.StatusOK},
		{name: "temporary error", payload: `{"message":"busy"}`, status: http.StatusServiceUnavailable, retryable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.payload)
			}))
			defer srv.Close()
			e, err := NewEmbedder(WithAPIKey("fixture"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithDimensions(256))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.Embed(t.Context(), []string{"x"}); err == nil || model.IsRetryable(err) != tc.retryable {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if _, err := NewEmbedder(WithAPIKey("fixture"), WithRegion("us-east-1"), WithModel("unrelated")); err == nil {
		t.Fatal("unsupported family accepted")
	}
	if _, err := NewEmbedder(WithAPIKey("fixture"), WithRegion("us-east-1"), WithDimensions(123)); err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Fatalf("invalid dimensions error=%v", err)
	}
}
