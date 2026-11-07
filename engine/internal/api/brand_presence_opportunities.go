package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/coverage"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Presence opportunities: the places worth the partnerships team's effort,
// ranked by leverage (SEO and AI answers together). Presence suggests
// them from what it already watches: pages competitors appear on where
// the brand isn't, its own placements that name the brand without a link,
// and links that were removed. Other modules suggest more through
// coverage.Suggest. Refreshing also marks the ones the brand has won.

func (s *Server) routesPresenceOpportunities(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/coverage/opportunities", s.onBrand(auth.ScopeCoverageRead, s.coverageOpportunities))
	mux.HandleFunc("PATCH /v1/brands/{brand}/coverage/opportunities/{id}", s.onBrand(auth.ScopeCoverageManage, s.coverageOpportunityUpdate))
	mux.HandleFunc("POST /v1/brands/{brand}/coverage/opportunities/refresh", s.onBrand(auth.ScopeCoverageManage, s.coverageOpportunitiesRefresh))
}

// coverageHostSQL is a URL column's site, lowercase and without www.
func coverageHostSQL(col string) string {
	return `regexp_replace(lower(substring(` + col + ` from '^[a-zA-Z]+://([^/:?#]+)')),'^www\.','')`
}

// coveragePublisherHere is SQL that is true when the brand has a publisher
// on the site in hostExpr ($1 workspace, $2 brand).
func coveragePublisherHere(hostExpr string) string {
	return `EXISTS (SELECT 1 FROM coverage_publishers pub WHERE pub.workspace_id=$1 AND pub.brand_id=$2
 AND (` + hostExpr + `=regexp_replace(lower(pub.domain),'^www\.','') OR ` + hostExpr + ` LIKE '%.'||regexp_replace(lower(pub.domain),'^www\.','')))`
}

const (
	jobOpportunitiesRefresh = "coverage.opportunities_refresh"
	// opportunitiesRefreshWindow gathers the competitor scans that finish
	// within it into one refresh of the brand's opportunities.
	opportunitiesRefreshWindow = 5 * time.Minute
)

// enqueueOpportunitiesRefresh queues one refresh of the brand's
// opportunities at the end of the current window; more calls in the same
// window return the same job.
func enqueueOpportunitiesRefresh(ctx context.Context, tx pgx.Tx, ws, brand string, now time.Time) error {
	end := now.UTC().Truncate(opportunitiesRefreshWindow).Add(opportunitiesRefreshWindow)
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobOpportunitiesRefresh, WorkspaceID: &ws, Payload: map[string]string{"brand_id": brand},
		IdempotencyKey: jobOpportunitiesRefresh + ":" + brand + ":" + end.Format(time.RFC3339), RunAt: end, MaxAttempts: 5})
	return err
}

