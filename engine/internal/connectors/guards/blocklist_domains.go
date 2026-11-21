package guards

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// A company is checked against the blocklist by its domain, before anyone
// there is known, from the service's domains export: the list of every
// blocked domain, taken once a day and kept in memory for each connection
// (a snapshot), as a company's own outreach tools keep it.
//
//	GET <base URL>/api/v1/exports?format=csv&view=domains
//	Authorization: Bearer <key>
//
//	200, Content-Type: text/csv, X-Generated-At: <RFC 3339 or HTTP date>
//	domain
//	blocked.example
//	...
//
// One column named domain, one lower-case domain a row. A company is
// blocked when its domain, or a domain it is under, is on the list. The
// snapshot is downloaded again on the first check of each UTC day; a
// failed download keeps the last one. It is used only while the export
// it came from is under a day old: an older one, or none, clears nobody.
var (
	// exportWait is how long the download may take, the body included.
	exportWait = 45 * time.Second
	// exportMaxAge is how old an export may be and still clear anyone, and
	// exportMaxAhead how far ahead its time may be.
	exportMaxAge   = 24 * time.Hour
	exportMaxAhead = 5 * time.Minute
	// exportRetry is how long a failed download waits before it is tried
	// again.
	exportRetry = time.Minute
	// exportMaxBytes is the largest export read.
	exportMaxBytes int64 = 64 << 20
	// clock is the time, for tests.
	clock = time.Now
)

// domainShape is a domain as the export writes it.
var domainShape = regexp.MustCompile(`^[a-z0-9.-]+\.[a-z0-9-]+$`)

// domainSnapshot is one connection's domains export.
type domainSnapshot struct {
	mu        sync.Mutex
	domains   map[string]bool
	generated time.Time
	// fetched is when it was downloaded, failed when a download last
	// failed and code why.
	fetched, failed time.Time
	code            string
	retryAfter      time.Duration
}

// snapshots holds each connection's snapshot, by a hash of its address and
// key, so a new key or address starts afresh and no key is kept as such.
var snapshots sync.Map

func snapshotFor(base, key string) *domainSnapshot {
	sum := sha256.Sum256([]byte(base + "\x00" + key))
	s, _ := snapshots.LoadOrStore(string(sum[:]), &domainSnapshot{})
	return s.(*domainSnapshot)
}

// sameDay says whether a and b are on the same UTC day.
func sameDay(a, b time.Time) bool {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}

// blockedDomains returns the domains export to check companies against, or
// why there is none that may be used.
func (a blocklistAPI) blockedDomains(ctx context.Context) (map[string]bool, *c.GuardError) {
	const name = "The outreach blocklist"
	s := snapshotFor(a.base, a.key)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := clock()
	if (s.domains == nil || !sameDay(s.fetched, now)) && (s.failed.IsZero() || now.Sub(s.failed) >= exportRetry) {
		domains, generated, gerr := a.downloadDomains(ctx)
		if gerr != nil {
			// The last snapshot stays.
			s.failed, s.code, s.retryAfter = now, gerr.Code, gerr.RetryAfter
		} else {
			s.domains, s.generated, s.fetched, s.failed, s.code, s.retryAfter = domains, generated, now, time.Time{}, "", 0
		}
	}
	if s.domains == nil {
		return nil, fail(name, s.code, s.retryAfter)
	}
	if age := now.Sub(s.generated); age > exportMaxAge || age < -exportMaxAhead {
		code := "stale_export"
		if s.code != "" {
			code = s.code
		}
		return nil, fail(name, code, s.retryAfter)
	}
	return s.domains, nil
}

// downloadDomains reads the domains export whole, or says why it can't be
// used.
func (a blocklistAPI) downloadDomains(ctx context.Context) (map[string]bool, time.Time, *c.GuardError) {
	const name = "The outreach blocklist"
	req, err := http.NewRequest(http.MethodGet, a.base+"/api/v1/exports?format=csv&view=domains", nil)
	if err != nil {
		return nil, time.Time{}, fail(name, "base_url", 0)
	}
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Accept", "text/csv")
	ans, code := send(ctx, a.cl, req, exportWait, exportMaxBytes)
	if code != "" {
		return nil, time.Time{}, fail(name, code, 0)
	}
	if ans.status == http.StatusUnauthorized || ans.status == http.StatusForbidden {
		// A key that may check addresses but not export: the live checks
		// still work, so the connection is not marked as refused.
		return nil, time.Time{}, &c.GuardError{Code: "export_refused", Err: c.Errf(c.ErrUnavailable,
			"%s refused its domains export (HTTP %d): its key needs the export scope. Companies wait until it is allowed.", name, ans.status)}
	}
	if ans.status != http.StatusOK {
		after := busyAfter(ans.status, ans.header, clock())
		return nil, time.Time{}, fail(name, "http_"+strconv.Itoa(ans.status), after)
	}
	if !strings.Contains(strings.ToLower(ans.header.Get("Content-Type")), "text/csv") {
		return nil, time.Time{}, fail(name, "content_type", 0)
	}
	stamp := strings.TrimSpace(ans.header.Get("X-Generated-At"))
	generated, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		if generated, err = http.ParseTime(stamp); err != nil {
			return nil, time.Time{}, fail(name, "schema", 0)
		}
	}
	domains, ok := readDomains(ans.body)
	if !ok {
		return nil, time.Time{}, fail(name, "schema", 0)
	}
	return domains, generated, nil
}

// readDomains reads a domains export: one column named domain, and a
// domain on every row. Anything else is not the export.
func readDomains(body []byte) (map[string]bool, bool) {
	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = 1
	head, err := r.Read()
	if err != nil || len(head) != 1 || strings.TrimPrefix(strings.TrimSpace(head[0]), "\ufeff") != "domain" {
		return nil, false
	}
	out := map[string]bool{}
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			return out, true
		}
		if err != nil {
			return nil, false
		}
		d := strings.ToLower(strings.TrimSpace(row[0]))
		if d == "" {
			continue
		}
		if len(d) > 253 || !domainShape.MatchString(d) {
			return nil, false
		}
		out[d] = true
	}
}

// domainBlocked says whether domain, or a domain it is under, is on the
// list.
func domainBlocked(list map[string]bool, domain string) bool {
	for d := domain; d != ""; {
		if list[d] {
			return true
		}
		i := strings.IndexByte(d, '.')
		if i < 0 {
			break
		}
		d = d[i+1:]
	}
	return false
}
