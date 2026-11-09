package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/reviews"
	"github.com/outflareai/outflare/engine/internal/sociallinks"
)

// A competitor's profiles: the social profiles and review pages Outflare
// follows for one tracked competitor. Its home page's links are suggested
// (crawl.Glance), a person says yes or no to each, or adds one by hand,
// and the confirmed ones are read every day by the same readers as the
// brand's own (social_public.go, reviews_jobs.go), through the
// workspace's own page reader. Their numbers stay out of the brand's own
// social and review pages.

func (s *Server) routesCompetitorProfiles(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/brands/{brand}/competition/{competitor}/profiles", s.onBrand(read, s.listCompetitorProfiles))
	mux.HandleFunc("POST /v1/brands/{brand}/competition/{competitor}/profiles", s.onBrand(manage, s.addCompetitorProfile))
	mux.HandleFunc("PATCH /v1/brands/{brand}/competition/{competitor}/profiles", s.onBrand(manage, s.answerCompetitorProfile))
}

// competitorProfileView is one profile with what its last read found.
type competitorProfileView struct {
	sociallinks.CompetitorLink
	// Social profiles: followers (or the channel's headline count) and
	// the change over 28 days.
	Followers          *int64 `json:"followers"`
	FollowersChange28d *int64 `json:"followers_change_28d"`
	// Review pages: the platform's rating out of 5 and its review count.
	Score       *float64   `json:"score"`
	ReviewCount *int64     `json:"review_count"`
	LastReadAt  *time.Time `json:"last_read_at"`
	LastError   *string    `json:"last_error"`
}

type competitorProfilesView struct {
	CompetitorID string                  `json:"competitor_id"`
	Profiles     []competitorProfileView `json:"profiles"`
	// NeedsReader is set when the workspace has no page reader connected
	// (Geonode): profiles are kept but not read until one is.
	NeedsReader bool `json:"needs_reader"`
}

type competitorRow struct {
	id, status string
	links      []sociallinks.CompetitorLink
}

