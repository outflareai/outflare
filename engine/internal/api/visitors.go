package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/tracking"
	"github.com/outflareai/outflare/engine/internal/visitors"
)

// Website visitors: the people and companies on a workspace's site, from
// the page views its site snippet sends (site_keys.go). A visitor is known
// once the workspace's product identifies them or they arrive from a
// tracked link in an email Outflare sent them; see package visitors.

const (
	// maxVisitorCandidates is how many of the most recent visitors a list
	// scores before sorting by score.
	maxVisitorCandidates = 2000
	// visitorAlertEvery is how often one person or company can become hot.
	visitorAlertEvery = 7 * 24 * time.Hour
)

func (s *Server) routesVisitors(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/visitors", s.inBrand(read, s.listVisitors))
	mux.HandleFunc("GET /v1/workspaces/{ws}/visitors/settings", s.inWorkspace(read, s.getVisitorSettings))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/visitors/settings", s.inWorkspace(manage, s.updateVisitorSettings))
}

// withVisitToken is where a click on a tracked email link goes: the link's
// own address, plus a token naming the click when the address is the
// workspace's own site (a brand domain or a site key origin) with the
// snippet on it. Clicks that look automated, and links to anywhere else,
// get no token.
func (s *Server) withVisitToken(ctx context.Context, c tracking.Clicked) string {
	if s.box == nil || c.LikelyBot || c.ID == 0 {
		return c.Target
	}
	u, err := url.Parse(c.Target)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return c.Target
	}
	host := strings.ToLower(u.Hostname())
	var own bool
	err = s.pool.QueryRow(ctx, `
		SELECT COALESCE((SELECT email_clicks FROM visitor_settings WHERE workspace_id = $1), true)
		   AND EXISTS (SELECT 1 FROM site_keys WHERE workspace_id = $1 AND revoked_at IS NULL)
		   AND (EXISTS (SELECT 1 FROM brands b, unnest(b.domains || ARRAY[b.website]) d
				WHERE b.workspace_id = $1 AND d <> '' AND ($2 = lower(regexp_replace(d, '^(https?://)?(www\.)?([^/:]+).*$', '\3'))
					OR $2 LIKE '%.' || lower(regexp_replace(d, '^(https?://)?(www\.)?([^/:]+).*$', '\3'))))
			OR EXISTS (SELECT 1 FROM site_keys k, unnest(k.origins) o WHERE k.workspace_id = $1 AND k.revoked_at IS NULL AND o = $3))`,
		c.Workspace, host, strings.ToLower(u.Scheme+"://"+u.Host)).Scan(&own)
	if err != nil {
		s.log.Error("visit token", "err", err)
		return c.Target
	}
	if !own {
		return c.Target
	}
	return visitors.WithToken(c.Target, visitors.Token(s.box, c.ID))
}

