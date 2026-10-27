// Package mailbox connects sending mailboxes: bulk CSV import, provider
// defaults, connection checks over SMTP and IMAP, and DNS health.
package mailbox

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strconv"
	"strings"
)

// Row is one mailbox parsed from an import. Empty hosts and ports are filled
// from provider defaults later.
type Row struct {
	Line         int
	Email        string
	DisplayName  string
	Provider     string // google, microsoft, other or empty (detect)
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	IMAPHost     string
	IMAPPort     int
	IMAPUsername string
	IMAPPassword string
	Pool         string
	DailyLimit   int // 0 means default
	// OAuth mailboxes have no password. They sign in through an admin
	// connection or with their own refresh token.
	ConnectionID *string
	RefreshToken string
}

// OAuth reports whether the row signs in with OAuth.
func (r *Row) OAuth() bool { return r.ConnectionID != nil || r.RefreshToken != "" }

// RowError reports a line that could not be imported.
type RowError struct {
	Line  int    `json:"line"`
	Email string `json:"email,omitempty"`
	Error string `json:"error"`
}

// Column names accepted for each field, after normalisation (lower case,
// underscores and hyphens as spaces). These cover the export formats of the
// common inbox resellers and sequencers, so their files import unchanged.
var aliases = map[string][]string{
	"email":         {"email", "email address", "from email", "sender email", "mailbox", "username"},
	"name":          {"name", "display name", "from name", "sender name"},
	"first_name":    {"first name", "firstname"},
	"last_name":     {"last name", "lastname"},
	"password":      {"password", "app password", "app specific password"},
	"provider":      {"provider", "esp", "type", "email provider"},
	"smtp_host":     {"smtp host", "smtp server", "smtp"},
	"smtp_port":     {"smtp port"},
	"smtp_username": {"smtp username", "smtp user", "smtp login"},
	"smtp_password": {"smtp password", "smtp pass"},
	"imap_host":     {"imap host", "imap server", "imap"},
	"imap_port":     {"imap port"},
	"imap_username": {"imap username", "imap user", "imap login"},
	"imap_password": {"imap password", "imap pass"},
	"pool":          {"pool", "mailbox pool", "tag", "tags", "group"},
	"daily_limit":   {"daily limit", "daily sending limit", "sending limit", "max per day", "daily cap", "limit"},
}

func normaliseHeader(h string) string {
	h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
	h = strings.NewReplacer("_", " ", "-", " ").Replace(h)
	return strings.Join(strings.Fields(h), " ")
}

// MaxRows bounds one import.
const MaxRows = 5000

// ParseCSV reads mailboxes from CSV with a header row. Bad rows are returned
// as errors; good rows are still returned.
func ParseCSV(r io.Reader) ([]Row, []RowError, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err == io.EOF {
		return nil, nil, errors.New("the file is empty")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read header: %w", err)
	}

	col := map[string]int{}
	for i, h := range header {
		n := normaliseHeader(h)
		for field, names := range aliases {
			for _, a := range names {
				if n == a {
					if _, taken := col[field]; !taken {
						col[field] = i
					}
				}
			}
		}
	}
	if _, ok := col["email"]; !ok {
		return nil, nil, errors.New("no email column found; the header row needs a column named email")
	}

	var rows []Row
	var bad []RowError
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		var line int
		if pe := (*csv.ParseError)(nil); errors.As(err, &pe) {
			line = pe.StartLine
		} else {
			line, _ = cr.FieldPos(0)
		}
		if err != nil {
			bad = append(bad, RowError{Line: line, Error: err.Error()})
			continue
		}
		get := func(field string) string {
			if i, ok := col[field]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		if strings.Join(rec, "") == "" {
			continue // blank line
		}
		if len(rows)+len(bad) >= MaxRows {
			return rows, bad, fmt.Errorf("an import can hold at most %d mailboxes", MaxRows)
		}
		row, err := buildRow(line, get)
		if err != nil {
			bad = append(bad, RowError{Line: line, Email: get("email"), Error: err.Error()})
			continue
		}
		rows = append(rows, row)
	}
	return rows, bad, nil
}

func buildRow(line int, get func(string) string) (Row, error) {
	r := Row{Line: line, Email: get("email"), Pool: get("pool")}
	if a, err := mail.ParseAddress(r.Email); err != nil || a.Address != r.Email {
		return r, fmt.Errorf("invalid email %q", r.Email)
	}
	r.DisplayName = get("name")
	if r.DisplayName == "" {
		r.DisplayName = strings.TrimSpace(get("first_name") + " " + get("last_name"))
	}
	if i := strings.IndexByte(r.Pool, ','); i >= 0 { // "tags" columns may hold several
		r.Pool = strings.TrimSpace(r.Pool[:i])
	}

	var err error
	if r.Provider, err = normaliseProvider(get("provider")); err != nil {
		return r, err
	}
	r.SMTPHost, r.IMAPHost = get("smtp_host"), get("imap_host")
	if r.SMTPPort, err = optionalPort(get("smtp_port"), "smtp_port"); err != nil {
		return r, err
	}
	if r.IMAPPort, err = optionalPort(get("imap_port"), "imap_port"); err != nil {
		return r, err
	}
	r.SMTPUsername = firstNonEmpty(get("smtp_username"), r.Email)
	r.IMAPUsername = firstNonEmpty(get("imap_username"), r.SMTPUsername)
	r.SMTPPassword = firstNonEmpty(get("smtp_password"), get("password"))
	r.IMAPPassword = firstNonEmpty(get("imap_password"), get("password"), r.SMTPPassword)
	if r.SMTPPassword == "" {
		return r, errors.New("missing password (use a password or smtp_password column)")
	}
	if v := get("daily_limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			return r, fmt.Errorf("daily_limit must be 1 to 500, got %q", v)
		}
		r.DailyLimit = n
	}
	return r, nil
}

func normaliseProvider(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return "", nil
	case "google", "gmail", "google workspace", "gsuite", "g suite":
		return ProviderGoogle, nil
	case "microsoft", "outlook", "office365", "office 365", "microsoft 365", "m365", "o365":
		return ProviderMicrosoft, nil
	case "other", "smtp", "custom":
		return ProviderOther, nil
	}
	return "", fmt.Errorf("unknown provider %q (use google, microsoft or other)", v)
}

func optionalPort(v, name string) (int, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%s must be a port number, got %q", name, v)
	}
	return n, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// RowFromFields builds a row from named fields (the canonical names in the
// alias table, such as smtp_host), for mailboxes added one at a time.
func RowFromFields(fields map[string]string) (Row, error) {
	return buildRow(0, func(f string) string { return strings.TrimSpace(fields[f]) })
}
