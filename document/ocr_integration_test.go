//go:build integration

package document_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nostosx/agentx-contrib/document"
)

func TestIntegrationOCR(t *testing.T) {
	ocr, renderer := os.Getenv("AGENTX_IT_TESSERACT"), os.Getenv("AGENTX_IT_PDFTOPPM")
	if ocr == "" || renderer == "" {
		t.Skip("set AGENTX_IT_TESSERACT and AGENTX_IT_PDFTOPPM for real OCR acceptance")
	}
	p := document.PDFImages{Command: renderer, OCR: document.Tesseract{Command: ocr}, MaxPages: 3}
	data := pdf("AGENTX OCR ACCEPTANCE", "SECOND PAGE")
	docs, err := p.Parse(t.Context(), "scan.pdf", data)
	if err != nil || len(docs) != 2 || !strings.Contains(strings.ToUpper(docs[0].Content), "AGENTX OCR ACCEPTANCE") || docs[1].Metadata[document.MetaPage] != 2 || docs[1].Metadata[document.MetaPageCount] != 2 {
		t.Fatalf("OCR: %+v %v", docs, err)
	}
	p.MaxPages = 1
	if _, err := p.Parse(t.Context(), "scan.pdf", data); !errors.Is(err, document.ErrLimit) {
		t.Fatalf("page limit: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.Parse(ctx, "scan.pdf", data); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
