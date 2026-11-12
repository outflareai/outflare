package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/geoip"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/tokens"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/tracking"
	"github.com/outflareai/outflare/engine/internal/trafficsources"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// Landers (plans/ad-tracker.md, T6b). A lander is a page a brand sends
// tracker clicks to before its offers: a review, a quiz, an advertorial. A
// link's destination can be a lander; a list of destinations (a path's, or
// the link's own) with landers sends each click to one of them first. The
// lander's calls to action link to /click (an offer or page of the same
// list, by weight) or /click/N (the Nth of them) on the tracking domain,
// with the click's id (ofclk), which the lander script adds to them.
//
// The click id is the protection: /click goes on only for a click Outflare
// sent to a lander in the last day, on the workspace's own domain; anything
// else gets the domain's fallback page. Nothing is kept in the browser.

const (
	maxLanders = 1000
	maxCTAs    = 100
	// landerClickMaxAge is how long after its click a lander's call to
	// action still goes on.
	landerClickMaxAge = 24 * time.Hour
)

func (s *Server) routesTrackerLanders(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /click", s.landerClick)
	mux.HandleFunc("GET /click/{n}", s.landerClick)
	mux.HandleFunc("GET /click.js", s.landerScript)
	mux.HandleFunc("GET /v1/brands/{brand}/landers", s.onBrand(read, s.listLanders))
	mux.HandleFunc("POST /v1/brands/{brand}/landers", s.onBrand(manage, s.createLander))
	mux.HandleFunc("GET /v1/brands/{brand}/landers/{id}", s.onBrand(read, s.getLander))
	mux.HandleFunc("PATCH /v1/brands/{brand}/landers/{id}", s.onBrand(manage, s.updateLander))
	mux.HandleFunc("GET /v1/brands/{brand}/lander-clicks", s.onBrand(read, s.listLanderClicks))
}

// The lander click.

// landerSource is the click a lander click continues, as /click needs it.
type landerSource struct {
	id                               int64
	ws, brand, key, link, path, code string
	lander, sourceClickID            string
	fields                           map[string]string
	cost                             int64
	at                               time.Time
	bot                              bool
	country, region, city, isp       string
	asn                              int64
	device, os, browser, language    string
	// fraud are the click's fraud reasons.
	fraud []string
}

