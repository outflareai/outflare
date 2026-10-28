package seosync

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/customers"
)

// Link prospects: pages that could link to one of the workspace's SEO
// sites, found by any way of earning links. Every method adds to the same
// list, so a site is pitched by one campaign at a time. The site behind a
// page is a company record. Adding a prospect reads only what is stored
// (backlinks, SEO snapshots, competitors, suppressions), so it costs
// nothing.

// ProspectMethods are the ways a prospect can be found.
var ProspectMethods = []string{"competitor_gap", "broken_link", "reclaim", "competitor_dead_page",
	"resource_page", "asset", "unlinked_mention", "partner", "manual"}

// ProspectStages are a prospect's stages, in order.
var ProspectStages = []string{"prospect", "excluded", "pitched", "replied", "agreed", "live", "declined", "removed"}

// ExcludedReasons say why a prospect is never pitched.
var ExcludedReasons = []string{"links_to_us", "competitor", "customer", "partner", "opted_out", "spam", "page_gone"}

// InOutreach are the stages in which a site is being pitched: no other
// prospect on the same site may be pitched meanwhile.
var InOutreach = []string{"pitched", "replied", "agreed"}

// MaxProspectsAdded is how many prospects one call may add.
const MaxProspectsAdded = 500

// Errors pitching a prospect.
var (
	ErrSiteInOutreach = errors.New("site in outreach")
	ErrSiteOptedOut   = errors.New("site opted out")
)

// Prospect is a page to add to the list.
type Prospect struct {
	PageURL   string
	Method    string
	TargetURL string
	Evidence  map[string]any
	// What the caller knows about the linking site; nil fills in from
	// what is stored.
	DomainRating *float64
	Traffic      *float64
	SpamScore    *int
	Note         string
}

// ProspectSite is the SEO site prospects would link to.
type ProspectSite struct {
	ID      string
	BrandID *string
	Host    string
}

// ScoreFactor is one part of a prospect's score.
type ScoreFactor struct {
	Code   string `json:"code"`
	Points int    `json:"points"`
}

// methodPoints favour the methods that most often end in a link: a site
// that linked before, or that knows the brand, says yes more often.
var methodPoints = map[string]int{
	"reclaim": 25, "partner": 20, "unlinked_mention": 20, "broken_link": 15, "competitor_dead_page": 15,
	"competitor_gap": 10, "resource_page": 10, "manual": 10, "asset": 5,
}

// ProspectScore is how worth pitching a page is, from 0 to 100, with why:
// half from the site's strength, a quarter from its search traffic and a
// quarter from the method, less points for a suspect spam score. Once the
// AI page reader has judged how well the page fits the site (fit, 0 to
// 100), that moves it up to 15 points either way.
func ProspectScore(method string, rating, traffic *float64, spam, fit *int) (int, []ScoreFactor) {
	factors := []ScoreFactor{}
	if rating != nil {
		factors = append(factors, ScoreFactor{"domain_rating", int(math.Round(math.Max(0, math.Min(100, *rating)) / 2))})
	}
	if traffic != nil && *traffic > 0 {
		// A million visits a month or more is the full 25.
		factors = append(factors, ScoreFactor{"traffic", int(math.Round(math.Min(1, math.Log10(*traffic+1)/6) * 25))})
	}
	factors = append(factors, ScoreFactor{"method_" + method, methodPoints[method]})
	if spam != nil && *spam >= SpamSuspect {
		factors = append(factors, ScoreFactor{"spam_suspect", -20})
	}
	if fit != nil {
		factors = append(factors, ScoreFactor{"relevance", int(math.Round(float64(*fit-50) * 0.3))})
	}
	score := 0
	for _, f := range factors {
		score += f.Points
	}
	return max(0, min(100, score)), factors
}

// ProspectPage checks a page URL: http or https with a domain, without its
// fragment. It returns the page and its domain, or "" for both.
func ProspectPage(raw string) (string, string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", ""
	}
	d := customers.CompanyDomain(u.Host)
	if d == "" {
		return "", ""
	}
	u.Fragment, u.RawFragment = "", ""
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), d
}

