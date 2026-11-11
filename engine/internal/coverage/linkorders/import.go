package linkorders

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ImportCSV upserts catalog rows from a CSV. The first row is the header.
// Domain is required. A repeated domain updates the existing profile
// rather than adding a second publisher. Bad rows are listed in the
// result and do not abort the rest of the file.
func ImportCSV(ctx context.Context, tx pgx.Tx, workspaceID, brandID uuid.UUID, r io.Reader) (*ImportResult, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.ReuseRecord = false
	cr.FieldsPerRecord = -1
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: csv: %v", ErrInvalid, err)
	}
	out := &ImportResult{Errors: []ImportError{}}
	if len(rows) == 0 {
		return out, nil
	}
	idx, err := csvHeader(rows[0])
	if err != nil {
		return nil, err
	}
	for i, rec := range rows[1:] {
		row := i + 2 // 1-based, counting the header
		domain, write, err := csvRow(idx, rec)
		if err != nil {
			out.Errors = append(out.Errors, ImportError{Row: row, Domain: domain, Error: err.Error()})
			continue
		}
		write.Source = SourceCSV
		_, created, err := UpsertDonor(ctx, tx, workspaceID, brandID, write)
		if err != nil {
			msg := err.Error()
			out.Errors = append(out.Errors, ImportError{Row: row, Domain: write.Domain, Error: msg})
			continue
		}
		if created {
			out.Created++
		} else {
			out.Updated++
		}
	}
	return out, nil
}

func csvHeader(row []string) (map[string]int, error) {
	idx := map[string]int{}
	for i, h := range row {
		key := csvCol(h)
		if key == "" {
			continue
		}
		idx[key] = i
	}
	if _, ok := idx["domain"]; !ok {
		return nil, fmt.Errorf("%w: csv header must include a domain column", ErrInvalid)
	}
	return idx, nil
}

func csvCol(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	aliases := map[string]string{
		"host": "domain", "website": "domain", "url": "domain",
		"price": "amount_minor", "amount": "amount_minor",
		"dr": "domain_rating", "domain_authority": "domain_rating",
		"vendor":   "vendor_id",
		"language": "languages", "niche": "niches",
		"slot_type": "slot_types", "rel": "rel_offered",
		"tat": "tat_days", "guarantee": "guarantee_days",
	}
	if a, ok := aliases[s]; ok {
		return a
	}
	return s
}

func csvRow(idx map[string]int, rec []string) (string, DonorWrite, error) {
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
	if v := get("pbn"); v != "" {
		b, err := parseCSVBool(v)
		if err != nil {
			return domain, in, fmt.Errorf("%w: pbn: %v", ErrInvalid, err)
		}
		in.PBN = &b
	}
	if v := get("country"); present(idx, "country") {
		in.Country = ptrPtr(v)
	}
	if present(idx, "languages") {
		s := splitList(get("languages"))
		in.Languages = &s
	}
	if present(idx, "niches") {
		s := splitList(get("niches"))
		in.Niches = &s
	}
	if v := get("casino_allowed"); v != "" {
		b, err := parseCSVBool(v)
		if err != nil {
			return domain, in, fmt.Errorf("%w: casino_allowed: %v", ErrInvalid, err)
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
		in.Duration = ptrPtr(get("duration"))
	}
	if v := get("duration_days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return domain, in, fmt.Errorf("%w: duration_days must be an integer", ErrInvalid)
		}
		in.DurationDays = ptrPtr(n)
	}
	if v := get("tat_days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return domain, in, fmt.Errorf("%w: tat_days must be an integer", ErrInvalid)
		}
		in.TATDays = ptrPtr(n)
	}
	if v := get("guarantee_days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return domain, in, fmt.Errorf("%w: guarantee_days must be an integer", ErrInvalid)
		}
		in.GuaranteeDays = ptrPtr(n)
	}
	if present(idx, "vendor_id") {
		in.VendorID = ptrPtr(get("vendor_id"))
	}
	if present(idx, "notes") {
		in.Notes = ptrPtr(get("notes"))
	}
	if v := get("domain_rating"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return domain, in, fmt.Errorf("%w: domain_rating must be an integer", ErrInvalid)
		}
		in.DomainRating = ptrPtr(n)
	}
	if v := get("traffic"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return domain, in, fmt.Errorf("%w: traffic must be an integer", ErrInvalid)
		}
		in.Traffic = ptrPtr(n)
	}
	currency := strings.ToUpper(get("currency"))
	amount := get("amount_minor")
	if currency != "" || amount != "" {
		if currency == "" || amount == "" {
			return domain, in, fmt.Errorf("%w: currency and amount_minor must be sent together", ErrInvalid)
		}
		n, err := strconv.ParseInt(amount, 10, 64)
		if err != nil {
			return domain, in, fmt.Errorf("%w: amount_minor must be an integer", ErrInvalid)
		}
		in.Prices = &[]Price{{Currency: currency, AmountMinor: n}}
	}
	return domain, in, nil
}

func present(idx map[string]int, name string) bool {
	_, ok := idx[name]
	return ok
}

func splitList(s string) []string {
	if s == "" {
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
	case "1", "t", "true", "yes", "y":
		return true, nil
	case "0", "f", "false", "no", "n":
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