// knownFromClick joins a browser to the person an email was sent to when
// its page view carries a genuine click token: the click is this
// workspace's, recent, not a mail scanner's, and its token was not handed
// to another browser already. The browser then joins the person's record
// the way a sign-in from the workspace's product would. Anything else is
// ignored: the page view is still counted, anonymous.
func (s *Server) knownFromClick(ctx context.Context, tx pgx.Tx, a *access, c *catalog, set visitors.Settings, anonID, token string, now time.Time,
	totals *ingestTotals) (string, error) {
	if s.box == nil || !set.EmailClicks {
		return "", nil
	}
	clickID, ok := visitors.ParseToken(s.box, token)
	if !ok {
		return "", nil
	}
	var rec *string
	var bot bool
	var at time.Time
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(r.merged_into, r.id)::text, c.likely_bot, c.clicked_at
		FROM link_clicks c JOIN tracked_links t ON t.id = c.link_id
		LEFT JOIN enrollments en ON en.id = t.enrollment_id
		LEFT JOIN leads l ON l.id = en.lead_id
		LEFT JOIN emails e ON e.id = t.email_id
		LEFT JOIN records r ON r.id = COALESCE(l.record_id, e.record_id)
		WHERE c.id = $1 AND c.workspace_id = $2 AND (c.anonymous_id IS NULL OR c.anonymous_id = $3)`,
		clickID, a.WorkspaceID, anonID).Scan(&rec, &bot, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if rec == nil || bot || now.Sub(at) > visitors.TokenTTL {
		return "", nil
	}
	// Claim the click for this browser in one statement, so two browsers
	// racing with one token can't both have it.
	tag, err := tx.Exec(ctx, `UPDATE link_clicks SET anonymous_id = $3 WHERE id = $1 AND workspace_id = $2 AND (anonymous_id IS NULL OR anonymous_id = $3)`, clickID, a.WorkspaceID, anonID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", nil
	}
	// A browser someone else already signed in on stays theirs.
	if owner, _, err := ownerOf(ctx, tx, a, "anonymous_id", anonID); err != nil {
		return "", err
	} else if owner != nil {
		return "", nil
	}
	// The browser joins the person, marked as known from an email click: a
	// forwarded email can put a colleague on it, so a later sign-in that
	// says otherwise takes the browser back (releaseClickBrowser).
	added, err := attach(ctx, tx, a, *rec, "anonymous_id", anonID, true, totals)
	if err != nil || !added {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE record_identities SET source = 'email_click' WHERE record_id = $1 AND namespace = 'anonymous_id' AND value = $2 AND valid_to IS NULL`,
		*rec, anonID); err != nil {
		return "", err
	}
	if err := backfill(ctx, tx, a, c, *rec, anonID); err != nil {
		return "", err
	}
	if err := visitors.Identify(ctx, tx, a.WorkspaceID, anonID, *rec, "email_click"); err != nil {
		return "", err
	}
	return *rec, nil
}

// countVisit adds a stored browser page view to its visitor's counts and,
// when a known person or their company turns hot, sends the alert once.
func (s *Server) countVisit(ctx context.Context, tx pgx.Tx, ws string, set visitors.Settings, brand *string, m eventMessage, rec *string, by string, newSession bool, at time.Time) error {
	var c struct {
		Page struct {
			Path string `json:"path"`
		} `json:"page"`
	}
	_ = json.Unmarshal(m.get("context"), &c)
	anon := m.text("anonymousId")
	if anon == "" {
		return nil
	}
	if err := visitors.Count(ctx, tx, set, visitors.Seen{Workspace: ws, AnonymousID: anon, RecordID: rec, BrandID: brand, Path: c.Page.Path, At: at, IdentifiedBy: by, NewSession: newSession}); err != nil {
		return err
	}
	if rec == nil {
		return nil
	}
	return s.alertIfHot(ctx, tx, ws, set, *rec, at)
}

