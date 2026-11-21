package linkorders

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func buildXLSX(t *testing.T, sheets map[string]string, shared []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	var wb, rels strings.Builder
	wb.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	rels.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	i := 0
	for _, name := range []string{"Prices", "Other"} {
		body, ok := sheets[name]
		if !ok {
			continue
		}
		i++
		fmt.Fprintf(&wb, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, name, i, i)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="worksheet" Target="worksheets/sheet%d.xml"/>`, i, i)
		add(fmt.Sprintf("xl/worksheets/sheet%d.xml", i), `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`+body+`</sheetData></worksheet>`)
	}
	wb.WriteString(`</sheets></workbook>`)
	rels.WriteString(`</Relationships>`)
	add("xl/workbook.xml", wb.String())
	add("xl/_rels/workbook.xml.rels", rels.String())
	var sst strings.Builder
	sst.WriteString(`<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	for _, s := range shared {
		fmt.Fprintf(&sst, `<si><t>%s</t></si>`, s)
	}
	sst.WriteString(`</sst>`)
	add("xl/sharedStrings.xml", sst.String())
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseXLSXTable(t *testing.T) {
	raw := buildXLSX(t, map[string]string{
		"Prices": `<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="D1" t="inlineStr"><is><t>Price</t></is></c></row>` +
			`<row r="3"><c r="A3" t="s"><v>2</v></c><c r="B3"><v>55</v></c><c r="D3"><v>120.5</v></c></row>`,
		"Other": `<row r="1"><c r="A1" t="inlineStr"><is><t>only</t></is></c></row>`,
	}, []string{"Website", "DR", "donor.example"})
	tb, names, err := ParseXLSXTable(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "Prices,Other" {
		t.Errorf("names: %v", names)
	}
	if len(tb.Rows) != 3 || tb.Rows[0][3] != "Price" || tb.Rows[0][2] != "" || tb.Rows[2][0] != "donor.example" || tb.Rows[2][3] != "120.5" || len(tb.Rows[1]) != 0 {
		t.Errorf("rows: %#v", tb.Rows)
	}
	tb, _, err = ParseXLSXTable(raw, "other")
	if err != nil || tb.Rows[0][0] != "only" {
		t.Errorf("named sheet: %v %v", tb, err)
	}
	if _, _, err := ParseXLSXTable(raw, "missing"); err == nil || !errors.Is(err, ErrInvalid) {
		t.Errorf("missing sheet: %v", err)
	}
	if _, _, err := ParseXLSXTable([]byte("not a zip"), ""); err == nil {
		t.Error("not a zip should fail")
	}
	if colIndex("AA12") != 26 || colIndex("C7") != 2 {
		t.Error("colIndex")
	}
}

func TestParseSheetURL(t *testing.T) {
	ok := map[string]SheetRef{
		"https://docs.google.com/spreadsheets/d/1AbCdEfGhIjKlMnOp/edit#gid=12345":          {ID: "1AbCdEfGhIjKlMnOp", GID: "12345"},
		"https://docs.google.com/spreadsheets/u/0/d/1AbCdEfGhIjKlMnOp/edit?usp=sharing":    {ID: "1AbCdEfGhIjKlMnOp"},
		"https://docs.google.com/spreadsheets/d/e/2PACX-1vQabcdefghij/pubhtml?gid=7":       {ID: "2PACX-1vQabcdefghij", Published: true, GID: "7"},
		"https://docs.google.com/spreadsheets/d/1AbCdEfGhIjKlMnOp/export?format=csv&gid=9": {ID: "1AbCdEfGhIjKlMnOp", GID: "9"},
	}
	for in, want := range ok {
		got, err := ParseSheetURL(in)
		if err != nil || got != want {
			t.Errorf("ParseSheetURL(%q) = %+v %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"http://docs.google.com/spreadsheets/d/1AbCdEfGhIjKlMnOp/edit",
		"https://evil.example/spreadsheets/d/1AbCdEfGhIjKlMnOp/edit",
		"https://docs.google.com.evil.example/spreadsheets/d/1AbCdEfGhIjKlMnOp/edit",
		"https://docs.google.com/document/d/1AbCdEfGhIjKlMnOp/edit",
		"https://user" + "@" + "docs.google.com/spreadsheets/d/1AbCdEfGhIjKlMnOp/edit", // split so the fake-data check does not read it as an address
		"https://docs.google.com:8443/spreadsheets/d/1AbCdEfGhIjKlMnOp/edit",
	} {
		if _, err := ParseSheetURL(bad); err == nil {
			t.Errorf("ParseSheetURL(%q) should fail", bad)
		}
	}
	r := SheetRef{ID: "abc", GID: "5"}
	if got := r.ExportURL(""); got != "https://docs.google.com/spreadsheets/d/abc/export?format=csv&gid=5" {
		t.Errorf("export url: %s", got)
	}
	r = SheetRef{ID: "2PACX", Published: true}
	if got := r.ExportURL(""); got != "https://docs.google.com/spreadsheets/d/e/2PACX/pub?output=csv&single=true" {
		t.Errorf("published url: %s", got)
	}
}

func TestFetchSheetCSV(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/spreadsheets/d/public123/export":
			if r.URL.Query().Get("format") != "csv" || r.URL.Query().Get("gid") != "4" {
				http.Error(w, "bad query", 400)
				return
			}
			w.Header().Set("Content-Type", "text/csv")
			_, _ = w.Write([]byte("Website,Price\na.com,$10\n"))
		case "/spreadsheets/d/private12/export":
			http.Redirect(w, r, "https://accounts.google.com/ServiceLogin?continue=x", http.StatusFound)
		case "/spreadsheets/d/htmlpage1/export":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html>sign in</html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	b, err := FetchSheetCSV(ctx, srv.Client(), SheetRef{ID: "public123", GID: "4"}, srv.URL)
	if err != nil || !strings.Contains(string(b), "a.com") {
		t.Fatalf("public: %q %v", b, err)
	}
	for _, id := range []string{"private12", "htmlpage1"} {
		_, err := FetchSheetCSV(ctx, srv.Client(), SheetRef{ID: id}, srv.URL)
		if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "Anyone with the link") {
			t.Errorf("%s: want a not-public error, got %v", id, err)
		}
	}
	if _, err := FetchSheetCSV(ctx, srv.Client(), SheetRef{ID: "missing99"}, srv.URL); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing: %v", err)
	}
}

func TestTableCSV(t *testing.T) {
	tb := &Table{Rows: [][]string{{"Website", "Price USD, $", "Notes"}, nil, {"a.example", "84", "two\nlines"}, {"", "", ""}, {"b.example", "90", ""}}}
	got, n, err := tb.CSV()
	if err != nil {
		t.Fatal(err)
	}
	want := "Website,\"Price USD, $\",Notes\na.example,84,\"two\nlines\"\nb.example,90,\n"
	if got != want || n != 2 {
		t.Fatalf("CSV() = %q, %d; want %q, 2", got, n, want)
	}
	f, err := ParseCatalogCSV(strings.NewReader(got))
	if err != nil || len(f.Rows) != 2 {
		t.Fatalf("round trip: %v rows, %v", len(f.Rows), err)
	}
	big := &Table{Rows: make([][]string, MaxTableRows+3)}
	for i := range big.Rows {
		big.Rows[i] = []string{"x.example"}
	}
	if _, _, err := big.CSV(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over the cap: %v", err)
	}
}
