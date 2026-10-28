package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nostosx/agentx/rag"
)

// PDF extracts one document per page through Poppler's pdftotext command.
// The zero value uses pdftotext from PATH, a 30-second deadline, at most
// 1000 pages, and the package's default input and text limits.
type PDF struct {
	// Command is a trusted executable path or name. It is never a shell
	// fragment. The application installs and maintains Poppler separately.
	Command                string
	MaxBytes, MaxTextBytes int64
	MaxPages               int
	Timeout                time.Duration
	// OCR optionally handles a document only when no page has extractable
	// text. It receives the original bytes. Its implementation owns its
	// own resource limits and must preserve source/page metadata as needed.
	OCR rag.Parser
	// command is a process seam used by platform-independent tests.
	command func(context.Context, string, ...string) *exec.Cmd
}

var _ rag.Parser = PDF{}

// Parse extracts text without writing temporary PDF or text files. A
// wholly textless document returns ErrNoText unless OCR is configured.
// Partial text extraction lists all textless pages in every document's
// metadata, rather than silently claiming those pages were extracted.
func (p PDF) Parse(ctx context.Context, source string, data []byte) ([]*rag.Document, error) {
	maxBytes, err := limit(p.MaxBytes, DefaultMaxBytes)
	if err != nil {
		return nil, err
	}
	maxTextBytes, err := limit(p.MaxTextBytes, DefaultMaxTextBytes)
	if err != nil {
		return nil, err
	}
	if err := checkInput(ctx, data, maxBytes); err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("%PDF-")) {
		return nil, errors.New("document: PDF header is missing")
	}
	pages := p.MaxPages
	if pages == 0 {
		pages = 1000
	}
	if pages < 1 || pages > 1_000_000 {
		return nil, errors.New("document: MaxPages must be between 1 and 1000000")
	}
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if timeout < 0 {
		return nil, errors.New("document: Timeout must not be negative")
	}
	processCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := p.command
	if command == nil {
		command = exec.CommandContext
	}
	executable := p.Command
	if executable == "" {
		executable = "pdftotext"
	}
	// Extract one extra page to detect, rather than silently truncate, a
	// document exceeding MaxPages. Form feeds retain original page numbers.
	cmd := command(processCtx, executable, "-enc", "UTF-8", "-eol", "unix", "-f", "1", "-l", strconv.Itoa(pages+1), "-", "-")
	cmd.Stdin = bytes.NewReader(data)
	stdout := cappedBuffer{remaining: maxTextBytes, onLimit: cancel}
	stderr := cappedBuffer{remaining: 8192, onLimit: cancel}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("%w: PDF converter output", ErrLimit)
	}
	if processCtx.Err() != nil {
		return nil, fmt.Errorf("document: PDF extraction: %w", processCtx.Err())
	}
	if err != nil {
		// Converter diagnostics are deliberately not copied into errors:
		// malformed documents can make external tools echo source content.
		return nil, fmt.Errorf("document: pdftotext failed: %w", err)
	}
	docs, err := pdfDocuments(source, stdout.String(), pages)
	if errors.Is(err, ErrNoText) && p.OCR != nil {
		return p.OCR.Parse(ctx, source, data)
	}
	return docs, err
}

type cappedBuffer struct {
	buffer    bytes.Buffer
	remaining int64
	exceeded  bool
	onLimit   context.CancelFunc
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	if int64(len(data)) > b.remaining {
		b.exceeded = true
		if b.onLimit != nil {
			b.onLimit()
		}
		return 0, ErrLimit
	}
	b.remaining -= int64(len(data))
	return b.buffer.Write(data)
}

func (b *cappedBuffer) String() string { return b.buffer.String() }

func pdfDocuments(source, text string, maxPages int) ([]*rag.Document, error) {
	// Keep a possible trailing page separator plus one excess page without
	// allocating a slice proportional to untrusted converter output.
	splitLimit := math.MaxInt
	if maxPages <= math.MaxInt-2 {
		splitLimit = maxPages + 2
	}
	pages := strings.SplitN(text, "\f", splitLimit)
	if len(pages) > 1 && strings.TrimSpace(pages[len(pages)-1]) == "" {
		pages = pages[:len(pages)-1]
	}
	if len(pages) > maxPages {
		return nil, fmt.Errorf("%w: PDF exceeds %d pages", ErrLimit, maxPages)
	}
	var textless []int
	var docs []*rag.Document
	for i, page := range pages {
		content := strings.TrimSpace(page)
		if content == "" {
			textless = append(textless, i+1)
			continue
		}
		docs = append(docs, &rag.Document{ID: source + "#page=" + strconv.Itoa(i+1), Source: source, Content: content,
			Metadata: map[string]any{rag.MetaFormat: "pdf", MetaPage: i + 1, MetaPageCount: len(pages)}})
	}
	if len(docs) == 0 {
		return nil, ErrNoText
	}
	if len(textless) > 0 {
		for _, doc := range docs {
			doc.Metadata[MetaTextlessPages] = append([]int(nil), textless...)
		}
	}
	return docs, nil
}
