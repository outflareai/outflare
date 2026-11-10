package api

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// Weighted destinations and daily click caps (plans/ad-tracker.md, T4a). A
// redirect link can send its clicks to several pages, each in proportion to
// its weight, and stop sending to one once it has had its clicks for the
// day (a cap of 0 stops it now). With every destination capped or stopped,
// clicks go to the link's own page, the fallback.
//
// A redirect never waits on the database for a cap: each server counts the
// clicks it sends to a capped destination in memory, and adds them to the
// day's count (tracking_destination_days) when it writes its batch, which
// gives back the count from every server. So a cap can be passed by what
// the servers send in the moment before their next batch, not more.

const maxLinkDestinations = 20

// linkDestination is one of a link's destinations, as a redirect needs it.
type linkDestination struct {
	id, name, url string
	weight        int
	cap           *int
}

// capDay is the day a cap counts clicks on: the brand's day once brands
// have a reporting time zone (B4a), UTC until then.
func capDay(t time.Time) string { return t.UTC().Format(time.DateOnly) }

// capCounts are this server's view of today's clicks on capped
// destinations.
type capCounts struct {
	mu sync.Mutex
	m  map[string]*capCount
}

type capCount struct {
	day string
	// total is the count from every server as of the last read or batch;
	// pending is what this server sent since and hasn't written yet.
	total, pending int64
}

func (c *capCounts) get(id, day string) *capCount {
	if c.m == nil {
		c.m = map[string]*capCount{}
	}
	e := c.m[id]
	if e == nil || e.day != day {
		e = &capCount{day: day}
		c.m[id] = e
	}
	return e
}

// seed records a count read from the database; it never lowers one.
func (c *capCounts) seed(id, day string, total int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.get(id, day); total > e.total {
		e.total = total
	}
}

// open reports whether a destination still takes clicks today.
func (c *capCounts) open(d linkDestination, day string) bool {
	if d.weight <= 0 {
		return false
	}
	if d.cap == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.get(d.id, day)
	return e.total+e.pending < int64(*d.cap)
}

// take counts a click for a capped destination, if it still has room.
func (c *capCounts) take(d linkDestination, day string) bool {
	if d.cap == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.get(d.id, day)
	if e.total+e.pending >= int64(*d.cap) {
		return false
	}
	e.pending++
	return true
}

// settle records a written batch: n of this server's clicks are now in the
// count, which is total.
func (c *capCounts) settle(id, day string, n, total int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.get(id, day)
	e.pending = max(e.pending-n, 0)
	if total > e.total {
		e.total = total
	}
}

// route picks a click's destination: one of the open ones, in proportion to
// their weights, or nil for the link's own page. A likely bot goes where a
// person would but isn't counted against a cap.
func (s *Server) route(dests []linkDestination, now time.Time, bot bool) *linkDestination {
	if len(dests) == 0 {
		return nil
	}
	day := capDay(now)
	for range 3 {
		var open []linkDestination
		total := 0
		for _, d := range dests {
			if s.caps.open(d, day) {
				open = append(open, d)
				total += d.weight
			}
		}
		if total == 0 {
			break
		}
		n := rand.IntN(total)
		var pick linkDestination
		for _, d := range open {
			if n < d.weight {
				pick = d
				break
			}
			n -= d.weight
		}
		// Another click may have taken the last of its cap meanwhile; then
		// pick again.
		if bot || s.caps.take(pick, day) {
			metrics.RedirectRoute("destination")
			return &pick
		}
	}
	metrics.RedirectRoute("fallback")
	return nil
}

