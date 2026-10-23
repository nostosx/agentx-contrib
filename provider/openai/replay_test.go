package openai_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/nostosx/agentx-contrib/provider/openai"
	"github.com/nostosx/agentx/eval/replay"
	"github.com/nostosx/agentx/model"
)

// sseServer streams a Chat Completions answer, counting the requests it
// serves in hits.
func sseServer(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	events := []string{
		`{"id":"r1","model":"gpt-test","choices":[{"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`,
		`[DONE]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events {
			if _, err := fmt.Fprintf(w, "data: %s\n\n", ev); err != nil {
				t.Error(err)
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEventStreamWithReplay(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := sseServer(t, &hits)
	path := filepath.Join(t.TempDir(), "stream.json")
	stream := func(rec *replay.Recorder) *model.Response {
		t.Helper()
		m, err := openai.New(openai.WithAPIKey("sk-secret"), openai.WithBaseURL(srv.URL+"/v1"),
			openai.WithHTTPClient(rec.Client()), openai.WithChatCompletions(), openai.WithModel("gpt-test"))
		if err != nil {
			t.Fatal(err)
		}
		deltas := 0
		var final *model.Response
		for ev, err := range m.Stream(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("hi")}}) {
			if err != nil {
				t.Fatal(err)
			}
			switch ev.Type {
			case model.StreamDelta:
				deltas++
			case model.StreamDone:
				final = ev.Response
			}
		}
		if deltas != 2 || final == nil {
			t.Fatalf("deltas = %d, final %+v", deltas, final)
		}
		return final
	}
	rec := replay.NewTest(t, path, replay.WithMode(replay.ModeRecord), replay.WithTransport(srv.Client().Transport))
	recorded := stream(rec)
	if err := rec.Save(); err != nil {
		t.Fatal(err)
	}
	c, err := replay.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Interactions) != 1 || hits.Load() != 1 {
		t.Fatalf("recorded %d interactions from %d requests, want one", len(c.Interactions), hits.Load())
	}
	rs := c.Interactions[0].Response
	if len(rs.Events) != 4 || rs.Body != "" || rs.Events[0] != "data: {\"id\":\"r1\",\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n" ||
		c.Interactions[0].Request.Header.Get("Authorization") != replay.Redacted {
		t.Fatalf("recorded stream = %+v", c.Interactions[0])
	}

	served := hits.Load()
	rp := replay.NewTest(t, path, replay.WithMode(replay.ModeReplay))
	replayed := stream(rp)
	if hits.Load() != served {
		t.Fatal("replay reached the server")
	}
	if replayed.Text() != "Hello" || replayed.Text() != recorded.Text() || replayed.Usage != recorded.Usage || replayed.ID != "r1" || replayed.FinishReason != model.FinishStop {
		t.Fatalf("replayed = %+v, recorded %+v", replayed, recorded)
	}
}
