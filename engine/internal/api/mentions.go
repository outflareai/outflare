package api

// Mentions: who names a brand or its competitors in conversational places
// (videos, podcasts, social posts, forums, news). What is listened for is
// read live from the brand's name and its Competitors list (tracked
// competitors' names, aliases and sites). Scans read what the brand's own
// sources already found (Social watches, News searches, Presence) and keep
// each place a name was said as a mention, so people can see who to thank,
// learn from or reach out to. Nothing here contacts anyone.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/mentions"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	jobMentionsScan = "mentions.scan"
	// A first scan looks this far back; later ones from the last scan.
	mentionFirstLook = 90 * 24 * time.Hour
	// At most this many rows a scan reads from each source.
	mentionScanRows = 2000
)

func (s *Server) routesMentions(mux *http.ServeMux) {
	read, manage := auth.ScopeSocialRead, auth.ScopeSocialManage
	mux.HandleFunc("GET /v1/brands/{brand}/mentions", s.onBrand(read, s.listBrandMentions))
	mux.HandleFunc("GET /v1/brands/{brand}/mentions/overview", s.onBrand(read, s.brandMentionsOverview))
	mux.HandleFunc("PATCH /v1/brands/{brand}/mentions/settings", s.onBrand(manage, s.updateBrandMentionSettings))
	mux.HandleFunc("PATCH /v1/brands/{brand}/mentions/listen-for", s.onBrand(manage, s.updateMentionListenFor))
	mux.HandleFunc("POST /v1/brands/{brand}/mentions/scan", s.onBrand(manage, s.scanBrandMentions))
	mux.HandleFunc("GET /v1/brands/{brand}/mentions/{id}", s.onBrand(read, s.getBrandMention))
	mux.HandleFunc("PATCH /v1/brands/{brand}/mentions/{id}", s.onBrand(manage, s.updateBrandMention))
}

func (s *Server) registerMentionJobs(q *jobs.Queue) {
	q.Handle(jobMentionsScan, s.mentionScanJob)
	q.Handle(jobMentionsVideo, s.mentionVideoJob)
	q.Handle(jobMentionsEpisode, s.mentionEpisodeJob)
}

// mentionSource is a source the engine reads today, and what feeds it.
type mentionSource struct {
	ID, Name string
	// Origin is the brand_mentions.origin its mentions get.
	Origin string
	// Setup says where to set up what feeds it: an app tab and what to do.
	SetupTab, SetupHint string
}

// mentionSources are the sources scans read now. X, Hacker News and
// Bluesky join as their readers do.
var mentionSources = []mentionSource{
	{ID: "reddit", Name: "Reddit", Origin: "social_items", SetupTab: "reddit",
		SetupHint: "Add a Reddit watch with your brand's and competitors' names as keywords."},
	{ID: "news", Name: "News", Origin: "news", SetupTab: "news",
		SetupHint: "Add a news search for your brand or a competitor."},
	{ID: "web", Name: "Videos, podcasts and forums from Presence", Origin: "presence", SetupTab: "competitors",
		SetupHint: "Track competitors on the Competitors page and scan where they appear."},
	{ID: "youtube", Name: "YouTube", Origin: "video_search", SetupTab: "connectors",
		SetupHint: "Connect Geonode, which searches YouTube and reads each video's captions on your own Geonode account."},
	{ID: "podcasts", Name: "Podcasts", Origin: "podcast_search", SetupTab: "connectors",
		SetupHint: "Connect Podcast Index (free: make a key at api.podcastindex.org), which searches every public podcast's episodes and show notes."},
}

func mentionSourceIDs() []string {
	out := make([]string, len(mentionSources))
	for i, m := range mentionSources {
		out[i] = m.ID
	}
	return out
}

var mentionStatuses = []string{"new", "contacted", "replied", "not_a_fit", "on_board"}

// ---- What is listened for ----

type mentionListen struct {
	About        string  `json:"about"`
	CompetitorID *string `json:"competitor_id"`
	Name         string  `json:"name"`
	// Listening: scans listen for this row. Off when someone turned it off.
	Listening bool `json:"listening"`
	// Terms are what scans listen for: the automatic terms not skipped,
	// then the ones added by hand. Empty when the row is off.
	Terms []string `json:"terms"`
	// AutoTerms are the terms read from the brand or competitor: the whole
	// website, and the name when it is a brand name.
	AutoTerms []string `json:"auto_terms"`
	// AddedTerms were added by hand; SkippedTerms are automatic terms
	// someone took out.
	AddedTerms   []string `json:"added_terms"`
	SkippedTerms []string `json:"skipped_terms"`
}

