// Package document provides agentx RAG parsers for DOCX and PDF documents.
// DOCX uses the Go standard library. PDF calls an explicitly configurable
// pdftotext executable, without a shell. No converter is downloaded or
// installed automatically and no document is sent to a remote service.
package document

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// ErrNoText reports a document with no extractable text. A PDF containing
// only images needs OCR; a blank document may also produce this error.
var ErrNoText = errors.New("document: no extractable text")

// ErrLimit reports an input, expanded archive, extracted text or page limit.
var ErrLimit = errors.New("document: size or page limit exceeded")

// DefaultMaxBytes is the default maximum input size of either parser.
const DefaultMaxBytes int64 = 32 << 20

// DefaultMaxTextBytes limits extracted text independently of input size.
const DefaultMaxTextBytes int64 = 32 << 20

// MetaPage is a PDF document's 1-based page number.
const MetaPage = "page"

// MetaPageCount is the number of pages in the PDF.
const MetaPageCount = "page_count"

// MetaTextlessPages identifies PDF pages with no extractable text. Such
// pages may be blank, scanned images, or use unsupported font encodings.
const MetaTextlessPages = "textless_pages"

func limit(value, fallback int64) (int64, error) {
	if value < 0 {
		return 0, errors.New("document: limits must not be negative")
	}
	if value == 0 {
		return fallback, nil
	}
	return value, nil
}

func checkInput(ctx context.Context, data []byte, maxBytes int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if int64(len(data)) > maxBytes {
		return fmt.Errorf("%w: input exceeds %d bytes", ErrLimit, maxBytes)
	}
	return nil
}

// contextReader bounds cancellation latency while decoding an archive.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
