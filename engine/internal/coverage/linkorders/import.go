package linkorders

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ImportChunkRows is how many rows one transaction of an import writes.
// A large file is written in several transactions, so a full intermediary
// database (11,000+ sites) never holds one long transaction open.
var ImportChunkRows = 500

// CatalogFile is a parsed catalog CSV: which column holds which field,
// and the data rows (the header is not one of them).
type CatalogFile struct {
	idx     map[string]int
	ignored []string
	Rows    [][]string
}

// ImportCSV upserts catalog rows from a CSV in one transaction. The first
// row is the header. Domain is required. A repeated domain updates the
// existing profile rather than adding a second publisher. Bad rows are
// listed in the result and do not abort the rest of the file.
func ImportCSV(ctx context.Context, tx pgx.Tx, workspaceID, brandID uuid.UUID, r io.Reader) (*ImportResult, error) {
	f, err := ParseCatalogCSV(r)
	if err != nil {
		return nil, err
	}
	out := f.NewResult()
	if err := f.ImportRows(ctx, tx, workspaceID, brandID, 0, len(f.Rows), out); err != nil {
		return nil, err
	}
	return out, nil
}

// ParseCatalogCSV reads a catalog CSV and maps its header. A file with no
// rows at all is empty, not an error.
func ParseCatalogCSV(r io.Reader) (*CatalogFile, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.ReuseRecord = false
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: csv: %v", ErrInvalid, err)
	}
	if len(rows) == 0 {
		return &CatalogFile{idx: map[string]int{}}, nil
	}
	if len(rows[0]) > 0 {
		rows[0][0] = strings.TrimPrefix(rows[0][0], "\ufeff") // a UTF-8 BOM from Excel
	}
	idx, ignored, err := csvHeader(rows[0])
	if err != nil {
		return nil, err
	}
	return &CatalogFile{idx: idx, ignored: ignored, Rows: rows[1:]}, nil
}

// NewResult is an empty result for this file, listing the header columns
// the import does not read.
func (f *CatalogFile) NewResult() *ImportResult {
	ign := f.ignored
	if ign == nil {
		ign = []string{}
	}
	return &ImportResult{Errors: []ImportError{}, IgnoredColumns: ign}
}

