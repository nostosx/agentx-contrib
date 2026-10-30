# document

`github.com/nostosx/agentx-contrib/document` provides `rag.Parser` adapters
for DOCX, PDF, XLSX and OCR inputs. Requires Go 1.27.3. XLSX and OCR are unreleased.

| Parser | Implementation | Runtime dependency |
| --- | --- | --- |
| `document.DOCX` | Streams the main OOXML document with `archive/zip` and `encoding/xml` | none |
| `document.PDF` | Calls Poppler `pdftotext` through `exec.CommandContext`, without a shell | separately installed `pdftotext` executable |
| `document.XLSX` | Bounded Excelize worksheet iteration; cached/raw cell values | Excelize Go module |
| `document.Tesseract` | Image OCR through stdin/stdout | application-installed Tesseract and language data |
| `document.PDFImages` | Rasterizes scanned PDF pages then calls an injected OCR Parser | application-installed `pdftoppm`, private temporary storage |

No converter is downloaded automatically, no PDF SDK is linked, and no file
is uploaded to a service. The application's deployment owns Poppler updates
and its executable's distribution/license requirements. Poppler's command
documentation is available in the [pdftotext manual](https://manpages.debian.org/testing/poppler-utils/pdftotext.1.en.html).

## Usage

Add the parsers to the application's filesystem loader:

```go
parsers := rag.DefaultParsers()
parsers[".docx"] = document.DOCX{}
parsers[".pdf"] = document.PDF{Command: "/usr/bin/pdftotext"}
loader := &rag.FSLoader{FS: os.DirFS("documents"), Parsers: parsers}
```

This is an assembly fragment; import `github.com/nostosx/agentx/rag`,
`github.com/nostosx/agentx-contrib/document` and `os` in the application.
For URL sources, set `rag.URLLoader.Parser` to the corresponding parser.

Both parsers implement `Parse(ctx, source, data)` and retain the source on
every returned document. They fail on malformed input and return
`document.ErrNoText` when no extractable text exists.

## PDF

The default executable is `pdftotext` from `PATH`; `Command` accepts one
trusted executable path or name, never shell syntax. Input and output use
pipes rather than temporary files. The converter runs with the caller's
environment and process privileges; applications handling hostile files
should apply their own process sandbox and OS resource limits.

PDF output contains one document for each page with text. IDs are
`<source>#page=<number>`; metadata records `format=pdf`, the original 1-based
`page`, and `page_count`. Empty pages keep their position and are listed
under `textless_pages` in every returned document. A textless page may be
blank, scanned, or have fonts without an extractable encoding; text extraction
cannot distinguish these cases.

A wholly textless PDF returns `ErrNoText`. Applications may set `PDF.OCR` to
an explicit `rag.Parser` fallback; it receives the original bytes only when
the whole document has no extractable text. An explicit `PDFImages` +
`Tesseract` fallback performs local OCR; no model is automatically called.
The fallback owns its limits and page/source metadata. Partial PDFs
retain `textless_pages`; an OCR fallback is not invoked for them.

Defaults: 32 MiB input, 32 MiB extracted text, 1000 pages and a 30-second
process deadline. Fields `MaxBytes`, `MaxTextBytes`, `MaxPages` and `Timeout`
can lower or raise these limits. Zero selects a default; negative values
are rejected. A page limit is detected by requesting one extra page, not
silently truncating the document. Converter failures preserve the underlying
error, while stderr content is excluded from returned errors.

## DOCX

DOCX extracts the main document's paragraphs and table-cell paragraphs,
preserving tabs and line breaks. Deleted revision text is omitted. It does
not extract headers, footers, comments, drawings, images or embedded files;
external relationships are ignored. DOCX pagination depends on layout and
fonts, so the parser never claims rendered page numbers. Metadata records
`format=docx` and the number of `explicit_page_breaks` in the main document.

Defaults: 32 MiB input, 32 MiB text, 128 MiB aggregate declared ZIP expansion,
4096 ZIP entries and at most 256 nested XML elements. Configure `MaxBytes`,
`MaxTextBytes`, `MaxArchiveBytes` and `MaxEntries` as needed. Duplicate main
document entries, unsafe archive paths, malformed XML and excessive limits
are rejected. Actual main-part reads are independently bounded and honor
context cancellation. Archives are decoded in memory and never extracted to
the filesystem.

## Tests

Unit tests need no Poppler installation. They cover OOXML parsing, ZIP/XML
limits and validation, PDF page metadata and partial text, bounded subprocess
output, deadline cancellation, error privacy and explicit OCR fallback.
Subprocess tests use the Go test binary itself, and run on all three CI OSes.

Run the complete converter/OCR gate from the contrib root with installed binaries:

```sh
AGENTX_IT_PDFTOTEXT=/absolute/path/to/pdftotext \
AGENTX_IT_PDFTOPPM=/absolute/path/to/pdftoppm \
AGENTX_IT_TESSERACT=/absolute/path/to/tesseract \
  task test INTEGRATION=true MODULES=document
```

Missing configuration skips the integration test in direct Go runs; skipping
does not prove converter compatibility. It uses generated, local PDF fixtures
and no network or production files. The Task gate rejects missing tools or skips.

## XLSX (unreleased)

`XLSX{}` returns one tab-separated document per nonempty visible worksheet,
with `sheet` and `rows` metadata. IDs append an escaped `#sheet=` name to
the source. Shared/rich strings, inline strings, sparse rows and cached formula
values are handled by Excelize. Formula evaluation, external link fetching and
macro execution are not performed. Numeric/date values use their raw stored
values; drawings and charts are not extracted. Hidden sheets require the
explicit `IncludeHidden` option.

Defaults: 32 MiB input and aggregate text, 128 MiB expanded ZIP, 4096 ZIP
entries, 100 sheets, 100000 row positions per sheet and 1024 columns per row.
All limits reject excess input instead of returning partial documents. The
archive limit can be raised to at most 256 MiB. XML stays in memory, avoiding
Excelize temporary-file spill. Cancellation is checked during archive reading
and row iteration; Excelize operations themselves are synchronous and are not
forcibly preempted mid-operation.

Excelize is pinned to public commit `c5fd73bde8f6`
(`v2.11.1-0.20261029001137-c5fd73bde8f6`) because the latest tag, v2.11.0,
has reachable issues GO-2026-6684, GO-2026-6685, GO-2026-6688 and GO-2026-6689.
The fixed commit resolves through the public Go proxy with real checksums.
Reassess this pin when a fixed stable release is available; do not downgrade
to the vulnerable tag solely to avoid a pseudo-version.

## Local OCR (unreleased)

```go
ocr := document.Tesseract{Command: "/usr/bin/tesseract", Languages: "eng+chi_sim"}
scanned := document.PDFImages{Command: "/usr/bin/pdftoppm", OCR: ocr, MaxPages: 100}
parser := document.PDF{Command: "/usr/bin/pdftotext", OCR: scanned}
```

Tesseract accepts image bytes, not PDFs. It defaults to English, a 30-second
deadline, 32 MiB input/text and an 8 KiB diagnostic cap. Language names are
validated, binaries are invoked without a shell, and stderr is omitted from
errors. The application installs binaries and language packs and owns their
licenses and updates. See [Tesseract usage](https://tesseract-ocr.github.io/tessdoc/Command-Line-Usage.html).

PDFImages renders at most MaxPages plus one detection page (default 100),
scales the longest edge to 2048 pixels (maximum 4096), and enforces a shared
two-minute render/OCR deadline and aggregate text limit. It preserves page
numbers and page count. Rendered files are kept under a private temporary
directory and removed before return. Empty OCR pages are omitted; a wholly
empty result returns ErrNoText. The configured page/resolution limits also
bound temporary storage; applications may lower them for constrained hosts.

## License

[MIT](LICENSE).
