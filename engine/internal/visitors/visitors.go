// Package visitors works out who is on a workspace's website: the people
// it already knows and the companies they work at, from the page events the
// site snippet (/site.js) sends.
//
// A visitor is known when the workspace's own product identifies them, or
// when they arrive from a tracked link in an email Outflare sent them: the
// click redirect adds a short signed token (ofvisit) to the address, and the
// snippet hands it back with the page view. The token is signed by this
// server, so a site key, which anyone can read, still never names a person
// on its own.
//
// Each visitor (one browser's anonymous id) has a row in site_visitors with
// their counts, so the Visitors page, scoring and alerts never scan events.
package visitors

import (
	"context"
	"errors"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/secrets"
)

// Querier is a pool or a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Param is the address parameter the click token travels in.
const Param = "ofvisit"

const tokenPurpose = "site_visit"

// TokenTTL is how long after the click a token still names its visitor.
const TokenTTL = 7 * 24 * time.Hour

// Token signs a click: the click's id and a signature, both URL-safe.
func Token(box *secrets.Box, clickID int64) string {
	id := strconv.FormatInt(clickID, 36)
	return id + "." + box.Sign(tokenPurpose, id)
}

// ParseToken returns the click id a genuine token names.
func ParseToken(box *secrets.Box, token string) (int64, bool) {
	id, sig, ok := strings.Cut(token, ".")
	if !ok || id == "" || len(id) > 13 || len(sig) > 40 || !box.Verify(tokenPurpose, id, sig) {
		return 0, false
	}
	n, err := strconv.ParseInt(id, 36, 64)
	return n, err == nil && n > 0
}

// linkPurpose signs visitor links, which the workspace puts in messages
// it sends itself.
const linkPurpose = "visitor_link"

// linkPrefix starts a visitor link's token, so it never reads as a click's.
const linkPrefix = "v_"

// LinkTTL is how long a visitor link names its person by default, and
// LinkMaxTTL the longest a workspace may ask for.
const (
	LinkTTL    = 30 * 24 * time.Hour
	LinkMaxTTL = 365 * 24 * time.Hour
)

// LinkToken signs a visitor link's id. It travels in the same parameter
// as a click's token, so the site snippet needs nothing new.
func LinkToken(box *secrets.Box, id int64) string {
	s := strconv.FormatInt(id, 36)
	return linkPrefix + s + "." + box.Sign(linkPurpose, s)
}

// IsLinkToken reports whether a token is a visitor link's rather than a
// click's.
func IsLinkToken(token string) bool { return strings.HasPrefix(token, linkPrefix) }

// ParseLinkToken returns the visitor link a genuine link token names.
func ParseLinkToken(box *secrets.Box, token string) (int64, bool) {
	rest, ok := strings.CutPrefix(token, linkPrefix)
	if !ok {
		return 0, false
	}
	id, sig, ok := strings.Cut(rest, ".")
	if !ok || id == "" || len(id) > 13 || len(sig) > 40 || !box.Verify(linkPurpose, id, sig) {
		return 0, false
	}
	n, err := strconv.ParseInt(id, 36, 64)
	return n, err == nil && n > 0
}

// WithToken adds the token to a link's address, keeping its fragment.
func WithToken(link, token string) string {
	u, err := url.Parse(link)
	if err != nil {
		return link
	}
	q := u.Query()
	q.Set(Param, token)
	u.RawQuery = q.Encode()
	return u.String()
}

// Settings are a workspace's choices for the Visitors page.
type Settings struct {
	// KeyPages are paths that show buying intent, such as /pricing. A
	// trailing * matches anything after it.
	KeyPages []string `json:"key_pages"`
	// HotScore is the score from which a visitor counts as hot (1 to 100).
	HotScore int `json:"hot_score"`
	// EmailClicks turns on knowing visitors from their email clicks.
	EmailClicks bool `json:"email_clicks"`
	// ExcludeDomains are companies left out: the workspace's own team,
	// competitors. People with an email there and companies with that
	// domain are not listed.
	ExcludeDomains []string `json:"exclude_domains"`
	// ExcludeCustomers leaves out people and companies that already pay.
	ExcludeCustomers bool `json:"exclude_customers"`
	// RevealCompanies looks up the company behind unknown visitors'
	// addresses with the workspace's own provider. Off until the workspace
	// turns it on.
	RevealCompanies bool `json:"reveal_companies"`
	// RevealIntegrationID is the connection that looks them up; nil means
	// the first one that can.
	RevealIntegrationID *string `json:"reveal_integration_id"`
	// RevealDailyLimit caps lookups a day (UTC).
	RevealDailyLimit int `json:"reveal_daily_limit"`
	// NotCompanies are domains marked as not a company, such as an ISP
	// whose customers' traffic a provider took for the ISP's own.
	NotCompanies []string `json:"not_companies"`
	// RevealPeople takes in the people a connected visitor identification
	// tool (RB2B, Vector, Warmly) names. Off until the workspace turns it on
	// and accepts the notice.
	RevealPeople bool `json:"reveal_people"`
	// When and by whom the notice was accepted.
	PeopleNoticeAcceptedAt *time.Time `json:"people_notice_accepted_at"`
	PeopleNoticeAcceptedBy *string    `json:"-"`
	UpdatedAt              *time.Time `json:"updated_at"`
}