// alertIfHot records visitor.became_hot for a person, and for the company
// they work at, whose score reached the workspace's hot score, at most once
// a week each.
func (s *Server) alertIfHot(ctx context.Context, tx pgx.Tx, ws string, set visitors.Settings, rec string, now time.Time) error {
	subjects := []string{rec}
	if co, err := companyOf(ctx, tx, rec); err != nil {
		return err
	} else if co != "" {
		subjects = append(subjects, co)
	}
	for i, id := range subjects {
		var act visitors.Activity
		var last *time.Time
		col := "sv.record_id = $2"
		if i == 1 {
			col = "(sv.account_id = $2 OR (sv.record_id IS NOT NULL AND (" + strings.ReplaceAll(companyOfSQL, "$1", "sv.record_id") + " LIMIT 1)::uuid = $2))"
		}
		err := tx.QueryRow(ctx, `SELECT COALESCE(sum(pageviews), 0), COALESCE(sum(key_pageviews), 0), COALESCE(sum(sessions), 0),
				max(last_seen_at), count(DISTINCT record_id)
			FROM site_visitors sv WHERE sv.workspace_id = $1 AND `+col, ws, id).
			Scan(&act.Pageviews, &act.KeyPageviews, &act.Sessions, &last, &act.People)
		if err != nil {
			return err
		}
		if last == nil {
			continue
		}
		act.LastSeen = *last
		score := visitors.Score(act, now)
		if score < set.HotScore {
			continue
		}
		tag, err := tx.Exec(ctx, `INSERT INTO visitor_alerts (workspace_id, record_id, kind, score, alerted_at) VALUES ($1, $2, 'hot', $3, $4)
			ON CONFLICT (record_id, kind) DO UPDATE SET score = EXCLUDED.score, alerted_at = EXCLUDED.alerted_at
			WHERE visitor_alerts.alerted_at < $4 - $5::interval`, ws, id, score, now, visitorAlertEvery.String())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		kind := "person"
		if i == 1 {
			kind = "company"
		}
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "visitor.became_hot",
			TargetType: "record", TargetID: id, RequestID: RequestID(ctx),
			Data:  map[string]any{"record_id": id, "kind": kind, "score": score, "hot_score": set.HotScore, "pageviews": act.Pageviews, "key_pageviews": act.KeyPageviews, "sessions": act.Sessions, "people": act.People},
			Props: map[string]any{"kind": kind, "score": score}}); err != nil {
			return err
		}
	}
	return nil
}

// companyOf is the company a person works at: their first company, else a
// company with their email's domain.
func companyOf(ctx context.Context, q pgx.Tx, rec string) (string, error) {
	var id string
	err := q.QueryRow(ctx, companyOfSQL+` LIMIT 1`, rec).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// companyOfSQL lists a person's ($1) companies, best first.
const companyOfSQL = `SELECT x.id::text FROM (
		SELECT rr.to_id AS id, 0 AS o, rr.created_at FROM record_relationships rr
		WHERE rr.from_id = $1 AND rr.field_key = 'companies' AND rr.valid_to IS NULL
		UNION ALL
		SELECT d.record_id, 1, d.created_at FROM record_identities e
		JOIN record_identities d ON d.workspace_id = e.workspace_id AND d.namespace = 'domain' AND d.valid_to IS NULL
			AND d.value = split_part(e.value, '@', 2)
		WHERE e.record_id = $1 AND e.namespace = 'email' AND e.valid_to IS NULL
	) x JOIN records c ON c.id = x.id AND c.merged_into IS NULL AND c.archived_at IS NULL
	ORDER BY x.o, x.created_at`

type visitorRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type visitorView struct {
	Kind         string       `json:"kind"`
	RecordID     string       `json:"record_id"`
	Name         string       `json:"name"`
	Email        *string      `json:"email"`
	Domain       *string      `json:"domain"`
	JobTitle     *string      `json:"job_title"`
	Company      *visitorRef  `json:"company"`
	People       int          `json:"people"`
	PeopleSample []visitorRef `json:"people_sample"`
	Browsers     int          `json:"browsers"`
	Pageviews    int          `json:"pageviews"`
	KeyPageviews int          `json:"key_pageviews"`
	Sessions     int          `json:"sessions"`
	FirstSeenAt  time.Time    `json:"first_seen_at"`
	LastSeenAt   time.Time    `json:"last_seen_at"`
	LastPath     string       `json:"last_path"`
	IdentifiedBy []string     `json:"identified_by"`
	Score        int          `json:"score"`
	Hot          bool         `json:"hot"`
	// For exclusions, not sent.
	customer bool
	domains  []string
}

type visitorTotals struct {
	Browsers  int `json:"browsers"`
	Known     int `json:"known"`
	People    int `json:"people"`
	Companies int `json:"companies"`
	Hot       int `json:"hot"`
}

func (s *Server) listVisitors(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	view := q.Get("view")
	if view == "" {
		view = "companies"
	}
	if view != "companies" && view != "people" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "view must be companies or people")
		return
	}
	days := 30
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days must be 1 to 365")
			return
		}
		days = n
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 200")
			return
		}
		limit = n
	}
	hotOnly := q.Get("hot") == "true"
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	set, err := visitors.Load(ctx, s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	now := time.Now()
	since := now.AddDate(0, 0, -days)
	args := []any{a.WorkspaceID, since}
	brand := sel.sql("sv.brand_id", &args)

	var totals visitorTotals
	if err := s.pool.QueryRow(ctx, `SELECT count(*), count(record_id), count(DISTINCT record_id) FROM site_visitors sv
		WHERE sv.workspace_id = $1 AND sv.last_seen_at >= $2`+brand, args...).Scan(&totals.Browsers, &totals.Known, &totals.People); err != nil {
		s.internal(w, r, err)
		return
	}

	var list []visitorView
	if view == "people" {
		list, err = s.visitorPeople(ctx, args, brand)
	} else {
		list, err = s.visitorCompanies(ctx, args, brand)
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := list[:0]
	for _, v := range list {
		if excluded(v, set) {
			continue
		}
		v.Score = visitors.Score(visitors.Activity{Pageviews: v.Pageviews, KeyPageviews: v.KeyPageviews, Sessions: v.Sessions, LastSeen: v.LastSeenAt, People: v.People}, now)
		v.Hot = v.Score >= set.HotScore
		if v.Hot {
			totals.Hot++
		}
		if hotOnly && !v.Hot {
			continue
		}
		out = append(out, v)
	}
	if view == "companies" {
		totals.Companies = len(out)
	} else if companies, err := s.visitorCompanies(ctx, args, brand); err == nil {
		for _, c := range companies {
			if !excluded(c, set) {
				totals.Companies++
			}
		}
	} else {
		s.internal(w, r, err)
		return
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].LastSeenAt.After(out[j].LastSeenAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		out = []visitorView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "totals": totals, "hot_score": set.HotScore, "days": days})
}

