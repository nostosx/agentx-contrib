package utils_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/nostosx/kitx/dataurl"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

func TestExtensionAndBaseType(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"a.PDF": "application/pdf", "dir/b.jpeg": "image/jpeg", "https://x.test/v.mp4?sig=1#t": "video/mp4",
		"s3://bucket/clip.mov": "video/quicktime", "a.unknown": "", "noext": "", "talk.m4a": "audio/mp4",
	} {
		if got := utils.ExtensionType(name); got != want {
			t.Errorf("ExtensionType(%q) = %q, want %q", name, got, want)
		}
	}
	if got := utils.BaseType(" Audio/L16; rate=24000"); got != "audio/l16" {
		t.Errorf("BaseType = %q", got)
	}
}

func TestInlineDataAndMediaType(t *testing.T) {
	t.Parallel()
	png := []byte("\x89PNG\r\n\x1a\n")
	for _, tc := range []struct {
		name   string
		part   model.Part
		inline bool
		mt     string
	}{
		{name: "data", part: model.Part{Type: model.PartImage, Data: png, MediaType: "Image/PNG"}, inline: true, mt: "image/png"},
		{name: "sniffed", part: model.Part{Type: model.PartImage, Data: png}, inline: true, mt: "image/png"},
		{name: "data URL", part: model.ImageURLPart("data:image/gif;base64,R0lGODlh"), inline: true, mt: "image/gif"},
		{name: "parameterized URL", part: model.ImageURLPart("data:image/svg+xml;charset=utf-8;base64,PHN2Zy8+"), inline: true, mt: "image/svg+xml"},
		{name: "invalid URL media type", part: model.ImageURLPart("data:invalid;base64,YQ==")},
		{name: "invalid URL parameter", part: model.ImageURLPart("data:image/png;name=%0D%0Aprivate,YQ==")},

		{name: "URL", part: model.DocumentURLPart("https://example.com/a.pdf"), mt: "application/pdf"},
		{name: "file name", part: model.Part{Type: model.PartDocument, FileID: "f", Filename: "a.csv"}, mt: "text/csv"},
		{name: "unknown", part: model.Part{Type: model.PartDocument, FileID: "f"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mt, data, ok := utils.InlineData(tc.part)
			if ok != tc.inline || (ok && (mt != tc.mt || len(data) == 0)) {
				t.Errorf("InlineData = %q, %d bytes, %v", mt, len(data), ok)
			}
			if got := utils.MediaType(tc.part); got != tc.mt {
				t.Errorf("MediaType = %q, want %q", got, tc.mt)
			}
		})
	}
}

func TestFilename(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		part model.Part
		want string
	}{
		{model.Part{Filename: "given.pdf", URL: "https://x.test/other.pdf"}, "given.pdf"},
		{model.Part{URL: "https://x.test/dir/paper.pdf?x=1"}, "paper.pdf"},
		{model.Part{URL: "https://x.test/download"}, "file"},
		{model.Part{MediaType: "application/pdf", Data: []byte("%PDF")}, "file.pdf"},
		{model.Part{URL: "data:text/csv;base64,YQ=="}, "file.csv"},
		{model.Part{MediaType: "application/zip"}, "file"},
	} {
		if got := utils.Filename(tc.part); got != tc.want {
			t.Errorf("Filename(%+v) = %q, want %q", tc.part, got, tc.want)
		}
	}
}

func TestCheckMedia(t *testing.T) {
	t.Parallel()
	ok := []model.Part{
		model.ImagePart("image/png", []byte{1}),
		model.ImageURLPart("DATA:image/png;base64,YQ=="),
		{Type: model.PartImage, Data: []byte{1}, URL: "data:invalid,unused"},
		model.FilePart(model.PartDocument, "", "f", ""),
		model.FilePart(model.PartDocument, "openai", "f", ""),
		model.FilePart(model.PartDocument, "azure", "f", ""),
		{Type: model.PartImage, URL: "https://x.test/a.png", Provider: "gemini"},
	}
	for _, p := range ok {
		if err := utils.CheckMedia("openai", p, "azure"); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	for _, p := range []model.Part{{Type: model.PartImage}, model.FilePart(model.PartDocument, "anthropic", "f", "")} {
		err := utils.CheckMedia("openai", p)
		if err == nil || model.IsRetryable(err) || !strings.HasPrefix(err.Error(), "openai: ") {
			t.Errorf("%+v: error = %v", p, err)
		}
	}
}

func TestCheckMediaRejectsInvalidDataURL(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"openai", "anthropic", "gemini", "bedrock"} {
		t.Run(provider, func(t *testing.T) {
			part := model.ImageURLPart(" DATA:image/png;name=%0D%0Aprivate,private-payload ")
			err := utils.CheckMedia(provider, part)
			var permanent *model.PermanentError
			if !errors.Is(err, dataurl.ErrInvalid) || !errors.As(err, &permanent) || model.IsRetryable(err) {
				t.Fatalf("error = %v; want permanent invalid-input error", err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatalf("error exposes input: %v", err)
			}
		})
	}
}
