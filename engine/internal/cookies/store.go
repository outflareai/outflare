package cookies

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// maxItems is the most a site keeps track of.
const maxItems = 2000

// Seen is one thing found on a site.
type Seen struct {
	Kind string
	Name string
	// The cookie's domain, or the script's host.
	Host string
	// crawl or browser.
	Source string
	// The page it was on: a path, never a query.
	Page string
	// A browser had it before the visitor allowed its category.
	BeforeConsent bool
	// How long a cookie is kept, from its Set-Cookie, for one the
	// catalogue doesn't know.
	Retention string
	// The vendor, when it is known some other way than the catalogue's
	// patterns (the website script, by its data-key).
	Vendor string
}

// Observe records what was found on a site key's site, and reports how
// many were new. New ones change the policy, so the caller publishes after.
func Observe(ctx context.Context, tx pgx.Tx, ws, keyID string, seen []Seen) (added int, err error) {
	known := 0
	defer func() { metrics.CookiesNew(known, added-known) }()
	for _, s := range seen {
		m := Lookup(s.Kind, s.Name)
		if v, ok := VendorByID(s.Vendor); ok {
			m = Match{Vendor: v.ID, Category: v.Category, Purpose: v.Purpose}
		}
		retention := m.Retention
		if retention == "" {
			retention = s.Retention
		}
		var pages []string
		if s.Page != "" {
			pages = []string{s.Page}
		}
		var inserted bool
		// A site holds at most maxItems: the key is public, so its reports
		// can't be allowed to fill the table. What is already there is still
		// seen again.
		err := tx.QueryRow(ctx, `
			INSERT INTO site_cookies (workspace_id, site_key_id, kind, name, host, vendor, category, purpose, retention, sources, before_consent, pages)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, ARRAY[$10::text], $11, $12
			WHERE (SELECT count(*) FROM site_cookies WHERE site_key_id = $2) < $13
			   OR EXISTS (SELECT 1 FROM site_cookies WHERE site_key_id = $2 AND kind = $3 AND name = $4)
			ON CONFLICT (site_key_id, kind, name) DO UPDATE SET
				last_seen_at = now(),
				sources = CASE WHEN $10 = ANY(site_cookies.sources) THEN site_cookies.sources ELSE site_cookies.sources || $10::text END,
				before_consent = site_cookies.before_consent OR EXCLUDED.before_consent,
				pages = CASE WHEN cardinality(EXCLUDED.pages) = 0 OR cardinality(site_cookies.pages) >= 5 OR EXCLUDED.pages[1] = ANY(site_cookies.pages)
					THEN site_cookies.pages ELSE site_cookies.pages || EXCLUDED.pages END,
				host = CASE WHEN site_cookies.host = '' THEN EXCLUDED.host ELSE site_cookies.host END,
				-- The catalogue may learn an unknown one later; a person's
				-- word always stands.
				vendor = CASE WHEN site_cookies.reviewed_at IS NULL THEN EXCLUDED.vendor ELSE site_cookies.vendor END,
				category = CASE WHEN site_cookies.reviewed_at IS NULL THEN EXCLUDED.category ELSE site_cookies.category END,
				purpose = CASE WHEN site_cookies.reviewed_at IS NULL THEN EXCLUDED.purpose ELSE site_cookies.purpose END,
				retention = CASE WHEN site_cookies.reviewed_at IS NULL AND EXCLUDED.retention <> '' THEN EXCLUDED.retention ELSE site_cookies.retention END
			RETURNING (xmax = 0)`,
			ws, keyID, s.Kind, s.Name, s.Host, m.Vendor, m.Category, m.Purpose, retention, s.Source, s.BeforeConsent, pages, maxItems).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return added, err
		}
		if inserted {
			added++
			if m.Known() {
				known++
			}
		}
	}
	return added, nil
}

// Policy is a site's cookie policy as published.
type Policy struct {
	Site       string           `json:"site"`
	Categories []PolicyCategory `json:"categories"`
}

// PolicyCategory is one category of the policy with its vendors.
type PolicyCategory struct {
	ID      string         `json:"id"`
	Vendors []PolicyVendor `json:"vendors"`
}

// PolicyVendor is a vendor and what of it the site uses. A vendor id of
// "" is the site's own.
type PolicyVendor struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Company    string       `json:"company"`
	PrivacyURL string       `json:"privacy_url"`
	Purpose    string       `json:"purpose"`
	Items      []PolicyItem `json:"items"`
}

// PolicyItem is one cookie, storage key or script in the policy.
type PolicyItem struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	Purpose   string `json:"purpose"`
	Retention string `json:"retention"`
}

// gone is how long something can go unseen before it leaves the policy.
const gone = 30 * 24 * time.Hour