// excluded reports whether the workspace leaves a visitor out: a customer
// when it hides customers, or anyone at an excluded domain.
func excluded(v visitorView, set visitors.Settings) bool {
	if set.ExcludeCustomers && v.customer {
		return true
	}
	for _, d := range v.domains {
		for _, x := range set.ExcludeDomains {
			if d == x || strings.HasSuffix(d, "."+x) {
				return true
			}
		}
	}
	return false
}

// customerSQL is true for a record ($col) that pays or is marked a customer.
func customerSQL(col string) string {
	return fmt.Sprintf(`COALESCE(%[1]s.attributes->>'billing_status' IN ('active', 'trialing', 'past_due') OR %[1]s.attributes->>'lifecycle_stage' = 'customer', false)`, col)
}

func (s *Server) visitorPeople(ctx context.Context, args []any, brand string) ([]visitorView, error) {
	rows, err := s.pool.Query(ctx, `
		WITH v AS (
			SELECT sv.record_id, count(*) AS browsers, sum(sv.pageviews) AS pageviews, sum(sv.key_pageviews) AS key_pageviews,
				sum(sv.sessions) AS sessions, min(sv.first_seen_at) AS first_seen, max(sv.last_seen_at) AS last_seen,
				(array_agg(sv.last_path ORDER BY sv.last_seen_at DESC))[1] AS last_path,
				array_agg(DISTINCT sv.identified_by) FILTER (WHERE sv.identified_by <> '') AS identified_by
			FROM site_visitors sv
			WHERE sv.workspace_id = $1 AND sv.last_seen_at >= $2 AND sv.record_id IS NOT NULL`+brand+`
			GROUP BY sv.record_id
			ORDER BY max(sv.last_seen_at) DESC
			LIMIT `+strconv.Itoa(maxVisitorCandidates)+`
		)
		SELECT r.id::text, r.name, r.attributes->>'job_title',
			(SELECT i.value FROM record_identities i WHERE i.record_id = r.id AND i.namespace = 'email' AND i.valid_to IS NULL ORDER BY i.verified DESC, i.created_at LIMIT 1),
			co.id::text, co.name, `+customerSQL("r")+` OR `+customerSQL("co")+`,
			ARRAY(SELECT split_part(i.value, '@', 2) FROM record_identities i WHERE i.record_id = r.id AND i.namespace = 'email' AND i.valid_to IS NULL)
				|| ARRAY(SELECT d.value FROM record_identities d WHERE d.record_id = co.id AND d.namespace = 'domain' AND d.valid_to IS NULL),
			v.browsers, v.pageviews, v.key_pageviews, v.sessions, v.first_seen, v.last_seen, v.last_path, COALESCE(v.identified_by, '{}')
		FROM v JOIN records r ON r.id = v.record_id AND r.merged_into IS NULL AND r.archived_at IS NULL
		LEFT JOIN LATERAL (SELECT c.id, c.name, c.attributes FROM records c WHERE c.id::text = (`+strings.ReplaceAll(companyOfSQL, "$1", "r.id")+` LIMIT 1)) co ON true`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (visitorView, error) {
		v := visitorView{Kind: "person", People: 1, PeopleSample: []visitorRef{}}
		var coID, coName *string
		err := row.Scan(&v.RecordID, &v.Name, &v.JobTitle, &v.Email, &coID, &coName, &v.customer, &v.domains,
			&v.Browsers, &v.Pageviews, &v.KeyPageviews, &v.Sessions, &v.FirstSeenAt, &v.LastSeenAt, &v.LastPath, &v.IdentifiedBy)
		if coID != nil {
			v.Company = &visitorRef{ID: *coID, Name: deref(coName)}
		}
		slices.Sort(v.IdentifiedBy)
		return v, err
	})
}

