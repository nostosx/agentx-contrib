package document

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/nostosx/agentx/rag"
)

// XLSX extracts one tab-separated document per nonempty worksheet. Formulas
// are not evaluated; only cached cell values are read. No links or macros run.
// Hidden sheets are skipped unless IncludeHidden is explicitly enabled.
type XLSX struct {
	MaxBytes, MaxTextBytes int64
	// MaxArchiveBytes defaults to 128 MiB, MaxEntries to 4096.
	MaxArchiveBytes int64
	MaxEntries      int
	// MaxRows bounds worksheet positions, including gaps. Defaults to 100000.
	MaxRows int
	// MaxColumns defaults to 1024; MaxSheets defaults to 100.
	MaxColumns, MaxSheets int
	IncludeHidden         bool
}

// Parse implements rag.Parser. Numeric/date cells use their raw stored values;
// formatting, charts and images are not included in the output.
func (p XLSX) Parse(ctx context.Context, source string, data []byte) (docs []*rag.Document, err error) {
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
	if maxArchive > 256<<20 {
		return nil, errors.New("document: XLSX archive limit exceeds 256 MiB")
	}
	if err := checkInput(ctx, data, maxBytes); err != nil {
		return nil, err
	}
	entries, rows, cols, sheets := p.MaxEntries, p.MaxRows, p.MaxColumns, p.MaxSheets
	if entries == 0 {
		entries = 4096
	}
	if rows == 0 {
		rows = 100000
	}
	if cols == 0 {
		cols = 1024
	}
	if sheets == 0 {
		sheets = 100
	}
	if entries < 1 || rows < 1 || cols < 1 || cols > 16384 || sheets < 1 {
		return nil, errors.New("document: invalid XLSX limits")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("document: invalid XLSX archive")
	}
	if len(zr.File) > entries {
		return nil, fmt.Errorf("%w: XLSX archive entries", ErrLimit)
	}
	var expanded uint64
	seen := map[string]bool{}
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if seen[f.Name] {
			return nil, errors.New("document: duplicate XLSX archive entry")
		}
		seen[f.Name] = true
		if f.UncompressedSize64 > uint64(maxArchive)-expanded {
			return nil, fmt.Errorf("%w: XLSX expanded archive", ErrLimit)
		}
		expanded += f.UncompressedSize64
	}
	// Keep worksheet/shared-string XML in memory; the expanded archive is
	// bounded independently so Excelize never spills uploaded XML to disk.
	f, err := excelize.OpenReader(contextReader{ctx: ctx, r: bytes.NewReader(data)}, excelize.Options{RawCellValue: true, UnzipSizeLimit: maxArchive, UnzipXMLSizeLimit: maxArchive})
	if err != nil {
		return nil, errors.New("document: cannot open XLSX workbook")
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			docs = nil
			err = errors.Join(err, errors.New("document: close XLSX workbook"))
		}
	}()
	if len(f.GetSheetList()) > sheets {
		return nil, fmt.Errorf("%w: XLSX sheets", ErrLimit)
	}
	var total int64
	for _, sheet := range f.GetSheetList() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		visible, err := f.GetSheetVisible(sheet)
		if err != nil {
			return nil, errors.New("document: read XLSX sheet visibility")
		}
		if !visible && !p.IncludeHidden {
			continue
		}
		iterator, err := f.Rows(sheet)
		if err != nil {
			return nil, errors.New("document: read XLSX sheet")
		}
		text, count, readErr := xlsxRows(ctx, iterator, rows, cols, maxText-total)
		closeErr := iterator.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, errors.New("document: close XLSX rows")
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		total += int64(len(text))
		docs = append(docs, &rag.Document{ID: source + "#sheet=" + url.QueryEscape(sheet), Source: source, Content: text, Metadata: map[string]any{rag.MetaFormat: "xlsx", "sheet": sheet, "rows": count}})
	}
	if len(docs) == 0 {
		return nil, ErrNoText
	}
	return docs, nil
}

func xlsxRows(ctx context.Context, rows *excelize.Rows, maxRows, maxColumns int, maxText int64) (string, int, error) {
	buf := cappedBuffer{remaining: maxText}
	w := csv.NewWriter(&buf)
	w.Comma = '\t'
	n := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		n++
		if n > maxRows {
			return "", 0, fmt.Errorf("%w: XLSX rows", ErrLimit)
		}
		cells, err := rows.Columns(excelize.Options{RawCellValue: true})
		if err != nil {
			return "", 0, errors.New("document: read XLSX cells")
		}
		if len(cells) > maxColumns {
			return "", 0, fmt.Errorf("%w: XLSX columns", ErrLimit)
		}
		if err := w.Write(cells); err != nil {
			return "", 0, fmt.Errorf("%w: XLSX text", ErrLimit)
		}
		w.Flush()
		if w.Error() != nil {
			return "", 0, fmt.Errorf("%w: XLSX text", ErrLimit)
		}
	}
	if rows.Error() != nil {
		return "", 0, errors.New("document: invalid XLSX worksheet")
	}
	return buf.String(), n, nil
}

var _ rag.Parser = XLSX{}
