package document

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strings"

	"github.com/nostosx/agentx/rag"
)

// DOCX extracts the main WordprocessingML document, including paragraphs
// inside tables. Headers, footers, comments, embedded objects and drawings
// are not read. The parser never follows external relationships.
type DOCX struct {
	MaxBytes, MaxTextBytes int64
	// MaxArchiveBytes bounds the sum of declared expanded ZIP entries,
	// and the bytes actually read from the main document. Default: 128 MiB.
	MaxArchiveBytes int64
	// MaxEntries bounds ZIP directory entries. Default: 4096.
	MaxEntries int
}

var _ rag.Parser = DOCX{}

// Parse returns the main document's text and source metadata. DOCX has no
// stable page numbers without a layout engine, so page numbers are not
// invented; explicit page breaks are counted in metadata instead.
func (p DOCX) Parse(ctx context.Context, source string, data []byte) ([]*rag.Document, error) {
	maxBytes, err := limit(p.MaxBytes, DefaultMaxBytes)
	if err != nil {
		return nil, err
	}
	maxText, err := limit(p.MaxTextBytes, DefaultMaxTextBytes)
	if err != nil {
		return nil, err
	}
	maxArchive, err := limit(p.MaxArchiveBytes, 128<<20)
	if err != nil {
		return nil, err
	}
	if err := checkInput(ctx, data, maxBytes); err != nil {
		return nil, err
	}
	maxEntries := p.MaxEntries
	if maxEntries == 0 {
		maxEntries = 4096
	}
	if maxEntries < 1 {
		return nil, errors.New("document: MaxEntries must be positive")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("document: open DOCX archive: %w", err)
	}
	if len(zr.File) > maxEntries {
		return nil, fmt.Errorf("%w: DOCX archive entries", ErrLimit)
	}
	var expanded uint64
	var main *zip.File
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if path.IsAbs(f.Name) || strings.Contains(f.Name, `\`) || strings.ContainsRune(f.Name, 0) ||
			strings.HasPrefix(path.Clean(f.Name), "../") || path.Clean(f.Name) == ".." {
			return nil, errors.New("document: unsafe DOCX archive entry")
		}
		if f.UncompressedSize64 > uint64(maxArchive)-expanded {
			return nil, fmt.Errorf("%w: DOCX expanded archive", ErrLimit)
		}
		expanded += f.UncompressedSize64
		if f.Name == "word/document.xml" {
			if main != nil {
				return nil, errors.New("document: duplicate DOCX main document")
			}
			main = f
		}
	}
	if main == nil {
		return nil, errors.New("document: DOCX main document is missing")
	}
	xmlData, err := readPart(ctx, main, maxArchive)
	if err != nil {
		return nil, err
	}
	content, breaks, err := wordText(ctx, xmlData, maxText)
	if err != nil {
		return nil, err
	}
	return []*rag.Document{{ID: source, Source: source, Content: content, Metadata: map[string]any{
		rag.MetaFormat: "docx", "explicit_page_breaks": breaks,
	}}}, nil
}

func readPart(ctx context.Context, part *zip.File, maxBytes int64) ([]byte, error) {
	r, err := part.Open()
	if err != nil {
		return nil, fmt.Errorf("document: open DOCX main document: %w", err)
	}
	readLimit := maxBytes
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	data, readErr := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, r: r}, readLimit))
	closeErr := r.Close()
	if readErr != nil {
		return nil, fmt.Errorf("document: read DOCX main document: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("document: close DOCX main document: %w", closeErr)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: DOCX main document", ErrLimit)
	}
	return data, nil
}

func isWord(name xml.Name) bool {
	return name.Space == "http://schemas.openxmlformats.org/wordprocessingml/2006/main" ||
		name.Space == "http://purl.oclc.org/ooxml/wordprocessingml/main"
}

func wordText(ctx context.Context, data []byte, maxText int64) (string, int, error) {
	d := xml.NewDecoder(contextReader{ctx: ctx, r: bytes.NewReader(data)})
	var text strings.Builder
	depth, body, textDepth, deleted, breaks := 0, 0, 0, 0, 0
	bodySeen := false
	write := func(value string) error {
		if int64(text.Len())+int64(len(value)) > maxText {
			return fmt.Errorf("%w: DOCX extracted text", ErrLimit)
		}
		text.WriteString(value)
		return nil
	}
	for {
		token, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", 0, fmt.Errorf("document: decode DOCX XML: %w", err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if depth > 256 {
				return "", 0, fmt.Errorf("%w: DOCX XML nesting", ErrLimit)
			}
			if !isWord(token.Name) {
				continue
			}
			if token.Name.Local == "body" {
				body, bodySeen = depth, true
			}
			if body == 0 {
				continue
			}
			if token.Name.Local == "del" {
				deleted++
			}
			if deleted > 0 {
				continue
			}
			switch token.Name.Local {
			case "t":
				textDepth = depth
			case "tab":
				if err := write("\t"); err != nil {
					return "", 0, err
				}
			case "br", "cr":
				for _, attr := range token.Attr {
					if isWord(attr.Name) && attr.Name.Local == "type" && attr.Value == "page" {
						breaks++
					}
				}
				if err := write("\n"); err != nil {
					return "", 0, err
				}
			}
		case xml.EndElement:
			if isWord(token.Name) {
				if token.Name.Local == "p" && body > 0 && deleted == 0 {
					if err := write("\n"); err != nil {
						return "", 0, err
					}
				}
				if token.Name.Local == "del" && body > 0 {
					deleted--
				}
				if depth == textDepth {
					textDepth = 0
				}
				if depth == body {
					body = 0
				}
			}
			depth--
		case xml.CharData:
			if textDepth > 0 && deleted == 0 {
				if err := write(string(token)); err != nil {
					return "", 0, err
				}
			}
		}
	}
	if !bodySeen {
		return "", 0, errors.New("document: DOCX body is missing")
	}
	content := strings.TrimSpace(text.String())
	if content == "" {
		return "", 0, ErrNoText
	}
	return content, breaks, nil
}
