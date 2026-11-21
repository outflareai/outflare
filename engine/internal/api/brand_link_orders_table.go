package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/netguard"
)

// fetchCatalogSheet downloads a Google Sheet tab as CSV with no
// credentials, only over public addresses. Tests replace it.
var fetchCatalogSheet = func(ctx context.Context, ref linkorders.SheetRef) ([]byte, error) {
	return linkorders.FetchSheetCSV(ctx, netguard.Client(time.Minute), ref, "")
}

// readLinkOrderCatalogTable turns a Google Sheet link or an Excel workbook
// into CSV. It writes nothing: the Catalog page sends the CSV to the
// import in batches, so a sheet goes through the same mapping, batching
// and intermediary handling as an uploaded CSV file.
func (s *Server) readLinkOrderCatalogTable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SheetURL   string `json:"sheet_url"`
		XLSXBase64 string `json:"xlsx_base64"`
		Sheet      string `json:"sheet"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCatalogImportBytes)).Decode(&in); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the workbook is larger than 32 MB; save the tab as CSV and import that")
			return
		}
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send JSON with sheet_url or xlsx_base64")
		return
	}
	in.SheetURL, in.XLSXBase64 = strings.TrimSpace(in.SheetURL), strings.TrimSpace(in.XLSXBase64)
	if (in.SheetURL == "") == (in.XLSXBase64 == "") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send one of sheet_url or xlsx_base64")
		return
	}
	start := time.Now()
	source := "xlsx"
	if in.SheetURL != "" {
		source = "google_sheet"
	}
	table, sheets, err := readCatalogTable(r.Context(), in.SheetURL, in.XLSXBase64, in.Sheet)
	var text string
	var rows int
	if err == nil {
		text, rows, err = table.CSV()
	}
	outcome := "success"
	switch {
	case err == nil:
	case errors.Is(err, linkorders.ErrSheetUnreachable):
		outcome = "error"
	case errors.Is(err, linkorders.ErrInvalid):
		outcome = "refused"
	default:
		outcome = "error"
	}
	metrics.LinkOrderTableRead(source, outcome, time.Since(start))
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	if sheets == nil {
		sheets = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"csv": text, "rows": rows, "source": source, "sheets": sheets})
}

func readCatalogTable(ctx context.Context, sheetURL, xlsxB64, sheet string) (*linkorders.Table, []string, error) {
	if sheetURL != "" {
		ref, err := linkorders.ParseSheetURL(sheetURL)
		if err != nil {
			return nil, nil, err
		}
		raw, err := fetchCatalogSheet(ctx, ref)
		if err != nil {
			return nil, nil, err
		}
		raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
		cr := csv.NewReader(bytes.NewReader(raw))
		cr.FieldsPerRecord = -1
		cr.LazyQuotes = true
		rows, err := cr.ReadAll()
		if err != nil {
			return nil, nil, fmt.Errorf("%w: the Google Sheet export is not readable CSV", linkorders.ErrInvalid)
		}
		return &linkorders.Table{Rows: rows}, nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(xlsxB64)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: xlsx_base64 is not base64", linkorders.ErrInvalid)
	}
	return linkorders.ParseXLSXTable(raw, sheet)
}