// findLanderSource finds the click a lander click names, writing this
// server's waiting clicks first if it isn't written yet: a visitor can
// press a lander's button seconds after the redirect.
func (s *Server) findLanderSource(ctx context.Context, key string) (landerSource, error) {
	var c landerSource
	lookup := func() error {
		var fields []byte
		err := s.pool.QueryRow(ctx, `SELECT k.id, k.workspace_id::text, COALESCE(k.brand_id::text, ''), k.click_key, k.link_id::text,
				COALESCE(k.path_id::text, ''), COALESCE(l.redirect_code, ''), COALESCE(k.lander_id::text, ''), k.source_click_id, k.fields, k.cost_micros,
				k.clicked_at, k.likely_bot, k.country, k.region, k.city, k.isp, k.asn, k.device, k.os, k.browser, k.language, k.fraud_reasons
			FROM tracking_link_clicks k JOIN tracking_links l ON l.id = k.link_id
			WHERE k.click_key = $1`, key).Scan(&c.id, &c.ws, &c.brand, &c.key, &c.link, &c.path, &c.code, &c.lander, &c.sourceClickID, &fields, &c.cost,
			&c.at, &c.bot, &c.country, &c.region, &c.city, &c.isp, &c.asn, &c.device, &c.os, &c.browser, &c.language, &c.fraud)
		if err == nil {
			err = json.Unmarshal(fields, &c.fields)
		}
		return err
	}
	err := lookup()
	if errors.Is(err, pgx.ErrNoRows) {
		if ferr := s.FlushClicks(ctx); ferr == nil {
			err = lookup()
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return c, errUnknownClick
	}
	return c, err
}

// landerClick sends a lander's call to action on: /click to one of the
// open offers or pages of the lander's list by weight, /click/N to the Nth
// of them while it is open (else by weight), and the link's own page when
// none is. Public: the click id is the authority.
func (s *Server) landerClick(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n := 0
	if v := r.PathValue("n"); v != "" {
		x, err := strconv.Atoi(v)
		if err != nil || x < 1 || x > maxCTAs {
			metrics.TrackerLanderClick("unknown_click")
			s.unknownCode(w, r)
			return
		}
		n = x
	}
	key := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(clickKeyParam)))
	if !clickKeyRe.MatchString(key) {
		metrics.TrackerLanderClick("unknown_click")
		s.unknownCode(w, r)
		return
	}
	now := time.Now()
	c, err := s.findLanderSource(ctx, key)
	switch {
	case errors.Is(err, errUnknownClick):
		metrics.TrackerLanderClick("unknown_click")
		s.unknownCode(w, r)
		return
	case err != nil:
		s.landerClickFailed(w, r, err)
		return
	case c.lander == "" || now.Sub(c.at) > landerClickMaxAge:
		// Only a click that went to a lander, lately, goes on.
		metrics.TrackerLanderClick("unknown_click")
		s.unknownCode(w, r)
		return
	}
	if _, own := s.ownDomain(r, c.ws); !own {
		metrics.TrackerLanderClick("wrong_domain")
		s.unknownCode(w, r)
		return
	}
	t, ok := s.redirects.get(c.code, now)
	if !ok {
		if t, err = s.lookupRedirect(ctx, c.code); err != nil {
			s.landerClickFailed(w, r, err)
			return
		}
		s.redirects.put(c.code, t, now)
	}
	if !t.found {
		metrics.TrackerLanderClick("unknown_click")
		s.unknownCode(w, r)
		return
	}
	// The lander's list: its path's destinations, or the link's own.
	list := t.dests
	for _, p := range t.paths {
		if p.id == c.path {
			list = p.dests
		}
	}
	// A likely bot's or a suspect click's lander click goes on like any
	// other, but counts against no cap.
	bot := c.bot || tracking.LikelyBot(r.UserAgent(), time.Time{}, now)
	uncounted := bot || len(c.fraud) > 0
	var d *linkDestination
	next := onward(list)
	if n > 0 && n <= len(next) && s.caps.open(next[n-1], capDay(now)) {
		d = s.take(next[n-1], capDay(now), uncounted)
	}
	if d == nil {
		d = s.route(next, now, uncounted)
	}
	link := t.link
	outcome := "fallback"
	if d != nil {
		link.URL, outcome = d.url, "routed"
	}
	if strings.ContainsAny(link.URL, "{%") {
		link.URL = tokens.ExpandKnown(link.URL, destinationValues(t, c.buffered(), formatMicros(c.cost)), trafficsources.DestinationTokens)
	}
	target := withParam(tagLink(t.campaign, link), clickKeyParam, c.key)
	if err := s.saveLanderClick(ctx, c, n, d, bot, uncounted, now); err != nil {
		// The person still goes on; the lander click is lost.
		track.Fail(ctx, s.log, track.Event{Name: "tracker_lander.click_failed", Surface: "system"}, "internal", err)
		outcome = "error"
	}
	metrics.TrackerLanderClick(outcome)
	s.sendOn(w, r, target, t.campaign.RedirectMode)
}

func (s *Server) landerClickFailed(w http.ResponseWriter, r *http.Request, err error) {
	metrics.TrackerLanderClick("error")
	track.Fail(r.Context(), s.log, track.Event{Name: "tracker_lander.click_failed", Surface: "system"}, "internal", err)
	s.unknownCode(w, r)
}

// buffered is the click as destination tokens read it.
func (c landerSource) buffered() bufferedClick {
	return bufferedClick{key: c.key, fields: c.fields, sourceClickID: c.sourceClickID, detail: clickDetail{
		place:  geoip.Place{Country: c.country, Region: c.region, City: c.city, Org: c.isp, ASN: uint32(c.asn)},
		device: c.device, os: c.os, browser: c.browser, language: c.language,
	}}
}

