package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors/reddit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/missions"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// Social: one module for every social channel, Reddit first. Watches find
// posts where a brand can help; a person or the workspace's own AI drafts
// a reply that says who wrote it; a person approves each action; a job
// runs it on the brand's own account. Each action carries its own UTM
// content tag, so visitors and conversions are read from customer events
// and land on the same records as every other channel. Work waiting for
// people (posts to answer, drafts to approve) is on the shared fixes list.
//
// There is no acting from accounts the brand does not own, no vote buying
// and no reply without its disclosure.

func (s *Server) routesSocial(mux *http.ServeMux) {
	read, manage, post := auth.ScopeSocialRead, auth.ScopeSocialManage, auth.ScopeSocialPost
	mux.HandleFunc("GET /v1/workspaces/{ws}/social", s.inBrand(read, s.socialOverview))
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/watches", s.inBrand(read, s.listSocialWatches))
	mux.HandleFunc("POST /v1/workspaces/{ws}/social/watches", s.inBrand(manage, s.createSocialWatch))
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/watches/{id}", s.inBrand(read, s.getSocialWatch))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/social/watches/{id}", s.inBrand(manage, s.updateSocialWatch))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/social/watches/{id}", s.inBrand(manage, s.deleteSocialWatch))
	mux.HandleFunc("POST /v1/workspaces/{ws}/social/watches/{id}/check", s.inBrand(manage, s.checkSocialWatch))
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/items", s.inBrand(read, s.listSocialItems))
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/items/{id}", s.inBrand(read, s.getSocialItem))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/social/items/{id}", s.inBrand(manage, s.updateSocialItem))
	mux.HandleFunc("POST /v1/workspaces/{ws}/social/items/{id}/draft", s.inBrand(manage, s.draftSocialReply))
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/actions", s.inBrand(read, s.listSocialActions))
	mux.HandleFunc("POST /v1/workspaces/{ws}/social/actions", s.inBrand(manage, s.createSocialAction))
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/actions/{id}", s.inBrand(read, s.getSocialAction))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/social/actions/{id}", s.inBrand(manage, s.updateSocialAction))
	mux.HandleFunc("POST /v1/workspaces/{ws}/social/actions/{id}/approve", s.inBrand(post, s.approveSocialAction))
	mux.HandleFunc("POST /v1/workspaces/{ws}/social/actions/{id}/reject", s.inBrand(manage, s.rejectSocialAction))
}

// socialChannel is what the engine knows about one channel.
type socialChannel struct {
	ID, Name string
	// The connector that searches and acts on it.
	Connector string
	// What a community is called there, and what a valid one looks like.
	Community      string
	CommunityValid *regexp.Regexp
}