// mentionListenFor reads every row of what a brand listens for: its own
// name and site, and each tracked competitor's, with the changes made by
// hand (mention_listen_rules). Rows that are off are listed too.
func (s *Server) mentionListenFor(ctx context.Context, ws, brand string) ([]mentionListen, error) {
	var name, website string
	if err := s.pool.QueryRow(ctx, `SELECT name, website FROM brands WHERE workspace_id = $1 AND id = $2`, ws, brand).Scan(&name, &website); err != nil {
		return nil, err
	}
	out := []mentionListen{{About: "brand", Name: name, AutoTerms: mentions.Terms(name, website)}}
	rows, err := s.pool.Query(ctx, `SELECT c.id::text, COALESCE(NULLIF(c.name, ''), c.host), c.host FROM seo_competitors c
		WHERE c.workspace_id = $1 AND c.brand_id = $2 AND c.status = 'tracked' ORDER BY lower(COALESCE(NULLIF(c.name, ''), c.host)), c.id`, ws, brand)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, cname, host string
		if err := rows.Scan(&id, &cname, &host); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, mentionListen{About: "competitor", CompetitorID: &id, Name: cname, AutoTerms: mentions.Terms(cname, host)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	type rule struct {
		listening     bool
		added, skippd []string
	}
	rules := map[string]rule{}
	rrows, err := s.pool.Query(ctx, `SELECT COALESCE(competitor_id::text, ''), listening, added_terms, skipped_terms FROM mention_listen_rules
		WHERE workspace_id = $1 AND brand_id = $2`, ws, brand)
	if err != nil {
		return nil, err
	}
	for rrows.Next() {
		var key string
		var r rule
		if err := rrows.Scan(&key, &r.listening, &r.added, &r.skippd); err != nil {
			rrows.Close()
			return nil, err
		}
		rules[key] = r
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		l := &out[i]
		key := ""
		if l.CompetitorID != nil {
			key = *l.CompetitorID
		}
		r, ok := rules[key]
		if !ok {
			r = rule{listening: true}
		}
		l.Listening = r.listening
		l.AddedTerms = nonNil(r.added)
		l.SkippedTerms = nonNil(r.skippd)
		l.AutoTerms = nonNil(l.AutoTerms)
		l.Terms = []string{}
		if !l.Listening {
			continue
		}
		for _, t := range l.AutoTerms {
			if !slices.ContainsFunc(l.SkippedTerms, func(x string) bool { return strings.EqualFold(x, t) }) {
				l.Terms = mentions.AddTerm(l.Terms, t)
			}
		}
		for _, t := range l.AddedTerms {
			l.Terms = mentions.AddTerm(l.Terms, t)
		}
	}
	return out, nil
}

// mentionSubjects reads what scans listen for: the rows of
// mentionListenFor that are on and have a term.
func (s *Server) mentionSubjects(ctx context.Context, ws, brand string) ([]mentions.Subject, error) {
	rows, err := s.mentionListenFor(ctx, ws, brand)
	if err != nil {
		return nil, err
	}
	var out []mentions.Subject
	for _, l := range rows {
		if !l.Listening || len(l.Terms) == 0 {
			continue
		}
		sub := mentions.Subject{About: l.About, Name: l.Name, Terms: l.Terms}
		if l.CompetitorID != nil {
			sub.CompetitorID = *l.CompetitorID
		}
		out = append(out, sub)
	}
	return out, nil
}

// updateMentionListenFor changes one row of what a brand listens for:
// turns it on or off, or sets the terms added by hand and the automatic
// ones skipped. competitor_id names the row; empty or null is the brand's
// own. The next scan uses it.
func (s *Server) updateMentionListenFor(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		CompetitorID *string   `json:"competitor_id"`
		Listening    *bool     `json:"listening"`
		AddedTerms   *[]string `json:"added_terms"`
		SkippedTerms *[]string `json:"skipped_terms"`
	}
	if !decode(w, r, &req) {
		return
	}
	var competitor *string
	if req.CompetitorID != nil && *req.CompetitorID != "" {
		competitor = req.CompetitorID
		if !validUUID(*competitor) {
			writeError(w, r, http.StatusNotFound, "not_found", "competitor not found")
			return
		}
	}
	clean := func(list *[]string, field string) ([]string, bool) {
		if list == nil {
			return nil, true
		}
		if len(*list) > 50 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", field+" takes at most 50 terms")
			return nil, false
		}
		out := []string{}
		for _, t := range *list {
			if !mentions.ValidTerm(t) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", field+": each term is 3 to 200 characters")
				return nil, false
			}
			out = mentions.AddTerm(out, t)
		}
		return out, true
	}
	added, ok := clean(req.AddedTerms, "added_terms")
	if !ok {
		return
	}
	skipped, ok := clean(req.SkippedTerms, "skipped_terms")
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if competitor != nil {
			var found bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM seo_competitors WHERE workspace_id = $1 AND brand_id = $2 AND id = $3)`,
				a.WorkspaceID, a.Brand, *competitor).Scan(&found); err != nil {
				return err
			}
			if !found {
				return errNotFound
			}
		}
		var id string
		err := tx.QueryRow(ctx, `UPDATE mention_listen_rules SET listening = COALESCE($4::boolean, listening),
				added_terms = COALESCE($5::text[], added_terms), skipped_terms = COALESCE($6::text[], skipped_terms), updated_by = $7, updated_at = now()
			WHERE workspace_id = $1 AND brand_id = $2 AND competitor_id IS NOT DISTINCT FROM $3::uuid RETURNING id::text`,
			a.WorkspaceID, a.Brand, competitor, req.Listening, added, skipped, actorRef(a)).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `INSERT INTO mention_listen_rules (workspace_id, brand_id, competitor_id, listening, added_terms, skipped_terms, updated_by)
				VALUES ($1, $2, $3, COALESCE($4::boolean, true), COALESCE($5::text[], '{}'), COALESCE($6::text[], '{}'), $7) RETURNING id::text`,
				a.WorkspaceID, a.Brand, competitor, req.Listening, added, skipped, actorRef(a)).Scan(&id)
		}
		if err != nil {
			return err
		}
		data := map[string]any{"brand_id": a.Brand, "about": "brand"}
		if competitor != nil {
			data["about"], data["competitor_id"] = "competitor", *competitor
		}
		if req.Listening != nil {
			data["listening"] = *req.Listening
		}
		if added != nil {
			data["added_terms"] = len(added)
		}
		if skipped != nil {
			data["skipped_terms"] = len(skipped)
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "brand_mentions.listen_for_updated", "mention_listen_rule", id, data)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "competitor not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.brandMentionsOverview(w, r)
}

// ---- Overview and settings ----

type mentionSourceView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// On: the brand's settings read it.
	On bool `json:"on"`
	// Ready: something feeds it (a watch, a search, a tracked competitor).
	Ready bool `json:"ready"`
	// What to set up when it isn't ready: an app tab and what to do there.
	SetupTab  string `json:"setup_tab"`
	SetupHint string `json:"setup_hint"`
}

type mentionTotals struct {
	Total       int `json:"total"`
	New         int `json:"new"`
	NewThisWeek int `json:"new_this_week"`
	Contacted   int `json:"contacted"`
	Replied     int `json:"replied"`
	OnBoard     int `json:"on_board"`
	// People behind mentions, told apart by their profile or name.
	People int `json:"people"`
}

type mentionsOverview struct {
	Enabled        bool                `json:"enabled"`
	ScanEveryHours int                 `json:"scan_every_hours"`
	LastScanAt     *time.Time          `json:"last_scan_at"`
	NextScanAt     *time.Time          `json:"next_scan_at"`
	LastErrorCode  *string             `json:"last_error_code"`
	Sources        []mentionSourceView `json:"sources"`
	ListenFor      []mentionListen     `json:"listen_for"`
	Totals         mentionTotals       `json:"totals"`
}

func (s *Server) readMentionsOverview(ctx context.Context, ws, brand string) (mentionsOverview, error) {
	o := mentionsOverview{ScanEveryHours: 6, Sources: []mentionSourceView{}, ListenFor: []mentionListen{}}
	on := []string{"reddit", "news", "web"}
	var next *time.Time
	err := s.pool.QueryRow(ctx, `SELECT enabled, sources, scan_every_hours, last_scan_at, next_scan_at, last_error_code
		FROM brand_mention_settings WHERE workspace_id = $1 AND brand_id = $2`, ws, brand).
		Scan(&o.Enabled, &on, &o.ScanEveryHours, &o.LastScanAt, &next, &o.LastErrorCode)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return o, err
	}
	if o.Enabled {
		o.NextScanAt = next
	}
	var watches, searches, competitors, searchers, podcasters int
	if err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM social_watches WHERE workspace_id = $1 AND brand_id = $2 AND channel = 'reddit' AND status = 'active'),
		(SELECT count(*) FROM news_searches WHERE workspace_id = $1 AND brand_id = $2 AND status = 'active'),
		(SELECT count(*) FROM seo_competitors WHERE workspace_id = $1 AND brand_id = $2 AND status = 'tracked')`, ws, brand).
		Scan(&watches, &searches, &competitors); err != nil {
		return o, err
	}
	if ids, err := s.webSearchers(ctx, s.pool, ws); err != nil {
		return o, err
	} else {
		searchers = len(ids)
	}
	if ids, err := s.podcastSearchers(ctx, s.pool, ws); err != nil {
		return o, err
	} else {
		podcasters = len(ids)
	}
	ready := map[string]bool{"reddit": watches > 0, "news": searches > 0, "web": competitors > 0, "youtube": searchers > 0, "podcasts": podcasters > 0}
	for _, m := range mentionSources {
		o.Sources = append(o.Sources, mentionSourceView{ID: m.ID, Name: m.Name, On: slices.Contains(on, m.ID), Ready: ready[m.ID],
			SetupTab: m.SetupTab, SetupHint: m.SetupHint})
	}
	listen, err := s.mentionListenFor(ctx, ws, brand)
	if err != nil {
		return o, err
	}
	o.ListenFor = listen
	err = s.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status = 'new'), count(*) FILTER (WHERE found_at > now() - interval '7 days'),
		count(*) FILTER (WHERE status = 'contacted'), count(*) FILTER (WHERE status = 'replied'), count(*) FILTER (WHERE status = 'on_board'),
		count(DISTINCT source || ':' || COALESCE(NULLIF(author_url, ''), NULLIF(author_handle, ''), NULLIF(author_name, ''))) FILTER (WHERE author_url <> '' OR author_handle <> '' OR author_name <> '')
		FROM brand_mentions WHERE workspace_id = $1 AND brand_id = $2`, ws, brand).
		Scan(&o.Totals.Total, &o.Totals.New, &o.Totals.NewThisWeek, &o.Totals.Contacted, &o.Totals.Replied, &o.Totals.OnBoard, &o.Totals.People)
	return o, err
}

func (s *Server) brandMentionsOverview(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	o, err := s.readMentionsOverview(r.Context(), a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// updateBrandMentionSettings turns mentions on or off, picks the sources
// and how often to scan. Turning them on scans straight away.
func (s *Server) updateBrandMentionSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Enabled        *bool     `json:"enabled"`
		Sources        *[]string `json:"sources"`
		ScanEveryHours *int      `json:"scan_every_hours"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Sources != nil {
		for _, id := range *req.Sources {
			if !slices.Contains(mentionSourceIDs(), id) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "sources are "+strings.Join(mentionSourceIDs(), ", "))
				return
			}
		}
		list := slices.Clone(*req.Sources)
		slices.Sort(list)
		req.Sources = ptr(slices.Compact(list))
	}
	if req.ScanEveryHours != nil && (*req.ScanEveryHours < 1 || *req.ScanEveryHours > 168) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "scan_every_hours is 1 to 168")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var was bool
		err := tx.QueryRow(ctx, `SELECT enabled FROM brand_mention_settings WHERE brand_id = $1 FOR UPDATE`, a.Brand).Scan(&was)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var sources []string
		if req.Sources != nil {
			sources = *req.Sources
		}
		var enabled bool
		if err := tx.QueryRow(ctx, `INSERT INTO brand_mention_settings AS m (brand_id, workspace_id, enabled, sources, scan_every_hours, updated_by)
			VALUES ($1, $2, COALESCE($3::boolean, false), COALESCE($4::text[], '{reddit,news,web}'), COALESCE($5::integer, 6), $6)
			ON CONFLICT (brand_id) DO UPDATE SET enabled = COALESCE($3::boolean, m.enabled), sources = COALESCE($4::text[], m.sources),
				scan_every_hours = COALESCE($5::integer, m.scan_every_hours), updated_by = $6, updated_at = now(),
				next_scan_at = CASE WHEN COALESCE($3::boolean, m.enabled) AND NOT m.enabled THEN now() ELSE m.next_scan_at END
			RETURNING enabled`, a.Brand, a.WorkspaceID, req.Enabled, sources, req.ScanEveryHours, actorRef(a)).Scan(&enabled); err != nil {
			return err
		}
		action := "brand_mentions.updated"
		switch {
		case enabled && !was:
			action = "brand_mentions.enabled"
			if err := enqueueMentionScan(ctx, tx, a.WorkspaceID, a.Brand, "on:"+strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
				return err
			}
		case !enabled && was:
			action = "brand_mentions.disabled"
		}
		data := map[string]any{"brand_id": a.Brand, "enabled": enabled}
		if req.Sources != nil {
			data["sources"] = sources
		}
		if req.ScanEveryHours != nil {
			data["scan_every_hours"] = *req.ScanEveryHours
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "brand", a.Brand, data)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.brandMentionsOverview(w, r)
}

