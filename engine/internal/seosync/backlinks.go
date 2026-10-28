package seosync

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Backlinks: every SEO connection that reads single links (Ahrefs,
// DataForSEO) reads the links to each of the workspace's SEO sites once a
// week, live ones and those lost in the last 90 days. Both providers'
// answers merge into one row per link (linking page to linked page), and
// each change to a link's state is an event that stays: new when a
// provider first finds it, lost when no provider still does, regained when
// one finds it again.

const (
	// DefaultLinkRows is how many links one provider reads per site and
	// week, strongest linking domains first.
	DefaultLinkRows = 200
	// lostLookback is how far back a read asks for lost links.
	lostLookback = 90 * 24 * time.Hour
	// eventWindow: a link seen for the first time becomes a new (or lost)
	// event only if its date is this recent, so the first read of a site
	// is a baseline, not a flood of old news.
	eventWindow = 30 * 24 * time.Hour
	// maxLinkSites caps the sites one connection reads links for.
	maxLinkSites = 25
)

// LinkSite is an SEO site whose links are read.
type LinkSite struct {
	ID      string
	Host    string
	BrandID *string
}

// LinkRun is what one provider's read of one site's links found.
type LinkRun struct {
	ID       string  `json:"id"`
	SiteID   string  `json:"site_id"`
	Host     string  `json:"host"`
	Seen     int     `json:"links_seen"`
	New      int     `json:"new"`
	Lost     int     `json:"lost"`
	Regained int     `json:"regained"`
	Cost     float64 `json:"cost"`
	Error    *string `json:"error"`
}

// LinkSites lists the active SEO sites of a workspace.
func LinkSites(ctx context.Context, pool *pgxpool.Pool, ws string) ([]LinkSite, error) {
	rows, err := pool.Query(ctx, `SELECT id::text, host, brand_id::text FROM seo_sites
		WHERE workspace_id = $1 AND status = 'active' ORDER BY host LIMIT $2`, ws, maxLinkSites)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (LinkSite, error) {
		var s LinkSite
		err := row.Scan(&s.ID, &s.Host, &s.BrandID)
		return s, err
	})
}

// LinkTarget is the domain links are read for: the site's host without
// www, so links to every subdomain count.
func LinkTarget(host string) string {
	h, _, _ := strings.Cut(host, ":")
	return strings.TrimPrefix(h, "www.")
}

// SyncSiteLinks reads one site's links from one provider and records what
// changed.
func SyncSiteLinks(ctx context.Context, pool *pgxpool.Pool, o *connectors.Opened, r connectors.SEOReader, site LinkSite, now time.Time) (LinkRun, error) {
	run := LinkRun{SiteID: site.ID, Host: site.Host}
	set := SettingsOf(o.Config)
	unit := Unit(r)
	connector := o.Connector.Definition().ID
	if err := pool.QueryRow(ctx, `INSERT INTO backlink_runs (workspace_id, site_id, integration_id, connector, unit)
		VALUES ($1, $2, $3, $4, $5) RETURNING id::text`, o.WorkspaceID, site.ID, o.ID, connector, unit).Scan(&run.ID); err != nil {
		return run, err
	}
	fail := func(err error) (LinkRun, error) {
		msg := err.Error()
		run.Error = &msg
		_, _ = pool.Exec(ctx, `UPDATE backlink_runs SET status = 'failed', error = $2, cost = $3, finished_at = now() WHERE id = $1`, run.ID, msg, run.Cost)
		return run, err
	}
	q, err := connectors.CheckSEOQuery(connectors.SEOQuery{Report: connectors.SEOLinks, Target: LinkTarget(site.Host),
		Country: set.Country, Limit: set.LinkRowsOrDefault(), Since: now.Add(-lostLookback).UTC().Format(time.DateOnly),
		Traffic: set.LinkTraffic && connector == "ahrefs"})
	if err != nil {
		return fail(err)
	}
	a, err := Fetch(ctx, pool, o, r, q, Options{Fresh: true})
	run.Cost = amountOf(unit, a.Cost)
	if err != nil {
		return fail(err)
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var before bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM backlink_runs WHERE site_id = $1 AND connector = $2 AND status = 'done')`,
			site.ID, connector).Scan(&before); err != nil {
			return err
		}
		for _, row := range a.Rows {
			if row.Link == nil {
				continue
			}
			ch, err := applyLink(ctx, tx, o.WorkspaceID, site, connector, run.ID, *row.Link, before, now)
			if err != nil {
				return err
			}
			if ch.seen {
				run.Seen++
			}
			switch ch.event {
			case "new":
				run.New++
			case "lost":
				run.Lost++
			case "regained":
				run.Regained++
			}
		}
		if err := ScoreSiteLinks(ctx, tx, o.WorkspaceID, site.ID); err != nil {
			return err
		}
		ch, err := SyncLinkFixes(ctx, tx, o.WorkspaceID, site, run.ID, true)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE backlink_runs SET status = 'done', cost = $2, links_seen = $3, new_links = $4, lost_links = $5,
			regained_links = $6, finished_at = now() WHERE id = $1`, run.ID, run.Cost, run.Seen, run.New, run.Lost, run.Regained); err != nil {
			return err
		}
		brand := ""
		if site.BrandID != nil {
			brand = *site.BrandID
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: o.WorkspaceID, ActorType: "system", Action: "backlinks.synced",
			TargetType: "seo_site", TargetID: site.ID, BrandID: brand, Data: map[string]any{
				"connector": connector, "run_id": run.ID, "links_seen": run.Seen, "new": run.New, "lost": run.Lost, "regained": run.Regained,
				"fixes_new": ch.New, "fixes_reopened": ch.Reopened, "fixes_confirmed": ch.Confirmed}})
	})
	if err != nil {
		return fail(err)
	}
	return run, nil
}

