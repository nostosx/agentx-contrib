package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nostosx/agentx/model"
)

// recorder is a test server that records request bodies and headers and
// answers with a fixed payload.
type recorder struct {
	mu      sync.Mutex
	bodies  [][]byte
	headers []http.Header
}

func (rec *recorder) serve(t *testing.T, payload string) *Model {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b bytes.Buffer
		if _, err := b.ReadFrom(r.Body); err != nil {
			t.Error(err)
		}
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, b.Bytes())
		rec.headers = append(rec.headers, r.Header.Clone())
		rec.mu.Unlock()
		if strings.HasPrefix(payload, "data:") {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		if _, err := w.Write([]byte(payload)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	m, err := New(WithAPIKey("fixture"), WithModel("claude-sonnet-4-6"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithoutPromptCaching(), WithFallbacks(false))
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
	var got map[string]any
	if err := json.Unmarshal(rec.bodies[len(rec.bodies)-1], &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// jsonEqual fails unless got encodes to the same JSON as want.
func jsonEqual(t *testing.T, name string, got any, want string) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal([]byte(want), &v); err != nil {
		t.Fatalf("%s: bad fixture: %v", name, err)
	}
	w, _ := json.Marshal(v)
	if string(g) != string(w) {
		t.Errorf("%s =\n%s\nwant\n%s", name, g, w)
	}
}

var (
	pdf = []byte("%PDF-1.7\n")
	png = []byte("\x89PNG\r\n\x1a\n")
)

const messageOK = `{"id":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

func TestMediaBlocks(t *testing.T) {
	t.Parallel()
	var rec recorder
	m := rec.serve(t, messageOK)
	cited := model.TextDocumentPart("Notes", "The sky is blue.")
	cited.Citations = true
	citedPDF := model.DocumentPart("application/pdf", pdf, "report.pdf")
	citedPDF.Citations = true
	citedURL := model.DocumentURLPart("https://example.com/paper.pdf")
	citedURL.Citations = true
	citedFile := model.FilePart(model.PartDocument, "anthropic", "file_doc", "")
	citedFile.Citations = true
	req := &model.Request{Messages: []*model.Message{model.NewMessage(model.RoleUser,
		model.ImagePart("image/png", png),
		model.FilePart(model.PartImage, "", "file_img", ""),
		cited, citedPDF, citedURL, citedFile,
		model.TextPart("summarize"),
	)}}
	if _, err := m.Generate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, "content", rec.last(t)["messages"].([]any)[0].(map[string]any)["content"], `[
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},
		{"type":"image","source":{"type":"file","file_id":"file_img"}},
		{"type":"document","source":{"type":"text","media_type":"text/plain","data":"The sky is blue."},"title":"Notes","citations":{"enabled":true}},
		{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjcK"},"title":"report.pdf","citations":{"enabled":true}},
		{"type":"document","source":{"type":"url","url":"https://example.com/paper.pdf"},"citations":{"enabled":true}},
		{"type":"document","source":{"type":"file","file_id":"file_doc"},"citations":{"enabled":true}},
		{"type":"text","text":"summarize"}]`)
	// The Files API needs no beta header.
	if beta := rec.headers[0].Get("anthropic-beta"); beta != "" {
		t.Errorf("beta = %q", beta)
	}
}

func TestToolResultMedia(t *testing.T) {
	t.Parallel()
	var rec recorder
	m := rec.serve(t, messageOK)
	req := &model.Request{Messages: []*model.Message{
		model.NewMessage(model.RoleAssistant, model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "t1", Name: "read"}}, model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "t2", Name: "read"}}),
		model.ToolResults(
			&model.ToolResult{CallID: "t1", Content: "page", Parts: []model.Part{model.ImagePart("image/png", png), model.TextPart("caption"), model.DocumentPart("application/pdf", pdf, "a.pdf")}, IsError: true},
			&model.ToolResult{CallID: "t2", Content: "plain", Metadata: map[string]any{"bytes": 5}}),
	}}
	if _, err := m.Generate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, "content", rec.last(t)["messages"].([]any)[1].(map[string]any)["content"], `[
		{"type":"tool_result","tool_use_id":"t1","content":[
			{"type":"text","text":"page"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},
			{"type":"text","text":"caption"},
			{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjcK"},"title":"a.pdf"}],"is_error":true},
		{"type":"tool_result","tool_use_id":"t2","content":"plain"}]`)
}

func TestRejectsUnsupportedMedia(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	vertex, err := New(WithVertex("project", "us-east5"), WithTokenSource(func(context.Context) (string, error) { return "token", nil }))
	if err != nil {
		t.Fatal(err)
	}
	cited := model.TextDocumentPart("a", "x")
	cited.Citations = true
	format := &model.ResponseFormat{Schema: json.RawMessage(`{"type":"object"}`)}
	_, _, err = m.build(&model.Request{ResponseFormat: format, Messages: []*model.Message{model.ToolResults(&model.ToolResult{CallID: "c", Parts: []model.Part{cited}})}}, false)
	if err == nil || !strings.Contains(err.Error(), "cannot be combined with structured output") || model.IsRetryable(err) {
		t.Fatalf("citations with structured output: %v", err)
	}
	for _, tc := range []struct {
		name  string
		m     *Model
		parts []model.Part
		want  string
	}{
		{name: "audio", m: m, parts: []model.Part{model.AudioPart("audio/wav", []byte("RIFF"))}, want: "audio input is not supported"},
		{name: "video", m: m, parts: []model.Part{model.VideoURLPart("https://example.com/a.mp4")}, want: "video input is not supported"},
		{name: "docx", m: m, parts: []model.Part{model.DocumentPart("application/msword", []byte{0xd0, 0xcf}, "a.doc")}, want: "PDF or plain text"},
		{name: "foreign file", m: m, parts: []model.Part{model.FilePart(model.PartImage, "openai", "file-1", "")}, want: "of provider openai"},
		{name: "mixed citations", m: m, parts: []model.Part{cited, model.TextDocumentPart("b", "y")}, want: "citations must be enabled on all documents"},
		{name: "vertex URL", m: vertex, parts: []model.Part{model.ImageURLPart("https://example.com/a.png")}, want: "Vertex AI takes image data"},
		{name: "vertex file", m: vertex, parts: []model.Part{model.FilePart(model.PartDocument, "", "file_1", "")}, want: "Vertex AI takes document data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := tc.m.build(&model.Request{Messages: []*model.Message{model.NewMessage(model.RoleUser, tc.parts...)}}, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) || model.IsRetryable(err) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

const citation = `{"type":"char_location","cited_text":"The sky is blue.","document_index":0,"document_title":"Notes","start_char_index":0,"end_char_index":16}`

func TestCitations(t *testing.T) {
	t.Parallel()
	payload := `{"id":"m","content":[{"type":"text","text":"According to the notes, "},{"type":"text","text":"the sky is blue","citations":[` + citation + `]}],"stop_reason":"end_turn","usage":{}}`
	var rec recorder
	m := rec.serve(t, payload)
	resp, err := m.Generate(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("q")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "According to the notes, the sky is blue" || resp.Message.Parts[0].Raw != nil || resp.Message.Parts[1].Provider != providerName {
		t.Fatalf("parts = %+v", resp.Message.Parts)
	}
	// Citations are sent back with their text.
	if _, err := m.Generate(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("q"), resp.Message}}); err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, "assistant", rec.last(t)["messages"].([]any)[1].(map[string]any)["content"], `[
		{"type":"text","text":"According to the notes, "},
		{"type":"text","text":"the sky is blue","citations":[`+citation+`]}]`)
}

func TestStreamCitations(t *testing.T) {
	t.Parallel()
	events := []string{
		`{"type":"message_start","message":{"id":"m","model":"x","usage":{"input_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"","citations":[]}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"blue"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":` + citation + `}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"page_location","cited_text":"p","document_index":1,"start_page_number":1,"end_page_number":2}}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`{"type":"message_stop"}`,
	}
	payload := ""
	for _, e := range events {
		payload += "data: " + e + "\n\n"
	}
	var rec recorder
	m := rec.serve(t, payload)
	resp, err := model.Collect(m.Stream(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("q")}}))
	if err != nil {
		t.Fatal(err)
	}
	p := resp.Message.Parts[0]
	if len(resp.Message.Parts) != 1 || p.Text != "blue" || p.Provider != providerName {
		t.Fatalf("parts = %+v", resp.Message.Parts)
	}
	var cites []json.RawMessage
	if err := json.Unmarshal(citations(p), &cites); err != nil || len(cites) != 2 || string(cites[0]) != citation {
		t.Fatalf("citations = %s, %v", citations(p), err)
	}
}