// socialChannels are the channels watches can use. X and LinkedIn join
// when their connectors do.
var socialChannels = map[string]socialChannel{
	"reddit": {ID: "reddit", Name: "Reddit", Connector: reddit.ID, Community: "subreddit",
		CommunityValid: regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_]{1,20}$`)},
}

func socialChannelIDs() []string {
	out := make([]string, 0, len(socialChannels))
	for id := range socialChannels {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// ---- Overview ----

type socialConnectionView struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Status  string  `json:"status"`
	Account *string `json:"account"`
	// Signed in to the brand's account, so approved actions can run.
	CanAct    bool    `json:"can_act"`
	LastError *string `json:"last_error"`
}

type socialChannelView struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Connector   string                 `json:"connector"`
	Connections []socialConnectionView `json:"connections"`
	// The redirect URI to register on the workspace's own app for the
	// channel, for signing in to the brand's account.
	RedirectURI string `json:"redirect_uri"`
}

func (s *Server) socialConnections(ctx context.Context, ws, connector string) ([]socialConnectionView, error) {
	out := []socialConnectionView{}
	if s.integrations == nil {
		return out, nil
	}
	list, err := s.integrations.List(ctx, s.pool, ws, connector)
	if err != nil {
		return nil, err
	}
	for _, i := range list {
		v := socialConnectionView{ID: i.ID, Name: i.Name, Status: i.Status, LastError: i.LastError, CanAct: i.Account.Details["posting"] == "on"}
		if v.CanAct && i.Account.Name != "" {
			name := i.Account.Name
			v.Account = &name
		}
		out = append(out, v)
	}
	return out, nil
}

type socialTotals struct {
	Watches     int `json:"watches"`
	NewItems    int `json:"new_items"`
	Drafts      int `json:"drafts"`
	Done30d     int `json:"done_30d"`
	Visitors30d int `json:"visitors_30d"`
	Conv30d     int `json:"conversions_30d"`
}

func (s *Server) socialOverview(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	channels := []socialChannelView{}
	for _, id := range socialChannelIDs() {
		ch := socialChannels[id]
		conns, err := s.socialConnections(ctx, a.WorkspaceID, ch.Connector)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		channels = append(channels, socialChannelView{ID: ch.ID, Name: ch.Name, Connector: ch.Connector, Connections: conns, RedirectURI: s.integrationRedirectURI()})
	}
	var t socialTotals
	args := []any{a.WorkspaceID}
	bw, bi, ba := sel.sql("w.brand_id", &args), sel.sql("i.brand_id", &args), sel.sql("x.brand_id", &args)
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM social_watches w WHERE w.workspace_id = $1`+bw+`),
		(SELECT count(*) FROM social_items i WHERE i.workspace_id = $1 AND i.status = 'new' AND NOT i.closed`+bi+`),
		(SELECT count(*) FROM social_actions x WHERE x.workspace_id = $1 AND x.status = 'draft'`+ba+`)`, args...).
		Scan(&t.Watches, &t.NewItems, &t.Drafts)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	args = []any{a.WorkspaceID}
	rows, err := s.pool.Query(ctx, `SELECT x.id::text FROM social_actions x WHERE x.workspace_id = $1 AND x.done_at > now() - interval '30 days'`+
		sel.sql("x.brand_id", &args), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	t.Done30d = len(ids)
	res, err := s.socialResults(ctx, a.WorkspaceID, ids, false)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for _, x := range res {
		t.Visitors30d += x.Visitors
		t.Conv30d += x.Conversions
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": channels, "totals": t})
}

// ---- Watches ----

type socialWatchView struct {
	ID               string     `json:"id"`
	Channel          string     `json:"channel"`
	Name             string     `json:"name"`
	BrandID          *string    `json:"brand_id"`
	Keywords         []string   `json:"keywords"`
	ExcludeKeywords  []string   `json:"exclude_keywords"`
	Communities      []string   `json:"communities"`
	IntegrationID    *string    `json:"integration_id"`
	LandingURL       string     `json:"landing_url"`
	Disclosure       string     `json:"disclosure"`
	Guidance         string     `json:"guidance"`
	ConversionEvents []string   `json:"conversion_events"`
	AutoDraft        bool       `json:"auto_draft"`
	DailyLimit       int        `json:"daily_limit"`
	Status           string     `json:"status"`
	CheckEvery       int        `json:"check_every_minutes"`
	LastCheckedAt    *time.Time `json:"last_checked_at"`
	NextCheckAt      time.Time  `json:"next_check_at"`
	LastError        *string    `json:"last_error"`
	Counts           struct {
		NewItems int `json:"new_items"`
		Drafts   int `json:"drafts"`
		Done     int `json:"done"`
	} `json:"counts"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const socialWatchSelect = `
	SELECT w.id, w.channel, w.name, w.brand_id, w.keywords, w.exclude_keywords, w.communities, w.integration_id, w.landing_url, w.disclosure,
	       w.guidance, w.conversion_events, w.auto_draft, w.daily_limit, w.status, w.check_every_minutes, w.last_checked_at,
	       w.next_check_at, w.last_error,
	       (SELECT count(*) FROM social_items i WHERE i.watch_id = w.id AND i.status = 'new' AND NOT i.closed)::int,
	       (SELECT count(*) FROM social_actions x WHERE x.watch_id = w.id AND x.status = 'draft')::int,
	       (SELECT count(*) FROM social_actions x WHERE x.watch_id = w.id AND x.status = 'done')::int,
	       w.created_at, w.updated_at
	FROM social_watches w WHERE w.workspace_id = $1`

func scanSocialWatch(row pgx.Row) (socialWatchView, error) {
	var v socialWatchView
	err := row.Scan(&v.ID, &v.Channel, &v.Name, &v.BrandID, &v.Keywords, &v.ExcludeKeywords, &v.Communities, &v.IntegrationID, &v.LandingURL,
		&v.Disclosure, &v.Guidance, &v.ConversionEvents, &v.AutoDraft, &v.DailyLimit, &v.Status, &v.CheckEvery, &v.LastCheckedAt,
		&v.NextCheckAt, &v.LastError, &v.Counts.NewItems, &v.Counts.Drafts, &v.Counts.Done, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// channelParam reads ?channel, which must be a known channel.
func channelParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	ch := r.URL.Query().Get("channel")
	if _, ok := socialChannels[ch]; ch != "" && !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "channel is "+strings.Join(socialChannelIDs(), ", "))
		return "", false
	}
	return ch, true
}

func (s *Server) listSocialWatches(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	ch, ok := channelParam(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, ch}
	rows, err := s.pool.Query(r.Context(), socialWatchSelect+` AND ($2 = '' OR w.channel = $2)`+sel.sql("w.brand_id", &args)+
		` ORDER BY w.created_at DESC LIMIT 200`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (socialWatchView, error) { return scanSocialWatch(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []socialWatchView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// findSocialWatch loads a watch the caller may see.
func (s *Server) findSocialWatch(w http.ResponseWriter, r *http.Request, a *access, id string) (*socialWatchView, bool) {
	if validUUID(id) {
		v, err := scanSocialWatch(s.pool.QueryRow(r.Context(), socialWatchSelect+` AND w.id = $2`, a.WorkspaceID, id))
		if err == nil && a.sees(v.BrandID) {
			return &v, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "watch not found")
	return nil, false
}

func (s *Server) getSocialWatch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findSocialWatch(w, r, a, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

type socialWatchFields struct {
	Channel          *string          `json:"channel"`
	Name             *string          `json:"name"`
	BrandID          optional[string] `json:"brand_id"`
	Keywords         []string         `json:"keywords"`
	ExcludeKeywords  []string         `json:"exclude_keywords"`
	Communities      []string         `json:"communities"`
	IntegrationID    optional[string] `json:"integration_id"`
	LandingURL       *string          `json:"landing_url"`
	Disclosure       *string          `json:"disclosure"`
	Guidance         *string          `json:"guidance"`
	ConversionEvents []string         `json:"conversion_events"`
	AutoDraft        *bool            `json:"auto_draft"`
	DailyLimit       *int             `json:"daily_limit"`
	Status           *string          `json:"status"`
	CheckEvery       *int             `json:"check_every_minutes"`
}

// cleanTerms trims, drops empties and duplicates (ignoring case) and checks
// each term's length.
func cleanTerms(in []string, field string, maxN, maxLen int) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.Join(strings.Fields(t), " ")
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		if len([]rune(t)) > maxLen {
			return nil, errInvalid(field + " entries are at most " + strconv.Itoa(maxLen) + " characters")
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	if len(out) > maxN {
		return nil, errInvalid(field + " has at most " + strconv.Itoa(maxN) + " entries")
	}
	return out, nil
}

// applySocialWatch checks f and applies it to v. The channel is set on
// create only.
func (s *Server) applySocialWatch(ctx context.Context, a *access, f socialWatchFields, v *socialWatchView) error {
	ch := socialChannels[v.Channel]
	if f.Name != nil {
		n := strings.TrimSpace(*f.Name)
		if n == "" || len(n) > 200 {
			return errInvalid("name must be 1 to 200 characters")
		}
		v.Name = n
	}
	if f.BrandID.Set {
		v.BrandID = nil
		if f.BrandID.Value != nil && *f.BrandID.Value != "" {
			id, err := s.brandIDFor(ctx, a.WorkspaceID, *f.BrandID.Value)
			if errors.Is(err, errNotFound) {
				return errInvalid("brand_id: brand not found")
			}
			if err != nil {
				return err
			}
			v.BrandID = &id
		}
	}
	if !a.writes(v.BrandID) {
		return errInvalid("brand_id: you can only add watches to your own brands")
	}
	var err error
	if f.Keywords != nil {
		if v.Keywords, err = cleanTerms(f.Keywords, "keywords", 50, 100); err != nil {
			return err
		}
	}
	if len(v.Keywords) == 0 {
		return errInvalid("keywords needs at least one keyword, such as \"which proxy\"")
	}
	if f.ExcludeKeywords != nil {
		if v.ExcludeKeywords, err = cleanTerms(f.ExcludeKeywords, "exclude_keywords", 50, 100); err != nil {
			return err
		}
	}
	if f.Communities != nil {
		list := make([]string, 0, len(f.Communities))
		for _, c := range f.Communities {
			c = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(c), "/"), "r/")
			if c == "" {
				continue
			}
			if !ch.CommunityValid.MatchString(c) {
				return errInvalid("communities are " + ch.Community + " names, such as webdev, without r/")
			}
			if !slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, c) }) {
				list = append(list, c)
			}
		}
		if len(list) > 20 {
			return errInvalid("communities has at most 20 entries")
		}
		v.Communities = list
	}
	if f.IntegrationID.Set {
		v.IntegrationID = nil
		if id := deref(f.IntegrationID.Value); id != "" {
			var ok bool
			if validUUID(id) {
				if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM integrations WHERE workspace_id = $1 AND id = $2 AND connector = $3)`,
					a.WorkspaceID, id, ch.Connector).Scan(&ok); err != nil {
					return err
				}
			}
			if !ok {
				return errInvalid("integration_id must be a " + ch.Name + " connection in this workspace")
			}
			v.IntegrationID = &id
		}
	}
	if f.LandingURL != nil {
		u := strings.TrimSpace(*f.LandingURL)
		if u != "" {
			p, err := url.Parse(u)
			if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" || len(u) > 500 {
				return errInvalid("landing_url must be an http or https address")
			}
		}
		v.LandingURL = u
	}
	if f.Disclosure != nil {
		v.Disclosure = strings.Join(strings.Fields(*f.Disclosure), " ")
	}
	if v.Disclosure == "" && v.BrandID != nil {
		var name string
		if err := s.pool.QueryRow(ctx, `SELECT name FROM brands WHERE id = $1`, *v.BrandID).Scan(&name); err != nil {
			return err
		}
		v.Disclosure = "Disclosure: I work at " + name + "."
	}
	if n := len([]rune(v.Disclosure)); n < 3 || n > 300 {
		return errInvalid("disclosure is required, 3 to 300 characters, such as \"Disclosure: I work at Acme.\" Every reply says it")
	}
	if f.Guidance != nil {
		if len(*f.Guidance) > 4000 {
			return errInvalid("guidance is at most 4000 characters")
		}
		v.Guidance = strings.TrimSpace(*f.Guidance)
	}
	if f.ConversionEvents != nil {
		ev := []string{}
		for _, e := range f.ConversionEvents {
			e = strings.TrimSpace(e)
			if !eventName.MatchString(e) || len(e) > 100 {
				return errInvalid("conversion_events are customer event names such as signed_up or order_completed")
			}
			if !slices.Contains(ev, e) {
				ev = append(ev, e)
			}
		}
		if len(ev) > 10 {
			return errInvalid("conversion_events has at most 10 entries")
		}
		v.ConversionEvents = ev
	}
	if f.AutoDraft != nil {
		v.AutoDraft = *f.AutoDraft
	}
	if f.DailyLimit != nil {
		if *f.DailyLimit < 1 || *f.DailyLimit > 50 {
			return errInvalid("daily_limit is 1 to 50")
		}
		v.DailyLimit = *f.DailyLimit
	}
	if f.Status != nil {
		if *f.Status != "active" && *f.Status != "paused" {
			return errInvalid("status is active or paused")
		}
		v.Status = *f.Status
	}
	if f.CheckEvery != nil {
		if *f.CheckEvery < 15 || *f.CheckEvery > 1440 {
			return errInvalid("check_every_minutes is 15 to 1440")
		}
		v.CheckEvery = *f.CheckEvery
	}
	return nil
}