// linkChange is what one reported link did to the store.
type linkChange struct {
	seen  bool
	event string
}

// providerView is one provider's latest view of a link, kept in
// backlinks.providers under its connector id.
type providerView struct {
	Status        string   `json:"status"`
	FirstSeen     string   `json:"first_seen,omitempty"`
	LastSeen      string   `json:"last_seen,omitempty"`
	LostReason    string   `json:"lost_reason,omitempty"`
	SourceRating  *float64 `json:"source_rating,omitempty"`
	Spam          *float64 `json:"spam,omitempty"`
	ExternalLinks *float64 `json:"external_links,omitempty"`
	Title         string   `json:"title,omitempty"`
	RunID         string   `json:"run_id"`
	CheckedAt     string   `json:"checked_at"`
}

// mergeStatus is a link's state over every provider's view: live while
// any provider finds it.
func mergeStatus(views map[string]providerView) string {
	for _, v := range views {
		if v.Status == "live" {
			return "live"
		}
	}
	return "lost"
}

// mergeRating prefers Ahrefs DR, the measure people know, then any other.
func mergeRating(views map[string]providerView) *float64 {
	if v, ok := views["ahrefs"]; ok && v.SourceRating != nil {
		return v.SourceRating
	}
	for _, v := range views {
		if v.SourceRating != nil {
			return v.SourceRating
		}
	}
	return nil
}

// earliest is the first date any provider found the link.
func earliest(views map[string]providerView) *time.Time {
	var out *time.Time
	for _, v := range views {
		if t, err := time.Parse(time.DateOnly, v.FirstSeen); err == nil && (out == nil || t.Before(*out)) {
			out = &t
		}
	}
	return out
}

func dateOr(s string, fallback time.Time) time.Time {
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t
	}
	return fallback
}

