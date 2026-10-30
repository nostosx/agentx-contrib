package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nostosx/agentx/rag"
)

// Tesseract extracts text from images using an application-installed binary.
// It accepts Tesseract/Leptonica image formats, not PDF. For scanned PDFs use
// PDFImages, which explicitly renders pages before applying this parser.
type Tesseract struct {
	Command                string
	Languages              string
	MaxBytes, MaxTextBytes int64
	Timeout                time.Duration
	command                func(context.Context, string, ...string) *exec.Cmd
}

var languageNames = regexp.MustCompile(`^[A-Za-z0-9_]+(\+[A-Za-z0-9_]+)*$`)

// Parse implements rag.Parser. Image input and UTF-8 text use stdin/stdout;
// command diagnostics are never copied into errors or document content.
func (p Tesseract) Parse(ctx context.Context, source string, data []byte) ([]*rag.Document, error) {
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
	if len(data) == 0 || bytes.HasPrefix(bytes.TrimSpace(data), []byte("%PDF-")) {
		return nil, errors.New("document: Tesseract requires image input, not PDF")
	}
	lang := p.Languages
	if lang == "" {
		lang = "eng"
	}
	if !languageNames.MatchString(lang) {
		return nil, errors.New("document: invalid OCR languages")
	}
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if timeout < 0 {
		return nil, errors.New("document: invalid OCR timeout")
	}
	processCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := p.command
	if command == nil {
		command = exec.CommandContext
	}
	executable := p.Command
	if executable == "" {
		executable = "tesseract"
	}
	cmd := command(processCtx, executable, "stdin", "stdout", "-l", lang)
	cmd.Stdin = bytes.NewReader(data)
	stdout := cappedBuffer{remaining: maxText, onLimit: cancel}
	stderr := cappedBuffer{remaining: 8192, onLimit: cancel}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("%w: OCR output", ErrLimit)
	}
	if processCtx.Err() != nil {
		return nil, fmt.Errorf("document: OCR: %w", processCtx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("document: tesseract failed: %w", err)
	}
	text := strings.TrimSpace(stdout.String())
	if !utf8.ValidString(text) {
		return nil, errors.New("document: OCR output is not UTF-8")
	}
	if text == "" {
		return nil, ErrNoText
	}
	return []*rag.Document{{ID: source, Source: source, Content: text, Metadata: map[string]any{rag.MetaFormat: "ocr", "languages": lang}}}, nil
}

var _ rag.Parser = Tesseract{}
