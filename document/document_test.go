package document

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nostosx/agentx/rag"
)

func archive(t *testing.T, entries ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for i := 0; i < len(entries); i += 2 {
		f, err := w.Create(entries[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(entries[i+1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const wordStart = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`
const wordEnd = `</w:body></w:document>`

func TestDOCXTextAndMetadata(t *testing.T) {
	xml := wordStart + `<w:p><w:r><w:t>Hello 世界</w:t><w:tab/><w:t>Go</w:t></w:r></w:p>` +
		`<w:del><w:r><w:t>deleted</w:t></w:r></w:del>` +
		`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>table</w:t><w:br w:type="page"/><w:t>next</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` + wordEnd
	docs, err := (DOCX{}).Parse(t.Context(), "guide.docx", archive(t, "word/document.xml", xml))
	if err != nil || len(docs) != 1 {
		t.Fatalf("Parse = %+v, %v", docs, err)
	}
	doc := docs[0]
	if doc.Content != "Hello 世界\tGo\ntable\nnext" || doc.Source != "guide.docx" || doc.ID != "guide.docx" || doc.Metadata[rag.MetaFormat] != "docx" || doc.Metadata["explicit_page_breaks"] != 1 {
		t.Fatalf("document = %+v", doc)
	}
	if _, ok := doc.Metadata[MetaPage]; ok {
		t.Fatal("DOCX invented rendered page numbers")
	}
}

func TestDOCXRejectsMalformedAndOversizedArchives(t *testing.T) {
	valid := archive(t, "word/document.xml", wordStart+`<w:p><w:r><w:t>text</w:t></w:r></w:p>`+wordEnd)
	for _, tc := range []struct {
		name   string
		parser DOCX
		data   []byte
		want   error
	}{
		{"input", DOCX{MaxBytes: 1}, valid, ErrLimit},
		{"expanded", DOCX{MaxArchiveBytes: 1}, valid, ErrLimit},
		{"text", DOCX{MaxTextBytes: 2}, valid, ErrLimit},
		{"entries", DOCX{MaxEntries: 1}, archive(t, "word/document.xml", wordStart+wordEnd, "extra", "x"), ErrLimit},
		{"empty", DOCX{}, archive(t, "word/document.xml", wordStart+wordEnd), ErrNoText},
		{"duplicate", DOCX{}, archive(t, "word/document.xml", wordStart+wordEnd, "word/document.xml", wordStart+wordEnd), nil},
		{"traversal", DOCX{}, archive(t, "../evil", "x", "word/document.xml", wordStart+wordEnd), nil},
		{"missing-main", DOCX{}, archive(t, "elsewhere", "x"), nil},
		{"malformed-xml", DOCX{}, archive(t, "word/document.xml", wordStart), nil},
		{"missing-body", DOCX{}, archive(t, "word/document.xml", "<x/>"), nil},
		{"deep-xml", DOCX{}, archive(t, "word/document.xml", wordStart+strings.Repeat("<x>", 300)+strings.Repeat("</x>", 300)+wordEnd), ErrLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.parser.Parse(t.Context(), "bad.docx", tc.data)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (DOCX{}).Parse(ctx, "cancelled.docx", valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled parse = %v", err)
	}
}

func TestDOCXMaximumArchiveLimit(t *testing.T) {
	t.Parallel()
	data := archive(t, "word/document.xml", wordStart+`<w:p><w:r><w:t>text</w:t></w:r></w:p>`+wordEnd)
	docs, err := (DOCX{MaxArchiveBytes: math.MaxInt64}).Parse(t.Context(), "guide.docx", data)
	if err != nil || len(docs) != 1 || docs[0].Content != "text" {
		t.Fatalf("maximum archive limit rejected valid input: docs=%v err=%v", docs, err)
	}
}

func TestPDFPageSplitLimits(t *testing.T) {
	t.Parallel()
	if _, err := pdfDocuments("many.pdf", strings.Repeat("\f", 1<<20), 2); !errors.Is(err, ErrLimit) {
		t.Fatalf("excessive page separators: %v", err)
	}
	docs, err := pdfDocuments("two.pdf", "first\fsecond\f", math.MaxInt)
	if err != nil || len(docs) != 2 || docs[1].Metadata[MetaPageCount] != 2 {
		t.Fatalf("maximum page limit: docs=%v err=%v", docs, err)
	}
}

func helperCommand(t *testing.T, mode string) func(context.Context, string, ...string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		if !reflect.DeepEqual(args[:8], []string{"-enc", "UTF-8", "-eol", "unix", "-f", "1", "-l", args[7]}) || args[len(args)-2] != "-" || args[len(args)-1] != "-" {
			t.Fatalf("unexpected converter arguments: %v", args)
		}
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPDFSubprocess$", "--")
		cmd.Env = append(os.Environ(), "AGENTX_DOCUMENT_HELPER="+mode)
		return cmd
	}
}

func TestPDFSubprocess(t *testing.T) {
	switch os.Getenv("AGENTX_DOCUMENT_HELPER") {
	case "pages":
		fmt.Print("First page\f\fThird page\f")
	case "empty":
		fmt.Print("\f\f")
	case "large":
		fmt.Print(strings.Repeat("x", 128<<10))
	case "sleep":
		time.Sleep(time.Hour)
	case "failure":
		fmt.Fprint(os.Stderr, "confidential source contents")
		os.Exit(1)
	default:
		return
	}
	os.Exit(0)
}

func TestPDFPageMetadataAndPartialText(t *testing.T) {
	p := PDF{command: helperCommand(t, "pages")}
	docs, err := p.Parse(t.Context(), "guide.pdf", []byte("%PDF-1.7\n"))
	if err != nil || len(docs) != 2 {
		t.Fatalf("Parse = %+v, %v", docs, err)
	}
	if docs[0].ID != "guide.pdf#page=1" || docs[1].ID != "guide.pdf#page=3" || docs[1].Metadata[MetaPage] != 3 || docs[1].Metadata[MetaPageCount] != 3 || !reflect.DeepEqual(docs[1].Metadata[MetaTextlessPages], []int{2}) {
		t.Fatalf("page metadata = %+v, %+v", docs[0], docs[1])
	}
}

func TestPDFLimitsCancellationAndDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		parser     PDF
		want       error
	}{
		{"input", "pages", PDF{MaxBytes: 1}, ErrLimit},
		{"pages", "pages", PDF{MaxPages: 1}, ErrLimit},
		{"output", "large", PDF{MaxTextBytes: 10}, ErrLimit},
		{"no-text", "empty", PDF{}, ErrNoText},
		{"deadline", "sleep", PDF{Timeout: 50 * time.Millisecond}, context.DeadlineExceeded},
		{"failure", "failure", PDF{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.parser
			p.command = helperCommand(t, tc.mode)
			_, err := p.Parse(t.Context(), "input.pdf", []byte("%PDF-1.7\n"))
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "confidential") {
				t.Fatal("converter diagnostics leaked into error")
			}
		})
	}
}

func TestPDFOCRFallbackIsExplicit(t *testing.T) {
	called := false
	p := PDF{command: helperCommand(t, "empty"), OCR: rag.ParserFunc(func(_ context.Context, source string, data []byte) ([]*rag.Document, error) {
		called = true
		return []*rag.Document{{ID: source, Source: source, Content: "OCR text"}}, nil
	})}
	docs, err := p.Parse(t.Context(), "scan.pdf", []byte("%PDF-1.7\n"))
	if err != nil || !called || len(docs) != 1 || docs[0].Content != "OCR text" {
		t.Fatalf("fallback = %+v, %v, called=%v", docs, err, called)
	}
}