// scanBrandMentions queues a scan now.
func (s *Server) scanBrandMentions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var enabled bool
	err := s.pool.QueryRow(ctx, `SELECT enabled FROM brand_mention_settings WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&enabled)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, err)
		return
	}
	if !enabled {
		writeError(w, r, http.StatusConflict, "not_active", "mentions are off for this brand; turn them on first (PATCH .../mentions/settings with enabled true)")
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := enqueueMentionScan(ctx, tx, a.WorkspaceID, a.Brand, strconv.FormatInt(time.Now().Unix()/60, 10)); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "brand_mentions.scan_requested", "brand", a.Brand, map[string]any{"brand_id": a.Brand})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

// ---- Mentions ----

type mentionCompetitor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type mentionAuthor struct {
	Name   string `json:"name"`
	Handle string `json:"handle"`
	URL    string `json:"url"`
}

type mentionView struct {
	ID              string             `json:"id"`
	BrandID         string             `json:"brand_id"`
	Source          string             `json:"source"`
	ExternalID      string             `json:"external_id"`
	URL             string             `json:"url"`
	Title           string             `json:"title"`
	Quote           string             `json:"quote"`
	AtSeconds       *int               `json:"at_seconds"`
	About           string             `json:"about"`
	Competitor      *mentionCompetitor `json:"competitor"`
	Matched         string             `json:"matched"`
	Label           *string            `json:"label"`
	Sentiment       *string            `json:"sentiment"`
	Author          mentionAuthor      `json:"author"`
	Reach           *int64             `json:"reach"`
	Score           int                `json:"score"`
	Status          string             `json:"status"`
	Origin          string             `json:"origin"`
	PublishedAt     *time.Time         `json:"published_at"`
	FoundAt         time.Time          `json:"found_at"`
	StatusChangedAt time.Time          `json:"status_changed_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
}