func (s *Server) visitorCompanies(ctx context.Context, args []any, brand string) ([]visitorView, error) {
	rows, err := s.pool.Query(ctx, `
		WITH sv AS (
			SELECT sv.*, COALESCE(sv.account_id, (`+strings.ReplaceAll(companyOfSQL, "$1", "sv.record_id")+` LIMIT 1)::uuid) AS company
			FROM site_visitors sv
			WHERE sv.workspace_id = $1 AND sv.last_seen_at >= $2 AND (sv.record_id IS NOT NULL OR sv.account_id IS NOT NULL)`+brand+`
		), v AS (
			SELECT company, count(*) AS browsers, count(DISTINCT record_id) AS people, sum(pageviews) AS pageviews, sum(key_pageviews) AS key_pageviews,
				sum(sessions) AS sessions, min(first_seen_at) AS first_seen, max(last_seen_at) AS last_seen,
				(array_agg(last_path ORDER BY last_seen_at DESC))[1] AS last_path,
				array_agg(DISTINCT identified_by) FILTER (WHERE identified_by <> '') AS identified_by,
				(array_agg(DISTINCT record_id) FILTER (WHERE record_id IS NOT NULL))[1:3] AS sample
			FROM sv WHERE company IS NOT NULL
			GROUP BY company
			ORDER BY max(last_seen_at) DESC
			LIMIT `+strconv.Itoa(maxVisitorCandidates)+`
		)
		SELECT c.id::text, c.name,
			(SELECT d.value FROM record_identities d WHERE d.record_id = c.id AND d.namespace = 'domain' AND d.valid_to IS NULL ORDER BY d.verified DESC, d.created_at LIMIT 1),
			`+customerSQL("c")+`,
			ARRAY(SELECT d.value FROM record_identities d WHERE d.record_id = c.id AND d.namespace = 'domain' AND d.valid_to IS NULL),
			COALESCE((SELECT json_agg(json_build_object('id', p.id, 'name', p.name)) FROM records p WHERE p.id = ANY(v.sample)), '[]'),
			v.people, v.browsers, v.pageviews, v.key_pageviews, v.sessions, v.first_seen, v.last_seen, v.last_path, COALESCE(v.identified_by, '{}')
		FROM v JOIN records c ON c.id = v.company AND c.merged_into IS NULL AND c.archived_at IS NULL`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (visitorView, error) {
		v := visitorView{Kind: "company"}
		err := row.Scan(&v.RecordID, &v.Name, &v.Domain, &v.customer, &v.domains, &v.PeopleSample,
			&v.People, &v.Browsers, &v.Pageviews, &v.KeyPageviews, &v.Sessions, &v.FirstSeenAt, &v.LastSeenAt, &v.LastPath, &v.IdentifiedBy)
		slices.Sort(v.IdentifiedBy)
		return v, err
	})
}

