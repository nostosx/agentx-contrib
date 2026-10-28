//go:build integration

package document_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/nostosx/agentx-contrib/document"
)

func TestIntegrationPDFConversion(t *testing.T) {
	executable := os.Getenv("AGENTX_IT_PDFTOTEXT")
	if executable == "" {
		t.Skip("set AGENTX_IT_PDFTOTEXT to the pdftotext executable for real converter acceptance")
	}
	p := document.PDF{Command: executable}
	docs, err := p.Parse(t.Context(), "manual.pdf", pdf("First page", "", "Third page"))
	if err != nil || len(docs) != 2 || !strings.Contains(docs[0].Content, "First page") || !strings.Contains(docs[1].Content, "Third page") || docs[1].Metadata[document.MetaPage] != 3 {
		t.Fatalf("real PDF extraction = %+v, %v", docs, err)
	}
	if _, err := p.Parse(t.Context(), "image-only.pdf", pdf("")); !errors.Is(err, document.ErrNoText) {
		t.Fatalf("textless PDF = %v, want ErrNoText", err)
	}
	p.MaxPages = 1
	if _, err := p.Parse(t.Context(), "large.pdf", pdf("First", "Second")); !errors.Is(err, document.ErrLimit) {
		t.Fatalf("page limit = %v, want ErrLimit", err)
	}
}

// pdf creates a tiny, valid, uncompressed fixture with one text string per
// page. Empty strings create a page without any text; no network is used.
func pdf(pages ...string) []byte {
	var kids []string
	for i := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", 4+i*2))
	}
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	for i, text := range pages {
		stream := ""
		if text != "" {
			stream = "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET\n"
		}
		objects = append(objects,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 5+i*2),
			fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	var offsets []int
	for i, object := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}