const mentionSelect = `
	SELECT m.id, m.brand_id, m.source, m.external_id, m.url, m.title, m.quote, m.at_seconds, m.about, m.competitor_id::text,
	       COALESCE(NULLIF(c.name, ''), c.host), m.matched, m.label, m.sentiment, m.author_name, m.author_handle, m.author_url, m.reach,
	       m.score, m.status, m.origin, m.published_at, m.found_at, m.status_changed_at, m.updated_at
	FROM brand_mentions m
	LEFT JOIN seo_competitors c ON c.id = m.competitor_id
	WHERE m.workspace_id = $1 AND m.brand_id = $2`

func scanMention(row pgx.Row) (mentionView, error) {
	var v mentionView
	var cid, cname *string
	err := row.Scan(&v.ID, &v.BrandID, &v.Source, &v.ExternalID, &v.URL, &v.Title, &v.Quote, &v.AtSeconds, &v.About, &cid, &cname,
		&v.Matched, &v.Label, &v.Sentiment, &v.Author.Name, &v.Author.Handle, &v.Author.URL, &v.Reach, &v.Score, &v.Status, &v.Origin,
		&v.PublishedAt, &v.FoundAt, &v.StatusChangedAt, &v.UpdatedAt)
	if cid != nil {
		v.Competitor = &mentionCompetitor{ID: *cid, Name: deref(cname)}
	}
	return v, err
}