func (s *Server) createSocialWatch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f socialWatchFields
	if !decode(w, r, &f) {
		return
	}
	if f.Name == nil || f.Channel == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "channel, name and keywords are required")
		return
	}
	ch, ok := socialChannels[*f.Channel]
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "channel is "+strings.Join(socialChannelIDs(), ", "))
		return
	}
	ctx := r.Context()
	v := socialWatchView{Channel: ch.ID, Keywords: []string{}, ExcludeKeywords: []string{}, Communities: []string{}, ConversionEvents: []string{"signed_up"},
		DailyLimit: 5, Status: "active", CheckEvery: 60}
	if !f.IntegrationID.Set {
		// The workspace's one connection for the channel, when it has
		// exactly one.
		conns, err := s.socialConnections(ctx, a.WorkspaceID, ch.Connector)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if len(conns) == 1 {
			v.IntegrationID = &conns[0].ID
		}
	}
	if !s.socialInvalid(w, r, s.applySocialWatch(ctx, a, f, &v)) {
		return
	}
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var createdBy *string
		if a.ActorID != "" {
			createdBy = &a.ActorID
		}
		if err := tx.QueryRow(ctx, `INSERT INTO social_watches (workspace_id, brand_id, channel, name, keywords, exclude_keywords, communities,
			integration_id, landing_url, disclosure, guidance, conversion_events, auto_draft, daily_limit, status, check_every_minutes, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17) RETURNING id`,
			a.WorkspaceID, v.BrandID, v.Channel, v.Name, v.Keywords, v.ExcludeKeywords, v.Communities, v.IntegrationID, v.LandingURL, v.Disclosure,
			v.Guidance, v.ConversionEvents, v.AutoDraft, v.DailyLimit, v.Status, v.CheckEvery, createdBy).Scan(&id); err != nil {
			return err
		}
		if v.Status == "active" && v.IntegrationID != nil {
			if err := enqueueSocialCheck(ctx, tx, a.WorkspaceID, id, "created"); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_watch.created", "social_watch", id,
			map[string]any{"channel": v.Channel, "keywords": len(v.Keywords), "communities": len(v.Communities), "auto_draft": v.AutoDraft,
				"has_connection": v.IntegrationID != nil})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out, ok := s.findSocialWatch(w, r, a, id); ok {
		writeJSON(w, http.StatusCreated, out)
	}
}

// socialInvalid answers err, reporting whether there was none.
func (s *Server) socialInvalid(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv errInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return false
	case err != nil:
		s.internal(w, r, err)
		return false
	}
	return true
}

