package openai

import (
	"errors"
	"strings"
	"testing"

	"github.com/nostosx/kitx/dataurl"

	"github.com/nostosx/agentx/model"
)

func TestInvalidInlineMedia(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		part model.Part
	}{
		{"image type", model.ImagePart("invalid", []byte("private-payload"))},
		{"document type", model.DocumentPart("invalid", []byte("private-payload"), "note.bin")},
		{"image URL type", model.ImageURLPart("data:invalid,private-payload")},
		{"document URL type", model.DocumentURLPart("data:invalid,private-payload")},
		{"image URL base64", model.ImageURLPart("data:image/png;base64,private-payload!")},
		{"document URL base64", model.DocumentURLPart("data:application/pdf;base64,private-payload!")},
		{"image URL parameter", model.ImageURLPart("data:image/png;name=%0D%0Aprivate-payload,YQ==")},
		{"document URL parameter", model.DocumentURLPart("data:application/pdf;name=%0D%0Aprivate-payload,YQ==")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, api := range []string{"chat", "responses"} {
				t.Run(api, func(t *testing.T) {
					var err error
					if api == "chat" {
						_, err = m.chatPart(tc.part, false)
					} else {
						_, err = m.inputPart(tc.part)
					}
					var permanent *model.PermanentError
					if !errors.Is(err, dataurl.ErrInvalid) || !errors.As(err, &permanent) || model.IsRetryable(err) {
						t.Fatalf("error = %v; want permanent invalid-input error", err)
					}
					if strings.Contains(err.Error(), "private-payload") {
						t.Fatalf("error exposes payload: %v", err)
					}
				})
			}
		})
	}
}

func TestInlineImageParameters(t *testing.T) {
	t.Parallel()
	m, err := New(WithAPIKey("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	part := model.ImagePart(`image/png; name="a;b.png"`, png)
	chat, err := m.chatPart(part, true)
	if err != nil {
		t.Fatal(err)
	}
	response, err := m.inputPart(part)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{chat.ImageURL.URL, response.ImageURL} {
		mediaType, data, err := dataurl.Decode(source)
		if err != nil || mediaType != part.MediaType || string(data) != string(png) {
			t.Fatalf("encoded image = %q, %q, %v", mediaType, data, err)
		}
	}
}