// brandCompetitor is one of the brand's competitors, or a 404.
func (s *Server) brandCompetitor(w http.ResponseWriter, r *http.Request, q pgx.Tx, lock bool) (*competitorRow, bool) {
	a := accessFrom(r.Context())
	id := r.PathValue("competitor")
	if validUUID(id) {
		var c competitorRow
		sql := `SELECT id::text, status, links FROM seo_competitors WHERE workspace_id = $1 AND brand_id = $2 AND id = $3`
		if lock {
			sql += ` FOR UPDATE`
		}
		var row pgx.Row
		if q != nil {
			row = q.QueryRow(r.Context(), sql, a.WorkspaceID, a.Brand, id)
		} else {
			row = s.pool.QueryRow(r.Context(), sql, a.WorkspaceID, a.Brand, id)
		}
		err := row.Scan(&c.id, &c.status, &c.links)
		if err == nil {
			return &c, true
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "competitor not found")
	return nil, false
}

// competitorProfiles adds each profile's latest numbers.
func (s *Server) competitorProfiles(ctx context.Context, ws, id string, links []sociallinks.CompetitorLink) (competitorProfilesView, error) {
	out := competitorProfilesView{CompetitorID: id, Profiles: make([]competitorProfileView, 0, len(links))}
	type read struct {
		followers, change, count *int64
		score                    *float64
		at                       *time.Time
		err                      *string
	}
	social := map[string]read{}
	rows, err := s.pool.Query(ctx, `
		SELECT a.channel || ' ' || a.url, a.followers,
			a.followers - (SELECT d.followers FROM social_account_days d WHERE d.account_id = a.id AND d.followers IS NOT NULL
				AND d.day <= (now() AT TIME ZONE 'UTC')::date - 28 ORDER BY d.day DESC LIMIT 1),
			a.last_synced_at, a.last_error
		FROM social_accounts a WHERE a.workspace_id = $1 AND a.competitor_id = $2 AND a.status <> 'disconnected'`, ws, id)
	if err != nil {
		return out, err
	}
	var key string
	var x read
	if _, err := pgx.ForEachRow(rows, []any{&key, &x.followers, &x.change, &x.at, &x.err}, func() error { social[key] = x; return nil }); err != nil {
		return out, err
	}
	pages := map[string]read{}
	rows, err = s.pool.Query(ctx, `SELECT platform || ' ' || external_id, score::float8, review_count, last_read_at, last_error
		FROM review_pages WHERE workspace_id = $1 AND competitor_id = $2 AND status <> 'removed'`, ws, id)
	if err != nil {
		return out, err
	}
	x = read{}
	if _, err := pgx.ForEachRow(rows, []any{&key, &x.score, &x.count, &x.at, &x.err}, func() error { pages[key] = x; return nil }); err != nil {
		return out, err
	}
	for _, l := range links {
		v := competitorProfileView{CompetitorLink: l}
		if p, ok := reviews.ParsePage(l.URL); ok && reviews.IsPlatform(l.Kind) {
			got := pages[p.Platform+" "+truncate(p.ID, 500)]
			v.Score, v.ReviewCount, v.LastReadAt, v.LastError = got.score, got.count, got.at, got.err
		} else if p, ok := sociallinks.PublicProfile(l.Kind, l.URL); ok {
			got := social[l.Kind+" "+p.URL]
			v.Followers, v.FollowersChange28d, v.LastReadAt, v.LastError = got.followers, got.change, got.at, got.err
		}
		out.Profiles = append(out.Profiles, v)
	}
	readers, err := s.pageReaders(ctx, s.pool, ws)
	out.NeedsReader = len(readers) == 0
	return out, err
}

func (s *Server) writeCompetitorProfiles(w http.ResponseWriter, r *http.Request, id string) {
	a := accessFrom(r.Context())
	var links []sociallinks.CompetitorLink
	if err := s.pool.QueryRow(r.Context(), `SELECT links FROM seo_competitors WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id).Scan(&links); err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := s.competitorProfiles(r.Context(), a.WorkspaceID, id, links)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listCompetitorProfiles(w http.ResponseWriter, r *http.Request) {
	c, ok := s.brandCompetitor(w, r, nil, false)
	if !ok {
		return
	}
	s.writeCompetitorProfiles(w, r, c.id)
}

// competitorProfileURL reads a profile address someone gave.
func competitorProfileURL(w http.ResponseWriter, r *http.Request, raw string) (sociallinks.Link, bool) {
	l, ok := sociallinks.Clean(raw, nil)
	if !ok || !sociallinks.Followable(l) {
		writeError(w, r, http.StatusBadRequest, "invalid_request",
			"url is a social profile (X, LinkedIn, YouTube, Instagram, TikTok, Facebook and others) or a review page (Trustpilot, G2, Capterra, SourceForge, Google) Outflare can read")
		return sociallinks.Link{}, false
	}
	return l, true
}

// addCompetitorProfile follows a profile a person names: added as
// confirmed, or a suggestion or dismissed one confirmed.
func (s *Server) addCompetitorProfile(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		URL string `json:"url"`
	}
	if !decode(w, r, &req) {
		return
	}
	l, ok := competitorProfileURL(w, r, req.URL)
	if !ok {
		return
	}
	s.changeCompetitorProfile(w, r, a, l, sociallinks.Confirmed, true)
}

// answerCompetitorProfile says yes (confirmed) or no (dismissed) to one of
// a competitor's profiles.
func (s *Server) answerCompetitorProfile(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.State != sociallinks.Confirmed && req.State != sociallinks.Dismissed {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "state is confirmed or dismissed")
		return
	}
	l, ok := sociallinks.Clean(req.URL, nil)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "url is one of the competitor's profiles")
		return
	}
	s.changeCompetitorProfile(w, r, a, l, req.State, false)
}

// errHandled stops a transaction whose answer is already written.
var errHandled = errors.New("answer written")

func (s *Server) changeCompetitorProfile(w http.ResponseWriter, r *http.Request, a *access, l sociallinks.Link, state string, add bool) {
	ctx := r.Context()
	var id string
	var missing, notTracked bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		c, ok := s.brandCompetitor(w, r, tx, true)
		if !ok {
			return errHandled
		}
		id = c.id
		if state == sociallinks.Confirmed && c.status != "tracked" {
			notTracked = true
			return nil
		}
		i := -1
		for n, x := range c.links {
			if x.URL == l.URL {
				i = n
			}
		}
		source, was := sociallinks.FromPerson, ""
		switch {
		case i < 0 && !add:
			missing = true
			return nil
		case i < 0:
			c.links = append(c.links, sociallinks.CompetitorLink{Kind: l.Kind, URL: l.URL, State: state, Source: source})
		default:
			was, source = c.links[i].State, c.links[i].Source
			if was == state {
				return nil
			}
			c.links[i].State = state
		}
		if _, err := tx.Exec(ctx, `UPDATE seo_competitors SET links = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2`, c.id, a.WorkspaceID, c.links); err != nil {
			return err
		}
		action := "competitor_profile." + state
		if i < 0 {
			action = "competitor_profile.added"
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "seo_competitor", c.id, map[string]any{
			"competitor_id": c.id, "brand_id": a.Brand, "kind": l.Kind, "source": source, "was": was,
		}); err != nil {
			return err
		}
		// Read it now (or let go of it now), not at the next daily read.
		return s.enqueuePublicSocialSync(ctx, tx, a.WorkspaceID, "competitor:"+c.id+":"+strconv.FormatInt(time.Now().Unix(), 10))
	})
	switch {
	case errors.Is(err, errHandled):
		return
	case s.allocError(w, r, err):
		return
	case notTracked:
		writeError(w, r, http.StatusConflict, "conflict", "track this competitor first: only tracked competitors' profiles are followed")
		return
	case missing:
		writeError(w, r, http.StatusNotFound, "not_found", "this competitor has no such profile; add it with POST")
		return
	}
	s.writeCompetitorProfiles(w, r, id)
}