// coverageOpportunitiesRefreshJob refreshes one brand's opportunities. Only
// one runs per brand at a time; another waits for it instead of queueing
// on the same rows.
func (s *Server) coverageOpportunitiesRefreshJob(ctx context.Context, j jobs.Job) error {
	var req struct {
		Brand string `json:"brand_id"`
	}
	if err := json.Unmarshal(j.Payload, &req); err != nil || j.WorkspaceID == nil || !validUUID(req.Brand) {
		return fmt.Errorf("%w: invalid opportunities refresh job", jobs.ErrPermanent)
	}
	ws := *j.WorkspaceID
	busy := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('coverage.opportunities_refresh:'||$1, 0))`, req.Brand).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			busy = true
			return nil
		}
		added, won, err := coverageRefreshOpportunities(ctx, tx, ws, req.Brand)
		if err != nil {
			return err
		}
		return recordWithProps(ctx, tx, nil, ws, "coverage_opportunities.refreshed", "brands", req.Brand, nil, map[string]any{"brand_id": req.Brand, "added": added, "won": won})
	})
	if err != nil {
		return err
	}
	if busy {
		return jobs.RetryLater(errors.New("another refresh of this brand is running"), time.Minute)
	}
	return nil
}

// coverageRefreshOpportunities suggests the brand's opportunities from
// Presence's own data and marks the ones it has won. It returns how many
// were new and how many were won.
func coverageRefreshOpportunities(ctx context.Context, tx pgx.Tx, ws, brand string) (int, int, error) {
	type row struct {
		url, title, kind, host, reason, placement string
		competitors                               []string
		paid                                      bool
		rating, traffic                           *float64
		rivals, cited                             int
	}
	citedSQL := `coalesce((SELECT count(*) FROM ai_citations ci JOIN ai_cohorts co ON co.id=ci.cohort_id AND co.workspace_id=ci.workspace_id
 WHERE ci.workspace_id=$1 AND co.brand_id=$2 AND ci.host=%s AND ci.sampled_at>now()-interval '90 days'),0)::int`
	rivalsSQL := `(SELECT count(DISTINCT q.competitor_id) FROM coverage_competitor_placements q WHERE q.workspace_id=$1 AND q.brand_id=$2 AND q.host=%s AND q.gone_at IS NULL)::int`
	rows, err := tx.Query(ctx, `SELECT p.url, max(p.title), max(p.kind), p.host, 'competitor_gap', '', array_agg(DISTINCT p.competitor_id::text), bool_or(p.arrangement='likely_paid'),
 max(p.domain_rating), max(p.domain_traffic), `+fmt.Sprintf(rivalsSQL, "p.host")+`, `+fmt.Sprintf(citedSQL, "p.host")+`
 FROM coverage_competitor_placements p WHERE p.workspace_id=$1 AND p.brand_id=$2 AND p.gone_at IS NULL AND NOT `+coveragePublisherHere("p.host")+`
 GROUP BY p.url, p.host
 UNION ALL
 SELECT pl.url, pl.title, pl.kind, h.host, CASE WHEN pl.link_status='removed' THEN 'link_removed' ELSE 'unlinked_mention' END, pl.id::text, '{}'::text[], false,
 (SELECT max(q.domain_rating) FROM coverage_competitor_placements q WHERE q.workspace_id=$1 AND q.brand_id=$2 AND q.host=h.host),
 (SELECT max(q.domain_traffic) FROM coverage_competitor_placements q WHERE q.workspace_id=$1 AND q.brand_id=$2 AND q.host=h.host),
 `+fmt.Sprintf(rivalsSQL, "h.host")+`, `+fmt.Sprintf(citedSQL, "h.host")+`
 FROM coverage_placements pl CROSS JOIN LATERAL (SELECT `+coverageHostSQL("pl.url")+` AS host) h
 WHERE pl.workspace_id=$1 AND pl.brand_id=$2 AND pl.url<>'' AND h.host<>'' AND pl.stage NOT IN ('archived','cancelled','declined')
 AND (pl.link_status='removed' OR (pl.mention_status='present' AND pl.link_status='missing'))
 LIMIT 5000`, ws, brand)
	if err != nil {
		return 0, 0, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.url, &x.title, &x.kind, &x.host, &x.reason, &x.placement, &x.competitors, &x.paid, &x.rating, &x.traffic, &x.rivals, &x.cited)
	})
	if err != nil {
		return 0, 0, err
	}
	// A page in another language is a copy of its English page: the
	// competitor gaps on all of a page's translations become one
	// opportunity on the English page.
	merged := make([]row, 0, len(list))
	at := map[string]int{}
	translated := map[string]bool{}
	for _, x := range list {
		if x.reason != "competitor_gap" {
			merged = append(merged, x)
			continue
		}
		english, lang := coverage.EnglishURL(x.url)
		if lang != "" {
			translated[english] = true
		}
		i, seen := at[english]
		if !seen {
			at[english] = len(merged)
			if lang != "" {
				x.title, x.host = "", ""
			}
			x.url = english
			merged = append(merged, x)
			continue
		}
		m := &merged[i]
		if lang == "" && x.title != "" {
			m.title, m.kind = x.title, x.kind
		}
		seenIDs := map[string]bool{}
		for _, id := range m.competitors {
			seenIDs[id] = true
		}
		for _, id := range x.competitors {
			if !seenIDs[id] {
				m.competitors = append(m.competitors, id)
			}
		}
		m.paid = m.paid || x.paid
		m.rating, m.traffic = maxPtr(m.rating, x.rating), maxPtr(m.traffic, x.traffic)
		m.rivals, m.cited = max(m.rivals, x.rivals), max(m.cited, x.cited)
	}
	list = merged
	// Open suggestions made on a translation before are replaced by the
	// English page's.
	if _, err := coverageDropTranslations(ctx, tx, ws, brand); err != nil {
		return 0, 0, err
	}
	added := 0
	for _, x := range list {
		evidence := map[string]any{}
		if x.reason == "competitor_gap" {
			evidence["competitor_ids"] = x.competitors
			evidence["likely_paid"] = x.paid
			if translated[x.url] {
				// Competitors were found on a translation of this page.
				evidence["translations"] = true
			}
		} else {
			evidence["placement_id"] = x.placement
		}
		_, created, err := coverage.Suggest(ctx, tx, ws, brand, coverage.Suggestion{URL: x.url, Title: x.title, Kind: x.kind, Reason: x.reason, Source: "presence",
			Evidence: evidence, PlacementID: x.placement,
			Factors: coverage.Factors{DomainRating: x.rating, DomainTraffic: x.traffic, AICitations: x.cited, Competitors: x.rivals}})
		if err == coverage.ErrNoURL {
			continue
		}
		if err != nil {
			return 0, 0, err
		}
		if created {
			added++
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE placement_opportunities o SET state='won', state_at=now(), updated_at=now()
 WHERE o.workspace_id=$1 AND o.brand_id=$2 AND o.state IN ('open','picked') AND (
  (o.reason IN ('competitor_gap','ai_cited') AND `+coveragePublisherHere("o.host")+`)
  OR (o.reason IN ('unlinked_mention','link_removed') AND EXISTS (SELECT 1 FROM coverage_placements pl
   WHERE pl.workspace_id=$1 AND pl.brand_id=$2 AND pl.id=o.placement_id AND pl.link_status='present')))`, ws, brand)
	if err != nil {
		return 0, 0, err
	}
	metrics.CoverageOpportunities(added, int(tag.RowsAffected()))
	return added, int(tag.RowsAffected()), nil
}

