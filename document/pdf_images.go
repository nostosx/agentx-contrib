package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nostosx/agentx/rag"
)

// PDFImages rasterizes a scanned PDF with application-installed pdftoppm and
// passes each page to OCR. Use it as PDF.OCR for wholly textless PDFs.
// Rendering uses a private temporary directory removed before returning.
type PDFImages struct {
	Command                string
	OCR                    rag.Parser
	MaxBytes, MaxTextBytes int64
	MaxPages               int
	// MaxImagePixels bounds the longest page edge, defaults to 2048.
	MaxImagePixels int
	Timeout        time.Duration
}

// Parse implements rag.Parser, preserving original 1-based page numbers.
func (p PDFImages) Parse(ctx context.Context, source string, data []byte) ([]*rag.Document, error) {
	maxBytes, err := limit(p.MaxBytes, DefaultMaxBytes)
	if err != nil {
		return nil, err
	}
	maxText, err := limit(p.MaxTextBytes, DefaultMaxTextBytes)
	if err != nil {
		return nil, err
	}
	if err := checkInput(ctx, data, maxBytes); err != nil {
		return nil, err
	}
	if p.OCR == nil || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("%PDF-")) {
		return nil, errors.New("document: PDFImages requires PDF input and an OCR parser")
	}
	pages, pixels := p.MaxPages, p.MaxImagePixels
	if pages == 0 {
		pages = 100
	}
	if pixels == 0 {
		pixels = 2048
	}
	if pages < 1 || pages > 1000 || pixels < 1 || pixels > 4096 {
		return nil, errors.New("document: invalid PDF OCR page or resolution limit")
	}
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	if timeout < 0 {
		return nil, errors.New("document: invalid PDF OCR timeout")
	}
	processCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "agentx-pdf-ocr-")
	if err != nil {
		return nil, fmt.Errorf("document: OCR temporary directory: %w", err)
	}
	defer os.RemoveAll(dir)
	executable := p.Command
	if executable == "" {
		executable = "pdftoppm"
	}
	cmd := exec.CommandContext(processCtx, executable, "-f", "1", "-l", strconv.Itoa(pages+1), "-scale-to", strconv.Itoa(pixels), "-png", "-", filepath.Join(dir, "page"))
	cmd.Stdin = bytes.NewReader(data)
	stdout := cappedBuffer{remaining: 8192, onLimit: cancel}
	stderr := cappedBuffer{remaining: 8192, onLimit: cancel}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("%w: PDF renderer output", ErrLimit)
	}
	if processCtx.Err() != nil {
		return nil, processCtx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("document: pdftoppm failed: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("document: rendered pages: %w", err)
	}
	if len(entries) == 0 {
		return nil, ErrNoText
	}
	if len(entries) > pages {
		return nil, fmt.Errorf("%w: PDF OCR pages", ErrLimit)
	}
	type pageFile struct {
		n    int
		name string
	}
	files := make([]pageFile, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "page-"), ".png"))
		if err != nil || !strings.HasPrefix(name, "page-") || !strings.HasSuffix(name, ".png") || entry.Type() != 0 || n < 1 || n > pages {
			return nil, errors.New("document: unexpected rendered PDF page")
		}
		files = append(files, pageFile{n, name})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].n < files[j].n })
	var docs []*rag.Document
	var total int64
	for i, file := range files {
		if file.n != i+1 {
			return nil, errors.New("document: missing rendered PDF page")
		}
		if err := processCtx.Err(); err != nil {
			return nil, err
		}
		f, err := os.Open(filepath.Join(dir, file.name))
		if err != nil {
			return nil, fmt.Errorf("document: open rendered page: %w", err)
		}
		image, readErr := io.ReadAll(io.LimitReader(f, DefaultMaxBytes+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.New("document: read rendered page")
		}
		if int64(len(image)) > DefaultMaxBytes {
			return nil, fmt.Errorf("%w: rendered page", ErrLimit)
		}
		parsed, err := p.OCR.Parse(processCtx, source, image)
		if errors.Is(err, ErrNoText) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var text strings.Builder
		for _, doc := range parsed {
			if doc != nil {
				if int64(text.Len())+int64(len(doc.Content))+1 > maxText-total {
					return nil, fmt.Errorf("%w: PDF OCR text", ErrLimit)
				}
				text.WriteString(doc.Content)
				text.WriteByte('\n')
			}
		}
		content := strings.TrimSpace(text.String())
		if content == "" {
			continue
		}
		total += int64(text.Len())
		docs = append(docs, &rag.Document{ID: source + "#page=" + strconv.Itoa(file.n), Source: source, Content: content, Metadata: map[string]any{rag.MetaFormat: "pdf_ocr", MetaPage: file.n, MetaPageCount: len(files)}})
	}
	if len(docs) == 0 {
		return nil, ErrNoText
	}
	return docs, nil
}

var _ rag.Parser = PDFImages{}
