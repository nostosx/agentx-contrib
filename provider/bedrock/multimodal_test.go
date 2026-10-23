package bedrock

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nostosx/agentx/model"
)

// jsonEqual fails unless got is the same JSON as want.
func jsonEqual(t *testing.T, name string, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: bad fixture: %v", name, err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("%s =\n%s\nwant\n%s", name, gb, wb)
	}
}

var pdfBytes = []byte("%PDF-1.7\n")

// converseServer returns a model whose requests go to a test server that
// records the request bodies and answers with payload.
func converseServer(t *testing.T, payload string) (*Model, *[][]byte) {
	t.Helper()
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(srv.Close)
	return newTestModel(t, WithBaseURL(srv.URL), WithModel("us.amazon.nova-pro-v1:0")), &bodies
}

func messagesOf(t *testing.T, body []byte) json.RawMessage {
	t.Helper()
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	return req["messages"]
}

const converseOK = `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{}}`

func TestMediaBlocks(t *testing.T) {
	t.Parallel()
	m, bodies := converseServer(t, converseOK)
	cited := model.TextDocumentPart("Q3 notes.txt", "Revenue grew.")
	cited.Citations = true
	req := &model.Request{Messages: []*model.Message{model.NewMessage(model.RoleUser,
		model.TextPart("compare"),
		model.DocumentPart("application/pdf", pdfBytes, "report_v2.final.pdf"),
		model.DocumentPart("application/pdf", pdfBytes, "report_v2.final.pdf"),
		cited,
		model.Part{Type: model.PartDocument, URL: "s3://bucket/sheet.xlsx"},
		model.VideoPart("video/mp4", []byte("mp4")),
		model.Part{Type: model.PartVideo, URL: "s3://bucket/clip.mov"},
		model.AudioPart("audio/mpeg", []byte("ID3")),
		model.AudioURLPart("data:audio/wav;base64,UklGRg=="),
	)}}
	if _, err := m.Generate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, "messages", messagesOf(t, (*bodies)[0]), `[{"role":"user","content":[
		{"text":"compare"},
		{"document":{"format":"pdf","name":"report-v2-final","source":{"bytes":"JVBERi0xLjcK"}}},
		{"document":{"format":"pdf","name":"report-v2-final (2)","source":{"bytes":"JVBERi0xLjcK"}}},
		{"document":{"format":"txt","name":"Q3 notes","source":{"bytes":"UmV2ZW51ZSBncmV3Lg=="},"citations":{"enabled":true}}},
		{"document":{"format":"xlsx","name":"document","source":{"s3Location":{"uri":"s3://bucket/sheet.xlsx"}}}},
		{"video":{"format":"mp4","source":{"bytes":"bXA0"}}},
		{"video":{"format":"mov","source":{"s3Location":{"uri":"s3://bucket/clip.mov"}}}},
		{"audio":{"format":"mp3","source":{"bytes":"SUQz"}}},
		{"audio":{"format":"wav","source":{"bytes":"UklGRg=="}}}]}]`)
}

func TestToolResultMedia(t *testing.T) {
	t.Parallel()
	m, bodies := converseServer(t, converseOK)
	req := &model.Request{
		Tools: []*model.ToolSpec{{Name: "capture"}},
		Messages: []*model.Message{
			model.UserText("go"),
			model.NewMessage(model.RoleAssistant, model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "t1", Name: "capture"}}),
			model.ToolResults(&model.ToolResult{CallID: "t1", Name: "capture", Content: "captured", IsError: true, Parts: []model.Part{
				model.ImagePart("image/png", pngBytes),
				model.DocumentPart("application/pdf", pdfBytes, "a.pdf"),
				model.VideoPart("video/webm", []byte("webm")),
				model.AudioPart("audio/ogg", []byte("OggS")),
			}}),
		},
	}
	if _, err := m.Generate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(messagesOf(t, (*bodies)[0]), &msgs); err != nil {
		t.Fatal(err)
	}
	// Tool results take no audio, which follows the result.
	jsonEqual(t, "result", msgs[2], `{"role":"user","content":[
		{"toolResult":{"toolUseId":"t1","status":"error","content":[
			{"text":"captured"},
			{"image":{"format":"png","source":{"bytes":"iVBORw0KGgoAAAANSUhEUg=="}}},
			{"document":{"format":"pdf","name":"a","source":{"bytes":"JVBERi0xLjcK"}}},
			{"video":{"format":"webm","source":{"bytes":"d2VibQ=="}}}]}},
		{"text":"[Content returned by tool call t1 (capture)]"},
		{"audio":{"format":"ogg","source":{"bytes":"T2dnUw=="}}}]}`)

	// Without tools, results are text and their media follow.
	req.Tools = nil
	if _, err := m.Generate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(messagesOf(t, (*bodies)[1]), &msgs); err != nil {
		t.Fatal(err)
	}
	if s := string(msgs[2]); !strings.Contains(s, `{"text":"[Tool error for call t1: captured]"}`) || !strings.Contains(s, `"image":`) || strings.Contains(s, "toolResult") {
		t.Fatalf("text result = %s", s)
	}
}

