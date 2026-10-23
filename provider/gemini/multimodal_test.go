package gemini

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nostosx/agentx/model"
)

// recorder is a test server that records request bodies and answers with
// a fixed payload.
type recorder struct {
	mu     sync.Mutex
	bodies [][]byte
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
		rec.mu.Unlock()
		if strings.HasPrefix(payload, "data:") {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		if _, err := w.Write([]byte(payload)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	m, err := New(WithAPIKey("fixture"), WithBaseURL(srv.URL+"/v1beta"), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (rec *recorder) contents(t *testing.T) any {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var got map[string]any
	if err := json.Unmarshal(rec.bodies[len(rec.bodies)-1], &got); err != nil {
		t.Fatal(err)
	}
	return got["contents"]
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

const contentOK = `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`

func TestMediaParts(t *testing.T) {
	t.Parallel()
	var rec recorder
	m := rec.serve(t, contentOK)
	high := model.ImagePart("image/png", png)
	high.Detail = "high"
	clip := model.VideoURLPart("https://www.youtube.com/watch?v=abc")
	clip.Detail = "low"
	req := &model.Request{Model: "gemini-3-flash", Messages: []*model.Message{model.NewMessage(model.RoleUser,
		high,
		model.DocumentPart("application/pdf", pdf, "a.pdf"),
		model.TextDocumentPart("notes", "plain"),
		model.DocumentURLPart("https://generativelanguage.googleapis.com/v1beta/files/abc.pdf"),
		model.AudioPart("audio/mpeg", []byte("ID3")),
		model.AudioURLPart("gs://bucket/talk.flac"),
		model.VideoPart("video/mp4", []byte("mp4")),
		clip,
		model.FilePart(model.PartVideo, "", "https://generativelanguage.googleapis.com/v1beta/files/xyz", "video/webm"),
	)}}
	if _, err := m.Generate(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, "contents", rec.contents(t), `[{"role":"user","parts":[
		{"mediaResolution":{"level":"MEDIA_RESOLUTION_HIGH"},"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}},
		{"inlineData":{"mimeType":"application/pdf","data":"JVBERi0xLjcK"}},
		{"inlineData":{"mimeType":"text/plain","data":"cGxhaW4="}},
		{"fileData":{"mimeType":"application/pdf","fileUri":"https://generativelanguage.googleapis.com/v1beta/files/abc.pdf"}},
		{"inlineData":{"mimeType":"audio/mpeg","data":"SUQz"}},
		{"fileData":{"mimeType":"audio/flac","fileUri":"gs://bucket/talk.flac"}},
		{"inlineData":{"mimeType":"video/mp4","data":"bXA0"}},
		{"mediaResolution":{"level":"MEDIA_RESOLUTION_LOW"},"fileData":{"fileUri":"https://www.youtube.com/watch?v=abc"}},
		{"fileData":{"mimeType":"video/webm","fileUri":"https://generativelanguage.googleapis.com/v1beta/files/xyz"}}]}]`)
}

func TestMediaErrors(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	low := model.ImagePart("image/png", png)
	low.Detail = "low"
	for _, tc := range []struct {
		name, model string
		part        model.Part
		want        string
	}{
		{name: "detail on 2.5", model: "gemini-2.5-flash", part: low, want: "needs Gemini 3"},
		{name: "file name", model: "gemini-3-flash", part: model.FilePart(model.PartDocument, "", "files/abc", ""), want: "by its URI"},
		{name: "foreign file", model: "gemini-3-flash", part: model.FilePart(model.PartDocument, "openai", "file-1", ""), want: "of provider openai"},
		{name: "empty", model: "gemini-3-flash", part: model.Part{Type: model.PartAudio}, want: "has no data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := m.build(&model.Request{Model: tc.model, Messages: []*model.Message{model.NewMessage(model.RoleUser, tc.part)}})
			if err == nil || !strings.Contains(err.Error(), tc.want) || model.IsRetryable(err) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

// toolTurn is a call and its result with media.
func toolTurn(parts ...model.Part) []*model.Message {
	return []*model.Message{
		model.NewMessage(model.RoleAssistant, model.Part{Type: model.PartToolCall, ToolCall: &model.ToolCall{ID: "c1", Name: "shot"}}),
		model.ToolResults(&model.ToolResult{CallID: "c1", Name: "shot", Content: `{"status":"ok"}`, Parts: parts}),
	}
}

func TestMultimodalFunctionResponse(t *testing.T) {
	t.Parallel()
	var rec recorder
	m := rec.serve(t, contentOK)
	msgs := toolTurn(model.ImagePart("image/png", png), model.DocumentPart("application/pdf", pdf, "a.pdf"), model.AudioPart("audio/wav", []byte("RIFF")))
	if _, err := m.Generate(t.Context(), &model.Request{Model: "gemini-3-flash", Messages: msgs}); err != nil {
		t.Fatal(err)
	}
	// Gemini 3 takes images and PDFs in function responses, referenced
	// from the response; audio follows the response.
	jsonEqual(t, "result turn", rec.contents(t).([]any)[1], `{"role":"user","parts":[
		{"functionResponse":{"id":"c1","name":"shot",
			"response":{"result":{"status":"ok"},"part_1":{"$ref":"part_1_file.png"},"part_2":{"$ref":"a.pdf"}},
			"parts":[
				{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo=","displayName":"part_1_file.png"}},
				{"inlineData":{"mimeType":"application/pdf","data":"JVBERi0xLjcK","displayName":"a.pdf"}}]}},
		{"text":"[Content returned by tool call c1 (shot)]"},
		{"inlineData":{"mimeType":"audio/wav","data":"UklGRg=="}}]}`)

	// Older models get every part after the responses.
	if _, err := m.Generate(t.Context(), &model.Request{Model: "gemini-2.5-flash", Messages: toolTurn(model.ImagePart("image/png", png))}); err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, "older model", rec.contents(t).([]any)[1], `{"role":"user","parts":[
		{"functionResponse":{"id":"c1","name":"shot","response":{"result":{"status":"ok"}}}},
		{"text":"[Content returned by tool call c1 (shot)]"},
		{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}}]}`)
}

func TestFunctionResponseFilesOnVertex(t *testing.T) {
	t.Parallel()
	msgs := toolTurn(model.Part{Type: model.PartImage, URL: "gs://bucket/a.png"}, model.Part{Type: model.PartImage, URL: "gs://bucket/b.png", Filename: "dup"}, model.Part{Type: model.PartImage, URL: "gs://bucket/c.png", Filename: "dup"})
	vertex, err := (&converter{gemini3: true, vertex: true}).messages(msgs)
	if err != nil {
		t.Fatal(err)
	}
	fr := vertex[1].Parts[0].FunctionResponse
	if len(fr.Parts) != 3 || fr.Parts[0].FileData == nil || fr.Parts[0].FileData.FileURI != "gs://bucket/a.png" || fr.Parts[1].FileData.DisplayName != "dup" || fr.Parts[2].FileData.DisplayName == "dup" {
		t.Fatalf("vertex response = %+v", fr)
	}
	// The Gemini API takes only inline data in function responses.
	api, err := (&converter{gemini3: true}).messages(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(api[1].Parts[0].FunctionResponse.Parts) != 0 || len(api[1].Parts) != 5 {
		t.Fatalf("api parts = %+v", api[1].Parts)
	}
}

func TestMediaOutput(t *testing.T) {
	t.Parallel()
	wav := base64.StdEncoding.EncodeToString([]byte("RIFFxxxxWAVE"))
	payload := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/wav","data":"` + wav + `"}},{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="},"thoughtSignature":"sig"}]},"finishReason":"STOP"}]}`
	var rec recorder
	m := rec.serve(t, payload)
	resp, err := m.Generate(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("speak")}})
	if err != nil {
		t.Fatal(err)
	}
	parts := resp.Message.Parts
	if len(parts) != 2 || parts[0].Type != model.PartAudio || parts[0].MediaType != "audio/wav" || parts[1].Type != model.PartImage || parts[1].Signature != "sig" {
		t.Fatalf("parts = %+v", parts)
	}
	// Output media go back as they came, with their signatures.
	if _, err := m.Generate(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("speak"), resp.Message}}); err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, "replay", rec.contents(t).([]any)[1], `{"role":"model","parts":[
		{"inlineData":{"mimeType":"audio/wav","data":"`+wav+`"}},
		{"thoughtSignature":"sig","inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}}]}`)
}

func TestStreamPCMAudio(t *testing.T) {
	t.Parallel()
	chunk := func(data string) string {
		return `data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/L16;codec=pcm;rate=24000","data":"` + data + `"}}]}}]}` + "\n\n"
	}
	payload := chunk("AQI=") + chunk("AwQ=") + `data: {"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}` + "\n\n"
	var rec recorder
	m := rec.serve(t, payload)
	resp, err := model.Collect(m.Stream(t.Context(), &model.Request{Messages: []*model.Message{model.UserText("speak")}}))
	if err != nil {
		t.Fatal(err)
	}
	parts := resp.Message.Parts
	if len(parts) != 2 || parts[0].Type != model.PartAudio || !bytes.Equal(parts[0].Data, []byte{1, 2, 3, 4}) || parts[0].MediaType != "audio/L16;codec=pcm;rate=24000" || parts[1].Text != "done" {
		t.Fatalf("parts = %+v", parts)
	}
}