// saveLanderClick keeps a lander click, gives the click the offer it went
// on to (so its conversions get the offer's terms), and counts it against
// the destination's and the offer's daily caps.
func (s *Server) saveLanderClick(ctx context.Context, c landerSource, cta int, d *linkDestination, bot, uncounted bool, now time.Time) error {
	var dest, offer string
	if d != nil {
		dest, offer = d.id, d.offerID
	}
	day := capDay(now)
	var settles []destinationSettle
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tracker_lander_clicks (workspace_id, brand_id, lander_id, click_id, click_key, cta, destination_id, offer_id,
				likely_bot, clicked_at)
			VALUES ($1, NULLIF($2, '')::uuid, (SELECT id FROM tracker_landers WHERE id = $3::uuid), $4, $5, $6, NULLIF($7, '')::uuid, NULLIF($8, '')::uuid, $9, $10)`,
			c.ws, c.brand, c.lander, c.id, c.key, cta, dest, offer, bot, now); err != nil {
			return err
		}
		if offer != "" {
			if _, err := tx.Exec(ctx, `UPDATE tracking_link_clicks SET offer_id = $2 WHERE id = $1`, c.id, offer); err != nil {
				return err
			}
		}
		if uncounted || dest == "" {
			return nil
		}
		var total int64
		if err := tx.QueryRow(ctx, `INSERT INTO tracking_destination_days (destination_id, workspace_id, brand_id, day, clicks)
			SELECT id, workspace_id, brand_id, $2::date, 1 FROM tracking_link_destinations WHERE id = $1
			ON CONFLICT (destination_id, day) DO UPDATE SET clicks = tracking_destination_days.clicks + 1 RETURNING clicks`, dest, day).Scan(&total); err != nil {
			return err
		}
		settles = append(settles, destinationSettle{id: dest, day: day, n: 1, total: total})
		if offer != "" {
			err := tx.QueryRow(ctx, `INSERT INTO tracker_offer_days (offer_id, workspace_id, brand_id, day, clicks)
				SELECT id, workspace_id, brand_id, $2::date, 1 FROM tracker_offers WHERE id = $1
				ON CONFLICT (offer_id, day) DO UPDATE SET clicks = tracker_offer_days.clicks + 1 RETURNING clicks`, offer, day).Scan(&total)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil {
				settles = append(settles, destinationSettle{id: offer, day: day, n: 1, total: total})
			}
		}
		return nil
	})
	if err != nil {
		// What this server held against the caps for the click is let go.
		if !uncounted {
			for _, id := range []string{dest, offer} {
				if id != "" {
					s.caps.settle(id, day, 1, 0)
				}
			}
		}
		return err
	}
	for _, x := range settles {
		s.caps.settle(x.id, x.day, x.n, x.total)
	}
	return nil
}

// landerScript adds the click id a lander was opened with to its links to
// /click on the tracking domain the script came from. It stores nothing.
func (s *Server) landerScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = io.WriteString(w, `(function(){var s=document.currentScript;if(!s)return;var o=new URL(s.src).origin;`+
		`var k=new URLSearchParams(location.search).get("ofclk");if(!k)return;`+
		`function f(){var a=document.querySelectorAll('a[href^="'+o+'/click"]');for(var i=0;i<a.length;i++){var u=new URL(a[i].href);`+
		`if(!u.searchParams.get("ofclk")){u.searchParams.set("ofclk",k);a[i].href=u.toString();}}}`+
		`if(document.readyState==="loading")document.addEventListener("DOMContentLoaded",f);else f();})();`)
}

// Landers.

type trackerLanderView struct {
	ID      string `json:"id"`
	BrandID string `json:"brand_id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	CTAs    int    `json:"ctas"`
	Status  string `json:"status"`
	// ClickURL is where its calls to action link to go on by weight, and
	// CTAURLs where each one links to go to its own offer (/click/1 to
	// /click/N), on the brand's tracking domain.
	ClickURL string   `json:"click_url"`
	CTAURLs  []string `json:"cta_urls"`
	// Script is the tag the lander's page carries, which adds the click id
	// to those links.
	Script string `json:"script"`
	// LanderClicksToday are clicks on its calls to action today (UTC),
	// without link scanners and previews.
	LanderClicksToday int64 `json:"lander_clicks_today"`
	// Destinations is how many link destinations send to it.
	Destinations int `json:"destinations"`
	// NeedsDomain says the brand has no verified tracking domain, where
	// /click answers.
	NeedsDomain bool      `json:"needs_domain"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const landerSelect = `
	SELECT x.id::text, x.brand_id::text, x.name, x.url, x.ctas, x.status,
	       (SELECT count(*) FROM tracker_lander_clicks c WHERE c.lander_id = x.id AND c.clicked_at >= $3::date AND NOT c.likely_bot),
	       (SELECT count(*) FROM tracking_link_destinations d WHERE d.lander_id = x.id AND d.removed_at IS NULL)::int, x.created_at, x.updated_at
	FROM tracker_landers x WHERE x.workspace_id = $1 AND x.brand_id = $2`

func scanLander(row pgx.Row) (trackerLanderView, error) {
	var v trackerLanderView
	err := row.Scan(&v.ID, &v.BrandID, &v.Name, &v.URL, &v.CTAs, &v.Status, &v.LanderClicksToday, &v.Destinations, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// withURLs fills in the lander's click URLs and script on the brand's
// tracking domain.
func (v *trackerLanderView) withURLs(base string, needsDomain bool) {
	v.ClickURL, v.NeedsDomain = base+"/click", needsDomain
	v.CTAURLs = make([]string, v.CTAs)
	for i := range v.CTAURLs {
		v.CTAURLs[i] = base + "/click/" + strconv.Itoa(i+1)
	}
	v.Script = `<script src="` + base + `/click.js" async></script>`
}

func (s *Server) landerBase(ctx context.Context, ws, brand string) (string, bool, error) {
	base, err := tracking.BaseURL(ctx, s.pool, ws, &brand, s.publicURL)
	return base, base == strings.TrimRight(s.publicURL, "/"), err
}

func (s *Server) listLanders(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	switch status {
	case "":
		status = "active"
	case "active", "archived", "all":
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active, archived or all")
		return
	}
	ctx := r.Context()
	rows, err := s.pool.Query(ctx, landerSelect+` AND ($4 = 'all' OR x.status = $4) ORDER BY lower(x.name)`, a.WorkspaceID, a.Brand, capDay(time.Now()), status)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (trackerLanderView, error) { return scanLander(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	base, needs, err := s.landerBase(ctx, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for i := range list {
		list[i].withURLs(base, needs)
	}
	if list == nil {
		list = []trackerLanderView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) writeLander(w http.ResponseWriter, r *http.Request, status int, id string) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var v trackerLanderView
	err := errNotFound
	if validUUID(id) {
		v, err = scanLander(s.pool.QueryRow(ctx, landerSelect+` AND x.id = $4`, a.WorkspaceID, a.Brand, capDay(time.Now()), id))
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "lander not found")
		return
	}
	var base string
	var needs bool
	if err == nil {
		base, needs, err = s.landerBase(ctx, a.WorkspaceID, a.Brand)
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v.withURLs(base, needs)
	writeJSON(w, status, v)
}

func (s *Server) getLander(w http.ResponseWriter, r *http.Request) {
	s.writeLander(w, r, http.StatusOK, r.PathValue("id"))
}

type trackerLanderInput struct {
	Name   *string `json:"name"`
	URL    *string `json:"url"`
	CTAs   *int    `json:"ctas"`
	Status *string `json:"status"`
}

func (in *trackerLanderInput) check() error {
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 120 {
			return errInvalid("name is 1 to 120 characters")
		}
		in.Name = &n
	}
	if in.URL != nil {
		u := strings.TrimSpace(*in.URL)
		if err := checkDestinationTokens("url", u); err != nil {
			return err
		}
		dest, err := utm.Destination(u)
		if err != nil {
			return errInvalid("url: " + err.Error())
		}
		in.URL = &dest
	}
	if in.CTAs != nil && (*in.CTAs < 1 || *in.CTAs > maxCTAs) {
		return errInvalid("ctas is 1 to 100: how many calls to action the page has")
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "archived" {
		return errInvalid("status is active or archived")
	}
	return nil
}

func (s *Server) landerSaveError(w http.ResponseWriter, r *http.Request, err error) bool {
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "lander not found")
		return true
	case errors.As(err, &pe) && pe.Code == "23505":
		writeError(w, r, http.StatusConflict, "conflict", "the brand already has a lander with that name")
		return true
	}
	return s.allocError(w, r, err)
}

func (s *Server) createLander(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in trackerLanderInput
	if !decode(w, r, &in) {
		return
	}
	switch {
	case in.Name == nil || in.URL == nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name and url are required")
		return
	case in.Status != nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a new lander is active; archive it with PATCH")
		return
	}
	if s.allocError(w, r, in.check()) {
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tracker_landers WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxLanders {
			return errInvalid("a brand has at most 1,000 landers; archive old ones")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO tracker_landers (workspace_id, brand_id, name, url, ctas, created_by)
			VALUES ($1, $2, $3, $4, COALESCE($5, 1), $6) RETURNING id::text`,
			a.WorkspaceID, a.Brand, *in.Name, *in.URL, in.CTAs, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		ctas := 1
		if in.CTAs != nil {
			ctas = *in.CTAs
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracker_lander.created", "tracker_lander", id,
			map[string]any{"brand_id": a.Brand, "ctas": ctas})
	})
	if s.landerSaveError(w, r, err) {
		return
	}
	s.writeLander(w, r, http.StatusCreated, id)
}

