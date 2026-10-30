package document

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func workbook(t *testing.T) []byte {
	t.Helper()
	return archive(t,
		"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/worksheets/sheet2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/></Types>`,
		"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/workbook.xml", `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="数据表" sheetId="1" r:id="rId1"/><sheet name="Hidden" sheetId="2" state="hidden" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/></Relationships>`,
		"xl/sharedStrings.xml", `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="2" uniqueCount="2"><si><t>Name</t></si><si><r><t>世</t></r><r><t>界</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml", `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="inlineStr"><is><t>Value</t></is></c></row><row r="2"><c r="A2" t="s"><v>1</v></c><c r="B2"><f>1+1</f><v>2</v></c></row><row r="4"><c r="A4" t="inlineStr"><is><t>last</t></is></c><c r="B4" t="b"><v>1</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml", `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>hidden data</t></is></c></row></sheetData></worksheet>`,
	)
}

func TestXLSXCellsSheetsAndCachedFormulas(t *testing.T) {
	data := workbook(t)
	docs, err := (XLSX{}).Parse(t.Context(), "book.xlsx", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Metadata["sheet"] != "数据表" || docs[0].Metadata["rows"] != 4 || docs[0].Source != "book.xlsx" || !strings.Contains(docs[0].Content, "Name\tValue\n世界\t2\n\nlast\t1") {
		t.Fatalf("%+v", docs)
	}
	if strings.Contains(docs[0].Content, "1+1") {
		t.Fatal("formula text included instead of cached value")
	}
	docs, err = (XLSX{IncludeHidden: true}).Parse(t.Context(), "book.xlsx", data)
	if err != nil || len(docs) != 2 || !strings.Contains(docs[1].Content, "hidden data") {
		t.Fatalf("hidden: %+v %v", docs, err)
	}
}

func TestXLSXLimitsAndCancellation(t *testing.T) {
	data := workbook(t)
	for _, tc := range []struct {
		name string
		p    XLSX
	}{
		{"input", XLSX{MaxBytes: 1}}, {"expanded", XLSX{MaxArchiveBytes: 100}}, {"entries", XLSX{MaxEntries: 2}}, {"rows", XLSX{MaxRows: 2}}, {"columns", XLSX{MaxColumns: 1}}, {"sheets", XLSX{MaxSheets: 1}}, {"text", XLSX{MaxTextBytes: 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.p.Parse(t.Context(), "book.xlsx", data); !errors.Is(err, ErrLimit) {
				t.Fatalf("limit: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (XLSX{}).Parse(ctx, "book.xlsx", data); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := (XLSX{}).Parse(t.Context(), "bad.xlsx", []byte("invalid")); err == nil {
		t.Fatal("invalid workbook accepted")
	}
}
