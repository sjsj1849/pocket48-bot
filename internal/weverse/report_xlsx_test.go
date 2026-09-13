package weverse

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestXLSXIsReadableXMLAndDoesNotExecuteChatFormulas(t *testing.T) {
	p, _ := NewReportPeriod("monthly", 2026, 9)
	r := AggregateReport(ReportSettings{CommunityID: 235}, p, []Member{{ID: "a", Name: "A & B"}}, []Event{{ID: "e", CommunityID: 235, PostID: "p", MemberID: "a", Kind: "post", Body: "=HYPERLINK(\"https://example.com\") <原文>\x01", Time: p.Start.UnixMilli()}})
	data, e := r.XLSX()
	if e != nil {
		t.Fatal(e)
	}
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil {
		t.Fatal(e)
	}
	sheets := 0
	for _, f := range z.File {
		reader, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(reader)
		reader.Close()
		if e != nil {
			t.Fatal(e)
		}
		decoder := xml.NewDecoder(bytes.NewReader(b))
		for {
			_, e := decoder.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				t.Fatalf("invalid Excel XML %s: %v", f.Name, e)
			}
		}
		if strings.Contains(f.Name, "worksheets/") {
			sheets++
		}
		if strings.Contains(string(b), "<f>") {
			t.Fatal("chat content became an Excel formula")
		}
	}
	if sheets != 7 {
		t.Fatal("missing workbook detail sheets", sheets)
	}
}