// coverageDropTranslations deletes the brand's open Presence gaps whose page
// is a translation (/fr/..., fr.site..., ?lang=fr); the refresh suggests the
// English page instead. Picked, won and dismissed ones stay as people left
// them. It returns how many it deleted.
func coverageDropTranslations(ctx context.Context, tx pgx.Tx, ws, brand string) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, url FROM placement_opportunities
 WHERE workspace_id=$1 AND brand_id=$2 AND state='open' AND source='presence' AND reason='competitor_gap'`, ws, brand)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id, u string
		if err := rows.Scan(&id, &u); err != nil {
			rows.Close()
			return 0, err
		}
		if _, lang := coverage.EnglishURL(u); lang != "" {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM placement_opportunities WHERE workspace_id=$1 AND brand_id=$2 AND id=ANY($3::uuid[])`, ws, brand, ids)
	return int(tag.RowsAffected()), err
}

func maxPtr(a, b *float64) *float64 {
	if a == nil || (b != nil && *b > *a) {
		return b
	}
	return a
}

// coverageOpportunitiesRefresh suggests the brand's opportunities now. It
// also runs every day with discovery and after each competitor scan.
func (s *Server) coverageOpportunitiesRefresh(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var added, won int
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if added, won, err = coverageRefreshOpportunities(r.Context(), tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "coverage_opportunities.refreshed", "brands", a.Brand, nil,
			map[string]any{"brand_id": a.Brand, "added": added, "won": won})
	})
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"added": added, "won": won})
}