func (s *Server) listBrandMentions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	about, source, competitor, status := q.Get("about"), q.Get("source"), q.Get("competitor_id"), q.Get("status")
	switch {
	case about != "" && about != "brand" && about != "competitor":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "about is brand or competitor")
		return
	case source != "" && !slices.Contains(mentions.Sources, source):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source is "+strings.Join(mentions.Sources, ", "))
		return
	case competitor != "" && !validUUID(competitor):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "competitor_id is a competitor's id")
		return
	case status != "" && !slices.Contains(mentionStatuses, status):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is "+strings.Join(mentionStatuses, ", "))
		return
	}
	before, beforeID, ok := beforeTimeID(w, r)
	if !ok {
		return
	}
	if before != nil && beforeID == "" {
		beforeID = firstUUID
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), mentionSelect+`
		AND ($3 = '' OR m.about = $3) AND ($4 = '' OR m.source = $4) AND ($5 = '' OR m.competitor_id = NULLIF($5, '')::uuid)
		AND ($6 = '' OR m.status = $6) AND ($7::timestamptz IS NULL OR (m.found_at, m.id) < ($7, NULLIF($8, '')::uuid))
		ORDER BY m.found_at DESC, m.id DESC LIMIT $9`,
		a.WorkspaceID, a.Brand, about, source, competitor, status, before, beforeID, limit+1)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (mentionView, error) { return scanMention(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next any = nil
	if len(list) > limit {
		list = list[:limit]
		last := list[limit-1]
		next = timeIDCursor(last.FoundAt, last.ID)
	}
	if list == nil {
		list = []mentionView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": next})
}

func (s *Server) findMention(w http.ResponseWriter, r *http.Request, a *access, id string) (*mentionView, bool) {
	if validUUID(id) {
		v, err := scanMention(s.pool.QueryRow(r.Context(), mentionSelect+` AND m.id = $3`, a.WorkspaceID, a.Brand, id))
		if err == nil {
			return &v, true
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "mention not found")
	return nil, false
}

func (s *Server) getBrandMention(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findMention(w, r, a, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// updateBrandMention moves a mention along: contacted, replied, on board,
// or not a fit (and back to new).
func (s *Server) updateBrandMention(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findMention(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !slices.Contains(mentionStatuses, req.Status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is "+strings.Join(mentionStatuses, ", "))
		return
	}
	ctx := r.Context()
	if req.Status != cur.Status {
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE brand_mentions SET status = $4, status_changed_at = now(), updated_at = now()
				WHERE workspace_id = $1 AND brand_id = $2 AND id = $3`, a.WorkspaceID, a.Brand, cur.ID, req.Status); err != nil {
				return err
			}
			return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "brand_mention.status_changed", "brand_mention", cur.ID,
				map[string]any{"brand_id": a.Brand, "from": cur.Status, "to": req.Status, "source": cur.Source, "about": cur.About},
				map[string]any{"from": cur.Status, "to": req.Status, "source": cur.Source, "about": cur.About})
		})
		if err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if v, ok := s.findMention(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// ---- Scans ----

func enqueueMentionScan(ctx context.Context, tx pgx.Tx, ws, brand, key string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobMentionsScan, WorkspaceID: &ws, Payload: map[string]string{"brand_id": brand},
		IdempotencyKey: "mentions.scan:" + brand + ":" + key, MaxAttempts: 3})
	return err
}

// QueueDueMentionScans moves each due brand's next scan on and queues it,
// in one transaction, and reports how many it queued.
func (s *Server) QueueDueMentionScans(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE brand_mention_settings m SET next_scan_at = now() + make_interval(hours => m.scan_every_hours)
			WHERE m.brand_id IN (SELECT brand_id FROM brand_mention_settings WHERE enabled AND next_scan_at <= now()
			                     ORDER BY next_scan_at LIMIT 100 FOR UPDATE SKIP LOCKED)
			RETURNING m.workspace_id::text, m.brand_id::text, m.next_scan_at`)
		if err != nil {
			return err
		}
		type due struct {
			ws, brand string
			at        time.Time
		}
		var list []due
		var d due
		if _, err := pgx.ForEachRow(rows, []any{&d.ws, &d.brand, &d.at}, func() error { list = append(list, d); return nil }); err != nil {
			return err
		}
		for _, d := range list {
			if err := enqueueMentionScan(ctx, tx, d.ws, d.brand, strconv.FormatInt(d.at.Unix(), 10)); err != nil {
				return err
			}
		}
		n = len(list)
		return nil
	})
	return n, err
}

func (s *Server) mentionScanJob(ctx context.Context, job jobs.Job) error {
	ws, brand, err := socialPayload(job, "brand_id")
	if err != nil {
		return err
	}
	_, err = s.ScanBrandMentions(ctx, ws, brand)
	return err
}

// foundMention is a mention a scan found, before it is kept.
type foundMention struct {
	source, externalID, url, title, quote, about, matched string
	competitorID                                          *string
	atSeconds                                             *int
	authorName, authorHandle, authorURL                   string
	reach                                                 *int64
	origin                                                string
	originID                                              string
	publishedAt                                           *time.Time
}

// ScanBrandMentions reads what the brand's sources found since its last
// scan, keeps every new place the brand or a tracked competitor is named,
// and reports how many it kept. A brand with mentions off is skipped.
func (s *Server) ScanBrandMentions(ctx context.Context, ws, brand string) (int, error) {
	started := time.Now()
	var enabled bool
	var sources []string
	var last *time.Time
	err := s.pool.QueryRow(ctx, `SELECT enabled, sources, last_scan_at FROM brand_mention_settings WHERE workspace_id = $1 AND brand_id = $2`, ws, brand).
		Scan(&enabled, &sources, &last)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !enabled) {
		return 0, nil
	}
	ev := track.Event{Name: "brand_mentions.scanned", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"brand_id": brand}}
	fail := func(err error) (int, error) {
		metrics.MentionScan("internal", time.Since(started))
		_, _ = s.pool.Exec(ctx, `UPDATE brand_mention_settings SET last_error_code = 'internal', updated_at = now() WHERE brand_id = $1`, brand)
		ev.Name = "brand_mentions.scan_failed"
		track.Fail(ctx, s.log, ev, "internal", err)
		return 0, err
	}
	if err != nil {
		return fail(err)
	}
	since := started.Add(-mentionFirstLook)
	if last != nil {
		// A little overlap, so a row written while the last scan ran is
		// read; kept mentions are never kept twice.
		since = last.Add(-time.Hour)
	}
	subjects, err := s.mentionSubjects(ctx, ws, brand)
	if err != nil {
		return fail(err)
	}
	var found []foundMention
	if slices.Contains(sources, "reddit") {
		f, err := s.mentionsFromSocial(ctx, ws, brand, since, subjects)
		if err != nil {
			return fail(err)
		}
		found = append(found, f...)
	}
	if slices.Contains(sources, "news") {
		f, err := s.mentionsFromNews(ctx, ws, brand, since, subjects)
		if err != nil {
			return fail(err)
		}
		found = append(found, f...)
	}
	if slices.Contains(sources, "web") {
		f, err := s.mentionsFromPresence(ctx, ws, brand, since)
		if err != nil {
			return fail(err)
		}
		found = append(found, f...)
	}
	// The brand's and competitors' own channels and shows aren't mentions.
	owners, err := s.mentionOwners(ctx, s.pool, ws, brand)
	if err != nil {
		return fail(err)
	}
	var errCode *string
	videoSearches, podcastSearches := 0, 0
	if slices.Contains(sources, "youtube") {
		f, n, code, err := s.mentionsFromYouTube(ctx, ws, brand, last, subjects)
		if err != nil {
			return fail(err)
		}
		found = append(found, f...)
		videoSearches = n
		if code != "" {
			errCode = &code
		}
	}
	if slices.Contains(sources, "podcasts") {
		f, n, code, err := s.mentionsFromPodcasts(ctx, ws, brand, subjects, owners)
		if err != nil {
			return fail(err)
		}
		found = append(found, f...)
		podcastSearches = n
		if code != "" && errCode == nil {
			errCode = &code
		}
	}
	ownVideos := map[string]bool{}
	if vrows, err := s.pool.Query(ctx, `SELECT video_id FROM mention_videos WHERE workspace_id = $1 AND brand_id = $2 AND own_about IS NOT NULL`, ws, brand); err != nil {
		return fail(err)
	} else {
		for vrows.Next() {
			var v string
			if err := vrows.Scan(&v); err != nil {
				vrows.Close()
				return fail(err)
			}
			ownVideos[v] = true
		}
		vrows.Close()
	}
	skippedOwn := 0
	kept := 0
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, f := range found {
			if (f.source == "youtube" && ownVideos[f.externalID]) ||
				((f.authorURL != "" || f.authorHandle != "") && mentions.OwnerOf(owners, f.authorName, f.authorURL) != nil) {
				skippedOwn++
				continue
			}
			score := mentions.Score(f.about, f.source, f.reach, f.publishedAt, started)
			var originID *string
			if f.originID != "" {
				originID = &f.originID
			}
			tag, err := tx.Exec(ctx, `INSERT INTO brand_mentions (workspace_id, brand_id, source, external_id, url, title, quote, at_seconds, about,
				competitor_id, matched, author_name, author_handle, author_url, reach, score, origin, origin_id, published_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19) ON CONFLICT DO NOTHING`,
				ws, brand, f.source, truncRunes(f.externalID, 2048), truncRunes(f.url, 2048), truncRunes(f.title, 500), truncRunes(f.quote, 2000),
				f.atSeconds, f.about, f.competitorID, truncRunes(f.matched, 200), truncRunes(f.authorName, 200), truncRunes(f.authorHandle, 200),
				truncRunes(f.authorURL, 2048), f.reach, score, f.origin, originID, f.publishedAt)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				kept++
				metrics.MentionFound(f.source, f.about)
			}
		}
		_, err := tx.Exec(ctx, `UPDATE brand_mention_settings SET last_scan_at = $2, last_error_code = $3, updated_at = now() WHERE brand_id = $1`, brand, started, errCode)
		return err
	})
	if err != nil {
		return fail(err)
	}
	if slices.Contains(sources, "youtube") {
		if err := s.queueFoundVideos(ctx, ws, brand); err != nil {
			return fail(err)
		}
	}
	metrics.MentionScan("success", time.Since(started))
	ev.Outcome = track.Success
	ev.Props["found"] = kept
	ev.Props["read"] = len(found)
	ev.Props["subjects"] = len(subjects)
	ev.Props["video_searches"] = videoSearches
	ev.Props["podcast_searches"] = podcastSearches
	ev.Props["own_skipped"] = skippedOwn
	track.Emit(ctx, ev)
	return kept, nil
}

