package openai

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nostosx/agentx/model"
)

// recorder is a test server that records request bodies and answers with
// a fixed payload.
type recorder struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (rec *recorder) serve(t *testing.T, payload string, opts ...Option) *Model {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b bytes.Buffer
		if _, err := b.ReadFrom(r.Body); err != nil {
			t.Error(err)
		}
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, b.Bytes())
		rec.mu.Unlock()
		if strings.HasPrefix(payload, "data:") {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		if _, err := w.Write([]byte(payload)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	m, err := New(append([]Option{WithAPIKey("fixture"), WithBaseURL(srv.URL + "/v1"), WithHTTPClient(srv.Client())}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (rec *recorder) last(t *testing.T) map[string]any {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.bodies) == 0 {
		t.Fatal("no request")
	}
	return decodeBody(t, rec.bodies[len(rec.bodies)-1])
}

// jsonEqual fails unless got encodes to the same JSON as want.
func jsonEqual(t *testing.T, name string, got any, want string) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var w bytes.Buffer
	if err := json.Compact(&w, []byte(want)); err != nil {
		t.Fatalf("%s: bad fixture: %v", name, err)
	}
	// Round-trip want through any so key order matches.
	var v any
	if err := json.Unmarshal(w.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	wb, _ := json.Marshal(v)
	if string(g) != string(wb) {
		t.Errorf("%s =\n%s\nwant\n%s", name, g, wb)
	}
}

var (
	pdf = []byte("%PDF-1.7\n1 0 obj\n<< /Type /Page >>\nendobj\n")
	png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	wav = append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 16)...)
)

const responsesOK = `{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`

func TestResponsesMediaInput(t *testing.T) {
	t.Parallel()
	var rec recorder
	m := rec.serve(t, responsesOK)
	detailed := model.ImagePart("image/png", png)
	detailed.Detail = "high"
	req := &model.Request{Messages: []*model.Message{
		model.NewMessage(model.RoleUser,
			model.TextPart("compare"),
			detailed,
			model.FilePart(model.PartImage, "openai", "file-img", ""),
			model.DocumentPart("application/pdf", pdf, "report.pdf"),
			model.DocumentPart("text/csv", []byte("a,b\n1,2\n"), ""),
			model.DocumentURLPart("https://example.com/paper.pdf"),
			model.FilePart(model.PartDocument, "", "file-doc", ""),
		),
	}}
	if _, err := m.Generate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, "input", rec.last(t)["input"], `[{"type":"message","role":"user","content":[
		{"type":"input_text","text":"compare"},
		{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUg==","detail":"high"},
		{"type":"input_image","file_id":"file-img"},
		{"type":"input_file","file_data":"data:application/pdf;base64,JVBERi0xLjcKMSAwIG9iago8PCAvVHlwZSAvUGFnZSA+PgplbmRvYmoK","filename":"report.pdf"},
		{"type":"input_file","file_data":"data:text/csv;base64,YSxiCjEsMgo=","filename":"file.csv"},
		{"type":"input_file","file_url":"https://example.com/paper.pdf"},
		{"type":"input_file","file_id":"file-doc"}]}]`)
}

func TestResponsesToolOutputParts(t *testing.T) {
	t.Parallel()
	var rec recorder
	m := rec.serve(t, responsesOK)
	req := &model.Request{
		Tools: []*model.ToolSpec{{Name: "shot"}, {Name: "patch", Grammar: &model.ToolGrammar{Syntax: "lark"}}},
		Messages: []*model.Message{
			model.UserText("go"),
			model.NewMessage(model.RoleAssistant,
				model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "c1", Name: "shot", Arguments: json.RawMessage(`{}`)}},
				model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "c2", Name: "patch", Arguments: json.RawMessage(`{"input":"x"}`)}},
				model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "c3", Name: "shot", Arguments: json.RawMessage(`{}`)}}),
			model.ToolResults(
				&model.ToolResult{CallID: "c1", Name: "shot", Content: "screen", Parts: []model.Part{model.ImagePart("image/png", png)}},
				&model.ToolResult{CallID: "c2", Name: "patch", Parts: []model.Part{model.DocumentPart("application/pdf", pdf, "out.pdf")}},
				&model.ToolResult{CallID: "c3", Name: "shot", Content: "plain", Metadata: map[string]any{"secret": true}}),
		},
	}
	if _, err := m.Generate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	input := rec.last(t)["input"].([]any)
	jsonEqual(t, "function output", input[4], `{"type":"function_call_output","call_id":"c1","output":[
		{"type":"input_text","text":"screen"},
		{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="}]}`)
	jsonEqual(t, "custom output", input[5], `{"type":"custom_tool_call_output","call_id":"c2","output":[
		{"type":"input_file","file_data":"data:application/pdf;base64,JVBERi0xLjcKMSAwIG9iago8PCAvVHlwZSAvUGFnZSA+PgplbmRvYmoK","filename":"out.pdf"}]}`)
	// Plain results stay strings, and metadata is not sent.
	jsonEqual(t, "text output", input[6], `{"type":"function_call_output","call_id":"c3","output":"plain"}`)
}