func applyLink(ctx context.Context, tx pgx.Tx, ws string, site LinkSite, connector, runID string, l connectors.SEOLink, before bool, now time.Time) (linkChange, error) {
	var ch linkChange
	from, to := NormalLinkURL(l.From), NormalLinkURL(l.To)
	if from == "" || to == "" {
		return ch, nil
	}
	domain := l.SourceDomain
	if u, err := url.Parse(from); err == nil && domain == "" {
		domain = u.Hostname()
	}
	domain = strings.TrimPrefix(strings.ToLower(domain), "www.")
	if domain == LinkTarget(site.Host) || strings.HasSuffix(domain, "."+LinkTarget(site.Host)) {
		// A link from the site itself (or its subdomains) is internal.
		return ch, nil
	}
	ch.seen = true
	view := providerView{Status: "live", FirstSeen: l.FirstSeen, SourceRating: l.SourceRating, Spam: l.Spam,
		ExternalLinks: l.ExternalLinks, Title: trunc(l.Title, 300), RunID: runID, CheckedAt: now.UTC().Format(time.RFC3339)}
	if l.Lost {
		view.Status, view.LastSeen, view.LostReason = "lost", l.LastSeen, l.LostReason
	}

	var id, oldStatus string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT id::text, status, providers FROM backlinks WHERE site_id = $1 AND source_url = $2 AND target_url = $3 FOR UPDATE`,
		site.ID, from, to).Scan(&id, &oldStatus, &raw)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ch, err
	}
	views := map[string]providerView{}
	if exists {
		_ = json.Unmarshal(raw, &views)
	}
	views[connector] = view
	status := mergeStatus(views)
	rating := mergeRating(views)
	provJSON, _ := json.Marshal(views)

	var lostAt, regainedAt *time.Time
	switch {
	case !exists:
		at := dateOr(l.FirstSeen, now)
		if status == "lost" {
			t := dateOr(l.LastSeen, now)
			lostAt = &t
			if now.Sub(t) <= eventWindow {
				ch.event = "lost"
			}
		} else if (l.FirstSeen != "" && now.Sub(at) <= eventWindow) || (l.FirstSeen == "" && before) {
			ch.event = "new"
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO backlinks (workspace_id, site_id, brand_id, source_url, target_url, source_domain, anchor, nofollow, sponsored, ugc,
				status, source_rating, first_seen_at, lost_at, providers, checked_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) RETURNING id::text`,
			ws, site.ID, site.BrandID, from, to, trunc(domain, 253), trunc(l.Anchor, 500), l.Nofollow, l.Sponsored, l.UGC,
			status, rating, earliest(views), lostAt, provJSON, now).Scan(&id)
		if err != nil {
			return ch, err
		}
	default:
		switch {
		case oldStatus == "live" && status == "lost":
			ch.event = "lost"
			t := dateOr(l.LastSeen, now)
			lostAt = &t
		case oldStatus == "lost" && status == "live":
			ch.event = "regained"
			regainedAt = &now
		}
		_, err = tx.Exec(ctx, `
			UPDATE backlinks SET brand_id = $2, source_domain = $3, anchor = $4, nofollow = $5, sponsored = $6, ugc = $7, status = $8,
				source_rating = $9, first_seen_at = $10, providers = $11, checked_at = $12, updated_at = now(),
				lost_at = CASE WHEN $8 = 'live' THEN NULL ELSE COALESCE($13, lost_at) END,
				regained_at = COALESCE($14, regained_at)
			WHERE id = $1`,
			id, site.BrandID, trunc(domain, 253), trunc(l.Anchor, 500), l.Nofollow, l.Sponsored, l.UGC, status, rating, earliest(views), provJSON, now,
			lostAt, regainedAt)
		if err != nil {
			return ch, err
		}
	}
	// Ahrefs' details win, being the fuller; another provider's fill in
	// only links Ahrefs has not seen.
	if _, ahrefs := views["ahrefs"]; connector == "ahrefs" || !ahrefs {
		if err := storeDetails(ctx, tx, id, l, now); err != nil {
			return ch, err
		}
	}
	if ch.event == "" {
		return ch, nil
	}
	at := now
	switch ch.event {
	case "new":
		at = dateOr(l.FirstSeen, now)
	case "lost":
		at = *lostAt
	}
	_, err = tx.Exec(ctx, `INSERT INTO backlink_events (workspace_id, site_id, backlink_id, kind, at, connector, run_id, source_rating, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, ws, site.ID, id, ch.event, at, connector, runID, rating, view.LostReason)
	return ch, err
}

// storeDetails keeps a provider's details of a link: what the linking page
// is and how strong, and where the linked URL leads. Traffic stays from an
// earlier read when this one did not ask for it.
func storeDetails(ctx context.Context, tx pgx.Tx, id string, l connectors.SEOLink, now time.Time) error {
	hops := []connectors.SEOHop{}
	for _, h := range l.Hops {
		if u := NormalLinkURL(h.URL); u != "" && len(hops) < 10 {
			hops = append(hops, connectors.SEOHop{Kind: trunc(h.Kind, 20), URL: u})
		}
	}
	hopsJSON, _ := json.Marshal(hops)
	var lastSeen *time.Time
	if l.LastChecked != "" {
		t := dateOr(l.LastChecked, now)
		lastSeen = &t
	}
	_, err := tx.Exec(ctx, `
		UPDATE backlinks SET url_rating = $2, domain_traffic = COALESCE($3, domain_traffic), page_traffic = COALESCE($4, page_traffic),
			page_keywords = $5, page_referring_domains = COALESCE($6, page_referring_domains), linked_domains = $7, external_links = $8,
			link_type = $9, in_content = $10, page_types = $11, page_categories = $12, languages = $13, platforms = $14,
			title = $15, author = $16, hops = $17, last_seen_at = COALESCE($18, last_seen_at)
		WHERE id = $1`,
		id, l.URLRating, l.DomainTraffic, l.PageTraffic, l.PageKeywords, l.PageRefDomains, l.LinkedDomains, l.ExternalLinks,
		trunc(l.LinkType, 20), l.InContent, short(l.PageTypes, 10, 200), short(l.PageCategories, 10, 200), short(l.Languages, 10, 20),
		short(l.Platforms, 10, 100), trunc(l.Title, 500), trunc(l.Author, 200), hopsJSON, lastSeen)
	return err
}

// short caps a list and each of its values.
func short(list []string, n, each int) []string {
	out := []string{}
	for _, v := range list {
		if v = strings.TrimSpace(v); v != "" && len(out) < n {
			out = append(out, trunc(v, each))
		}
	}
	return out
}

// NormalLinkURL tidies a link's URL so both providers' copies match: the
// scheme and host lowercase, no fragment, no default port.
func NormalLinkURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Host = strings.TrimSuffix(strings.TrimSuffix(u.Host, ":80"), ":443")
	u.Fragment, u.RawFragment = "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	return trunc(u.String(), 2000)
}

// LinkRowsOrDefault is how many links a read asks for.
func (s Settings) LinkRowsOrDefault() int {
	if s.LinkRows != nil && *s.LinkRows > 0 {
		return *s.LinkRows
	}
	return DefaultLinkRows
}