// loadDestinations reads a link's destinations for the redirect cache and
// seeds today's counts of the capped ones.
func (s *Server) loadDestinations(ctx context.Context, link string, now time.Time) ([]linkDestination, error) {
	day := capDay(now)
	rows, err := s.pool.Query(ctx, `SELECT d.id::text, d.name, d.url, d.weight, d.daily_click_cap, COALESCE(c.clicks, 0)
		FROM tracking_link_destinations d
		LEFT JOIN tracking_destination_days c ON c.destination_id = d.id AND c.day = $2::date
		WHERE d.link_id = $1 AND d.removed_at IS NULL AND d.path_id IS NULL ORDER BY d.position, d.created_at`, link, day)
	if err != nil {
		return nil, err
	}
	var out []linkDestination
	for rows.Next() {
		var d linkDestination
		var clicks int64
		if err := rows.Scan(&d.id, &d.name, &d.url, &d.weight, &d.cap, &clicks); err != nil {
			rows.Close()
			return nil, err
		}
		if d.cap != nil {
			s.caps.seed(d.id, day, clicks)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// countDestinationClicks adds a written batch's clicks to the day's counts,
// in the batch's transaction, and returns what to settle once it commits.
func countDestinationClicks(ctx context.Context, tx pgx.Tx, batch []bufferedClick) ([]destinationSettle, error) {
	type key struct{ id, day string }
	counts := map[key]int64{}
	for _, c := range batch {
		if c.destinationID != "" && !c.bot {
			counts[key{c.destinationID, capDay(c.at)}]++
		}
	}
	if len(counts) == 0 {
		return nil, nil
	}
	var ids, days []string
	var ns []int64
	for k, n := range counts {
		ids, days, ns = append(ids, k.id), append(days, k.day), append(ns, n)
	}
	rows, err := tx.Query(ctx, `INSERT INTO tracking_destination_days (destination_id, workspace_id, brand_id, day, clicks)
		SELECT d.id, d.workspace_id, d.brand_id, t.day, t.n
		FROM unnest($1::text[], $2::date[], $3::bigint[]) AS t(id, day, n)
		JOIN tracking_link_destinations d ON d.id = t.id::uuid
		ON CONFLICT (destination_id, day) DO UPDATE SET clicks = tracking_destination_days.clicks + EXCLUDED.clicks
		RETURNING destination_id::text, day::text, clicks`, ids, days, ns)
	if err != nil {
		return nil, err
	}
	var out []destinationSettle
	for rows.Next() {
		var d destinationSettle
		if err := rows.Scan(&d.id, &d.day, &d.total); err != nil {
			rows.Close()
			return nil, err
		}
		d.n = counts[key{d.id, d.day}]
		out = append(out, d)
	}
	return out, rows.Err()
}

type destinationSettle struct {
	id, day  string
	n, total int64
}

// API: a link's destinations, read and replaced as one list.

func (s *Server) routesLinkDestinations(mux *http.ServeMux) {
	path := "/v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/destinations"
	mux.HandleFunc("GET "+path, s.onBrand(auth.ScopeAnalyticsRead, s.listLinkDestinations))
	mux.HandleFunc("PUT "+path, s.onBrand(auth.ScopeAnalyticsManage, s.putLinkDestinations))
}

type linkDestinationView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Weight int    `json:"weight"`
	// DailyClickCap is how many clicks a day it takes; nil for no cap, 0
	// stops it.
	DailyClickCap *int `json:"daily_click_cap"`
	// ClicksToday are its clicks today, counted against its cap, without
	// likely bots.
	ClicksToday int64 `json:"clicks_today"`
	// Open says it takes clicks now: a weight above 0 and room in its cap.
	Open bool `json:"open"`
}

type linkDestinationsView struct {
	LinkID string `json:"link_id"`
	// FallbackURL is the link's own page, where clicks go when every
	// destination is capped or stopped.
	FallbackURL  string                `json:"fallback_url"`
	Destinations []linkDestinationView `json:"destinations"`
}

// findRedirectLink finds a link of the route's campaign.
func (s *Server) findRedirectLink(w http.ResponseWriter, r *http.Request) (c *trackingCampaignView, link string, url string, code *string, ok bool) {
	a := accessFrom(r.Context())
	c, ok = s.findTrackingCampaign(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return nil, "", "", nil, false
	}
	link = r.PathValue("link")
	if !validUUID(link) {
		writeError(w, r, http.StatusNotFound, "not_found", "link not found")
		return nil, "", "", nil, false
	}
	err := s.pool.QueryRow(r.Context(), `SELECT url, redirect_code FROM tracking_links WHERE workspace_id = $1 AND campaign_id = $2 AND id = $3`,
		a.WorkspaceID, c.ID, link).Scan(&url, &code)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "link not found")
		return nil, "", "", nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, "", "", nil, false
	}
	return c, link, url, code, true
}

func (s *Server) readLinkDestinations(ctx context.Context, ws, link, fallback string) (linkDestinationsView, error) {
	day := capDay(time.Now())
	out := linkDestinationsView{LinkID: link, FallbackURL: fallback, Destinations: []linkDestinationView{}}
	rows, err := s.pool.Query(ctx, `SELECT d.id::text, d.name, d.url, d.weight, d.daily_click_cap, COALESCE(c.clicks, 0)
		FROM tracking_link_destinations d
		LEFT JOIN tracking_destination_days c ON c.destination_id = d.id AND c.day = $3::date
		WHERE d.workspace_id = $1 AND d.link_id = $2 AND d.removed_at IS NULL AND d.path_id IS NULL ORDER BY d.position, d.created_at`, ws, link, day)
	if err != nil {
		return out, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (linkDestinationView, error) {
		var v linkDestinationView
		err := row.Scan(&v.ID, &v.Name, &v.URL, &v.Weight, &v.DailyClickCap, &v.ClicksToday)
		v.Open = v.Weight > 0 && (v.DailyClickCap == nil || v.ClicksToday < int64(*v.DailyClickCap))
		return v, err
	})
	if list != nil {
		out.Destinations = list
	}
	return out, err
}