func TestResponsesRejectsUnsupportedMedia(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		part model.Part
		want string
	}{
		{name: "audio", part: model.AudioPart("audio/wav", wav), want: "does not take audio"},
		{name: "video", part: model.VideoURLPart("https://example.com/a.mp4"), want: "video input is not supported"},
		{name: "foreign file", part: model.FilePart(model.PartDocument, "anthropic", "file_1", ""), want: "file file_1 of provider anthropic"},
		{name: "empty", part: model.Part{Type: model.PartImage}, want: "has no data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.buildResponses(&model.Request{Messages: []*model.Message{model.NewMessage(model.RoleUser, tc.part)}}, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) || model.IsRetryable(err) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

const chatOK = `{"id":"c","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`

func TestChatMediaInput(t *testing.T) {
	t.Parallel()
	var rec recorder
	m := rec.serve(t, chatOK, WithChatCompletions())
	low := model.ImageURLPart("https://example.com/cat.png")
	low.Detail = "low"
	req := &model.Request{Messages: []*model.Message{
		model.NewMessage(model.RoleUser,
			low,
			model.DocumentPart("application/pdf", pdf, "report.pdf"),
			model.FilePart(model.PartDocument, "openai", "file-1", ""),
			model.TextDocumentPart("notes.txt", "remember"),
			model.AudioPart("audio/wav", wav),
			model.AudioURLPart("data:audio/mpeg;base64,SUQz"),
		),
	}}
	if _, err := m.Generate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, "messages", rec.last(t)["messages"], `[{"role":"user","content":[
		{"type":"image_url","image_url":{"url":"https://example.com/cat.png","detail":"low"}},
		{"type":"file","file":{"filename":"report.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjcKMSAwIG9iago8PCAvVHlwZSAvUGFnZSA+PgplbmRvYmoK"}},
		{"type":"file","file":{"file_id":"file-1"}},
		{"type":"text","text":"Document notes.txt:\n\nremember"},
		{"type":"input_audio","input_audio":{"data":"UklGRiQAAABXQVZFZm10IAAAAAAAAAAAAAAAAAAAAAA=","format":"wav"}},
		{"type":"input_audio","input_audio":{"data":"SUQz","format":"mp3"}}]}]`)
}

func TestChatToolResultParts(t *testing.T) {
	t.Parallel()
	var rec recorder
	m := rec.serve(t, chatOK, WithChatCompletions())
	req := &model.Request{Messages: []*model.Message{
		model.NewMessage(model.RoleAssistant,
			model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "c1", Name: "shot"}},
			model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "c2", Name: "read"}}),
		model.NewMessage(model.RoleTool,
			model.Part{Type: model.PartToolResult, ToolResult: &model.ToolResult{CallID: "c1", Name: "shot", Content: "screen", Parts: []model.Part{model.ImagePart("image/png", png)}}},
			model.Part{Type: model.PartToolResult, ToolResult: &model.ToolResult{CallID: "c2", Name: "read", Parts: []model.Part{model.DocumentPart("application/pdf", pdf, "a.pdf")}}},
			model.TextPart("continue")),
	}}
	if _, err := m.Generate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	// Tool messages take only text, so the parts follow in a user
	// message.
	jsonEqual(t, "messages", rec.last(t)["messages"].([]any)[1:], `[
		{"role":"tool","content":"screen","tool_call_id":"c1"},
		{"role":"tool","content":"[The content follows in the next message.]","tool_call_id":"c2"},
		{"role":"user","content":[
			{"type":"text","text":"[Content returned by tool call c1 (shot)]"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="}},
			{"type":"text","text":"[Content returned by tool call c2 (read)]"},
			{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjcKMSAwIG9iago8PCAvVHlwZSAvUGFnZSA+PgplbmRvYmoK"}},
			{"type":"text","text":"continue"}]}]`)
}