func TestMediaErrors(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)
	citedXLSX := model.Part{Type: model.PartDocument, URL: "s3://bucket/a.xlsx", Citations: true}
	for _, tc := range []struct {
		name  string
		parts []model.Part
		want  string
	}{
		{name: "document without text", parts: []model.Part{model.DocumentPart("application/pdf", pdfBytes, "a")}, want: "needs text as well"},
		{name: "document URL", parts: []model.Part{model.TextPart("x"), model.DocumentURLPart("https://example.com/a.pdf")}, want: "data: or s3://"},
		{name: "file ID", parts: []model.Part{model.FilePart(model.PartDocument, "", "file-1", "application/pdf")}, want: "file IDs are not supported"},
		{name: "unknown document", parts: []model.Part{model.TextPart("x"), model.DocumentPart("application/zip", []byte("PK"), "a.zip")}, want: `unsupported document type "application/zip"`},
		{name: "unknown video", parts: []model.Part{model.VideoPart("video/ogg", []byte("x"))}, want: "unsupported video type"},
		{name: "unknown audio", parts: []model.Part{model.AudioPart("audio/midi", []byte("x"))}, want: "unsupported audio type"},
		{name: "citations", parts: []model.Part{model.TextPart("x"), citedXLSX}, want: "citations need a PDF or text document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.build(&model.Request{Messages: []*model.Message{model.NewMessage(model.RoleUser, tc.parts...)}}, "x", false)
			if err == nil || !strings.Contains(err.Error(), tc.want) || model.IsRetryable(err) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	// Media of other providers' answers are not sent.
	got := buildJSON(t, m, &model.Request{Messages: []*model.Message{model.UserText("q"), model.NewMessage(model.RoleAssistant, model.TextPart("a"), model.AudioPart("audio/wav", []byte("RIFF")))}}, "x", false)
	if strings.Contains(string(got["messages"]), "audio") {
		t.Fatalf("messages = %s", got["messages"])
	}
}

func TestImageOutput(t *testing.T) {
	t.Parallel()
	m, _ := converseServer(t, `{"output":{"message":{"role":"assistant","content":[{"image":{"format":"png","source":{"bytes":"iVBORw0KGgo="}}}]}},"stopReason":"end_turn","usage":{}}`)
	resp, err := m.Generate(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("draw")}})
	if err != nil {
		t.Fatal(err)
	}
	if p := resp.Message.Parts[0]; p.Type != model.PartImage || p.MediaType != "image/png" || !bytes.Equal(p.Data, pngBytes[:8]) {
		t.Fatalf("part = %+v", p)
	}
	stream, _, err := collect(
		event("messageStart", `{"role":"assistant"}`),
		event("contentBlockStart", `{"contentBlockIndex":0,"start":{"image":{"format":"jpeg"}}}`),
		event("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"image":{"source":{"bytes":"AQI="}}}}`),
		event("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"image":{"source":{"bytes":"Aw=="}}}}`),
		event("contentBlockStop", `{"contentBlockIndex":0}`),
		event("messageStop", `{"stopReason":"end_turn"}`),
		event("metadata", `{"usage":{}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if p := stream.Message.Parts[0]; p.Type != model.PartImage || p.MediaType != "image/jpeg" || !bytes.Equal(p.Data, []byte{1, 2, 3}) {
		t.Fatalf("streamed part = %+v", p)
	}
}