// mentionsFromSocial reads the brand's Reddit finds for its names.
func (s *Server) mentionsFromSocial(ctx context.Context, ws, brand string, since time.Time, subjects []mentions.Subject) ([]foundMention, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, external_id, url, title, body, author, posted_at FROM social_items
		WHERE workspace_id = $1 AND brand_id = $2 AND channel = 'reddit' AND found_at > $3 ORDER BY found_at LIMIT $4`, ws, brand, since, mentionScanRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []foundMention
	for rows.Next() {
		var id, ext, u, title, body, author string
		var posted time.Time
		if err := rows.Scan(&id, &ext, &u, &title, &body, &author, &posted); err != nil {
			return nil, err
		}
		authorURL := ""
		if author != "" && author != "[deleted]" {
			authorURL = "https://www.reddit.com/user/" + author
		}
		for _, h := range mentions.Find(title+"\n\n"+body, subjects) {
			out = append(out, foundMention{source: "reddit", externalID: ext, url: u, title: title, quote: h.Quote, about: h.Subject.About,
				competitorID: subjectCompetitor(h.Subject), matched: h.Matched, authorName: author, authorHandle: author, authorURL: authorURL,
				origin: "social_items", originID: id, publishedAt: &posted})
		}
	}
	return out, rows.Err()
}

// mentionsFromNews reads articles the brand's news searches found.
func (s *Server) mentionsFromNews(ctx context.Context, ws, brand string, since time.Time, subjects []mentions.Subject) ([]foundMention, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (a.id) a.id::text, a.url, a.title, a.snippet FROM news_articles a
		JOIN news_hits h ON h.article_id = a.id AND h.workspace_id = a.workspace_id
		JOIN news_searches n ON n.id = h.search_id AND n.workspace_id = a.workspace_id
		WHERE a.workspace_id = $1 AND n.brand_id = $2 AND h.found_at > $3 ORDER BY a.id LIMIT $4`, ws, brand, since, mentionScanRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []foundMention
	for rows.Next() {
		var id, u, title, snippet string
		if err := rows.Scan(&id, &u, &title, &snippet); err != nil {
			return nil, err
		}
		source := mentions.SourceFor(u, "")
		if source == "" {
			source = "news"
		}
		for _, h := range mentions.Find(title+"\n\n"+snippet, subjects) {
			out = append(out, foundMention{source: source, externalID: u, url: u, title: title, quote: h.Quote, about: h.Subject.About,
				competitorID: subjectCompetitor(h.Subject), matched: h.Matched, authorName: mentionHost(u), atSeconds: mentions.AtSeconds(u),
				origin: "news", originID: id})
		}
	}
	return out, rows.Err()
}

