package linkorders

import (
	"bytes"
	"encoding/csv"
	"fmt"
)

// MaxTableRows caps the rows read from a Google Sheet or a workbook tab:
// a full intermediary database (11,000+ sites) fits with room to spare.
const MaxTableRows = 50000

// Table is a spreadsheet tab as text cells, every row as written. A Google
// Sheet and an Excel workbook become one, then CSV, so they go through the
// same catalog import (and its batches) as an uploaded CSV file.
type Table struct {
	Rows [][]string
}

// CSV writes the table as CSV with a header row first. Rows that are all
// blank are dropped; a table over MaxTableRows is refused.
func (t *Table) CSV() (string, int, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	n := 0
	for _, row := range t.Rows {
		blank := true
		for _, c := range row {
			if c != "" {
				blank = false
				break
			}
		}
		if blank {
			continue
		}
		if n > MaxTableRows {
			return "", 0, errTooManyRows
		}
		if err := w.Write(row); err != nil {
			return "", 0, err
		}
		n++
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", 0, err
	}
	if n > 0 {
		n-- // the header row
	}
	return buf.String(), n, nil
}

var errTooManyRows = fmt.Errorf("%w: the sheet has more than %d rows; split it into smaller sheets or files", ErrInvalid, MaxTableRows)