// ImportRows writes rows [from, to) into tx, adding to out. Each row runs
// in its own savepoint, so a row the database refuses is listed in errors
// and the rest of the chunk still lands.
func (f *CatalogFile) ImportRows(ctx context.Context, tx pgx.Tx, workspaceID, brandID uuid.UUID, from, to int, out *ImportResult) error {
	now := time.Now().UTC()
	for i := from; i < to && i < len(f.Rows); i++ {
		rec := f.Rows[i]
		row := i + 2 // 1-based, counting the header
		if emptyRecord(rec) {
			continue
		}
		domain, write, err := csvRow(f.idx, rec, now)
		if err != nil {
			out.Errors = append(out.Errors, ImportError{Row: row, Domain: domain, Error: err.Error()})
			continue
		}
		write.Source = SourceCSV
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		_, created, err := UpsertDonor(ctx, sp, workspaceID, brandID, write)
		if err != nil {
			if rbErr := sp.Rollback(ctx); rbErr != nil {
				return rbErr
			}
			out.Errors = append(out.Errors, ImportError{Row: row, Domain: write.Domain, Error: rowError(err)})
			continue
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
		if created {
			out.Created++
		} else {
			out.Updated++
		}
	}
	return nil
}

// rowError is the message for a row the database refused: our own
// validation errors as they are, anything else without database detail.
func rowError(err error) string {
	if isOurs(err) {
		return err.Error()
	}
	return "the row could not be saved; check its values"
}

func isOurs(err error) bool {
	for _, e := range []error{ErrInvalid, ErrNotFound, ErrConflict} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func emptyRecord(rec []string) bool {
	for _, c := range rec {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func csvHeader(row []string) (map[string]int, []string, error) {
	idx := map[string]int{}
	var ignored []string
	for i, h := range row {
		key := csvCol(h)
		if key == "" {
			if strings.TrimSpace(h) != "" {
				ignored = append(ignored, strings.TrimSpace(h))
			}
			continue
		}
		if _, dup := idx[key]; dup {
			// The first column for a field wins; a second "Traffic"
			// column never overwrites it.
			ignored = append(ignored, strings.TrimSpace(h))
			continue
		}
		idx[key] = i
	}
	if _, ok := idx["domain"]; !ok {
		return nil, nil, fmt.Errorf("%w: csv header must include a domain column (domain, website, site or url)", ErrInvalid)
	}
	return idx, ignored, nil
}

func csvRow(idx map[string]int, rec []string, now time.Time) (string, DonorWrite, error) {
	get := func(name string) string {
		i, ok := idx[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	domain := get("domain")
	if domain == "" {
		return "", DonorWrite{}, fmt.Errorf("%w: domain is required", ErrInvalid)
	}
	in := DonorWrite{Domain: domain, Name: get("name"), Source: SourceCSV}
	bad := func(field, why string) (string, DonorWrite, error) {
		return domain, in, fmt.Errorf("%w: %s: %s", ErrInvalid, field, why)
	}
	if v := get("pbn"); !isBlank(v) {
		b, err := parseCSVBool(v)
		if err != nil {
			return bad("pbn", err.Error())
		}
		in.PBN = &b
	}
	if present(idx, "country") {
		in.Country = ptrPtr(clip(get("country"), 100))
	} else if v := get(MetricGeoTop1); !isBlank(v) {
		// A broker file names the site's top country as Geo Top 1; use it
		// as the country when the file has no country column.
		in.Country = ptrPtr(clip(v, 100))
	}
	if present(idx, "languages") {
		s := splitList(get("languages"))
		in.Languages = &s
	}
	if present(idx, "niches") {
		s := splitList(get("niches"))
		in.Niches = &s
	}
	if v := get("casino_allowed"); !isBlank(v) {
		b, err := parseCSVBool(v)
		if err != nil {
			return bad("casino_allowed", err.Error())
		}
		in.CasinoAllowed = ptrPtr(b)
	}
	if present(idx, "rel_offered") {
		s := splitList(get("rel_offered"))
		in.RelOffered = &s
	}
	if present(idx, "slot_types") {
		s := splitList(get("slot_types"))
		in.SlotTypes = &s
	}
	if present(idx, "duration") {
		in.Duration = ptrPtr(clip(get("duration"), 100))
	}
	if v := get("duration_days"); !isBlank(v) {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return bad("duration_days", "must be a whole number of days")
		}
		in.DurationDays = ptrPtr(n)
	}
	if v := get("tat_days"); !isBlank(v) {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return bad("tat_days", "must be a whole number of days")
		}
		in.TATDays = ptrPtr(n)
	}
	if v := get("guarantee_days"); !isBlank(v) {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return bad("guarantee_days", "must be a whole number of days")
		}
		in.GuaranteeDays = ptrPtr(n)
	}
	if present(idx, "vendor_id") {
		in.VendorID = ptrPtr(clip(get("vendor_id"), 200))
	}
	if present(idx, "notes") {
		in.Notes = ptrPtr(clip(get("notes"), 20000))
	}
	if present(idx, "intermediary") {
		in.Intermediary = ptrPtr(clip(get("intermediary"), 500))
	}
	if present(idx, "theme") {
		in.Theme = ptrPtr(clip(get("theme"), 200))
	}
	if v := get("site_age"); !isBlank(v) {
		t, ok, err := ParseSiteAge(v, now)
		if err != nil {
			return bad("site age", err.Error())
		}
		if ok {
			in.SiteSince = ptrPtr(t)
		}
	}
	hasMetrics := false
	if v := get("domain_rating"); !isBlank(v) {
		n, _, err := ParseCount(v)
		if err != nil || n > 100 {
			return bad("domain_rating", "must be a number from 0 to 100")
		}
		in.DomainRating = ptrPtr(int(n))
		hasMetrics = true
	}
	if v := get("traffic"); !isBlank(v) {
		n, _, err := ParseCount(v)
		if err != nil {
			return bad("traffic", err.Error())
		}
		in.Traffic = ptrPtr(n)
		hasMetrics = true
	}
	for _, k := range MetricKeys {
		if !present(idx, k) {
			continue
		}
		v := get(k)
		if in.Metrics == nil {
			in.Metrics = map[string]any{}
		}
		if isBlank(v) {
			// A blank cell in a metric column clears the stored value, so a
			// re-import of a newer export never keeps a stale number.
			in.Metrics[k] = nil
			continue
		}
		hasMetrics = true
		if textMetrics[k] {
			if k == MetricDateAdded {
				if t, ok := ParseDate(v); ok {
					v = t.Format("2006-01-02")
				}
			}
			in.Metrics[k] = clip(v, maxMetricText)
			continue
		}
		n, _, err := ParseCount(v)
		if err != nil {
			return bad(metricLabels[k], err.Error())
		}
		in.Metrics[k] = n
	}
	if hasMetrics {
		src, at := "csv", now
		in.MetricsSource, in.MetricsAt = ptrPtr(src), ptrPtr(at)
	}

	prices, err := csvPrices(get)
	if err != nil {
		return domain, in, err
	}
	if prices != nil {
		in.Prices = &prices
	}
	return domain, in, nil
}

// csvPrices reads the donor price. amount_minor is in minor units, as the
// original import and the export write it; price, price_usd and price_eur
// are in major units ("150" is 150.00), as price lists write them. The
// currency is the currency column, else the symbol or code in the cell,
// else USD.
func csvPrices(get func(string) string) ([]Price, error) {
	currency := strings.ToUpper(strings.TrimSpace(get("currency")))
	switch currency {
	case "$":
		currency = CurrencyUSD
	case "€":
		currency = CurrencyEUR
	}
	var out []Price
	seen := map[string]bool{}
	addPrice := func(cur string, minor int64) error {
		if err := validCurrency(cur); err != nil {
			return err
		}
		if seen[cur] {
			return nil // the first column for a currency wins
		}
		seen[cur] = true
		out = append(out, Price{Currency: cur, AmountMinor: minor})
		return nil
	}
	if amount := get("amount_minor"); !isBlank(amount) {
		if currency == "" {
			return nil, fmt.Errorf("%w: currency and amount_minor must be sent together", ErrInvalid)
		}
		n, err := strconv.ParseInt(strings.ReplaceAll(amount, ",", ""), 10, 64)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%w: amount_minor must be a whole number of cents", ErrInvalid)
		}
		if err := addPrice(currency, n); err != nil {
			return nil, err
		}
	}
	for _, col := range []struct{ key, cur string }{{"price_usd", CurrencyUSD}, {"price_eur", CurrencyEUR}, {"price", ""}} {
		v := get(col.key)
		if isBlank(v) {
			continue
		}
		minor, named, ok, err := ParsePrice(v)
		if err != nil {
			return nil, fmt.Errorf("%w: price: %v", ErrInvalid, err)
		}
		if !ok {
			continue
		}
		cur := col.cur
		if cur == "" {
			cur = named
		}
		if cur == "" {
			cur = currency
		}
		if cur == "" {
			cur = CurrencyUSD
		}
		if err := addPrice(cur, minor); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func present(idx map[string]int, name string) bool {
	_, ok := idx[name]
	return ok
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	cut := 0
	for i := range s {
		if i > n {
			break
		}
		cut = i
	}
	return s[:cut]
}

func splitList(s string) []string {
	if isBlank(s) {
		return []string{}
	}
	s = strings.ReplaceAll(s, "|", ",")
	s = strings.ReplaceAll(s, ";", ",")
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func parseCSVBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true", "yes", "y", "+", "да":
		return true, nil
	case "0", "f", "false", "no", "n", "-", "нет":
		return false, nil
	default:
		return false, fmt.Errorf("want true or false")
	}
}

func ptrPtr[T any](v T) **T {
	p := v
	pp := &p
	return &pp
}
