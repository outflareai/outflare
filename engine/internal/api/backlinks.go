package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// Backlinks: the links other sites have to the workspace's SEO sites,
// merged from every SEO connection that reads links (package seosync), and
// the feed of links gained, lost and regained. Reads only what is stored,
// so nothing here costs money; the weekly SEO snapshot (or POST
// .../seo/{id}/sync) reads the providers.

func (s *Server) routesBacklinks(mux *http.ServeMux) {
	read := auth.ScopeSEORead
	mux.HandleFunc("GET /v1/workspaces/{ws}/backlinks", s.inBrand(read, s.listBacklinks))
	mux.HandleFunc("GET /v1/workspaces/{ws}/backlinks/events", s.inBrand(read, s.listBacklinkEvents))
	mux.HandleFunc("GET /v1/workspaces/{ws}/backlinks/summary", s.inBrand(read, s.backlinksSummary))
	mux.HandleFunc("GET /v1/workspaces/{ws}/backlinks/disavow", s.inBrand(read, s.disavowFile))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/backlinks/{backlink}", s.inBrand(auth.ScopeSEOManage, s.reviewBacklink))
}

type backlinkView struct {
	ID           string                     `json:"id"`
	SiteID       string                     `json:"site_id"`
	BrandID      *string                    `json:"brand_id"`
	SourceURL    string                     `json:"source_url"`
	TargetURL    string                     `json:"target_url"`
	SourceDomain string                     `json:"source_domain"`
	Anchor       string                     `json:"anchor"`
	Nofollow     bool                       `json:"nofollow"`
	Sponsored    bool                       `json:"sponsored"`
	UGC          bool                       `json:"ugc"`
	Status       string                     `json:"status"`
	SourceRating *float64                   `json:"source_rating"`
	FirstSeenAt  *time.Time                 `json:"first_seen_at"`
	LostAt       *time.Time                 `json:"lost_at"`
	RegainedAt   *time.Time                 `json:"regained_at"`
	CheckedAt    time.Time                  `json:"checked_at"`
	Providers    map[string]json.RawMessage `json:"providers"`
	// SpamScore is 0 (clean) to 100 (spam), SpamLevel clean, suspect or
	// toxic, and SpamFactors why; Review is a person's verdict.
	SpamScore   *int            `json:"spam_score"`
	SpamLevel   *string         `json:"spam_level"`
	SpamFactors json.RawMessage `json:"spam_factors"`
	Review      string          `json:"review"`
	ReviewNote  string          `json:"review_note"`
	ReviewedAt  *time.Time      `json:"reviewed_at"`
	// The linking page: its strength (UR), traffic, keywords, how many
	// domains link to it and it links to, what kind of page it is.
	URLRating            *float64 `json:"url_rating"`
	DomainTraffic        *float64 `json:"domain_traffic"`
	PageTraffic          *float64 `json:"page_traffic"`
	PageKeywords         *float64 `json:"page_keywords"`
	PageReferringDomains *float64 `json:"page_referring_domains"`
	LinkedDomains        *float64 `json:"linked_domains"`
	ExternalLinks        *float64 `json:"external_links"`
	Title                string   `json:"title"`
	Author               string   `json:"author"`
	PageTypes            []string `json:"page_types"`
	PageCategories       []string `json:"page_categories"`
	Languages            []string `json:"languages"`
	Platforms            []string `json:"platforms"`
	// The link: its kind, whether it sits in the main content, and where
	// the linked URL leads (redirects).
	LinkType   string          `json:"link_type"`
	InContent  bool            `json:"in_content"`
	Hops       json.RawMessage `json:"hops"`
	LastSeenAt *time.Time      `json:"last_seen_at"`
	// GroupLinks is how many links the row stands for when the list is
	// grouped (one link per domain or page); 1 otherwise.
	GroupLinks int `json:"group_links"`
}

const backlinkCols = `b.id::text, b.site_id::text, b.brand_id::text, b.source_url, b.target_url, b.source_domain, b.anchor, b.nofollow,
	b.sponsored, b.ugc, b.status, b.source_rating, b.first_seen_at, b.lost_at, b.regained_at, b.checked_at, b.providers,
	b.spam_score, b.spam_factors, b.review, b.review_note, b.reviewed_at, b.url_rating, b.domain_traffic, b.page_traffic, b.page_keywords,
	b.page_referring_domains, b.linked_domains, b.external_links, b.title, b.author, b.page_types, b.page_categories, b.languages,
	b.platforms, b.link_type, b.in_content, b.hops, b.last_seen_at`