// OwnDomain reports whether domain is the site's own (or a subdomain).
func OwnDomain(site ProspectSite, domain string) bool {
	own := LinkTarget(site.Host)
	return domain == own || strings.HasSuffix(domain, "."+own)
}

// ProspectsAdded is what AddProspects did.
type ProspectsAdded struct {
	IDs      []string // every page's prospect, in the order given
	Added    int
	Existing int
	Excluded int // of those added
}

type siteFacts struct {
	rating, traffic *float64
	spam            *int
	linksToUs       bool
	competitor      bool
	suppressed      string
}

// AddProspects adds pages to the list for site, in tx: each page's
// company record (added as a prospect company when nobody has one), the
// site's numbers from what is stored when the caller has none, a score,
// and why it must never be pitched, if so: it already links to the site,
// it is a tracked competitor, its domain is suppressed, or its links are
// toxic. A page already on the list for the site is kept as it is. Pages
// must have passed ProspectPage and not be the site's own. Each page added
// records link_prospect.found through rec (SystemRecorder when no person
// or token asked); createdBy is the actor who asked, or nil.
func AddProspects(ctx context.Context, tx pgx.Tx, ws string, site ProspectSite, source string, createdBy *string, rec Recorder, list []Prospect) (ProspectsAdded, error) {
	out := ProspectsAdded{IDs: make([]string, len(list))}
	if len(list) == 0 {
		return out, nil
	}
	companies := make([]customers.Company, 0, len(list))
	domains := make([]string, len(list))
	for i, p := range list {
		_, d := ProspectPage(p.PageURL)
		domains[i] = d
		companies = append(companies, customers.Company{Domain: d})
	}
	records, err := customers.EnsureCompanies(ctx, tx, ws, site.BrandID, "link_prospects", companies)
	if err != nil {
		return out, err
	}
	facts, err := prospectFacts(ctx, tx, ws, site.ID, uniq(domains))
	if err != nil {
		return out, err
	}
	for i, p := range list {
		page, d := ProspectPage(p.PageURL)
		f := facts[d]
		rating, traffic, spam := p.DomainRating, p.Traffic, p.SpamScore
		if rating == nil {
			rating = f.rating
		}
		if traffic == nil {
			traffic = f.traffic
		}
		if spam == nil {
			spam = f.spam
		}
		score, factors := ProspectScore(p.Method, rating, traffic, spam, nil)
		var reason *string
		switch {
		case f.suppressed != "":
			reason = &f.suppressed
		case f.competitor:
			reason = ptr("competitor")
		// Winning back a lost link, or asking for a link where the site
		// only names the brand, is about a site that may link already.
		case f.linksToUs && p.Method != "reclaim" && p.Method != "unlinked_mention":
			reason = ptr("links_to_us")
		case spam != nil && *spam >= SpamToxic:
			reason = ptr("spam")
		}
		stage := "prospect"
		if reason != nil {
			stage = "excluded"
		}
		evidence := p.Evidence
		if evidence == nil {
			evidence = map[string]any{}
		}
		ev, _ := json.Marshal(evidence)
		fb, _ := json.Marshal(factors)
		var id string
		var added bool
		err := tx.QueryRow(ctx, `
			INSERT INTO link_prospects (workspace_id, site_id, brand_id, record_id, page_url, method, target_url, evidence,
				domain_rating, traffic, spam_score, score, score_factors, stage, excluded_reason, source, note, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
			ON CONFLICT (site_id, page_url) DO UPDATE SET updated_at = link_prospects.updated_at
			RETURNING id::text, xmax = 0`,
			ws, site.ID, site.BrandID, records[d], page, p.Method, p.TargetURL, ev, rating, traffic, spam, score, fb, stage, reason,
			source, p.Note, createdBy).Scan(&id, &added)
		if err != nil {
			return out, err
		}
		out.IDs[i] = id
		if !added {
			out.Existing++
			continue
		}
		out.Added++
		data := map[string]any{"site_id": site.ID, "brand_id": site.BrandID, "record_id": records[d], "method": p.Method,
			"score": score, "stage": stage, "excluded_reason": reason, "source": source}
		if reason != nil {
			out.Excluded++
		}
		props := map[string]any{"site_id": site.ID, "method": p.Method, "stage": stage, "score": score, "excluded_reason": reason}
		if err := rec(ctx, tx, "link_prospect.found", id, data, props); err != nil {
			return out, err
		}
	}
	return out, nil
}

