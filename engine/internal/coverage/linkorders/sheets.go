package linkorders

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// MaxSheetBytes caps a Google Sheet CSV export.
const MaxSheetBytes = 8 << 20

// SheetRef is a Google Sheet a user pasted: a normal sheet id, or the
// id of a sheet published to the web (File → Share → Publish to web).
type SheetRef struct {
	ID        string
	Published bool
	GID       string
}

var sheetPath = regexp.MustCompile(`^/spreadsheets/(?:u/\d+/)?d/(e/)?([A-Za-z0-9_-]{10,200})(?:/|$)`)
var gidRe = regexp.MustCompile(`(?:^|[&#?])gid=(\d{1,20})`)

// ErrSheetUnreachable means Google could not be reached or answered with
// a server error: something to retry, unlike a private sheet or a bad link.
var ErrSheetUnreachable = errors.New("Google Sheets could not be reached")

// ErrSheetPrivate means Google asked for a sign-in: the sheet is not
// readable by anyone with the link.
var ErrSheetPrivate = errors.New("the Google Sheet is not public: set General access to \"Anyone with the link\" (Viewer), or File → Share → Publish to web, then try again; or download it as CSV or .xlsx and upload the file")

// ParseSheetURL accepts a https://docs.google.com/spreadsheets/... link
// (edit, view, export or a published /d/e/... link) and returns the sheet
// it names, with the tab's gid when the link has one.
func ParseSheetURL(raw string) (SheetRef, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "docs.google.com") || u.Port() != "" || u.User != nil {
		return SheetRef{}, fmt.Errorf("%w: sheet_url must be a https://docs.google.com/spreadsheets/... link", ErrInvalid)
	}
	m := sheetPath.FindStringSubmatch(u.Path)
	if m == nil {
		return SheetRef{}, fmt.Errorf("%w: sheet_url is not a Google Sheets link (want https://docs.google.com/spreadsheets/d/<id>/...)", ErrInvalid)
	}
	ref := SheetRef{ID: m[2], Published: m[1] != ""}
	for _, s := range []string{u.RawQuery, u.Fragment} {
		if g := gidRe.FindStringSubmatch(s); g != nil {
			ref.GID = g[1]
		}
	}
	return ref, nil
}

// ExportURL is the CSV export of the sheet's tab, under base (normally
// https://docs.google.com).
func (r SheetRef) ExportURL(base string) string {
	if base == "" {
		base = "https://docs.google.com"
	}
	q := url.Values{}
	var p string
	if r.Published {
		p = "/spreadsheets/d/e/" + r.ID + "/pub"
		q.Set("output", "csv")
		q.Set("single", "true")
	} else {
		p = "/spreadsheets/d/" + r.ID + "/export"
		q.Set("format", "csv")
	}
	if r.GID != "" {
		q.Set("gid", r.GID)
	}
	return strings.TrimRight(base, "/") + p + "?" + q.Encode()
}

// FetchSheetCSV downloads a sheet tab as CSV with no credentials: it
// works for a sheet anyone with the link can view, or one published to
// the web. It never uses a Google connection or the user's Drive. base is
// for tests; production passes "". The client should be netguard's, so
// only public addresses are dialled.
func FetchSheetCSV(ctx context.Context, client *http.Client, ref SheetRef, base string) ([]byte, error) {
	allowed := func(host string) bool {
		host = strings.ToLower(host)
		if base != "" {
			if b, err := url.Parse(base); err == nil && strings.EqualFold(b.Hostname(), host) {
				return true
			}
		}
		return host == "docs.google.com" || strings.HasSuffix(host, ".googleusercontent.com")
	}
	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		h := strings.ToLower(req.URL.Hostname())
		if h == "accounts.google.com" || strings.Contains(req.URL.Path, "ServiceLogin") {
			return ErrSheetPrivate
		}
		if req.URL.Scheme != "https" && base == "" {
			return fmt.Errorf("refusing a non-https redirect")
		}
		if !allowed(h) {
			return fmt.Errorf("refusing a redirect to %s", h)
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.ExportURL(base), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/csv")
	resp, err := c.Do(req)
	if err != nil {
		if errors.Is(err, ErrSheetPrivate) {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, ErrSheetPrivate)
		}
		return nil, fmt.Errorf("%w: %w: could not read the Google Sheet, try again: %v", ErrInvalid, ErrSheetUnreachable, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: %v", ErrInvalid, ErrSheetPrivate)
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: Google Sheet not found (check the link, or the tab was deleted)", ErrInvalid)
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: %w: Google returned %d for the sheet export, try again", ErrInvalid, ErrSheetUnreachable, resp.StatusCode)
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("%w: Google returned %d for the sheet export", ErrInvalid, resp.StatusCode)
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "text/html" {
		// A sign-in or "you need access" page instead of CSV.
		return nil, fmt.Errorf("%w: %v", ErrInvalid, ErrSheetPrivate)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxSheetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w: could not read the Google Sheet, try again: %v", ErrInvalid, ErrSheetUnreachable, err)
	}
	if len(b) > MaxSheetBytes {
		return nil, fmt.Errorf("%w: the sheet export is larger than %d MB", ErrInvalid, MaxSheetBytes>>20)
	}
	return b, nil
}
