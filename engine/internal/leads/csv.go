// Package leads imports and stores the people a workspace emails.
package leads

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
)

// Lead is one parsed lead. Custom holds every column that is not a standard
// field, keyed by its normalised header, for use as a template variable.
type Lead struct {
	Line        int
	Email       string
	FirstName   string
	LastName    string
	Company     string
	Title       string
	Phone       string
	LinkedInURL string
	Website     string
	Custom      map[string]string
}

// RowError reports a line that could not be imported.
type RowError struct {
	Line  int    `json:"line"`
	Email string `json:"email,omitempty"`
	Error string `json:"error"`
}

// Header names accepted for each standard field, after normalisation. They
// cover exports from Apollo, Clay, Sales Navigator tools, HubSpot and the
// common sequencers.
var aliases = map[string][]string{
	"email":        {"email", "email address", "work email", "business email", "person email", "contact email", "e mail"},
	"first_name":   {"first name", "firstname", "first", "given name"},
	"last_name":    {"last name", "lastname", "last", "surname", "family name"},
	"name":         {"name", "full name", "contact name", "person name"},
	"company":      {"company", "company name", "organization", "organisation", "organization name", "account name", "employer"},
	"title":        {"title", "job title", "position", "role", "headline"},
	"phone":        {"phone", "phone number", "mobile", "mobile phone", "direct phone", "work phone"},
	"linkedin_url": {"linkedin", "linkedin url", "person linkedin url", "linkedin profile", "profile url"},
	"website":      {"website", "company website", "domain", "company domain", "website url"},
}

const (
	// MaxRows bounds one import.
	MaxRows = 100_000
	// maxCustom bounds the extra columns kept per lead.
	maxCustom = 50
	// maxValue bounds any single value.
	maxValue = 2000
)

// NormaliseKey turns a header into a lower snake_case key, e.g.
// "Company Size (est.)" becomes "company_size_est".
func NormaliseKey(h string) string {
	h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
	var b strings.Builder
	sep := false
	for _, r := range h {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if sep && b.Len() > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
			sep = false
		} else {
			sep = true
		}
	}
	return b.String()
}

// ParseCSV reads leads from CSV with a header row. Rows with a bad email are
// returned as errors; the rest are returned in file order (duplicates
// included, see Dedupe).
func ParseCSV(r io.Reader) ([]Lead, []RowError, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err == io.EOF {
		return nil, nil, errors.New("the file is empty")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read header: %w", err)
	}
	header = append([]string(nil), header...)

	col := map[string]int{}
	custom := map[int]string{}
	for i, h := range header {
		key := NormaliseKey(h)
		spaced := strings.ReplaceAll(key, "_", " ")
		field := ""
		for f, names := range aliases {
			for _, a := range names {
				if spaced == a {
					field = f
				}
			}
		}
		switch {
		case field != "":
			if _, taken := col[field]; !taken {
				col[field] = i
			}
		case key != "" && len(custom) < maxCustom:
			custom[i] = key
		}
	}
	if _, ok := col["email"]; !ok {
		return nil, nil, errors.New("no email column found; the header row needs a column named email")
	}

	var leads []Lead
	var bad []RowError
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line, _ := cr.FieldPos(0)
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				line = pe.StartLine
			}
			bad = append(bad, RowError{Line: line, Error: err.Error()})
			continue
		}
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		if len(leads)+len(bad) >= MaxRows {
			return leads, bad, fmt.Errorf("an import can hold at most %d leads; split the file", MaxRows)
		}
		get := func(field string) string {
			if i, ok := col[field]; ok && i < len(rec) {
				return clip(rec[i])
			}
			return ""
		}
		l := Lead{
			Line: line, Email: get("email"), FirstName: get("first_name"), LastName: get("last_name"),
			Company: get("company"), Title: get("title"), Phone: get("phone"),
			LinkedInURL: get("linkedin_url"), Website: get("website"),
		}
		if l.FirstName == "" && l.LastName == "" {
			l.FirstName, l.LastName = splitName(get("name"))
		}
		for i, key := range custom {
			if i < len(rec) {
				if v := clip(rec[i]); v != "" {
					if l.Custom == nil {
						l.Custom = map[string]string{}
					}
					l.Custom[key] = v
				}
			}
		}
		if err := l.Validate(); err != nil {
			bad = append(bad, RowError{Line: line, Email: l.Email, Error: err.Error()})
			continue
		}
		leads = append(leads, l)
	}
	return leads, bad, nil
}

// Validate checks the email address.
func (l *Lead) Validate() error {
	l.Email = strings.TrimSpace(l.Email)
	if l.Email == "" {
		return errors.New("missing email")
	}
	a, err := mail.ParseAddress(l.Email)
	if err != nil || a.Address != l.Email || !strings.Contains(l.Email[strings.LastIndexByte(l.Email, '@')+1:], ".") {
		return fmt.Errorf("invalid email %q", l.Email)
	}
	return nil
}

// Dedupe keeps one lead per email (case-insensitive). A later row fills the
// fields an earlier one left empty, so a file listing a person twice merges
// rather than loses data. It returns how many rows were merged away.
func Dedupe(in []Lead) ([]Lead, int) {
	index := map[string]int{}
	out := in[:0:0]
	for _, l := range in {
		k := strings.ToLower(l.Email)
		i, seen := index[k]
		if !seen {
			index[k] = len(out)
			out = append(out, l)
			continue
		}
		o := &out[i]
		for _, p := range [][2]*string{
			{&o.FirstName, &l.FirstName}, {&o.LastName, &l.LastName}, {&o.Company, &l.Company},
			{&o.Title, &l.Title}, {&o.Phone, &l.Phone}, {&o.LinkedInURL, &l.LinkedInURL}, {&o.Website, &l.Website},
		} {
			if *p[1] != "" {
				*p[0] = *p[1]
			}
		}
		for k, v := range l.Custom {
			if o.Custom == nil {
				o.Custom = map[string]string{}
			}
			o.Custom[k] = v
		}
	}
	return out, len(in) - len(out)
}

func splitName(n string) (string, string) {
	n = strings.TrimSpace(n)
	if i := strings.LastIndexByte(n, ' '); i > 0 {
		return strings.TrimSpace(n[:i]), strings.TrimSpace(n[i+1:])
	}
	return n, ""
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxValue {
		s = s[:maxValue]
	}
	return s
}