// updateLander changes a lander. A new URL goes to every destination that
// sends to it at once; archiving it is refused while one does.
func (s *Server) updateLander(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var in trackerLanderInput
	if !decode(w, r, &in) {
		return
	}
	if s.allocError(w, r, in.check()) {
		return
	}
	ctx := r.Context()
	err := errNotFound
	if validUUID(id) {
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			var was string
			var used int
			err := tx.QueryRow(ctx, `SELECT status, (SELECT count(*) FROM tracking_link_destinations d WHERE d.lander_id = x.id AND d.removed_at IS NULL)
				FROM tracker_landers x WHERE workspace_id = $1 AND brand_id = $2 AND id = $3 FOR UPDATE`, a.WorkspaceID, a.Brand, id).Scan(&was, &used)
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			if err != nil {
				return err
			}
			if in.Status != nil && *in.Status == "archived" && was != "archived" && used > 0 {
				return errInvalid("the lander is a destination of " + strconv.Itoa(used) + " link(s); take it off them before archiving it")
			}
			if _, err := tx.Exec(ctx, `UPDATE tracker_landers SET name = COALESCE($3, name), url = COALESCE($4, url), ctas = COALESCE($5, ctas),
					status = COALESCE($6, status), updated_at = now()
				WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id, in.Name, in.URL, in.CTAs, in.Status); err != nil {
				return err
			}
			// Destinations keep the lander's URL, so the version before
			// still sends their clicks there.
			if in.URL != nil {
				if _, err := tx.Exec(ctx, `UPDATE tracking_link_destinations SET url = $3, updated_at = now() WHERE workspace_id = $1 AND lander_id = $2`,
					a.WorkspaceID, id, *in.URL); err != nil {
					return err
				}
			}
			action := "tracker_lander.updated"
			if in.Status != nil && *in.Status != was {
				action = map[string]string{"archived": "tracker_lander.archived", "active": "tracker_lander.restored"}[*in.Status]
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, action, "tracker_lander", id,
				map[string]any{"brand_id": a.Brand, "destinations": used, "url": in.URL != nil})
		})
	}
	if s.landerSaveError(w, r, err) {
		return
	}
	s.redirects.clear()
	s.writeLander(w, r, http.StatusOK, id)
}

// Lander clicks.

type landerClickView struct {
	ID            string    `json:"id"`
	LanderID      *string   `json:"lander_id"`
	ClickKey      string    `json:"click_key"`
	CTA           int       `json:"cta"`
	DestinationID *string   `json:"destination_id"`
	OfferID       *string   `json:"offer_id"`
	LikelyBot     bool      `json:"likely_bot"`
	ClickedAt     time.Time `json:"clicked_at"`
}

// listLanderClicks lists the brand's lander clicks, newest first.
func (s *Server) listLanderClicks(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	lander, click := q.Get("lander"), strings.ToLower(q.Get("click_id"))
	switch {
	case lander != "" && !validUUID(lander):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "lander is a lander's id")
		return
	case len(click) > 32:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "click_id is a click key, 16 letters and digits")
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id::text, lander_id::text, click_key, cta, destination_id::text, offer_id::text, likely_bot, clicked_at
		FROM tracker_lander_clicks WHERE workspace_id = $1 AND brand_id = $2
		  AND ($3 = '' OR lander_id = $3::uuid) AND ($4 = '' OR click_key = $4) AND ($5::timestamptz IS NULL OR clicked_at < $5)
		ORDER BY clicked_at DESC LIMIT $6`, a.WorkspaceID, a.Brand, lander, click, before, limit+1)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (landerClickView, error) {
		var v landerClickView
		err := row.Scan(&v.ID, &v.LanderID, &v.ClickKey, &v.CTA, &v.DestinationID, &v.OfferID, &v.LikelyBot, &v.ClickedAt)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *time.Time
	if len(list) > limit {
		next, list = &list[limit-1].ClickedAt, list[:limit]
	}
	if list == nil {
		list = []landerClickView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}
