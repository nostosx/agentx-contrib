package document

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestTesseractProcessLimitsAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		max        int64
		timeout    time.Duration
		want       error
	}{
		{"text", "pages", 0, 0, nil}, {"output", "pages", 2, 0, ErrLimit}, {"failure", "failure", 0, 0, nil}, {"deadline", "sleep", 0, 30 * time.Millisecond, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			p := Tesseract{command: func(ctx context.Context, _ string, args ...string) *exec.Cmd {
				if strings.Join(args, " ") != "stdin stdout -l eng" {
					t.Fatalf("OCR arguments: %v", args)
				}
				cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPDFSubprocess$", "--")
				cmd.Env = append(os.Environ(), "AGENTX_DOCUMENT_HELPER="+tc.mode)
				return cmd
			}, MaxTextBytes: tc.max, Timeout: tc.timeout}
			docs, err := p.Parse(t.Context(), "scan.png", []byte("image"))
			if tc.name == "text" {
				if err != nil || len(docs) != 1 || docs[0].Source != "scan.png" {
					t.Fatalf("%+v %v", docs, err)
				}
				return
			}
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || strings.Contains(err.Error(), "private") {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestTesseractPreflight(t *testing.T) {
	if _, err := (Tesseract{Languages: "eng;echo"}).Parse(t.Context(), "x", []byte("image")); err == nil {
		t.Fatal("invalid language accepted")
	}
	if _, err := (Tesseract{}).Parse(t.Context(), "x", []byte("%PDF-1.4")); err == nil {
		t.Fatal("PDF passed directly to Tesseract")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (Tesseract{}).Parse(ctx, "x", []byte("image")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
