package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/visitors"
)

// Visitor browsers: every browser on a brand's site, known or not, with
// its journey (visits, pages and events), the way a product analytics tool
// shows an anonymous person. Visitor links join a browser to a person from
// a link the workspace sends them itself, so a phone where they never sign
// in still counts as theirs. See docs/website-visitors.md.

func (s *Server) routesVisitorBrowsers(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("GET /v1/brands/{brand}/visitors/browsers", s.onBrand(read, s.listVisitorBrowsers))
	mux.HandleFunc("GET /v1/brands/{brand}/visitors/browsers/{anonymous_id}", s.onBrand(read, s.getVisitorBrowser))
	mux.HandleFunc("GET /v1/brands/{brand}/visitors/browsers/{anonymous_id}/events", s.onBrand(read, s.listVisitorBrowserEvents))
	mux.HandleFunc("POST /v1/brands/{brand}/visitors/links", s.onBrand(manage, s.createVisitorLink))
}

// visitorBrowser is one browser on the site: the site snippet's anonymous
// id with its counts, where it was last seen from, and the person it
// turned out to be.
type visitorBrowser struct {
	AnonymousID  string     `json:"anonymous_id"`
	RecordID     *string    `json:"record_id"`
	Name         *string    `json:"name"`
	IdentifiedBy string     `json:"identified_by"`
	IdentifiedAt *time.Time `json:"identified_at"`
	// The company a lookup named from the browser's address (not in the
	// CRM until someone adds it), or the CRM company it was matched to.
	CompanyDomain string  `json:"company_domain"`
	CompanyName   string  `json:"company_name"`
	AccountID     *string `json:"account_id"`
	// business, isp, mobile, hosting or "" (not looked up).
	Network      string    `json:"network"`
	Country      string    `json:"country"`
	Device       string    `json:"device"`
	Browser      string    `json:"browser"`
	OS           string    `json:"os"`
	Pageviews    int       `json:"pageviews"`
	KeyPageviews int       `json:"key_pageviews"`
	Sessions     int       `json:"sessions"`
	FirstSeenAt  time.Time `json:"first_seen_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
	LastPath     string    `json:"last_path"`
	Score        int       `json:"score"`
	Hot          bool      `json:"hot"`
}

// visitorBrowserSQL selects a browser's columns in visitorBrowser order
// from site_visitors sv; $1 is the workspace.
const visitorBrowserSQL = `SELECT sv.anonymous_id, r.id::text, r.name, CASE WHEN r.id IS NULL THEN '' ELSE sv.identified_by END,
		(SELECT i.valid_from FROM record_identities i WHERE i.record_id = r.id AND i.namespace = 'anonymous_id' AND i.value = sv.anonymous_id
			AND i.valid_to IS NULL ORDER BY i.valid_from LIMIT 1),
		sv.company_domain, sv.company_name, sv.account_id::text, sv.network,
		COALESCE(l.country, ''), COALESCE(l.device, ''), COALESCE(l.browser, ''), COALESCE(l.os, ''),
		sv.pageviews, sv.key_pageviews, sv.sessions, sv.first_seen_at, sv.last_seen_at, sv.last_path
	FROM site_visitors sv
	LEFT JOIN records r ON r.id = sv.record_id AND r.merged_into IS NULL
	LEFT JOIN LATERAL (SELECT v.country, v.device, v.browser, v.os FROM site_sessions v
		WHERE v.workspace_id = sv.workspace_id AND v.anonymous_id = sv.anonymous_id ORDER BY v.last_seen_at DESC LIMIT 1) l ON true`

func scanVisitorBrowser(row pgx.Row, set visitors.Settings, now time.Time) (visitorBrowser, error) {
	var b visitorBrowser
	err := row.Scan(&b.AnonymousID, &b.RecordID, &b.Name, &b.IdentifiedBy, &b.IdentifiedAt, &b.CompanyDomain, &b.CompanyName, &b.AccountID, &b.Network,
		&b.Country, &b.Device, &b.Browser, &b.OS, &b.Pageviews, &b.KeyPageviews, &b.Sessions, &b.FirstSeenAt, &b.LastSeenAt, &b.LastPath)
	if err != nil {
		return b, err
	}
	b.Score = visitors.Score(visitors.Activity{Pageviews: b.Pageviews, KeyPageviews: b.KeyPageviews, Sessions: b.Sessions, LastSeen: b.LastSeenAt, People: 1}, now)
	b.Hot = b.Score >= set.HotScore
	return b, nil
}

func (s *Server) listVisitorBrowsers(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	days := 30
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days must be 1 to 365")
			return
		}
		days = n
	}
	known := q.Get("known")
	if known != "" && known != "true" && known != "false" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "known is true or false; leave it out for everyone")
		return
	}
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	beforeAt, beforeID, ok := timeCursor(w, r)
	if !ok {
		return
	}
	set, err := visitors.Load(ctx, s.pool, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	now := time.Now()
	where := ` WHERE sv.workspace_id = $1 AND sv.brand_id = $2 AND sv.last_seen_at >= $3 AND (sv.last_seen_at, sv.anonymous_id) < ($4, $5)`
	switch known {
	case "true":
		where += ` AND sv.record_id IS NOT NULL`
	case "false":
		where += ` AND sv.record_id IS NULL`
	}
	rows, err := s.pool.Query(ctx, visitorBrowserSQL+where+` ORDER BY sv.last_seen_at DESC, sv.anonymous_id DESC LIMIT $6`,
		a.WorkspaceID, a.Brand, now.AddDate(0, 0, -days), beforeAt, beforeID, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (visitorBrowser, error) { return scanVisitorBrowser(row, set, now) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []visitorBrowser{}
	}
	var next *string
	if len(list) == limit {
		last := list[len(list)-1]
		v := last.LastSeenAt.Format(time.RFC3339Nano) + "|" + last.AnonymousID
		next = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next), "days": days})
}

// visitorVisit is one visit (site_sessions) in a browser's journey.
type visitorVisit struct {
	ID           string    `json:"id"`
	StartedAt    time.Time `json:"started_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
	Pageviews    int       `json:"pageviews"`
	Events       int       `json:"events"`
	LandingPath  string    `json:"landing_path"`
	ReferrerHost string    `json:"referrer_host"`
	Channel      string    `json:"channel"`
	Source       string    `json:"source"`
	Campaign     string    `json:"campaign"`
	UTMSource    string    `json:"utm_source"`
	UTMMedium    string    `json:"utm_medium"`
	UTMCampaign  string    `json:"utm_campaign"`
	Country      string    `json:"country"`
	Device       string    `json:"device"`
	Browser      string    `json:"browser"`
	OS           string    `json:"os"`
	// Whose the visit was when it happened: false for one made before the
	// browser was known.
	Known bool `json:"known"`
}