// coverageOpportunities lists the brand's opportunities, highest leverage
// first. state defaults to open.
func (s *Server) coverageOpportunities(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	args := []any{a.WorkspaceID, a.Brand}
	where := "o.workspace_id=$1 AND o.brand_id=$2"
	add := func(clause string, v any) {
		args = append(args, v)
		where += " AND " + fmt.Sprintf(clause, len(args))
	}
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	if state != "all" {
		if !oneOf(state, "open", "picked", "dismissed", "won") {
			writeError(w, r, 400, "invalid_request", "state is open, picked, dismissed, won or all")
			return
		}
		add("o.state=$%d", state)
	}
	if v := q.Get("reason"); v != "" {
		if coverage.Action(v) == "" {
			writeError(w, r, 400, "invalid_request", "reason is competitor_gap, ai_cited, unlinked_mention or link_removed")
			return
		}
		add("o.reason=$%d", v)
	}
	if v := q.Get("action"); v != "" {
		if !oneOf(v, "pitch", "request_link", "reclaim_link") {
			writeError(w, r, 400, "invalid_request", "action is pitch, request_link or reclaim_link")
			return
		}
		add("o.action=$%d", v)
	}
	if v := q.Get("kind"); v != "" {
		add("o.kind=$%d", v)
	}
	if v := q.Get("min_score"); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || n < 0 || n > 100 {
			writeError(w, r, 400, "invalid_request", "min_score is a number from 0 to 100")
			return
		}
		add("o.score>=$%d", n)
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		add("(o.host ILIKE '%%'||$%[1]d||'%%' OR o.title ILIKE '%%'||$%[1]d||'%%')", v)
	}
	if v := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(q.Get("host"))), "www."); v != "" {
		add("o.host=$%d", v)
	}
	if v := q.Get("access"); v != "" {
		if !oneOf(v, "cant_buy", "earned", "paid", "free", "unknown") {
			writeError(w, r, 400, "invalid_request", "access is cant_buy, earned, paid, free or unknown")
			return
		}
		add("coalesce(o.factors->>'access','unknown')=$%d", v)
	}
	if v := q.Get("min_domain_rating"); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || n < 0 || n > 100 {
			writeError(w, r, 400, "invalid_request", "min_domain_rating is a number from 0 to 100")
			return
		}
		add("coalesce((o.factors->>'domain_rating')::float8,0)>=$%d", n)
	}
	switch q.Get("exclude_competitors") {
	case "", "false":
	case "true":
		// Competitors' own sites rarely link to a rival, so they can be hidden.
		where += " AND NOT " + coverageCompetitorSite("o.host")
	default:
		writeError(w, r, 400, "invalid_request", "exclude_competitors is true or false")
		return
	}
	order, ok := opportunitySorts[q.Get("sort")]
	if !ok {
		writeError(w, r, 400, "invalid_request", "sort is score, domain_rating, traffic, ai_citations, competitors or recent")
		return
	}
	group := q.Get("group")
	if group != "" && group != "domain" {
		writeError(w, r, 400, "invalid_request", "group is domain")
		return
	}
	limit := 50
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 200 {
			writeError(w, r, 400, "invalid_request", "limit must be a whole number from 1 to 200")
			return
		}
		limit = n
	}
	offset, ok := pageOffsetParam(w, r)
	if !ok {
		return
	}
	// Each row says how many opportunities its site has; grouped by
	// domain, a site is one row, its best opportunity.
	from := `(SELECT o.*, count(*) OVER (PARTITION BY o.host) AS site_opportunities,
 row_number() OVER (PARTITION BY o.host ORDER BY ` + order + `) AS site_rank FROM placement_opportunities o WHERE ` + where + `) o`
	if group == "domain" {
		from += " WHERE o.site_rank=1"
	}
	var total int
	if err := s.pool.QueryRow(r.Context(), "SELECT count(*) FROM "+from, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	args = append(args, limit, offset)
	rows, err := s.pool.Query(r.Context(), fmt.Sprintf(`SELECT to_jsonb(o)-'workspace_id'-'site_rank' || jsonb_build_object('publisher_id',
 (SELECT pub.id FROM coverage_publishers pub WHERE pub.workspace_id=o.workspace_id AND pub.brand_id=o.brand_id
  AND (o.host=regexp_replace(lower(pub.domain),'^www\.','') OR o.host LIKE '%%.'||regexp_replace(lower(pub.domain),'^www\.','')) ORDER BY pub.created_at LIMIT 1),
 'competitor_site', %s)
 FROM %s ORDER BY %s LIMIT $%d OFFSET $%d`, coverageCompetitorSite("o.host"), from, order, len(args)-1, len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	data, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int
	if offset+len(data) < total {
		n := offset + len(data)
		next = &n
	}
	writeJSON(w, 200, map[string]any{"data": data, "total": total, "next_cursor": nextCursor(pageOffset, next)})
}

// coverageOpportunityUpdate picks, dismisses, reopens or marks won an
// opportunity, and sets who owns it.
func (s *Server) coverageOpportunityUpdate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, 404, "not_found", "Opportunity not found")
		return
	}
	var in struct {
		State *string         `json:"state"`
		Owner json.RawMessage `json:"owner_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.State != nil && !oneOf(*in.State, "open", "picked", "dismissed", "won") {
		writeError(w, r, 400, "invalid_request", "state is open, picked, dismissed or won")
		return
	}
	setOwner, owner := len(in.Owner) > 0, (*string)(nil)
	if setOwner && string(in.Owner) != "null" {
		var o string
		if err := json.Unmarshal(in.Owner, &o); err != nil || !validUUID(o) {
			writeError(w, r, 400, "invalid_request", "owner_id is a user id or null")
			return
		}
		owner = &o
	}
	if in.State == nil && !setOwner {
		writeError(w, r, 400, "invalid_request", "set state or owner_id")
		return
	}
	var out json.RawMessage
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if owner != nil {
			var member bool
			if err := tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM memberships WHERE workspace_id=$1 AND user_id=$2 AND (role IN ('owner','admin') OR cardinality(brand_ids)=0 OR $3=ANY(brand_ids)))", a.WorkspaceID, *owner, a.Brand).Scan(&member); err != nil {
				return err
			}
			if !member {
				return errInvalid("owner_id is not a member who can see this brand")
			}
		}
		if err := tx.QueryRow(r.Context(), `UPDATE placement_opportunities SET
 state=coalesce($4,state), state_at=CASE WHEN $4::text IS NOT NULL AND $4<>state THEN now() ELSE state_at END,
 owner_id=CASE WHEN $5 THEN $6::uuid ELSE owner_id END, updated_at=now()
 WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 RETURNING to_jsonb(placement_opportunities)-'workspace_id'`,
			a.WorkspaceID, a.Brand, id, in.State, setOwner, owner).Scan(&out); err != nil {
			return err
		}
		state := ""
		if in.State != nil {
			state = *in.State
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "coverage_opportunity.updated", "placement_opportunities", id,
			map[string]any{"state": in.State, "owner_id": owner}, map[string]any{"brand_id": a.Brand, "state": state, "owner_set": setOwner})
	})
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 200, out)
}

// opportunitySorts orders opportunities; the default is leverage.
var opportunitySorts = map[string]string{
	"":              "o.score DESC, o.last_seen_at DESC, o.id",
	"score":         "o.score DESC, o.last_seen_at DESC, o.id",
	"domain_rating": "(o.factors->>'domain_rating')::float8 DESC NULLS LAST, o.score DESC, o.id",
	"traffic":       "(o.factors->>'domain_traffic')::float8 DESC NULLS LAST, o.score DESC, o.id",
	"ai_citations":  "coalesce((o.factors->>'ai_citations')::int,0) DESC, o.score DESC, o.id",
	"competitors":   "coalesce((o.factors->>'competitors')::int,0) DESC, o.score DESC, o.id",
	"recent":        "o.first_seen_at DESC, o.id",
}

// coverageCompetitorSite is SQL that is true when the site in hostExpr is
// one of the brand's tracked competitors ($1 workspace, $2 brand).
func coverageCompetitorSite(hostExpr string) string {
	return `EXISTS (SELECT 1 FROM seo_competitors sc WHERE sc.workspace_id=$1 AND sc.brand_id=$2 AND sc.status='tracked'
 AND (` + hostExpr + `=sc.host OR ` + hostExpr + ` LIKE '%.'||sc.host))`
}

func oneOf(v string, list ...string) bool {
	for _, x := range list {
		if v == x {
			return true
		}
	}
	return false
}