func (s *Server) listLinkDestinations(w http.ResponseWriter, r *http.Request) {
	_, link, url, _, ok := s.findRedirectLink(w, r)
	if !ok {
		return
	}
	v, err := s.readLinkDestinations(r.Context(), accessFrom(r.Context()).WorkspaceID, link, url)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type linkDestinationInput struct {
	// ID keeps a destination the link has (its counts and its clicks'
	// history); without one, it is a new destination.
	ID            string `json:"id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	Weight        *int   `json:"weight"`
	DailyClickCap *int   `json:"daily_click_cap"`
}

// putLinkDestinations replaces a link's destinations with the list given.
// Destinations left out are taken off the link (their clicks keep them).
func (s *Server) putLinkDestinations(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	c, link, url, code, ok := s.findRedirectLink(w, r)
	if !ok {
		return
	}
	var in struct {
		Destinations []linkDestinationInput `json:"destinations"`
	}
	if !decode(w, r, &in) {
		return
	}
	if code == nil && len(in.Destinations) > 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "destinations route a redirect link's clicks, and this link has none; make it again with redirect: true first")
		return
	}
	if len(in.Destinations) > maxLinkDestinations {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a link has at most 20 destinations")
		return
	}
	seen := map[string]bool{}
	capped := 0
	for i := range in.Destinations {
		d := &in.Destinations[i]
		d.ID, d.Name = strings.TrimSpace(d.ID), strings.TrimSpace(d.Name)
		if d.Weight == nil {
			hundred := 100
			d.Weight = &hundred
		}
		what := "destinations[" + strconv.Itoa(i) + "]"
		switch {
		case d.ID != "" && (!validUUID(d.ID) || seen[d.ID]):
			writeError(w, r, http.StatusBadRequest, "invalid_request", what+".id is one of the link's destinations, once")
			return
		case len(d.Name) > 120:
			writeError(w, r, http.StatusBadRequest, "invalid_request", what+".name is at most 120 characters")
			return
		case *d.Weight < 0 || *d.Weight > 10000:
			writeError(w, r, http.StatusBadRequest, "invalid_request", what+".weight is 0 to 10,000; 0 sends it no clicks")
			return
		case d.DailyClickCap != nil && (*d.DailyClickCap < 0 || *d.DailyClickCap > 100_000_000):
			writeError(w, r, http.StatusBadRequest, "invalid_request", what+".daily_click_cap is 0 (stopped) or more; leave it out for no cap")
			return
		}
		seen[d.ID] = d.ID != ""
		if err := checkDestinationTokens(what+".url", d.URL); err != nil {
			s.allocError(w, r, err)
			return
		}
		dest, err := utm.Destination(d.URL)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", what+".url: "+err.Error())
			return
		}
		d.URL = dest
		if d.DailyClickCap != nil {
			capped++
		}
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock the link's destinations so two saves don't interleave.
		rows, err := tx.Query(ctx, `SELECT id::text FROM tracking_link_destinations WHERE workspace_id = $1 AND link_id = $2 AND removed_at IS NULL AND path_id IS NULL FOR UPDATE`,
			a.WorkspaceID, link)
		if err != nil {
			return err
		}
		have, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		keep := map[string]bool{}
		for i, d := range in.Destinations {
			if d.ID == "" {
				if _, err := tx.Exec(ctx, `INSERT INTO tracking_link_destinations (workspace_id, brand_id, link_id, name, url, weight, daily_click_cap, position)
					SELECT l.workspace_id, COALESCE(l.brand_id, c.brand_id, outflare_first_brand(l.workspace_id)), l.id, $3, $4, $5, $6, $7
					FROM tracking_links l JOIN tracking_campaigns c ON c.id = l.campaign_id WHERE l.workspace_id = $1 AND l.id = $2`,
					a.WorkspaceID, link, d.Name, d.URL, *d.Weight, d.DailyClickCap, i); err != nil {
					return err
				}
				continue
			}
			tag, err := tx.Exec(ctx, `UPDATE tracking_link_destinations SET name = $4, url = $5, weight = $6, daily_click_cap = $7, position = $8, updated_at = now()
				WHERE workspace_id = $1 AND link_id = $2 AND id = $3 AND removed_at IS NULL AND path_id IS NULL`,
				a.WorkspaceID, link, d.ID, d.Name, d.URL, *d.Weight, d.DailyClickCap, i)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errInvalid("destinations[" + strconv.Itoa(i) + "].id isn't one of the link's destinations; leave it out to add a new one")
			}
			keep[d.ID] = true
		}
		var gone []string
		for _, id := range have {
			if !keep[id] {
				gone = append(gone, id)
			}
		}
		if len(gone) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE tracking_link_destinations SET removed_at = now(), updated_at = now()
				WHERE workspace_id = $1 AND link_id = $2 AND id = ANY ($3::uuid[])`, a.WorkspaceID, link, gone); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracking_link.destinations_updated", "tracking_link", link,
			map[string]any{"campaign_id": c.ID, "destinations": len(in.Destinations), "capped": capped, "removed": len(gone)})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.redirects.clear()
	v, err := s.readLinkDestinations(ctx, a.WorkspaceID, link, url)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