// prospectFacts reads what is stored about each linking domain: its
// numbers from the site's backlinks or the SEO snapshots, whether it
// links to the site now, whether it is a tracked competitor, and the
// suppression that keeps it out.
func prospectFacts(ctx context.Context, tx pgx.Tx, ws, site string, domains []string) (map[string]siteFacts, error) {
	out := map[string]siteFacts{}
	rows, err := tx.Query(ctx, `
		SELECT d,
			COALESCE(b.rating, o.rating, r.rating), COALESCE(b.traffic, o.traffic), b.spam, COALESCE(b.live, false),
			EXISTS (SELECT 1 FROM seo_competitors c WHERE c.workspace_id = $1 AND c.site_id = $2 AND c.host = d AND c.status = 'tracked'),
			(SELECT CASE WHEN s.reason IN ('competitor', 'customer', 'partner') THEN s.reason ELSE 'opted_out' END
			 FROM suppressions s WHERE s.workspace_id = $1 AND s.kind = 'domain' AND (s.value = d OR d LIKE '%.' || s.value)
			 ORDER BY (s.reason IN ('competitor', 'customer', 'partner')) DESC LIMIT 1)
		FROM unnest($3::text[]) AS d
		LEFT JOIN LATERAL (
			SELECT max(source_rating) AS rating, max(domain_traffic) AS traffic, max(spam_score) AS spam, bool_or(status = 'live') AS live
			FROM backlinks WHERE workspace_id = $1 AND site_id = $2 AND source_domain = d
		) b ON true
		LEFT JOIN LATERAL (
			SELECT (metrics->>'domain_rating')::float AS rating, (metrics->>'organic_traffic')::float AS traffic
			FROM seo_snapshots WHERE workspace_id = $1 AND report = 'overview' AND key = '' AND host = d
			ORDER BY day DESC LIMIT 1
		) o ON true
		LEFT JOIN LATERAL (
			SELECT (metrics->>'domain_rating')::float AS rating
			FROM seo_snapshots WHERE workspace_id = $1 AND report = 'backlinks' AND regexp_replace(lower(key), '^www\.', '') = d
			ORDER BY day DESC LIMIT 1
		) r ON true`, ws, site, domains)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		var f siteFacts
		var suppressed *string
		if err := rows.Scan(&d, &f.rating, &f.traffic, &f.spam, &f.linksToUs, &f.competitor, &suppressed); err != nil {
			return nil, err
		}
		if suppressed != nil {
			f.suppressed = *suppressed
		}
		out[d] = f
	}
	return out, rows.Err()
}