func scanBacklink(row pgx.Row) (backlinkView, error) { return scanBacklinkWith(row) }

// scanBacklinkWith scans backlinkCols, then extra columns after them.
func scanBacklinkWith(row pgx.Row, extra ...any) (backlinkView, error) {
	var v backlinkView
	var raw []byte
	err := row.Scan(append([]any{&v.ID, &v.SiteID, &v.BrandID, &v.SourceURL, &v.TargetURL, &v.SourceDomain, &v.Anchor, &v.Nofollow,
		&v.Sponsored, &v.UGC, &v.Status, &v.SourceRating, &v.FirstSeenAt, &v.LostAt, &v.RegainedAt, &v.CheckedAt, &raw,
		&v.SpamScore, &v.SpamFactors, &v.Review, &v.ReviewNote, &v.ReviewedAt, &v.URLRating, &v.DomainTraffic, &v.PageTraffic, &v.PageKeywords,
		&v.PageReferringDomains, &v.LinkedDomains, &v.ExternalLinks, &v.Title, &v.Author, &v.PageTypes, &v.PageCategories, &v.Languages,
		&v.Platforms, &v.LinkType, &v.InContent, &v.Hops, &v.LastSeenAt}, extra...)...)
	v.GroupLinks = 1
	v.Providers = map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &v.Providers)
	if v.SpamScore != nil {
		l := seosync.SpamLevel(*v.SpamScore)
		v.SpamLevel = &l
	}
	return v, err
}

var backlinkOrders = map[string]string{
	"rating":         "b.source_rating DESC NULLS LAST, b.id",
	"url_rating":     "b.url_rating DESC NULLS LAST, b.source_rating DESC NULLS LAST, b.id",
	"domain_traffic": "b.domain_traffic DESC NULLS LAST, b.source_rating DESC NULLS LAST, b.id",
	"page_traffic":   "b.page_traffic DESC NULLS LAST, b.source_rating DESC NULLS LAST, b.id",
	"keywords":       "b.page_keywords DESC NULLS LAST, b.source_rating DESC NULLS LAST, b.id",
	"newest":         "b.first_seen_at DESC NULLS LAST, b.id",
	"oldest":         "b.first_seen_at ASC NULLS LAST, b.id",
	"last_seen":      "b.last_seen_at DESC NULLS LAST, b.id",
	"lost":           "b.lost_at DESC NULLS LAST, b.id",
	"spam":           "b.spam_score DESC NULLS LAST, b.source_rating DESC NULLS LAST, b.id",
}

// groupBest picks the link that stands for its group (a domain or a page):
// a live one first, then followed, in the main content, from the strongest
// page.
const groupBest = `(b.status = 'live') DESC, (NOT b.nofollow) DESC, b.in_content DESC, b.url_rating DESC NULLS LAST, b.first_seen_at, b.id`

var backlinkLinkTypes = []string{"text", "image", "redirect", "canonical", "alternate", "frame", "form", "rss"}

// spamRanges are the spam filter's score ranges.
var spamRanges = map[string][2]int{
	"clean":   {0, seosync.SpamSuspect - 1},
	"suspect": {seosync.SpamSuspect, seosync.SpamToxic - 1},
	"toxic":   {seosync.SpamToxic, 100},
}

// offsetParam reads ?offset=, 0 when unset.
func offsetParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.URL.Query().Get("offset")
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "offset is a whole number of 0 or more")
		return 0, false
	}
	return n, true
}

// siteParam reads ?site_id=, "" when unset.
func siteParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	site := r.URL.Query().Get("site_id")
	if site != "" && !validUUID(site) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "site_id is an SEO site's id")
		return "", false
	}
	return site, true
}