// maxProfileVisits caps the visits a profile lists.
const maxProfileVisits = 100

// visitorBrowserParam reads the browser in the path: the site snippet's
// anonymous id.
func visitorBrowserParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("anonymous_id")
	if len(id) < 1 || len(id) > 200 {
		writeError(w, r, http.StatusNotFound, "not_found", "visitor not found")
		return "", false
	}
	return id, true
}

func (s *Server) getVisitorBrowser(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	anon, ok := visitorBrowserParam(w, r)
	if !ok {
		return
	}
	set, err := visitors.Load(ctx, s.pool, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	now := time.Now()
	b, err := scanVisitorBrowser(s.pool.QueryRow(ctx, visitorBrowserSQL+` WHERE sv.workspace_id = $1 AND sv.brand_id = $2 AND sv.anonymous_id = $3`,
		a.WorkspaceID, a.Brand, anon), set, now)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "visitor not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT v.id::text, v.started_at, v.last_seen_at, v.pageviews, v.events, v.landing_path, v.referrer_host,
			v.channel, v.source, v.campaign, v.utm_source, v.utm_medium, v.utm_campaign, v.country, v.device, v.browser, v.os, v.record_id IS NOT NULL
				AND ($4::timestamptz IS NULL OR v.started_at >= $4)
		FROM site_sessions v WHERE v.workspace_id = $1 AND v.anonymous_id = $2 AND v.brand_id IS NOT DISTINCT FROM $3::uuid
		ORDER BY v.started_at DESC LIMIT $5`, a.WorkspaceID, anon, a.Brand, b.IdentifiedAt, maxProfileVisits)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	visits, err := pgx.CollectRows(rows, pgx.RowToStructByPos[visitorVisit])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if visits == nil {
		visits = []visitorVisit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"browser": b, "visits": visits})
}

// visitorEvent is one page view or event in a browser's journey.
type visitorEvent struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	Path       string          `json:"path"`
	Title      string          `json:"title"`
	SessionID  *string         `json:"session_id"`
	Properties json.RawMessage `json:"properties"`
	OccurredAt time.Time       `json:"occurred_at"`
}

func (s *Server) listVisitorBrowserEvents(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	anon, ok := visitorBrowserParam(w, r)
	if !ok {
		return
	}
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	beforeAt, beforeID, ok := timeCursor(w, r)
	if !ok {
		return
	}
	session := r.URL.Query().Get("session")
	if session != "" && !validUUID(session) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "session is a visit's id")
		return
	}
	// The browser's own events: those it made while anonymous, and, once
	// known, the ones on its person that came from it.
	var rec *string
	err = s.pool.QueryRow(ctx, `SELECT record_id::text FROM site_visitors WHERE workspace_id = $1 AND anonymous_id = $2 AND brand_id = $3`,
		a.WorkspaceID, anon, a.Brand).Scan(&rec)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "visitor not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT e.id::text, e.kind, e.name, COALESCE(e.context->'page'->>'path', ''), COALESCE(e.context->'page'->>'title', ''),
			e.context->>'sessionId', e.properties, e.occurred_at
		FROM customer_events e
		WHERE e.workspace_id = $1 AND e.anonymous_id = $2 AND e.brand_id = $3
		  AND (e.record_id IS NULL OR e.record_id = $4::uuid)
		  AND ($7 = '' OR e.context->>'sessionId' = $7)
		  AND (e.occurred_at, e.id::text) < ($5, $6)
		ORDER BY e.occurred_at DESC, e.id::text DESC LIMIT $8`, a.WorkspaceID, anon, a.Brand, rec, beforeAt, beforeID, session, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[visitorEvent])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []visitorEvent{}
	}
	var next *string
	if len(list) == limit {
		last := list[len(list)-1]
		v := last.OccurredAt.Format(time.RFC3339Nano) + "|" + last.ID
		next = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}