func (s *Server) updateSocialWatch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findSocialWatch(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	var f socialWatchFields
	if !decode(w, r, &f) {
		return
	}
	if f.Channel != nil && *f.Channel != cur.Channel {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a watch's channel cannot change; make a new watch")
		return
	}
	v := *cur
	ctx := r.Context()
	if !s.socialInvalid(w, r, s.applySocialWatch(ctx, a, f, &v)) {
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE social_watches SET brand_id = $3, name = $4, keywords = $5, exclude_keywords = $6, communities = $7,
			integration_id = $8, landing_url = $9, disclosure = $10, guidance = $11, conversion_events = $12, auto_draft = $13,
			daily_limit = $14, status = $15, check_every_minutes = $16, updated_at = now(),
			next_check_at = CASE WHEN status = 'paused' AND $15 = 'active' THEN now() ELSE next_check_at END
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID, v.BrandID, v.Name, v.Keywords, v.ExcludeKeywords, v.Communities,
			v.IntegrationID, v.LandingURL, v.Disclosure, v.Guidance, v.ConversionEvents, v.AutoDraft, v.DailyLimit, v.Status, v.CheckEvery); err != nil {
			return err
		}
		// Items and actions follow their watch's brand.
		for _, table := range []string{"social_items", "social_actions"} {
			if _, err := tx.Exec(ctx, `UPDATE `+table+` SET brand_id = $3 WHERE workspace_id = $1 AND watch_id = $2 AND brand_id IS DISTINCT FROM $3`,
				a.WorkspaceID, cur.ID, v.BrandID); err != nil {
				return err
			}
		}
		if err := syncSocialFixes(ctx, tx, a.WorkspaceID, cur.ID); err != nil {
			return err
		}
		action := "social_watch.updated"
		switch {
		case cur.Status == "active" && v.Status == "paused":
			action = "social_watch.paused"
		case cur.Status == "paused" && v.Status == "active":
			action = "social_watch.resumed"
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "social_watch", cur.ID, map[string]any{"channel": cur.Channel})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out, ok := s.findSocialWatch(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, out)
	}
}

func (s *Server) deleteSocialWatch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findSocialWatch(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM fixes WHERE workspace_id = $1 AND source = $2 AND subject_id = $3`, a.WorkspaceID, socialFixSource, cur.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM social_watches WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_watch.deleted", "social_watch", cur.ID, map[string]any{"channel": cur.Channel})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkSocialWatch searches the channel for the watch now, in a job.
func (s *Server) checkSocialWatch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findSocialWatch(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	if cur.IntegrationID == nil {
		name := socialChannels[cur.Channel].Name
		writeError(w, r, http.StatusConflict, "connector_needed", "connect "+name+" under Connectors and choose the connection on this watch first")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := enqueueSocialCheck(ctx, tx, a.WorkspaceID, cur.ID, strconv.FormatInt(time.Now().Unix()/60, 10)); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_watch.check_requested", "social_watch", cur.ID, map[string]any{"channel": cur.Channel})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

// ---- Items ----

type socialItemView struct {
	ID              string    `json:"id"`
	Channel         string    `json:"channel"`
	WatchID         string    `json:"watch_id"`
	BrandID         *string   `json:"brand_id"`
	ExternalID      string    `json:"external_id"`
	Community       string    `json:"community"`
	Title           string    `json:"title"`
	Body            string    `json:"body"`
	Author          string    `json:"author"`
	URL             string    `json:"url"`
	Score           int       `json:"score"`
	Comments        int       `json:"comments"`
	PostedAt        time.Time `json:"posted_at"`
	MatchedKeywords []string  `json:"matched_keywords"`
	Status          string    `json:"status"`
	Closed          bool      `json:"closed"`
	// The newest action on it, if any.
	Action    *socialActionRef `json:"action"`
	FoundAt   time.Time        `json:"found_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

type socialActionRef struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

const socialItemSelect = `
	SELECT i.id, i.channel, i.watch_id, i.brand_id, i.external_id, i.community, i.title, i.body, i.author, i.url, i.score, i.comments,
	       i.posted_at, i.matched_keywords, i.status, i.closed, la.id::text, la.type, la.status, i.found_at, i.updated_at
	FROM social_items i
	LEFT JOIN LATERAL (SELECT x.id, x.type, x.status FROM social_actions x WHERE x.item_id = i.id ORDER BY x.created_at DESC LIMIT 1) la ON true
	WHERE i.workspace_id = $1`

func scanSocialItem(row pgx.Row) (socialItemView, error) {
	var v socialItemView
	var aid, atype, astatus *string
	err := row.Scan(&v.ID, &v.Channel, &v.WatchID, &v.BrandID, &v.ExternalID, &v.Community, &v.Title, &v.Body, &v.Author, &v.URL, &v.Score,
		&v.Comments, &v.PostedAt, &v.MatchedKeywords, &v.Status, &v.Closed, &aid, &atype, &astatus, &v.FoundAt, &v.UpdatedAt)
	if aid != nil {
		v.Action = &socialActionRef{*aid, deref(atype), deref(astatus)}
	}
	return v, err
}

var socialItemStatuses = []string{"new", "drafted", "handled", "ignored"}

// beforeTime reads ?before, a time from next_before.
func beforeTime(w http.ResponseWriter, r *http.Request) (*time.Time, bool) {
	before := r.URL.Query().Get("before")
	if before == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339Nano, before)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "before is a time, such as next_before from the previous page")
		return nil, false
	}
	return &t, true
}

// pageLimit reads ?limit, def when absent.
func pageLimit(w http.ResponseWriter, r *http.Request, def, max int) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > max {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to "+strconv.Itoa(max))
		return 0, false
	}
	return n, true
}