func (s *Server) listBacklinks(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	site, ok := siteParam(w, r)
	if !ok {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	args := []any{a.WorkspaceID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := `b.workspace_id = $1`
	if site != "" {
		where += ` AND b.site_id = ` + arg(site) + `::uuid`
	}

	status := q.Get("status")
	if status != "" && status != "live" && status != "lost" {
		bad("status is live or lost")
		return
	}
	// History: since (a date) adds the links lost since then to the live
	// ones; change=new lists links first seen since then, change=lost
	// links lost since then.
	var since *time.Time
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			bad("since is a date, like 2026-01-31")
			return
		}
		since = &t
	}
	change := q.Get("change")
	switch change {
	case "":
	case "new", "lost":
		if since == nil {
			t := time.Now().UTC().AddDate(0, 0, -30).Truncate(24 * time.Hour)
			since = &t
		}
	default:
		bad("change is new or lost")
		return
	}
	switch {
	case change == "new":
		where += ` AND b.first_seen_at >= ` + arg(*since)
	case change == "lost":
		where += ` AND b.status = 'lost' AND b.lost_at >= ` + arg(*since)
	case status == "" && since != nil:
		where += ` AND (b.status = 'live' OR b.lost_at >= ` + arg(*since) + `)`
	}
	if status != "" {
		where += ` AND b.status = ` + arg(status)
	}

	switch follow := q.Get("nofollow"); follow {
	case "":
	case "true", "false":
		where += ` AND b.nofollow = ` + arg(follow == "true")
	default:
		bad("nofollow is true or false")
		return
	}
	switch q.Get("follow") {
	case "":
	case "dofollow":
		where += ` AND NOT b.nofollow`
	case "nofollow":
		where += ` AND b.nofollow`
	case "ugc":
		where += ` AND b.ugc`
	case "sponsored":
		where += ` AND b.sponsored`
	default:
		bad("follow is dofollow, nofollow, ugc or sponsored")
		return
	}
	if t := q.Get("link_type"); t != "" {
		if t == "content" {
			where += ` AND b.in_content`
		} else if !slices.Contains(backlinkLinkTypes, t) {
			bad("link_type is content, " + strings.Join(backlinkLinkTypes, ", "))
			return
		} else {
			where += ` AND b.link_type = ` + arg(t)
		}
	}
	for _, rg := range []struct{ param, col string }{
		{"dr", "b.source_rating"}, {"ur", "b.url_rating"}, {"domain_traffic", "b.domain_traffic"}, {"page_traffic", "b.page_traffic"},
	} {
		for _, side := range []string{"min", "max"} {
			v := q.Get(rg.param + "_" + side)
			if v == "" {
				continue
			}
			n, err := strconv.ParseFloat(v, 64)
			if err != nil || n < 0 {
				bad(rg.param + "_" + side + " is a number of 0 or more")
				return
			}
			op := ">="
			if side == "max" {
				op = "<="
			}
			where += ` AND ` + rg.col + ` ` + op + ` ` + arg(n)
		}
	}
	if t := strings.TrimSpace(q.Get("page_type")); t != "" {
		if len(t) > 100 || strings.ContainsAny(t, "/,") {
			bad("page_type is a top-level page type, like Article")
			return
		}
		where += ` AND EXISTS (SELECT 1 FROM unnest(b.page_types) p WHERE split_part(p, '/', 2) = ` + arg(t) + `)`
	}
	if d := strings.ToLower(strings.TrimSpace(q.Get("source_domain"))); d != "" {
		where += ` AND b.source_domain = ` + arg(strings.TrimPrefix(d, "www."))
	}
	if search := strings.ToLower(strings.TrimSpace(q.Get("q"))); search != "" {
		p := arg(search)
		in := map[string]string{
			"":           `(strpos(b.source_domain, ` + p + `) > 0 OR strpos(lower(b.source_url), ` + p + `) > 0 OR strpos(lower(b.anchor), ` + p + `) > 0 OR strpos(lower(b.title), ` + p + `) > 0 OR strpos(lower(b.target_url), ` + p + `) > 0)`,
			"source_url": `strpos(lower(b.source_url), ` + p + `) > 0`,
			"target_url": `strpos(lower(b.target_url), ` + p + `) > 0`,
			"anchor":     `strpos(lower(b.anchor), ` + p + `) > 0`,
			"title":      `strpos(lower(b.title), ` + p + `) > 0`,
		}
		cond, known := in[q.Get("q_in")]
		if !known {
			bad("q_in is source_url, target_url, anchor or title")
			return
		}
		where += ` AND ` + cond
	}
	if spam := q.Get("spam"); spam != "" {
		rng, known := spamRanges[spam]
		if !known {
			bad("spam is clean, suspect or toxic")
			return
		}
		where += ` AND b.spam_score BETWEEN ` + arg(rng[0]) + ` AND ` + arg(rng[1])
	}
	switch review := q.Get("review"); review {
	case "":
	case "none":
		where += ` AND b.review = ''`
	case "ok", "disavow":
		where += ` AND b.review = ` + arg(review)
	default:
		bad("review is none, ok or disavow")
		return
	}
	where += sel.sql("b.brand_id", &args)

	order := q.Get("order")
	if order == "" {
		order = "rating"
	}
	orderBy, known := backlinkOrders[order]
	if !known {
		bad("order is " + strings.Join(slices.Sorted(maps.Keys(backlinkOrders)), ", "))
		return
	}
	group := q.Get("group")
	partition := map[string]string{"": "", "all": "", "domain": "b.source_domain", "page": "b.source_url"}[group]
	if _, known := map[string]bool{"": true, "all": true, "domain": true, "page": true}[group]; !known {
		bad("group is all, domain or page")
		return
	}
	limit, err := limitParam(r, 100, 500)
	if s.allocError(w, r, err) {
		return
	}
	offset, ok := offsetParam(w, r)
	if !ok {
		return
	}

	countSQL := `SELECT count(*) FROM backlinks b WHERE ` + where
	from := `backlinks b WHERE ` + where
	if partition != "" {
		countSQL = `SELECT count(DISTINCT ` + partition + `) FROM backlinks b WHERE ` + where
		from = `(SELECT b.*, count(*) OVER (PARTITION BY ` + partition + `) AS group_links,
			row_number() OVER (PARTITION BY ` + partition + ` ORDER BY ` + groupBest + `) AS group_rank
			FROM backlinks b WHERE ` + where + `) b WHERE b.group_rank = 1`
	}
	var total int
	if err := s.pool.QueryRow(r.Context(), countSQL, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	groupCol := `1`
	if partition != "" {
		groupCol = `b.group_links`
	}
	page := `SELECT ` + backlinkCols + `, ` + groupCol + ` FROM ` + from + ` ORDER BY ` + orderBy + ` LIMIT ` + arg(limit) + ` OFFSET ` + arg(offset)
	rows, err := s.pool.Query(r.Context(), page, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (backlinkView, error) {
		var n int
		v, err := scanBacklinkWith(row, &n)
		v.GroupLinks = n
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []backlinkView{}
	}
	var next *int
	if offset+len(list) < total {
		n := offset + len(list)
		next = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_offset": next})
}

type backlinkEventLink struct {
	ID           string `json:"id"`
	SourceURL    string `json:"source_url"`
	TargetURL    string `json:"target_url"`
	SourceDomain string `json:"source_domain"`
	Anchor       string `json:"anchor"`
	Nofollow     bool   `json:"nofollow"`
	Status       string `json:"status"`
}

type backlinkEventView struct {
	ID           string            `json:"id"`
	SiteID       string            `json:"site_id"`
	Kind         string            `json:"kind"`
	At           time.Time         `json:"at"`
	Connector    string            `json:"connector"`
	SourceRating *float64          `json:"source_rating"`
	Reason       string            `json:"reason"`
	Backlink     backlinkEventLink `json:"backlink"`
}

var backlinkEventKinds = []string{"new", "lost", "regained"}

func (s *Server) listBacklinkEvents(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	site, ok := siteParam(w, r)
	if !ok {
		return
	}
	kind := q.Get("kind")
	if kind != "" && !slices.Contains(backlinkEventKinds, kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is new, lost or regained")
		return
	}
	days := 90
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 730 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 730")
			return
		}
		days = n
	}
	limit, err := limitParam(r, 100, 500)
	if s.allocError(w, r, err) {
		return
	}
	offset, ok := offsetParam(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, site, kind, days, limit + 1, offset}
	rows, err := s.pool.Query(r.Context(), `
		SELECT e.id::text, e.site_id::text, e.kind, e.at, e.connector, e.source_rating, e.reason,
			b.id::text, b.source_url, b.target_url, b.source_domain, b.anchor, b.nofollow, b.status
		FROM backlink_events e JOIN backlinks b ON b.id = e.backlink_id
		WHERE e.workspace_id = $1 AND ($2 = '' OR e.site_id = NULLIF($2, '')::uuid) AND ($3 = '' OR e.kind = $3)
		  AND e.at > now() - make_interval(days => $4)`+sel.sql("b.brand_id", &args)+`
		ORDER BY e.at DESC, e.id DESC LIMIT $5 OFFSET $6`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (backlinkEventView, error) {
		var v backlinkEventView
		err := row.Scan(&v.ID, &v.SiteID, &v.Kind, &v.At, &v.Connector, &v.SourceRating, &v.Reason,
			&v.Backlink.ID, &v.Backlink.SourceURL, &v.Backlink.TargetURL, &v.Backlink.SourceDomain, &v.Backlink.Anchor, &v.Backlink.Nofollow, &v.Backlink.Status)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int
	if len(list) > limit {
		list = list[:limit]
		n := offset + limit
		next = &n
	}
	if list == nil {
		list = []backlinkEventView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_offset": next})
}

type backlinkCounts struct {
	New      int `json:"new"`
	Lost     int `json:"lost"`
	Regained int `json:"regained"`
}

// backlinkSpamCounts counts a site's live links by spam level, and those
// people reviewed.
type backlinkSpamCounts struct {
	Toxic      int `json:"toxic"`
	Suspect    int `json:"suspect"`
	Unreviewed int `json:"unreviewed"`
	Disavowed  int `json:"disavowed"`
}

type backlinkWeek struct {
	Week string `json:"week"`
	backlinkCounts
}

type backlinkProvider struct {
	Connector     string     `json:"connector"`
	IntegrationID *string    `json:"integration_id"`
	LastRunAt     time.Time  `json:"last_run_at"`
	Status        string     `json:"status"`
	Error         *string    `json:"error"`
	LinksSeen     int        `json:"links_seen"`
	FinishedAt    *time.Time `json:"finished_at"`
}

type backlinkSiteSummary struct {
	Site struct {
		ID      string  `json:"id"`
		Host    string  `json:"host"`
		BrandID *string `json:"brand_id"`
	} `json:"site"`
	Live             int                `json:"live"`
	Lost             int                `json:"lost"`
	ReferringDomains int                `json:"referring_domains"`
	Followed         int                `json:"followed"`
	Spam             backlinkSpamCounts `json:"spam"`
	Last30Days       backlinkCounts     `json:"last_30_days"`
	Weeks            []backlinkWeek     `json:"weeks"`
	Providers        []backlinkProvider `json:"providers"`
	// PageTypes and LinkTypes count the live links by the linking page's
	// top-level type (Article, Listing, ...) and by link type, for filters.
	PageTypes []backlinkFacet `json:"page_types"`
	LinkTypes []backlinkFacet `json:"link_types"`
	// Traffic: some links carry the linking page's traffic, so traffic
	// columns and filters mean something.
	Traffic bool `json:"traffic"`
}

type backlinkFacet struct {
	Value string `json:"value"`
	Links int    `json:"links"`
}

// summaryWeeks is how many weeks of gains and losses the summary charts.
const summaryWeeks = 12

func (s *Server) backlinksSummary(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	site, ok := siteParam(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	args := []any{a.WorkspaceID, site}
	rows, err := s.pool.Query(ctx, `SELECT id::text, host, brand_id::text FROM seo_sites
		WHERE workspace_id = $1 AND ($2 = '' OR id = NULLIF($2, '')::uuid)`+sel.sql("brand_id", &args)+` ORDER BY host`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (backlinkSiteSummary, error) {
		var v backlinkSiteSummary
		err := row.Scan(&v.Site.ID, &v.Site.Host, &v.Site.BrandID)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []backlinkSiteSummary{}
	}
	for i := range list {
		v := &list[i]
		if err := s.pool.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE status = 'live'), count(*) FILTER (WHERE status = 'lost'),
				count(DISTINCT source_domain) FILTER (WHERE status = 'live'), count(*) FILTER (WHERE status = 'live' AND NOT nofollow),
				count(*) FILTER (WHERE status = 'live' AND spam_score >= $3), count(*) FILTER (WHERE status = 'live' AND spam_score >= $4 AND spam_score < $3),
				count(*) FILTER (WHERE status = 'live' AND spam_score >= $4 AND review = ''), count(*) FILTER (WHERE review = 'disavow')
			FROM backlinks WHERE workspace_id = $1 AND site_id = $2`, a.WorkspaceID, v.Site.ID, seosync.SpamToxic, seosync.SpamSuspect).
			Scan(&v.Live, &v.Lost, &v.ReferringDomains, &v.Followed, &v.Spam.Toxic, &v.Spam.Suspect, &v.Spam.Unreviewed, &v.Spam.Disavowed); err != nil {
			s.internal(w, r, err)
			return
		}
		if err := s.pool.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE kind = 'new'), count(*) FILTER (WHERE kind = 'lost'), count(*) FILTER (WHERE kind = 'regained')
			FROM backlink_events WHERE workspace_id = $1 AND site_id = $2 AND at > now() - interval '30 days'`, a.WorkspaceID, v.Site.ID).
			Scan(&v.Last30Days.New, &v.Last30Days.Lost, &v.Last30Days.Regained); err != nil {
			s.internal(w, r, err)
			return
		}
		wk, err := s.pool.Query(ctx, `
			SELECT to_char(w.week, 'YYYY-MM-DD'),
				count(e.id) FILTER (WHERE e.kind = 'new'), count(e.id) FILTER (WHERE e.kind = 'lost'), count(e.id) FILTER (WHERE e.kind = 'regained')
			FROM generate_series(date_trunc('week', now()) - make_interval(weeks => $3 - 1), date_trunc('week', now()), interval '1 week') AS w(week)
			LEFT JOIN backlink_events e ON e.workspace_id = $1 AND e.site_id = $2 AND date_trunc('week', e.at) = w.week
			GROUP BY w.week ORDER BY w.week`, a.WorkspaceID, v.Site.ID, summaryWeeks)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if v.Weeks, err = pgx.CollectRows(wk, func(row pgx.CollectableRow) (backlinkWeek, error) {
			var x backlinkWeek
			err := row.Scan(&x.Week, &x.New, &x.Lost, &x.Regained)
			return x, err
		}); err != nil {
			s.internal(w, r, err)
			return
		}
		pr, err := s.pool.Query(ctx, `
			SELECT DISTINCT ON (connector) connector, integration_id::text, started_at, status, error, links_seen, finished_at
			FROM backlink_runs WHERE workspace_id = $1 AND site_id = $2 ORDER BY connector, started_at DESC`, a.WorkspaceID, v.Site.ID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if v.Providers, err = pgx.CollectRows(pr, func(row pgx.CollectableRow) (backlinkProvider, error) {
			var x backlinkProvider
			err := row.Scan(&x.Connector, &x.IntegrationID, &x.LastRunAt, &x.Status, &x.Error, &x.LinksSeen, &x.FinishedAt)
			return x, err
		}); err != nil {
			s.internal(w, r, err)
			return
		}
		if v.Providers == nil {
			v.Providers = []backlinkProvider{}
		}
		facet := func(sql string) ([]backlinkFacet, error) {
			rows, err := s.pool.Query(ctx, sql, a.WorkspaceID, v.Site.ID)
			if err != nil {
				return nil, err
			}
			list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (backlinkFacet, error) {
				var x backlinkFacet
				err := row.Scan(&x.Value, &x.Links)
				return x, err
			})
			if list == nil {
				list = []backlinkFacet{}
			}
			return list, err
		}
		if v.PageTypes, err = facet(`
			SELECT t, count(DISTINCT b.id) FROM backlinks b, unnest(b.page_types) p, split_part(p, '/', 2) t
			WHERE b.workspace_id = $1 AND b.site_id = $2 AND b.status = 'live' AND t <> ''
			GROUP BY t ORDER BY 2 DESC, 1 LIMIT 30`); err != nil {
			s.internal(w, r, err)
			return
		}
		if v.LinkTypes, err = facet(`
			SELECT link_type, count(*) FROM backlinks WHERE workspace_id = $1 AND site_id = $2 AND status = 'live' AND link_type <> ''
			GROUP BY link_type ORDER BY 2 DESC, 1`); err != nil {
			s.internal(w, r, err)
			return
		}
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM backlinks WHERE workspace_id = $1 AND site_id = $2 AND domain_traffic IS NOT NULL)`,
			a.WorkspaceID, v.Site.ID).Scan(&v.Traffic); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// reviewBacklink records a person's verdict on a link: ok (a real link) or
// disavow (for the disavow file), or none to clear it. domain: true applies
// it to every link from the same domain to the site.
func (s *Server) reviewBacklink(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Review string  `json:"review"`
		Note   *string `json:"note"`
		Domain bool    `json:"domain"`
	}
	if !decode(w, r, &req) {
		return
	}
	review := req.Review
	switch review {
	case "ok", "disavow":
	case "none":
		review = ""
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "review is ok, disavow or none")
		return
	}
	if req.Note != nil && len(*req.Note) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is at most 2,000 characters")
		return
	}
	id := r.PathValue("backlink")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "backlink not found")
		return
	}
	ctx := r.Context()
	args := []any{a.WorkspaceID, id}
	b, err := scanBacklink(s.pool.QueryRow(ctx, `SELECT `+backlinkCols+` FROM backlinks b WHERE b.workspace_id = $1 AND b.id = $2`+brandSel{in: a.Brands}.sql("b.brand_id", &args), args...))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "backlink not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !a.writes(nil, b.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	var n int64
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE backlinks SET review = $4, review_note = COALESCE($5, review_note), reviewed_by = $6,
				reviewed_at = CASE WHEN $4 = '' THEN NULL ELSE now() END, updated_at = now()
			WHERE workspace_id = $1 AND site_id = $2 AND (id = $3 OR ($7 AND source_domain = $8))`,
			a.WorkspaceID, b.SiteID, b.ID, review, req.Note, a.ActorID, req.Domain, b.SourceDomain)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		// A review changes what is worth doing: toxic links left to look
		// at, lost links marked spam.
		site := seosync.LinkSite{ID: b.SiteID}
		if err := tx.QueryRow(ctx, `SELECT host, brand_id::text FROM seo_sites WHERE id = $1 AND workspace_id = $2`, b.SiteID, a.WorkspaceID).Scan(&site.Host, &site.BrandID); err != nil {
			return err
		}
		if _, err := seosync.SyncLinkFixes(ctx, tx, a.WorkspaceID, site, newID(), false); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "backlink.reviewed", "backlink", b.ID, map[string]any{
			"site_id": b.SiteID, "review": req.Review, "domain": req.Domain, "links": n, "spam_score": b.SpamScore,
		})
	})
	if s.allocError(w, r, err) {
		return
	}
	args = []any{a.WorkspaceID, id}
	b, err = scanBacklink(s.pool.QueryRow(ctx, `SELECT `+backlinkCols+` FROM backlinks b WHERE b.workspace_id = $1 AND b.id = $2`, args...))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backlink": b, "updated": n})
}