type visitorLinkInput struct {
	URL      string  `json:"url"`
	UserID   *string `json:"user_id"`
	Email    *string `json:"email"`
	RecordID *string `json:"record_id"`
	// How long the link names its person; default 30, at most 365.
	ExpiresInDays *int `json:"expires_in_days"`
}

type visitorLinkView struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	RecordID  string    `json:"record_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// createVisitorLink makes a link to the brand's own site that joins the
// first browser to open it to a person, for a message the workspace sends
// itself: its own product's email, a text message, a QR code.
func (s *Server) createVisitorLink(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if s.box == nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "visitor links need the server's secret key (OUTFLARE_SECRET_KEY)")
		return
	}
	var in visitorLinkInput
	if !decode(w, r, &in) {
		return
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(in.URL) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "url must be a full address on your own site, such as https://example.com/pricing")
		return
	}
	named := 0
	for _, v := range []*string{in.UserID, in.Email, in.RecordID} {
		if v != nil && strings.TrimSpace(*v) != "" {
			named++
		}
	}
	if named != 1 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name the person with one of user_id, email or record_id")
		return
	}
	ttl := visitors.LinkTTL
	if in.ExpiresInDays != nil {
		if *in.ExpiresInDays < 1 || time.Duration(*in.ExpiresInDays)*24*time.Hour > visitors.LinkMaxTTL {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "expires_in_days must be 1 to 365")
			return
		}
		ttl = time.Duration(*in.ExpiresInDays) * 24 * time.Hour
	}
	own, err := s.ownSite(ctx, a.WorkspaceID, u)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !own {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "url must be on one of your brands' domains or a site key's origins, with the site snippet on it")
		return
	}
	var out visitorLinkView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := s.visitorLinkPerson(ctx, tx, a, in)
		if err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO visitor_links (workspace_id, brand_id, record_id, created_by, expires_at)
			VALUES ($1, $2, $3, $4, now() + $5::interval) RETURNING id, expires_at`,
			a.WorkspaceID, a.Brand, rec, nullIfEmpty(a.ActorID), ttl.String()).Scan(&id, &out.ExpiresAt); err != nil {
			return err
		}
		out.ID = strconv.FormatInt(id, 10)
		out.RecordID = rec
		out.URL = visitors.WithToken(u.String(), visitors.LinkToken(s.box, id))
		return record(ctx, tx, &a.principal, a.WorkspaceID, "visitor_link.created", "record", rec,
			map[string]any{"link_id": out.ID, "brand_id": a.Brand, "expires_in_days": int(ttl.Hours() / 24)})
	})
	if errors.Is(err, errVisitorLinkNobody) {
		writeError(w, r, http.StatusNotFound, "not_found", "no person in this brand has that user id, email or record id")
		return
	}
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