func (s *Server) listSocialItems(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	ch, ok := channelParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	status, watch := q.Get("status"), q.Get("watch_id")
	if status != "" && !slices.Contains(socialItemStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is "+strings.Join(socialItemStatuses, ", "))
		return
	}
	if watch != "" && !validUUID(watch) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "watch_id is a watch id")
		return
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, status, watch, before, limit + 1, ch}
	rows, err := s.pool.Query(r.Context(), socialItemSelect+`
		AND ($2 = '' OR i.status = $2) AND ($3 = '' OR i.watch_id = NULLIF($3, '')::uuid) AND ($4::timestamptz IS NULL OR i.posted_at < $4)
		AND ($6 = '' OR i.channel = $6)`+sel.sql("i.brand_id", &args)+` ORDER BY i.posted_at DESC, i.id LIMIT $5`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (socialItemView, error) { return scanSocialItem(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) > limit {
		list = list[:limit]
		n := list[limit-1].PostedAt.Format(time.RFC3339Nano)
		next = &n
	}
	if list == nil {
		list = []socialItemView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

func (s *Server) findSocialItem(w http.ResponseWriter, r *http.Request, a *access, id string) (*socialItemView, bool) {
	if validUUID(id) {
		v, err := scanSocialItem(s.pool.QueryRow(r.Context(), socialItemSelect+` AND i.id = $2`, a.WorkspaceID, id))
		if err == nil && a.sees(v.BrandID) {
			return &v, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "item not found")
	return nil, false
}

func (s *Server) getSocialItem(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findSocialItem(w, r, a, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// updateSocialItem ignores an item, or brings an ignored one back.
func (s *Server) updateSocialItem(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findSocialItem(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Status != "ignored" && req.Status != "new" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is ignored or new")
		return
	}
	if cur.Status == "handled" || cur.Status == "drafted" {
		writeError(w, r, http.StatusConflict, "conflict", "the item is "+cur.Status+"; reject its draft first")
		return
	}
	ctx := r.Context()
	if req.Status != cur.Status {
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE social_items SET status = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2`,
				a.WorkspaceID, cur.ID, req.Status); err != nil {
				return err
			}
			if err := syncSocialFixes(ctx, tx, a.WorkspaceID, cur.WatchID); err != nil {
				return err
			}
			action := "social_item.ignored"
			if req.Status == "new" {
				action = "social_item.restored"
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, action, "social_item", cur.ID, map[string]any{"channel": cur.Channel, "watch_id": cur.WatchID})
		})
		if err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if v, ok := s.findSocialItem(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// ---- Actions ----

type socialActionResults struct {
	// People who came to the brand's site from the action's link, known or
	// anonymous, read from customer events by its UTM content tag.
	Visitors int `json:"visitors"`
	// Known people among them with a conversion event (the watch's
	// conversion_events) after their first visit.
	Conversions int `json:"conversions"`
	// Those people's customer records (up to 100): only on a single action.
	ConvertedRecordIDs []string `json:"converted_record_ids,omitempty"`
}

type socialItemRef struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Community string `json:"community"`
	URL       string `json:"url"`
}

type socialExternal struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Score   *int   `json:"score"`
	Removed bool   `json:"removed"`
}

type socialActionView struct {
	ID              string         `json:"id"`
	Channel         string         `json:"channel"`
	Type            string         `json:"type"`
	Item            *socialItemRef `json:"item"`
	WatchID         *string        `json:"watch_id"`
	BrandID         *string        `json:"brand_id"`
	IntegrationID   *string        `json:"integration_id"`
	Body            string         `json:"body"`
	Disclosure      string         `json:"disclosure"`
	LinkURL         string         `json:"link_url"`
	UTMContent      string         `json:"utm_content"`
	Status          string         `json:"status"`
	StatusReason    *string        `json:"status_reason"`
	DraftedVia      string         `json:"drafted_via"`
	Relevance       *float32       `json:"relevance"`
	RelevanceReason *string        `json:"relevance_reason"`
	MissionID       *string        `json:"mission_id"`
	CreatedBy       *string        `json:"created_by"`
	ApprovedBy      *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"approved_by"`
	ApprovedAt *time.Time `json:"approved_at"`
	DoneAt     *time.Time `json:"done_at"`
	// What it made on the channel, such as the Reddit comment.
	External  *socialExternal     `json:"external"`
	Results   socialActionResults `json:"results"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
}

const socialActionSelect = `
	SELECT x.id, x.channel, x.type, i.id::text, i.title, i.community, i.url, x.watch_id, x.brand_id, x.integration_id, x.body, x.disclosure,
	       x.link_url, x.utm_content, x.status, x.status_reason, x.drafted_via, x.relevance, x.relevance_reason, x.mission_id, x.created_by,
	       ap.id::text, ap.display_name, x.approved_at, x.done_at, x.external_id, x.external_url, x.score, x.removed,
	       x.created_at, x.updated_at
	FROM social_actions x
	LEFT JOIN social_items i ON i.id = x.item_id
	LEFT JOIN actors ap ON ap.id = x.approved_by
	WHERE x.workspace_id = $1`

func scanSocialAction(row pgx.Row) (socialActionView, error) {
	var v socialActionView
	var iID, iTitle, iCommunity, iURL, apID, apName, eID, eURL *string
	var score *int
	var removed bool
	err := row.Scan(&v.ID, &v.Channel, &v.Type, &iID, &iTitle, &iCommunity, &iURL, &v.WatchID, &v.BrandID, &v.IntegrationID, &v.Body,
		&v.Disclosure, &v.LinkURL, &v.UTMContent, &v.Status, &v.StatusReason, &v.DraftedVia, &v.Relevance, &v.RelevanceReason, &v.MissionID,
		&v.CreatedBy, &apID, &apName, &v.ApprovedAt, &v.DoneAt, &eID, &eURL, &score, &removed, &v.CreatedAt, &v.UpdatedAt)
	if iID != nil {
		v.Item = &socialItemRef{*iID, deref(iTitle), deref(iCommunity), deref(iURL)}
	}
	if apID != nil {
		v.ApprovedBy = &struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}{*apID, deref(apName)}
	}
	if eID != nil {
		v.External = &socialExternal{*eID, deref(eURL), score, removed}
	}
	return v, err
}

// socialResults reads the visitors and conversions of actions that ran,
// from customer events, by each action's UTM content tag.
func (s *Server) socialResults(ctx context.Context, ws string, ids []string, people bool) (map[string]socialActionResults, error) {
	out := map[string]socialActionResults{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		WITH acts AS (
			SELECT x.id, x.utm_content, COALESCE(w.conversion_events, '{signed_up}') AS conversion_events
			FROM social_actions x LEFT JOIN social_watches w ON w.id = x.watch_id
			WHERE x.workspace_id = $1 AND x.id = ANY($2::uuid[]) AND x.done_at IS NOT NULL),
		ev AS (
			SELECT acts.id, e.record_id, e.anonymous_id, e.occurred_at FROM acts JOIN customer_events e ON e.workspace_id = $1
			  AND COALESCE(e.context->'campaign'->>'content', e.properties->>'utm_content') = acts.utm_content),
		touch AS (SELECT id, record_id, min(occurred_at) AS first FROM ev WHERE record_id IS NOT NULL GROUP BY id, record_id),
		conv AS (
			SELECT t.id, t.record_id FROM touch t JOIN acts ON acts.id = t.id
			WHERE EXISTS (SELECT 1 FROM customer_events c WHERE c.workspace_id = $1 AND c.record_id = t.record_id
			              AND c.name = ANY(acts.conversion_events) AND c.occurred_at >= t.first))
		SELECT acts.id::text,
		       (SELECT count(DISTINCT COALESCE(ev.record_id::text, ev.anonymous_id)) FROM ev WHERE ev.id = acts.id)::int,
		       (SELECT count(*) FROM conv WHERE conv.id = acts.id)::int,
		       CASE WHEN $3 THEN (SELECT array_agg(c.record_id::text ORDER BY c.record_id) FROM (SELECT * FROM conv WHERE conv.id = acts.id LIMIT 100) c) END
		FROM acts`, ws, ids, people)
	if err != nil {
		return nil, err
	}
	var id string
	var x socialActionResults
	var recs []string
	_, err = pgx.ForEachRow(rows, []any{&id, &x.Visitors, &x.Conversions, &recs}, func() error {
		res := socialActionResults{Visitors: x.Visitors, Conversions: x.Conversions}
		if people {
			res.ConvertedRecordIDs = recs
		}
		out[id] = res
		return nil
	})
	return out, err
}

var socialActionStatuses = []string{"drafting", "draft", "approved", "done", "rejected", "failed"}

func (s *Server) listSocialActions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	ch, ok := channelParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	status, watch, item := q.Get("status"), q.Get("watch_id"), q.Get("item_id")
	if status != "" && !slices.Contains(socialActionStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is "+strings.Join(socialActionStatuses, ", "))
		return
	}
	if (watch != "" && !validUUID(watch)) || (item != "" && !validUUID(item)) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "watch_id and item_id are ids")
		return
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, status, watch, item, before, limit + 1, ch}
	rows, err := s.pool.Query(r.Context(), socialActionSelect+`
		AND ($2 = '' OR x.status = $2) AND ($3 = '' OR x.watch_id = NULLIF($3, '')::uuid) AND ($4 = '' OR x.item_id = NULLIF($4, '')::uuid)
		AND ($5::timestamptz IS NULL OR x.created_at < $5) AND ($7 = '' OR x.channel = $7)`+sel.sql("x.brand_id", &args)+
		` ORDER BY x.created_at DESC LIMIT $6`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (socialActionView, error) { return scanSocialAction(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) > limit {
		list = list[:limit]
		n := list[limit-1].CreatedAt.Format(time.RFC3339Nano)
		next = &n
	}
	ids := make([]string, len(list))
	for i, v := range list {
		ids[i] = v.ID
	}
	res, err := s.socialResults(r.Context(), a.WorkspaceID, ids, false)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for i := range list {
		list[i].Results = res[list[i].ID]
	}
	if list == nil {
		list = []socialActionView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

func (s *Server) findSocialAction(w http.ResponseWriter, r *http.Request, a *access, id string) (*socialActionView, bool) {
	if validUUID(id) {
		v, err := scanSocialAction(s.pool.QueryRow(r.Context(), socialActionSelect+` AND x.id = $2`, a.WorkspaceID, id))
		if err == nil && a.sees(v.BrandID) {
			res, err := s.socialResults(r.Context(), a.WorkspaceID, []string{v.ID}, true)
			if err != nil {
				s.internal(w, r, err)
				return nil, false
			}
			v.Results = res[v.ID]
			if v.Results.ConvertedRecordIDs == nil {
				v.Results.ConvertedRecordIDs = []string{}
			}
			return &v, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "action not found")
	return nil, false
}

func (s *Server) getSocialAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findSocialAction(w, r, a, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// hasDisclosure reports whether body says disclosure, ignoring case and
// spacing.
func hasDisclosure(body, disclosure string) bool {
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	return disclosure != "" && strings.Contains(norm(body), norm(disclosure))
}

// checkReplyBody checks a reply's text.
func checkReplyBody(body, disclosure string) error {
	if n := len([]rune(body)); n == 0 || n > 10000 {
		return errInvalid("body must be 1 to 10,000 characters")
	}
	if !hasDisclosure(body, disclosure) {
		return errInvalid("the reply must say who wrote it: include the watch's disclosure, \"" + disclosure + "\"")
	}
	return nil
}

// actionLink is the watch's landing page with its campaign's tags: one
// campaign per watch (made the first time it has a reply), the network as
// source and the action as content. Reddit is its own channel; X and
// LinkedIn are organic social.
func actionLink(ctx context.Context, tx pgx.Tx, a *access, ws, watchID, watchName, landing, network string, brandID *string, content string) (string, error) {
	if landing == "" {
		return "", nil
	}
	channel := "organic_social"
	if network == "reddit" {
		channel = "reddit"
	}
	c, created, err := utm.Ensure(ctx, tx, ws, utm.Spec{Channel: channel, Source: network, Name: watchName, BrandID: brandID,
		ObjectKind: "social_watch", ObjectID: watchID})
	if err != nil {
		return "", err
	}
	if created {
		var p *principal
		if a != nil {
			p = &a.principal
		}
		if err := record(ctx, tx, p, ws, "tracking_campaign.created", "tracking_campaign", c.ID,
			map[string]any{"channel": c.Channel, "source": c.Source, "object_kind": "social_watch"}); err != nil {
			return "", err
		}
	}
	link, err := c.Link(landing, content)
	if err != nil {
		return "", nil // a landing page saved before it was checked
	}
	return link, nil
}

// newSocialReply adds a reply to item in tx, with the watch's disclosure,
// a fresh UTM tag and link, and the item marked drafted.
func newSocialReply(ctx context.Context, tx pgx.Tx, a *access, ws string, it *socialItemView, status, via, body string) (string, error) {
	var disclosure, landing, name string
	if err := tx.QueryRow(ctx, `SELECT disclosure, landing_url, name FROM social_watches WHERE id = $1 AND workspace_id = $2`, it.WatchID, ws).
		Scan(&disclosure, &landing, &name); err != nil {
		return "", err
	}
	if status == "draft" {
		if err := checkReplyBody(body, disclosure); err != nil {
			return "", err
		}
	}
	content := it.Channel + "-" + strings.ReplaceAll(newID(), "-", "")[:12]
	var createdBy, mission *string
	if a != nil && a.ActorID != "" {
		createdBy = &a.ActorID
	}
	if m := missions.ID(ctx); m != "" {
		mission = &m
	}
	link, err := actionLink(ctx, tx, a, ws, it.WatchID, name, landing, it.Channel, it.BrandID, content)
	if err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO social_actions (workspace_id, brand_id, channel, type, item_id, watch_id, body, disclosure, link_url,
		utm_content, status, drafted_via, mission_id, created_by, request_id, provider)
		VALUES ($1, $2, $3, 'reply', $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NULLIF($14, ''), 'official') RETURNING id`,
		ws, it.BrandID, it.Channel, it.ID, it.WatchID, body, disclosure, link, content, status, via, mission,
		createdBy, RequestID(ctx)).Scan(&id)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE social_items SET status = 'drafted', updated_at = now() WHERE id = $1 AND status IN ('new', 'ignored')`, it.ID); err != nil {
		return "", err
	}
	return id, nil
}

// openAction reports whether the item already has an action in the works
// or done.
func openAction(ctx context.Context, q pgx.Tx, itemID string) (bool, error) {
	var busy bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM social_actions WHERE item_id = $1 AND status IN ('drafting', 'draft', 'approved', 'done'))`,
		itemID).Scan(&busy)
	return busy, err
}

// createSocialAction drafts a reply a person or agent wrote.
func (s *Server) createSocialAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Type   string `json:"type"`
		ItemID string `json:"item_id"`
		Body   string `json:"body"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Type != "reply" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "type is reply; other actions come with the channels that support them")
		return
	}
	it, ok := s.findSocialItem(w, r, a, req.ItemID)
	if !ok {
		return
	}
	if !a.writes(it.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if busy, err := openAction(ctx, tx, it.ID); err != nil || busy {
			if err == nil {
				err = errConflict
			}
			return err
		}
		var err error
		if id, err = newSocialReply(ctx, tx, a, a.WorkspaceID, it, "draft", "person", strings.TrimSpace(req.Body)); err != nil {
			return err
		}
		if err := syncSocialFixes(ctx, tx, a.WorkspaceID, it.WatchID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_action.drafted", "social_action", id,
			map[string]any{"channel": it.Channel, "type": "reply", "item_id": it.ID, "watch_id": it.WatchID, "via": "person"})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the item already has a reply; edit or reject that one")
		return
	}
	if !s.socialInvalid(w, r, err) {
		return
	}
	if v, ok := s.findSocialAction(w, r, a, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

// draftSocialReply has the workspace's own AI draft a reply, in a job.
func (s *Server) draftSocialReply(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	it, ok := s.findSocialItem(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(it.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	ai, err := s.hasAIModel(ctx, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !ai {
		writeError(w, r, http.StatusConflict, "connector_needed", "drafting needs your own AI model: connect one under Settings, AI model, or write the reply yourself")
		return
	}
	if it.Closed {
		writeError(w, r, http.StatusConflict, "conflict", "the post is archived or locked; it takes no new replies")
		return
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if busy, err := openAction(ctx, tx, it.ID); err != nil || busy {
			if err == nil {
				err = errConflict
			}
			return err
		}
		var err error
		if id, err = queueSocialDraft(ctx, tx, a, a.WorkspaceID, it); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_action.draft_requested", "social_action", id,
			map[string]any{"channel": it.Channel, "type": "reply", "item_id": it.ID, "watch_id": it.WatchID})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the item already has a reply; edit or reject that one")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSocialAction(w, r, a, id); ok {
		writeJSON(w, http.StatusAccepted, v)
	}
}

// queueSocialDraft adds a drafting reply and the job that writes it.
func queueSocialDraft(ctx context.Context, tx pgx.Tx, a *access, ws string, it *socialItemView) (string, error) {
	id, err := newSocialReply(ctx, tx, a, ws, it, "drafting", "ai", "")
	if err != nil {
		return "", err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobSocialDraft, WorkspaceID: &ws, Payload: map[string]string{"action_id": id},
		IdempotencyKey: "social.draft:" + id, MaxAttempts: 3})
	return id, err
}

// hasAIModel reports whether the workspace connected an AI model.
func (s *Server) hasAIModel(ctx context.Context, ws string) (bool, error) {
	var ai bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM integrations WHERE workspace_id = $1 AND connector = ANY($2) AND status = 'connected')`,
		ws, aiConnectorIDs()).Scan(&ai)
	return ai, err
}

// updateSocialAction edits a draft's text.
func (s *Server) updateSocialAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findSocialAction(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	var req struct {
		Body *string `json:"body"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Body == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "body is required")
		return
	}
	if cur.Status != "draft" {
		writeError(w, r, http.StatusConflict, "conflict", "the action is "+cur.Status+"; only a draft can change")
		return
	}
	body := strings.TrimSpace(*req.Body)
	if !s.socialInvalid(w, r, checkReplyBody(body, cur.Disclosure)) {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE social_actions SET body = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2 AND status = 'draft'`,
			a.WorkspaceID, cur.ID, body)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_action.updated", "social_action", cur.ID, map[string]any{"channel": cur.Channel})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the action is no longer a draft")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSocialAction(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// approveSocialAction is a person saying yes to a draft: a job runs it on
// the watch's connection. Only a person can approve, never an API token
// or an agent, and an item gets one reply.
func (s *Server) approveSocialAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if a.ActorType != "human" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a person can approve a social action, in Outflare under Social")
		return
	}
	cur, ok := s.findSocialAction(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	if cur.Status != "draft" {
		writeError(w, r, http.StatusConflict, "conflict", "the action is "+cur.Status+"; only a draft can be approved")
		return
	}
	if !s.socialInvalid(w, r, checkReplyBody(cur.Body, cur.Disclosure)) {
		return
	}
	if cur.WatchID == nil || cur.Item == nil {
		writeError(w, r, http.StatusConflict, "conflict", "the action's watch or post is gone")
		return
	}
	ctx := r.Context()
	watch, ok := s.findSocialWatch(w, r, a, *cur.WatchID)
	if !ok {
		return
	}
	name := socialChannels[cur.Channel].Name
	if watch.IntegrationID == nil {
		writeError(w, r, http.StatusConflict, "connector_needed", "choose the "+name+" connection that acts for this watch first")
		return
	}
	var canAct, closed bool
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT account->'details'->>'posting' = 'on' FROM integrations WHERE id = $1 AND workspace_id = $2), false),
		COALESCE((SELECT closed FROM social_items WHERE id = $3), false)`, *watch.IntegrationID, a.WorkspaceID, cur.Item.ID).Scan(&canAct, &closed); err != nil {
		s.internal(w, r, err)
		return
	}
	if !canAct {
		writeError(w, r, http.StatusConflict, "connector_needed", "sign the "+name+" connection in to the brand's account first: under Social, Sign in to post")
		return
	}
	if closed {
		writeError(w, r, http.StatusConflict, "conflict", "the post is archived or locked; it takes no new replies")
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The watch's daily limit, counted under its lock so two approvals
		// cannot both take the last place.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM social_watches WHERE id = $1 FOR UPDATE`, watch.ID); err != nil {
			return err
		}
		var today int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM social_actions WHERE watch_id = $1 AND status IN ('approved', 'done') AND approved_at > now() - interval '24 hours'`,
			watch.ID).Scan(&today); err != nil {
			return err
		}
		if today >= watch.DailyLimit {
			return errInvalid("limit")
		}
		tag, err := tx.Exec(ctx, `UPDATE social_actions SET status = 'approved', approved_by = $3, approved_at = now(), integration_id = $4, updated_at = now()
			WHERE workspace_id = $1 AND id = $2 AND status = 'draft'`, a.WorkspaceID, cur.ID, a.ActorID, *watch.IntegrationID)
		if isUnique(err) {
			return errConflict
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobSocialAct, WorkspaceID: &a.WorkspaceID, Payload: map[string]string{"action_id": cur.ID},
			IdempotencyKey: "social.act:" + cur.ID, MaxAttempts: 4}); err != nil {
			return err
		}
		if err := syncSocialFixes(ctx, tx, a.WorkspaceID, watch.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_action.approved", "social_action", cur.ID,
			map[string]any{"channel": cur.Channel, "type": cur.Type, "item_id": cur.Item.ID, "watch_id": watch.ID, "drafted_via": cur.DraftedVia})
	})
	var inv errInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusConflict, "conflict", "this watch already reached its "+strconv.Itoa(watch.DailyLimit)+
			" actions in the last 24 hours (its daily_limit); approve this one later")
		return
	case errors.Is(err, errConflict):
		writeError(w, r, http.StatusConflict, "conflict", "the action is no longer a draft, or the post already has an approved reply")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSocialAction(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// rejectSocialAction says no to a draft; the item is open for another.
func (s *Server) rejectSocialAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findSocialAction(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	note, ok := readText(w, r, "note")
	if !ok {
		return
	}
	if cur.Status != "draft" && cur.Status != "drafting" {
		writeError(w, r, http.StatusConflict, "conflict", "the action is "+cur.Status+"; only a draft can be rejected")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE social_actions SET status = 'rejected', status_reason = NULLIF($3, ''), updated_at = now()
			WHERE workspace_id = $1 AND id = $2 AND status IN ('draft', 'drafting')`, a.WorkspaceID, cur.ID, truncate(note, 2000))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		if cur.Item != nil {
			if _, err := tx.Exec(ctx, `UPDATE social_items SET status = 'new', updated_at = now() WHERE id = $1 AND status = 'drafted'`, cur.Item.ID); err != nil {
				return err
			}
		}
		if cur.WatchID != nil {
			if err := syncSocialFixes(ctx, tx, a.WorkspaceID, *cur.WatchID); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_action.rejected", "social_action", cur.ID,
			map[string]any{"channel": cur.Channel, "type": cur.Type, "drafted_via": cur.DraftedVia})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the action is no longer a draft")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSocialAction(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// ---- Fixes ----

const socialFixSource = "social"

// syncSocialFixes keeps a watch's work on the fixes list: posts waiting
// for a reply, and drafts waiting for a person's approval. A fix with
// nothing left is done; one that comes back is open again.
func syncSocialFixes(ctx context.Context, tx pgx.Tx, ws, watchID string) error {
	_, err := tx.Exec(ctx, `
		WITH w AS (SELECT id, brand_id FROM social_watches WHERE id = $2 AND workspace_id = $1),
		n AS (
			SELECT 'social_items_waiting' AS rule, 'notice' AS severity, 'posts' AS unit,
			       (SELECT count(*) FROM social_items i WHERE i.watch_id = $2 AND i.status = 'new' AND NOT i.closed)::int AS cnt
			UNION ALL
			SELECT 'social_actions_to_approve', 'warning', 'actions',
			       (SELECT count(*) FROM social_actions x WHERE x.watch_id = $2 AND x.status = 'draft')::int),
		up AS (
			INSERT INTO fixes (workspace_id, brand_id, source, subject_type, subject_id, rule, severity, group_key, count, unit)
			SELECT $1, w.brand_id, $3, 'social_watch', w.id, n.rule, n.severity, '', n.cnt, n.unit FROM w, n WHERE n.cnt > 0
			ON CONFLICT (workspace_id, source, subject_id, rule, group_key) DO UPDATE SET
				count = EXCLUDED.count, brand_id = EXCLUDED.brand_id,
				status = CASE WHEN fixes.status = 'done' THEN 'open' ELSE fixes.status END,
				fixed_at = CASE WHEN fixes.status = 'done' THEN NULL ELSE fixes.fixed_at END,
				updated_at = now()
			RETURNING rule)
		UPDATE fixes f SET count = 0, status = CASE WHEN f.status = 'ignored' THEN f.status ELSE 'done' END,
			fixed_at = CASE WHEN f.status = 'ignored' THEN f.fixed_at ELSE now() END, updated_at = now()
		FROM n WHERE f.workspace_id = $1 AND f.source = $3 AND f.subject_id = $2 AND f.rule = n.rule AND n.cnt = 0
		  AND (f.count <> 0 OR f.status IN ('open', 'in_progress'))`, ws, watchID, socialFixSource)
	return err
}