// CheckPitch says whether a prospect on the company record may be pitched
// now, in tx: no other prospect on the same site is being pitched or was
// declined within the workspace's cool-down (LinkSettings), and the site's domain is not suppressed
// (opt-outs win, even after the prospect was added). It locks the record
// until tx ends, so two pitches to one site never pass together.
func CheckPitch(ctx context.Context, tx pgx.Tx, ws, prospectID, recordID, domain string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('link_prospects:' || $1))`, recordID); err != nil {
		return err
	}
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM link_prospects
		WHERE workspace_id = $1 AND record_id = $2 AND id <> $3
		  AND (stage = ANY($4) OR (stage = 'declined' AND ended_at > now() - make_interval(days => COALESCE(
			(SELECT pitch_cooldown_days FROM link_building_settings WHERE workspace_id = $1), 60)))))`,
		ws, recordID, prospectID, InOutreach).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return ErrSiteInOutreach
	}
	var suppressed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM suppressions
		WHERE workspace_id = $1 AND kind = 'domain' AND ($2 = value OR $2 LIKE '%.' || value))`, ws, domain).Scan(&suppressed); err != nil {
		return err
	}
	if suppressed {
		return ErrSiteOptedOut
	}
	return nil
}

func uniq(list []string) []string {
	out := slices.Clone(list)
	slices.Sort(out)
	return slices.Compact(out)
}

func ptr[T any](v T) *T { return &v }

// Recorder writes the audit event for action on id in tx; the target is
// the action's object (link_prospect, link_read).
// props are its product event properties: ids, enums and counts only.
type Recorder func(ctx context.Context, tx pgx.Tx, action, id string, data, props map[string]any) error

// SystemRecorder records actions nobody asked for, such as a playbook's
// job adding what it found.
func SystemRecorder(ws string) Recorder {
	return func(ctx context.Context, tx pgx.Tx, action, id string, data, props map[string]any) error {
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, Action: action, TargetType: TargetOf(action), TargetID: id, ActorType: "system", Data: data, Props: props})
	}
}

// TargetOf is an action's object: link_read for link_read.done.
func TargetOf(action string) string {
	t, _, _ := strings.Cut(action, ".")
	return t
}

// ReclaimRequest picks the lost links to win back.
type ReclaimRequest struct {
	// Days: links lost in this many days.
	Days int
	// MinRating leaves out weaker linking sites; 0 keeps them all.
	MinRating float64
}

// ReclaimProspects adds the site's links lost in the last req.Days as
// reclaim prospects, at most MaxProspectsAdded, strongest first: their page
// is where the link was, their page to offer is what it linked to, and the
// evidence names the backlink and why the provider says it went. Links
// someone marked to disavow, or that look toxic, are left out. It costs
// nothing: the links are already stored.
func ReclaimProspects(ctx context.Context, tx pgx.Tx, ws string, site ProspectSite, req ReclaimRequest, createdBy *string, rec Recorder) (ProspectsAdded, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (b.source_url) b.id::text, b.source_url, b.target_url, b.anchor, b.source_rating, b.domain_traffic, b.spam_score,
			b.lost_at, COALESCE((SELECT e.reason FROM backlink_events e WHERE e.backlink_id = b.id AND e.kind = 'lost' ORDER BY e.at DESC LIMIT 1), '')
		FROM backlinks b
		WHERE b.workspace_id = $1 AND b.site_id = $2 AND b.status = 'lost' AND b.lost_at > now() - make_interval(days => $3)
		  AND b.review <> 'disavow' AND COALESCE(b.spam_score, 0) < $4 AND COALESCE(b.source_rating, 0) >= $5
		ORDER BY b.source_url, b.source_rating DESC NULLS LAST, b.lost_at DESC`,
		ws, site.ID, req.Days, SpamToxic, req.MinRating)
	if err != nil {
		return ProspectsAdded{}, err
	}
	type lost struct {
		p      Prospect
		rating float64
	}
	var found []lost
	for rows.Next() {
		var id, from, to, anchor, reason string
		var rating, traffic *float64
		var spam *int
		var lostAt *time.Time
		if err := rows.Scan(&id, &from, &to, &anchor, &rating, &traffic, &spam, &lostAt, &reason); err != nil {
			rows.Close()
			return ProspectsAdded{}, err
		}
		page, d := ProspectPage(from)
		if page == "" || OwnDomain(site, d) {
			continue
		}
		target, td := ProspectPage(to)
		if !OwnDomain(site, td) {
			target = ""
		}
		ev := map[string]any{"backlink_id": id, "anchor": trunc(anchor, 200), "lost_reason": reason}
		if lostAt != nil {
			ev["lost_at"] = lostAt.UTC().Format(time.RFC3339)
		}
		r := 0.0
		if rating != nil {
			r = *rating
		}
		found = append(found, lost{Prospect{PageURL: page, Method: "reclaim", TargetURL: target, Evidence: ev, DomainRating: rating, Traffic: traffic, SpamScore: spam}, r})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ProspectsAdded{}, err
	}
	slices.SortStableFunc(found, func(a, b lost) int { return cmpDesc(a.rating, b.rating) })
	list := make([]Prospect, 0, min(len(found), MaxProspectsAdded))
	for _, f := range found[:min(len(found), MaxProspectsAdded)] {
		list = append(list, f.p)
	}
	return AddProspects(ctx, tx, ws, site, "reclaim", createdBy, rec, list)
}

func cmpDesc(a, b float64) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	}
	return 0
}