var errVisitorLinkNobody = errors.New("visitor link: no such person")

// visitorLinkPerson is the person in the brand a link is for.
func (s *Server) visitorLinkPerson(ctx context.Context, tx pgx.Tx, a *access, in visitorLinkInput) (string, error) {
	var rec string
	var err error
	switch {
	case in.RecordID != nil && strings.TrimSpace(*in.RecordID) != "":
		if !validUUID(*in.RecordID) {
			return "", errVisitorLinkNobody
		}
		err = tx.QueryRow(ctx, `SELECT COALESCE(merged_into, id)::text FROM records WHERE id = $1 AND workspace_id = $2 AND brand_id = $3
			AND record_type = 'person' AND archived_at IS NULL`, *in.RecordID, a.WorkspaceID, a.Brand).Scan(&rec)
	default:
		ns, value := "user_id", ""
		if in.UserID != nil && strings.TrimSpace(*in.UserID) != "" {
			value = strings.TrimSpace(*in.UserID)
		} else {
			ns, value = "email", strings.ToLower(strings.TrimSpace(*in.Email))
		}
		err = tx.QueryRow(ctx, `SELECT r.id::text FROM record_identities i JOIN records r ON r.id = i.record_id
			WHERE i.workspace_id = $1 AND i.namespace = $2 AND i.value = $3 AND i.valid_to IS NULL AND r.brand_id = $4
			  AND r.merged_into IS NULL AND r.archived_at IS NULL AND r.record_type = 'person'
			ORDER BY i.verified DESC, i.valid_from LIMIT 1`, a.WorkspaceID, ns, value, a.Brand).Scan(&rec)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errVisitorLinkNobody
	}
	return rec, err
}

// ownSite reports whether an address is on the workspace's own site: a
// brand's domain or a site key's origin, with a live site key to take its
// page views.
func (s *Server) ownSite(ctx context.Context, ws string, u *url.URL) (bool, error) {
	var own bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM site_keys WHERE workspace_id = $1 AND revoked_at IS NULL)
		   AND (EXISTS (SELECT 1 FROM brands b, unnest(b.domains || ARRAY[b.website]) d
				WHERE b.workspace_id = $1 AND d <> '' AND ($2 = lower(regexp_replace(d, '^(https?://)?(www\.)?([^/:]+).*$', '\3'))
					OR $2 LIKE '%.' || lower(regexp_replace(d, '^(https?://)?(www\.)?([^/:]+).*$', '\3'))))
			OR EXISTS (SELECT 1 FROM site_keys k, unnest(k.origins) o WHERE k.workspace_id = $1 AND k.revoked_at IS NULL
				AND (o = $3 OR (o LIKE 'https://*.%' AND ($2 = substr(o, 11) OR $2 LIKE '%.' || substr(o, 11))))))`,
		ws, strings.ToLower(u.Hostname()), strings.ToLower(u.Scheme+"://"+u.Host)).Scan(&own)
	return own, err
}
