package linkorders

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// maxXLSXPart caps how much of one decompressed workbook part is read,
// so a small zip cannot expand into gigabytes.
const maxXLSXPart = 64 << 20

// ParseXLSXTable reads one worksheet of an Excel .xlsx workbook: the tab
// named sheet, or the first tab when sheet is empty. Only cell text and
// numbers are read (formulas give their cached value); styles, merged
// cells and other tabs are ignored. Dates arrive as Excel serial numbers.
func ParseXLSXTable(raw []byte, sheet string) (*Table, []string, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: not an .xlsx workbook (an old .xls must be saved as .xlsx or CSV first)", ErrInvalid)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[strings.TrimPrefix(f.Name, "/")] = f
	}
	read := func(name string) ([]byte, error) {
		f, ok := files[name]
		if !ok {
			return nil, nil
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, maxXLSXPart+1))
		if err != nil {
			return nil, err
		}
		if len(b) > maxXLSXPart {
			return nil, fmt.Errorf("%w: the workbook is too large", ErrInvalid)
		}
		return b, nil
	}

	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	b, err := read("xl/workbook.xml")
	if err != nil || b == nil {
		return nil, nil, fmt.Errorf("%w: the workbook has no xl/workbook.xml", ErrInvalid)
	}
	if err := xml.Unmarshal(b, &wb); err != nil || len(wb.Sheets) == 0 {
		return nil, nil, fmt.Errorf("%w: the workbook has no sheets", ErrInvalid)
	}
	var rels struct {
		Rels []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if b, _ := read("xl/_rels/workbook.xml.rels"); b != nil {
		_ = xml.Unmarshal(b, &rels)
	}
	names := make([]string, len(wb.Sheets))
	pick := -1
	for i, s := range wb.Sheets {
		names[i] = s.Name
		if pick < 0 && (sheet == "" || strings.EqualFold(strings.TrimSpace(s.Name), strings.TrimSpace(sheet))) {
			pick = i
		}
	}
	if pick < 0 {
		return nil, names, fmt.Errorf("%w: no sheet named %q (sheets: %s)", ErrInvalid, sheet, strings.Join(names, ", "))
	}
	target := fmt.Sprintf("xl/worksheets/sheet%d.xml", pick+1)
	for _, r := range rels.Rels {
		if r.ID == wb.Sheets[pick].RID {
			t := r.Target
			if strings.HasPrefix(t, "/") {
				target = strings.TrimPrefix(t, "/")
			} else {
				target = path.Clean(path.Join("xl", t))
			}
		}
	}

	var shared []string
	if b, err := read("xl/sharedStrings.xml"); err != nil {
		return nil, names, err
	} else if b != nil {
		shared, err = sharedStrings(b)
		if err != nil {
			return nil, names, fmt.Errorf("%w: unreadable shared strings: %v", ErrInvalid, err)
		}
	}
	b, err = read(target)
	if err != nil {
		return nil, names, err
	}
	if b == nil {
		return nil, names, fmt.Errorf("%w: the sheet %q has no data part", ErrInvalid, wb.Sheets[pick].Name)
	}
	rows, err := sheetRows(b, shared)
	if err != nil {
		return nil, names, fmt.Errorf("%w: unreadable sheet: %v", ErrInvalid, err)
	}
	return &Table{Rows: rows}, names, nil
}

type xlsxText struct {
	T  string `xml:"t"`
	RS []struct {
		T string `xml:"t"`
	} `xml:"r"`
}

func (x xlsxText) String() string {
	if len(x.RS) == 0 {
		return x.T
	}
	var b strings.Builder
	b.WriteString(x.T)
	for _, r := range x.RS {
		b.WriteString(r.T)
	}
	return b.String()
}

func sharedStrings(b []byte) ([]string, error) {
	var sst struct {
		SI []xlsxText `xml:"si"`
	}
	if err := xml.Unmarshal(b, &sst); err != nil {
		return nil, err
	}
	out := make([]string, len(sst.SI))
	for i, s := range sst.SI {
		out[i] = s.String()
	}
	return out, nil
}

func sheetRows(b []byte, shared []string) ([][]string, error) {
	var ws struct {
		Rows []struct {
			R     int `xml:"r,attr"`
			Cells []struct {
				Ref string   `xml:"r,attr"`
				T   string   `xml:"t,attr"`
				V   string   `xml:"v"`
				IS  xlsxText `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := xml.Unmarshal(b, &ws); err != nil {
		return nil, err
	}
	var out [][]string
	for i, r := range ws.Rows {
		rowNum := r.R
		if rowNum <= 0 {
			rowNum = len(out) + 1
		}
		if rowNum-1 > MaxTableRows+1 || i > MaxTableRows+1 {
			return nil, errTooManyRows
		}
		for len(out) < rowNum-1 {
			out = append(out, nil)
		}
		var rec []string
		for j, c := range r.Cells {
			col := j
			if c.Ref != "" {
				col = colIndex(c.Ref)
			}
			if col < 0 || col > 1000 {
				continue
			}
			var v string
			switch c.T {
			case "s":
				n, err := strconv.Atoi(strings.TrimSpace(c.V))
				if err == nil && n >= 0 && n < len(shared) {
					v = shared[n]
				}
			case "inlineStr":
				v = c.IS.String()
			case "b":
				if c.V == "1" {
					v = "true"
				} else {
					v = "false"
				}
			default:
				v = c.V
			}
			for len(rec) <= col {
				rec = append(rec, "")
			}
			rec[col] = v
		}
		out = append(out, rec)
	}
	return out, nil
}

// colIndex turns a cell reference such as "C7" or "AA12" into a 0-based
// column index.
func colIndex(ref string) int {
	n := 0
	for _, c := range ref {
		switch {
		case c >= 'A' && c <= 'Z':
			n = n*26 + int(c-'A'+1)
		case c >= 'a' && c <= 'z':
			n = n*26 + int(c-'a'+1)
		default:
			return n - 1
		}
	}
	return n - 1
}