// mentionsFromPresence reads the conversational pages Presence found
// naming a tracked competitor: videos, episodes, posts and forum threads,
// not articles or directories.
func (s *Server) mentionsFromPresence(ctx context.Context, ws, brand string, since time.Time) ([]foundMention, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.id::text, p.competitor_id::text, COALESCE(NULLIF(c.name, ''), c.host), p.url, p.host, p.title, p.kind, p.first_seen_at
		FROM coverage_competitor_placements p JOIN seo_competitors c ON c.id = p.competitor_id AND c.workspace_id = p.workspace_id
		WHERE p.workspace_id = $1 AND p.brand_id = $2 AND c.status = 'tracked' AND p.first_seen_at > $3 AND p.gone_at IS NULL
		ORDER BY p.first_seen_at LIMIT $4`, ws, brand, since, mentionScanRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []foundMention
	for rows.Next() {
		var id, cid, cname, u, host, title, kind string
		var seen time.Time
		if err := rows.Scan(&id, &cid, &cname, &u, &host, &title, &kind, &seen); err != nil {
			return nil, err
		}
		source := mentions.SourceFor(u, kind)
		if source == "" {
			continue
		}
		competitor := cid
		f := foundMention{source: source, externalID: u, url: u, title: title, quote: title, about: "competitor",
			competitorID: &competitor, matched: cname, authorName: host, atSeconds: mentions.AtSeconds(u), origin: "presence", originID: id}
		if source == "youtube" {
			// Kept by the video's id, like videos found by searching, so
			// reading the video fills in its channel, views and the second
			// the name was said.
			if v := mentions.VideoID(u); v != "" {
				f.externalID, f.authorName = v, ""
				f.title = strings.TrimSuffix(strings.TrimSpace(title), " - YouTube")
				f.quote = f.title
			}
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func subjectCompetitor(s *mentions.Subject) *string {
	if s.CompetitorID == "" {
		return nil
	}
	id := s.CompetitorID
	return &id
}

// mentionHost is a URL's host without www., or "".
func mentionHost(raw string) string {
	raw = strings.TrimSpace(raw)
	_, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return ""
	}
	host, _, _ := strings.Cut(rest, "/")
	host, _, _ = strings.Cut(host, "?")
	return strings.TrimPrefix(strings.ToLower(host), "www.")
}