// Build works out a site key's policy from what was found: everything not
// ignored whose category is known and that was seen in the last 30 days.
func Build(ctx context.Context, q pgx.Tx, ws, keyID string, now time.Time) (Policy, error) {
	var p Policy
	if err := q.QueryRow(ctx, `SELECT name FROM site_keys WHERE id = $1 AND workspace_id = $2`, keyID, ws).Scan(&p.Site); err != nil {
		return p, err
	}
	rows, err := q.Query(ctx, `SELECT kind, name, host, vendor, category, purpose, retention FROM site_cookies
		WHERE site_key_id = $1 AND workspace_id = $2 AND status <> 'ignored' AND category <> 'unknown' AND last_seen_at > $3
		ORDER BY category, vendor, kind, name`, keyID, ws, now.Add(-gone))
	if err != nil {
		return p, err
	}
	type row struct{ kind, name, host, vendor, category, purpose, retention string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.kind, &x.name, &x.host, &x.vendor, &x.category, &x.purpose, &x.retention)
	})
	if err != nil {
		return p, err
	}
	for _, cat := range Categories {
		pc := PolicyCategory{ID: cat, Vendors: []PolicyVendor{}}
		byVendor := map[string]*PolicyVendor{}
		var order []string
		for _, x := range list {
			if x.category != cat {
				continue
			}
			v, ok := byVendor[x.vendor]
			if !ok {
				v = &PolicyVendor{ID: x.vendor, Name: p.Site, Items: []PolicyItem{}}
				if cv, known := VendorByID(x.vendor); known {
					v.Name, v.Company, v.PrivacyURL, v.Purpose = cv.Name, cv.Company, cv.PrivacyURL, cv.Purpose
				}
				byVendor[x.vendor] = v
				order = append(order, x.vendor)
			}
			v.Items = append(v.Items, PolicyItem{Kind: x.kind, Name: x.name, Host: x.host, Purpose: x.purpose, Retention: x.retention})
		}
		sort.SliceStable(order, func(i, j int) bool { return byVendor[order[i]].Name < byVendor[order[j]].Name })
		for _, id := range order {
			pc.Vendors = append(pc.Vendors, *byVendor[id])
		}
		p.Categories = append(p.Categories, pc)
	}
	return p, nil
}

// keys lists a policy's items as kind:name, and its vendors by category.
func (p Policy) keys() (items []string, vendors map[string][]string) {
	vendors = map[string][]string{}
	for _, c := range p.Categories {
		for _, v := range c.Vendors {
			vendors[c.ID] = append(vendors[c.ID], v.ID)
			for _, it := range v.Items {
				items = append(items, it.Kind+":"+it.Name)
			}
		}
	}
	return items, vendors
}

// Who did something, for the audit log.
type Who struct {
	ActorID   string
	ActorType string
	RequestID string
}

// Published is the result of publishing a site's policy.
type Published struct {
	Version int
	Changed bool
	Added   []string
	Removed []string
	// The consent banner asks visitors again.
	Reasked bool
}

// Publish stores the site key's policy as a new version when it differs
// from the last one. A version that adds a vendor in a category needing
// consent asks visitors again (the banner's consent_version), since what
// they agreed to has changed; a cookie dropping out doesn't.
func Publish(ctx context.Context, tx pgx.Tx, ws, keyID string, who Who, now time.Time) (Published, error) {
	var out Published
	next, err := Build(ctx, tx, ws, keyID, now)
	if err != nil {
		return out, err
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT version, content FROM cookie_policies WHERE site_key_id = $1 ORDER BY version DESC LIMIT 1 FOR UPDATE`, keyID).
		Scan(&out.Version, &raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var prev Policy
	if raw != nil {
		if err := json.Unmarshal(raw, &prev); err != nil {
			return out, err
		}
	}
	a, _ := json.Marshal(next)
	b, _ := json.Marshal(prev)
	if raw != nil && string(a) == string(b) {
		return out, nil
	}
	nowItems, nowVendors := next.keys()
	oldItems, oldVendors := prev.keys()
	for _, k := range nowItems {
		if !slices.Contains(oldItems, k) {
			out.Added = append(out.Added, k)
		}
	}
	for _, k := range oldItems {
		if !slices.Contains(nowItems, k) {
			out.Removed = append(out.Removed, k)
		}
	}
	if raw != nil {
		for _, cat := range []string{Analytics, Marketing, Preferences} {
			for _, v := range nowVendors[cat] {
				if !slices.Contains(oldVendors[cat], v) {
					out.Reasked = true
				}
			}
		}
	}
	if out.Reasked {
		tag, err := tx.Exec(ctx, `UPDATE consent_banners SET consent_version = consent_version + 1, updated_at = now() WHERE site_key_id = $1`, keyID)
		if err != nil {
			return out, err
		}
		out.Reasked = tag.RowsAffected() > 0
	}
	out.Version++
	out.Changed = true
	if out.Added == nil {
		out.Added = []string{}
	}
	if out.Removed == nil {
		out.Removed = []string{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO cookie_policies (workspace_id, site_key_id, version, content, added, removed, reasked)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, ws, keyID, out.Version, a, out.Added, out.Removed, out.Reasked); err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorID: who.ActorID, ActorType: who.ActorType, RequestID: who.RequestID,
		Action: "cookie_policy.published", TargetType: "site_key", TargetID: keyID,
		Data: map[string]any{"version": out.Version, "added": len(out.Added), "removed": len(out.Removed), "reasked": out.Reasked}})
}

// cleanPath keeps a page's path without its query or fragment.
func cleanPath(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if !strings.HasPrefix(p, "/") || len(p) > 300 {
		return ""
	}
	return p
}