// disavowFile writes a site's disavow file for Google Search Console: the
// links and domains people marked disavow, a whole domain when every link
// from it is marked. It lists nothing nobody reviewed.
func (s *Server) disavowFile(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site := r.URL.Query().Get("site_id")
	if !validUUID(site) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "site_id is an SEO site's id")
		return
	}
	ctx := r.Context()
	sites, err := s.loadSEOSites(ctx, a.WorkspaceID, &site, brandSel{in: a.Brands})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(sites) != 1 {
		writeError(w, r, http.StatusNotFound, "not_found", "site not found")
		return
	}
	rows, err := s.pool.Query(ctx, `
		SELECT source_domain, bool_and(review = 'disavow'), array_agg(source_url ORDER BY source_url) FILTER (WHERE review = 'disavow')
		FROM backlinks WHERE workspace_id = $1 AND site_id = $2
		GROUP BY source_domain HAVING bool_or(review = 'disavow') ORDER BY source_domain`, a.WorkspaceID, site)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# Disavow file for %s, from Outflare on %s.\n# Upload it in Google Search Console's disavow links tool. It lists only links people marked disavow.\n",
		sites[0].Host, time.Now().UTC().Format(time.DateOnly))
	err = forEachRow(rows, func(row pgx.CollectableRow) error {
		var domain string
		var all bool
		var urls []string
		if err := row.Scan(&domain, &all, &urls); err != nil {
			return err
		}
		if all {
			fmt.Fprintf(&out, "domain:%s\n", domain)
			return nil
		}
		for _, u := range urls {
			out.WriteString(u + "\n")
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="disavow-`+strings.NewReplacer(":", "-", "/", "-").Replace(sites[0].Host)+`.txt"`)
	_, _ = io.WriteString(w, out.String())
}

func forEachRow(rows pgx.Rows, f func(pgx.CollectableRow) error) error {
	_, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (struct{}, error) { return struct{}{}, f(row) })
	return err
}