func TestChatRejectsUnsupportedMedia(t *testing.T) {
	t.Parallel()
	official, err := New(WithAPIKey("fixture"), WithChatCompletions())
	if err != nil {
		t.Fatal(err)
	}
	compatible, err := New(WithAPIKey("fixture"), WithChatCompletions(), WithBaseURL("http://localhost:8000/v1"))
	if err != nil {
		t.Fatal(err)
	}
	docx := model.DocumentPart("application/vnd.openxmlformats-officedocument.wordprocessingml.document", []byte("PK\x03\x04"), "a.docx")
	ogg := model.AudioPart("audio/ogg", []byte("OggS"))
	for _, tc := range []struct {
		name string
		m    *Model
		part model.Part
		want string
	}{
		{name: "document URL", m: official, part: model.DocumentURLPart("https://example.com/a.pdf"), want: "does not take document URLs"},
		{name: "docx", m: official, part: docx, want: "takes PDF documents"},
		{name: "ogg", m: official, part: ogg, want: "takes WAV and MP3"},
		{name: "image file ID", m: official, part: model.FilePart(model.PartImage, "", "file-1", ""), want: "image file IDs"},
		{name: "audio URL", m: official, part: model.AudioURLPart("https://example.com/a.mp3"), want: "audio data"},
		{name: "video", m: compatible, part: model.VideoPart("video/mp4", []byte("x")), want: "video input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.m.build(&model.Request{Messages: []*model.Message{model.NewMessage(model.RoleUser, tc.part)}}, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) || model.IsRetryable(err) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	// Compatible services decide which formats they take.
	body, err := compatible.build(&model.Request{Messages: []*model.Message{model.NewMessage(model.RoleUser, docx, ogg)}}, false)
	if err != nil || !strings.Contains(string(body), `"format":"ogg"`) || !strings.Contains(string(body), `"filename":"a.docx"`) {
		t.Fatalf("compatible body = %s, %v", body, err)
	}
}

func TestChatAudioOutput(t *testing.T) {
	t.Parallel()
	expires := time.Now().Add(time.Hour).Unix()
	payload := `{"id":"c","choices":[{"message":{"content":null,"audio":{"id":"audio_1","data":"UklGRiQAAABXQVZFZm10IAAAAAAAAAAAAAAAAAAAAAA=","expires_at":` + jsonInt(expires) + `,"transcript":"hello"}},"finish_reason":"stop"}]}`
	var rec recorder
	m := rec.serve(t, payload, WithChatCompletions())
	resp, err := m.Generate(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("speak")}})
	if err != nil {
		t.Fatal(err)
	}
	p := resp.Message.Parts[0]
	if p.Type != model.PartAudio || p.MediaType != "audio/wave" || !bytes.Equal(p.Data, wav) || p.Text != "hello" || p.Provider != "openai" {
		t.Fatalf("audio part = %+v", p)
	}
	// The audio is referred to by ID while it lasts, then by transcript.
	expired := resp.Message.Clone()
	expired.Parts[0].Raw = json.RawMessage(`{"id":"audio_1","expires_at":1}`)
	for _, tc := range []struct {
		msg  *model.Message
		want string
	}{
		{msg: resp.Message, want: `{"role":"assistant","content":"","audio":{"id":"audio_1"}}`},
		{msg: expired, want: `{"role":"assistant","content":"hello"}`},
	} {
		if _, err := m.Generate(t.Context(), &model.Request{Messages: []*model.Message{tc.msg}}); err != nil {
			t.Fatal(err)
		}
		jsonEqual(t, "replay", rec.last(t)["messages"].([]any)[0], tc.want)
	}
	// The Responses API replays the transcript.
	input, err := m.convertInput([]*model.Message{resp.Message}, nil)
	if err != nil || len(input) != 1 || !strings.Contains(string(input[0]), `"content":"hello"`) {
		t.Fatalf("responses replay = %s, %v", input, err)
	}
}

func TestChatStreamAudio(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"id":"c","choices":[{"delta":{"audio":{"id":"audio_1","transcript":"hel"}}}]}`,
		`{"choices":[{"delta":{"audio":{"data":"AQID","transcript":"lo"}}}]}`,
		`{"choices":[{"delta":{"audio":{"data":"BAU="}}}]}`,
		`{"choices":[{"delta":{"audio":{"expires_at":4102444800}},"finish_reason":"stop"}]}`,
	}
	payload := ""
	for _, c := range chunks {
		payload += "data: " + c + "\n\n"
	}
	payload += "data: [DONE]\n\n"
	var rec recorder
	m := rec.serve(t, payload, WithChatCompletions())
	resp, err := model.Collect(m.Stream(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("speak")}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.Parts) != 1 {
		t.Fatalf("parts = %+v, want one audio part", resp.Message.Parts)
	}
	p := resp.Message.Parts[0]
	if p.Type != model.PartAudio || !bytes.Equal(p.Data, []byte{1, 2, 3, 4, 5}) || p.MediaType != "audio/pcm" || p.Text != "hello" || string(p.Raw) != `{"id":"audio_1","expires_at":4102444800}` {
		t.Fatalf("parts = %+v", resp.Message.Parts)
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