func (s *Server) getVisitorSettings(w http.ResponseWriter, r *http.Request) {
	set, err := visitors.Load(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) updateVisitorSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		KeyPages         *[]string `json:"key_pages"`
		HotScore         *int      `json:"hot_score"`
		EmailClicks      *bool     `json:"email_clicks"`
		ExcludeDomains   *[]string `json:"exclude_domains"`
		ExcludeCustomers *bool     `json:"exclude_customers"`
	}
	if !decode(w, r, &in) {
		return
	}
	var set visitors.Settings
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if set, err = visitors.Load(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		changed := []string{}
		if in.KeyPages != nil {
			if len(*in.KeyPages) > 50 {
				return errInvalid("key_pages takes at most 50 paths")
			}
			pages := []string{}
			for _, p := range *in.KeyPages {
				c, ok := visitors.CleanPattern(p)
				if !ok {
					return errInvalid("key_pages are paths such as /pricing, optionally ending in * (/pricing*); " + strconv.Quote(cutUTF8(p, 60)) + " is not one")
				}
				if !slices.Contains(pages, c) {
					pages = append(pages, c)
				}
			}
			set.KeyPages = pages
			changed = append(changed, "key_pages")
		}
		if in.HotScore != nil {
			if *in.HotScore < 1 || *in.HotScore > 100 {
				return errInvalid("hot_score must be 1 to 100")
			}
			set.HotScore = *in.HotScore
			changed = append(changed, "hot_score")
		}
		if in.EmailClicks != nil {
			set.EmailClicks = *in.EmailClicks
			changed = append(changed, "email_clicks")
		}
		if in.ExcludeDomains != nil {
			if len(*in.ExcludeDomains) > 200 {
				return errInvalid("exclude_domains takes at most 200 domains")
			}
			domains := []string{}
			for _, d := range *in.ExcludeDomains {
				c, ok := tracking.ValidDomain(d)
				if !ok {
					return errInvalid("exclude_domains are domains such as acme.example; " + strconv.Quote(cutUTF8(d, 60)) + " is not one")
				}
				c = strings.TrimPrefix(c, "www.")
				if !slices.Contains(domains, c) {
					domains = append(domains, c)
				}
			}
			set.ExcludeDomains = domains
			changed = append(changed, "exclude_domains")
		}
		if in.ExcludeCustomers != nil {
			set.ExcludeCustomers = *in.ExcludeCustomers
			changed = append(changed, "exclude_customers")
		}
		if err := visitors.Save(ctx, tx, a.WorkspaceID, set); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "visitor_settings.updated", "workspace", a.WorkspaceID,
			map[string]any{"changed": changed, "hot_score": set.HotScore, "email_clicks": set.EmailClicks, "key_pages": len(set.KeyPages),
				"exclude_domains": len(set.ExcludeDomains), "exclude_customers": set.ExcludeCustomers})
	})
	if err != nil {
		var bad errInvalid
		if errors.As(err, &bad) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		s.internal(w, r, err)
		return
	}
	now := time.Now()
	set.UpdatedAt = &now
	writeJSON(w, http.StatusOK, set)
}