// DefaultKeyPages are the pages most sites mean by buying intent.
var DefaultKeyPages = []string{"/pricing*", "/plans*", "/demo*", "/contact*", "/book*", "/compare*", "/vs*", "/alternatives*", "/enterprise*", "/signup*", "/trial*"}

// Defaults are the settings of a workspace that has not changed them.
func Defaults() Settings {
	return Settings{KeyPages: DefaultKeyPages, HotScore: 60, EmailClicks: true, ExcludeDomains: []string{}, RevealCompanies: false, RevealDailyLimit: 500, NotCompanies: []string{}}
}

// inBrand keeps the settings row of brand $2: a brand id, or "" for the
// workspace's first brand (none when it has no brand).
const inBrand = `brand_id IS NOT DISTINCT FROM COALESCE(NULLIF($2, '')::uuid, outflare_first_brand($1))`

// Load reads a brand's settings ("": the workspace's first brand's), or
// the defaults.
func Load(ctx context.Context, q Querier, ws, brand string) (Settings, error) {
	s := Defaults()
	var at time.Time
	err := q.QueryRow(ctx, `SELECT key_pages, hot_score, email_clicks, exclude_domains, exclude_customers,
			reveal_companies, reveal_integration_id::text, reveal_daily_limit, not_companies,
			reveal_people, people_notice_accepted_at, people_notice_accepted_by::text, updated_at
		FROM visitor_settings WHERE workspace_id = $1 AND `+inBrand, ws, brand).Scan(&s.KeyPages, &s.HotScore, &s.EmailClicks, &s.ExcludeDomains, &s.ExcludeCustomers,
		&s.RevealCompanies, &s.RevealIntegrationID, &s.RevealDailyLimit, &s.NotCompanies,
		&s.RevealPeople, &s.PeopleNoticeAcceptedAt, &s.PeopleNoticeAcceptedBy, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.UpdatedAt = &at
	return s, nil
}

// Save stores a brand's settings ("": the workspace's first brand's).
func Save(ctx context.Context, q Querier, ws, brand string, s Settings) error {
	tag, err := q.Exec(ctx, `UPDATE visitor_settings SET key_pages = $3, hot_score = $4, email_clicks = $5, exclude_domains = $6, exclude_customers = $7,
			reveal_companies = $8, reveal_integration_id = $9, reveal_daily_limit = $10, not_companies = $11,
			reveal_people = $12, people_notice_accepted_at = $13, people_notice_accepted_by = $14, updated_at = now()
		WHERE workspace_id = $1 AND `+inBrand,
		ws, brand, s.KeyPages, s.HotScore, s.EmailClicks, s.ExcludeDomains, s.ExcludeCustomers, s.RevealCompanies, s.RevealIntegrationID, s.RevealDailyLimit, s.NotCompanies,
		s.RevealPeople, s.PeopleNoticeAcceptedAt, s.PeopleNoticeAcceptedBy)
	if err != nil || tag.RowsAffected() > 0 {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO visitor_settings (workspace_id, brand_id, key_pages, hot_score, email_clicks, exclude_domains, exclude_customers,
			reveal_companies, reveal_integration_id, reveal_daily_limit, not_companies,
			reveal_people, people_notice_accepted_at, people_notice_accepted_by)
		VALUES ($1, COALESCE(NULLIF($2, '')::uuid, outflare_first_brand($1)), $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		ws, brand, s.KeyPages, s.HotScore, s.EmailClicks, s.ExcludeDomains, s.ExcludeCustomers, s.RevealCompanies, s.RevealIntegrationID, s.RevealDailyLimit, s.NotCompanies,
		s.RevealPeople, s.PeopleNoticeAcceptedAt, s.PeopleNoticeAcceptedBy)
	return err
}

// Matches reports whether a domain is one of domains or under one.
func Matches(domain string, domains []string) bool {
	for _, x := range domains {
		if domain == x || strings.HasSuffix(domain, "."+x) {
			return true
		}
	}
	return false
}

// CleanPattern normalises a key page pattern a person typed: a path that
// starts with /, lower case, at most one * at the end. ok is false for
// anything else.
func CleanPattern(p string) (string, bool) {
	p = strings.ToLower(strings.TrimSpace(p))
	if u, err := url.Parse(p); err == nil && u.Host != "" {
		p = u.Path
	}
	if p == "" || p[0] != '/' || len(p) > 200 || strings.ContainsAny(p, "?# ") {
		return "", false
	}
	if i := strings.IndexByte(p, '*'); i >= 0 && i != len(p)-1 {
		return "", false
	}
	return p, true
}

// KeyPage reports whether a path matches one of the patterns.
func KeyPage(p string, patterns []string) bool {
	p = strings.ToLower(p)
	if p == "" {
		return false
	}
	if p != "/" {
		p = strings.TrimSuffix(path.Clean(p), "/")
	}
	for _, pat := range patterns {
		if prefix, ok := strings.CutSuffix(pat, "*"); ok {
			if strings.HasPrefix(p, prefix) {
				return true
			}
		} else if p == strings.TrimSuffix(pat, "/") || (pat == "/" && p == "/") {
			return true
		}
	}
	return false
}

// Activity is what a visitor, person or company did on the site.
type Activity struct {
	Pageviews    int
	KeyPageviews int
	Sessions     int
	LastSeen     time.Time
	// People is how many different people from a company visited.
	People int
}

// Score rates buying intent from 0 to 100: views of key pages count most,
// then return visits, then reading, and it fades over two weeks without a
// visit. A company counts each extra person from it too.
func Score(a Activity, now time.Time) int {
	if a.Pageviews == 0 {
		return 0
	}
	s := 10.0
	s += min(float64(a.KeyPageviews)*15, 45)
	s += min(float64(max(a.Sessions-1, 0))*8, 24)
	s += min(float64(a.Pageviews)*1.5, 15)
	s += min(float64(max(a.People-1, 0))*6, 18)
	days := now.Sub(a.LastSeen).Hours() / 24
	switch {
	case days > 30:
		s *= 0.2
	case days > 14:
		s *= 0.5
	case days > 7:
		s *= 0.75
	}
	return int(min(s, 100) + 0.5)
}

// Seen is one page view to count.
type Seen struct {
	Workspace   string
	AnonymousID string
	RecordID    *string
	BrandID     *string
	Path        string
	At          time.Time
	// IdentifiedBy is how the visitor became known on this view: product
	// (the workspace's own sign-in) or email_click. Empty: as before.
	IdentifiedBy string
	// NewSession is set when the view started a visit (site_sessions).
	NewSession bool
}

// Count adds a page view to the visitor's row. Key pages are counted with
// the workspace's settings.
func Count(ctx context.Context, q Querier, set Settings, v Seen) error {
	key := 0
	if KeyPage(v.Path, set.KeyPages) {
		key = 1
	}
	by := v.IdentifiedBy
	if by == "" && v.RecordID != nil {
		by = "product"
	}
	path := v.Path
	if len(path) > 500 {
		path = path[:500]
	}
	session := 0
	if v.NewSession {
		session = 1
	}
	_, err := q.Exec(ctx, `
		INSERT INTO site_visitors (workspace_id, anonymous_id, record_id, brand_id, identified_by, first_seen_at, last_seen_at, pageviews, key_pageviews, sessions, last_path)
		VALUES ($1, $2, $3, $4, $5, $6, $6, 1, $7, GREATEST($9, 1), $8)
		ON CONFLICT (workspace_id, anonymous_id) DO UPDATE SET
			record_id = COALESCE(EXCLUDED.record_id, site_visitors.record_id),
			brand_id = COALESCE(site_visitors.brand_id, EXCLUDED.brand_id),
			identified_by = CASE WHEN site_visitors.identified_by = '' OR (EXCLUDED.identified_by <> '' AND site_visitors.record_id IS NULL)
				THEN EXCLUDED.identified_by ELSE site_visitors.identified_by END,
			sessions = site_visitors.sessions + $9,
			pageviews = site_visitors.pageviews + 1,
			key_pageviews = site_visitors.key_pageviews + EXCLUDED.key_pageviews,
			first_seen_at = LEAST(site_visitors.first_seen_at, EXCLUDED.first_seen_at),
			last_seen_at = GREATEST(site_visitors.last_seen_at, EXCLUDED.last_seen_at),
			last_path = CASE WHEN EXCLUDED.last_seen_at >= site_visitors.last_seen_at THEN EXCLUDED.last_path ELSE site_visitors.last_path END`,
		v.Workspace, v.AnonymousID, v.RecordID, v.BrandID, by, v.At, key, path, session)
	return err
}

// Identify marks a visitor as a known person, from the workspace's product
// or an email click, when their anonymous id joins a record.
func Identify(ctx context.Context, q Querier, ws, anonymousID, recordID, by string) error {
	_, err := q.Exec(ctx, `UPDATE site_visitors SET record_id = $3, identified_by = $4
		WHERE workspace_id = $1 AND anonymous_id = $2 AND (record_id IS NULL OR record_id = $3::uuid)`, ws, anonymousID, recordID, by)
	return err
}
