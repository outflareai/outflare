package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
)

// A Google Sheet link and an Excel workbook become CSV for the catalog
// import; nothing is written until that CSV is imported.
func TestLinkOrderCatalogTable(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders/catalog"

	defer api.SetCatalogSheetFetch(func(_ context.Context, id, gid string) ([]byte, error) {
		switch id {
		case "1PublicSheet0001":
			if gid != "7" {
				return nil, fmt.Errorf("%w: wrong tab", linkorders.ErrInvalid)
			}
			return []byte("\ufeffURL,\"Price USD, $\",DR\nsheet-one.example,84,38\n,,\nsheet-two.example,120,40\n"), nil
		default:
			return nil, fmt.Errorf("%w: %v", linkorders.ErrInvalid, linkorders.ErrSheetPrivate)
		}
	})()

	got := owner.must(200, "POST", root+"/table", map[string]any{"sheet_url": "https://docs.google.com/spreadsheets/d/1PublicSheet0001/edit#gid=7"})
	if got["source"] != "google_sheet" || got["rows"] != 2.0 {
		t.Fatalf("sheet: %v", got)
	}
	csv := got["csv"].(string)
	if !strings.HasPrefix(csv, "URL,\"Price USD, $\",DR\n") || strings.Contains(csv, ",,\n") {
		t.Fatalf("sheet csv: %q", csv)
	}
	// Reading a sheet writes nothing.
	if list := owner.must(200, "GET", root, nil); len(list["data"].([]any)) != 0 {
		t.Fatalf("reading a sheet added donors: %v", list)
	}
	// Its CSV goes through the normal import, prices included.
	imported := owner.must200Raw(t, root+"/import", csv)
	if imported["created"] != 2.0 {
		t.Fatalf("import: %v", imported)
	}

	owner.must(400, "POST", root+"/table", map[string]any{"sheet_url": "https://docs.google.com/spreadsheets/d/1PrivateSheet001/edit"})
	owner.must(400, "POST", root+"/table", map[string]any{"sheet_url": "https://example.com/spreadsheets/d/1PublicSheet0001/edit"})
	owner.must(400, "POST", root+"/table", map[string]any{})

	// An .xlsx workbook: shared strings and numbers, the first tab.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Prices" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>Website</t></si><si><t>Price</t></si><si><t>book.example</t></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2"><v>150</v></c></row></sheetData></worksheet>`,
	} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	got = owner.must(200, "POST", root+"/table", map[string]any{"xlsx_base64": base64.StdEncoding.EncodeToString(buf.Bytes())})
	if got["source"] != "xlsx" || got["csv"] != "Website,Price\nbook.example,150\n" || got["rows"] != 1.0 {
		t.Fatalf("xlsx: %v", got)
	}
	owner.must(400, "POST", root+"/table", map[string]any{"xlsx_base64": base64.StdEncoding.EncodeToString([]byte("not a workbook"))})
}
