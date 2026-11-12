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
	"github.com/outflareai/outflare/engine/internal/track"
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
	// offer is the offer it sends to (T6a), nil for a page of its own.
	offer *routeOffer
	// offerID is the offer a routed click went to: the offer, or its
	// fallback while the offer is capped.
	offerID string
	// landerID is the lander it is (T6b), "" for none: a list with landers
	// sends a click to one of them first, and the lander's calls to action
	// to its other destinations.
	landerID string
}

// routeOwner is what a list of paths and destinations belongs to: a link,
// or a flow (T6c) that links share. col is its column in the paths and
// destinations tables, never text from a request.
type routeOwner struct{ col, id string }

func linkOwner(id string) routeOwner { return routeOwner{"link_id", id} }
func flowOwner(id string) routeOwner { return routeOwner{"flow_id", id} }

// source is a query for the owner's workspace and brand, as o(w, b), with
// the workspace as $1 and the owner's id as $2.
func (o routeOwner) source() string {
	if o.col == "flow_id" {
		return `(SELECT f.workspace_id, f.brand_id FROM tracker_flows f WHERE f.workspace_id = $1 AND f.id = $2) o(w, b)`
	}
	return `(SELECT l.workspace_id, COALESCE(l.brand_id, c.brand_id, outflare_first_brand(l.workspace_id))
		FROM tracking_links l JOIN tracking_campaigns c ON c.id = l.campaign_id WHERE l.workspace_id = $1 AND l.id = $2) o(w, b)`
}

// entry is where a list's clicks go first: its landers when it has any,
// else every destination.
func entry(dests []linkDestination) []linkDestination {
	var landers []linkDestination
	for _, d := range dests {
		if d.landerID != "" {
			landers = append(landers, d)
		}
	}
	if landers != nil {
		return landers
	}
	return dests
}

// onward are where a list's lander clicks go: its destinations that aren't
// landers.
func onward(dests []linkDestination) []linkDestination {
	var out []linkDestination
	for _, d := range dests {
		if d.landerID == "" {
			out = append(out, d)
		}
	}
	return out
}

// routeOffer is an offer as a redirect needs it.
type routeOffer struct {
	id, url string
	cap     *int
	// conversions says the cap counts conversions, not clicks.
	conversions bool
	fallback    *routeOffer
}

// capKey is the key its cap is counted under: clicks under its id,
// conversions apart from them.
func (o *routeOffer) capKey() string {
	if o.conversions {
		return conversionCapKey(o.id)
	}
	return o.id
}

func conversionCapKey(offer string) string { return "conv:" + offer }

// capDay is the day a cap counts clicks on: the day in the brand's
// reporting time zone (B4a), so caps reset at the brand's midnight; UTC
// for a nil zone.
func capDay(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format(time.DateOnly)
}

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

// open reports whether a destination still takes clicks today: a weight
// above 0, room in its cap, and for an offer, room in the offer's cap or
// its fallback's.
func (c *capCounts) open(d linkDestination, day string) bool {
	return d.weight > 0 && c.has(d.id, d.cap, day) && (d.offer == nil || c.offerFor(d.offer, day) != nil)
}

// has reports whether a cap has room today.
func (c *capCounts) has(id string, cap *int, day string) bool {
	if cap == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.get(id, day)
	return e.total+e.pending < int64(*cap)
}

// take counts a click for a capped destination, if it still has room.
func (c *capCounts) take(d linkDestination, day string) bool {
	return c.claim(d.id, d.cap, day)
}

// claim counts a click against a cap, if it still has room.
func (c *capCounts) claim(id string, cap *int, day string) bool {
	if cap == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.get(id, day)
	if e.total+e.pending >= int64(*cap) {
		return false
	}
	e.pending++
	return true
}

// offerFor is the offer a click goes to: o while it has room today, else
// its fallback while that has, else nil.
func (c *capCounts) offerFor(o *routeOffer, day string) *routeOffer {
	for _, x := range []*routeOffer{o, o.fallback} {
		if x != nil && c.has(x.capKey(), x.cap, day) {
			return x
		}
	}
	return nil
}

// conversionCaps are the offers with a conversion cap this server has
// counts for, with the day each counts (its brand's today), to refresh
// from every server's.
func (c *capCounts) conversionCaps() (ids, days []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if id, ok := strings.CutPrefix(k, "conv:"); ok {
			ids, days = append(ids, id), append(days, e.day)
		}
	}
	return ids, days
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
func (s *Server) route(dests []linkDestination, day string, bot bool) *linkDestination {
	if len(dests) == 0 {
		return nil
	}
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
		if d := s.take(pick, day, bot); d != nil {
			return d
		}
	}
	return nil
}

// take counts a click against a destination's cap and its offer's, and
// says where it goes: nil when either has no room left.
func (s *Server) take(pick linkDestination, day string, bot bool) *linkDestination {
	if !bot && !s.caps.take(pick, day) {
		return nil
	}
	if pick.offer == nil {
		return &pick
	}
	// An offer sends the click on while it has room, else to its fallback
	// offer.
	o := s.caps.offerFor(pick.offer, day)
	if o != nil && (bot || o.conversions || s.caps.claim(o.id, o.cap, day)) {
		pick.url, pick.offerID = o.url, o.id
		return &pick
	}
	if !bot {
		s.caps.settle(pick.id, day, 1, 0)
	}
	return nil
}

// routeMetric counts where a click on a link with destinations went: path,
// destination (a default one), fallback or shared_domain.
func routeMetric(outcome string) { metrics.RedirectRoute(outcome) }

// loadDestinations reads a link's default destinations for the redirect
// cache and seeds the counts of the capped ones on day (capDay).
func (s *Server) loadDestinations(ctx context.Context, o routeOwner, day string) ([]linkDestination, error) {
	return s.loadDestinationsOf(ctx, o, nil, day)
}

// loadDestinationsOf reads the destinations of one of a link's paths (nil
// for the default ones), with the offers they send to, and seeds today's
// counts of the capped ones on day.
func (s *Server) loadDestinationsOf(ctx context.Context, o routeOwner, path *string, day string) ([]linkDestination, error) {
	rows, err := s.pool.Query(ctx, `SELECT d.id::text, d.name, d.url, d.weight, d.daily_click_cap, COALESCE(c.clicks, 0), COALESCE(d.lander_id::text, ''),
			o.id::text, COALESCE(o.url, ''), o.daily_cap, o.cap_kind = 'conversions', COALESCE(od.clicks, 0), COALESCE(od.conversions, 0), COALESCE(op.click_id_param, ''),
			f.id::text, COALESCE(f.url, ''), f.daily_cap, f.cap_kind = 'conversions', COALESCE(fd.clicks, 0), COALESCE(fd.conversions, 0), COALESCE(fp.click_id_param, '')
		FROM tracking_link_destinations d
		LEFT JOIN tracking_destination_days c ON c.destination_id = d.id AND c.day = $2::date
		LEFT JOIN tracker_offers o ON o.id = d.offer_id AND o.workspace_id = d.workspace_id
		LEFT JOIN tracker_offer_days od ON od.offer_id = o.id AND od.day = $2::date
		LEFT JOIN tracker_postback_sources op ON op.id = o.source_id
		LEFT JOIN tracker_offers f ON f.id = o.fallback_offer_id AND f.workspace_id = o.workspace_id AND f.status = 'active'
		LEFT JOIN tracker_offer_days fd ON fd.offer_id = f.id AND fd.day = $2::date
		LEFT JOIN tracker_postback_sources fp ON fp.id = f.source_id
		WHERE d.`+o.col+` = $1 AND d.removed_at IS NULL AND d.path_id IS NOT DISTINCT FROM $3::uuid ORDER BY d.position, d.created_at`, o.id, day, path)
	if err != nil {
		return nil, err
	}
	var out []linkDestination
	for rows.Next() {
		var d linkDestination
		var clicks int64
		var or, f offerRow
		if err := rows.Scan(&d.id, &d.name, &d.url, &d.weight, &d.cap, &clicks, &d.landerID,
			&or.id, &or.url, &or.cap, &or.conversions, &or.clicks, &or.converted, &or.param,
			&f.id, &f.url, &f.cap, &f.conversions, &f.clicks, &f.converted, &f.param); err != nil {
			rows.Close()
			return nil, err
		}
		if d.cap != nil {
			s.caps.seed(d.id, day, clicks)
		}
		if d.offer = s.routeOffer(or, day); d.offer != nil {
			d.offer.fallback = s.routeOffer(f, day)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// offerRow is an offer as loadDestinationsOf reads it.
type offerRow struct {
	id                *string
	url, param        string
	cap               *int
	conversions       *bool
	clicks, converted int64
}

// routeOffer makes an offer read with a destination ready to route to, and
// seeds today's count of its cap.
func (s *Server) routeOffer(r offerRow, day string) *routeOffer {
	if r.id == nil {
		return nil
	}
	o := &routeOffer{id: *r.id, url: offerURL(r.url, r.param), cap: r.cap, conversions: r.conversions != nil && *r.conversions}
	if o.cap != nil {
		if o.conversions {
			s.caps.seed(o.capKey(), day, r.converted)
		} else {
			s.caps.seed(o.id, day, r.clicks)
		}
	}
	return o
}

// offerURL is an offer's URL with the click id added under its network's
// parameter, unless the URL already carries it.
func offerURL(raw, param string) string {
	if param == "" || strings.Contains(raw, "{click_id") {
		return raw
	}
	base, frag, _ := strings.Cut(raw, "#")
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	out := base + sep + param + "={click_id}"
	if frag != "" {
		out += "#" + frag
	}
	return out
}

// countDestinationClicks adds a written batch's clicks to the day's counts,
// in the batch's transaction, and returns what to settle once it commits.
func countDestinationClicks(ctx context.Context, tx pgx.Tx, batch []bufferedClick) ([]destinationSettle, error) {
	type key struct{ id, day string }
	counts := map[key]int64{}
	for _, c := range batch {
		if c.destinationID != "" && !c.uncounted() {
			counts[key{c.destinationID, c.day}]++
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

// countOfferClicks adds a written batch's clicks to each offer's day, in
// the batch's transaction, and returns what to settle once it commits.
func countOfferClicks(ctx context.Context, tx pgx.Tx, batch []bufferedClick) ([]destinationSettle, error) {
	type key struct{ id, day string }
	counts := map[key]int64{}
	for _, c := range batch {
		if c.offerID != "" && !c.uncounted() {
			counts[key{c.offerID, c.day}]++
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
	rows, err := tx.Query(ctx, `INSERT INTO tracker_offer_days (offer_id, workspace_id, brand_id, day, clicks)
		SELECT o.id, o.workspace_id, o.brand_id, t.day, t.n
		FROM unnest($1::text[], $2::date[], $3::bigint[]) AS t(id, day, n)
		JOIN tracker_offers o ON o.id = t.id::uuid
		ON CONFLICT (offer_id, day) DO UPDATE SET clicks = tracker_offer_days.clicks + EXCLUDED.clicks
		RETURNING offer_id::text, day::text, clicks`, ids, days, ns)
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

// refreshOfferCaps reads today's conversions of the conversion-capped
// offers this server routes to, so a cap counts every server's
// conversions within a minute.
func (s *Server) refreshOfferCaps(ctx context.Context) {
	ids, days := s.caps.conversionCaps()
	if len(ids) == 0 {
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT d.offer_id::text, d.day::text, d.conversions
		FROM unnest($1::uuid[], $2::date[]) AS k(offer_id, day) JOIN tracker_offer_days d ON d.offer_id = k.offer_id AND d.day = k.day`, ids, days)
	if err != nil {
		if ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "tracker_offer.caps_refresh_failed", Surface: "system"}, "internal", err)
		}
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, day string
		var n int64
		if rows.Scan(&id, &day, &n) == nil {
			s.caps.seed(conversionCapKey(id), day, n)
		}
	}
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
	// OfferID is the offer it sends to (its URL is the offer's), nil for a
	// page of its own.
	OfferID   *string `json:"offer_id"`
	OfferName *string `json:"offer_name"`
	// LanderID is the lander it is, nil for none.
	LanderID   *string `json:"lander_id"`
	LanderName *string `json:"lander_name"`
}

type linkDestinationsView struct {
	LinkID string `json:"link_id"`
	// FallbackURL is the link's own page, where clicks go when every
	// destination is capped or stopped.
	FallbackURL  string                `json:"fallback_url"`
	Destinations []linkDestinationView `json:"destinations"`
	// NeedsDomain says the brand has no verified tracking domain, so the
	// destinations aren't used: tracker links run only on the workspace's
	// own domains.
	NeedsDomain bool `json:"needs_domain"`
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
	list, err := s.destinationViews(ctx, ws, linkOwner(link), nil)
	return linkDestinationsView{LinkID: link, FallbackURL: fallback, Destinations: list}, err
}

// destinationViews are the destinations of one of a link's paths (nil for
// the default ones), with today's clicks.
func (s *Server) destinationViews(ctx context.Context, ws string, o routeOwner, path *string) ([]linkDestinationView, error) {
	loc, err := s.ownerZone(ctx, ws, o)
	if err != nil {
		return nil, err
	}
	day := capDay(time.Now(), loc)
	rows, err := s.pool.Query(ctx, `SELECT d.id::text, d.name, d.url, d.weight, d.daily_click_cap, COALESCE(c.clicks, 0), o.id::text, o.name, ld.id::text, ld.name
		FROM tracking_link_destinations d
		LEFT JOIN tracking_destination_days c ON c.destination_id = d.id AND c.day = $3::date
		LEFT JOIN tracker_offers o ON o.id = d.offer_id
		LEFT JOIN tracker_landers ld ON ld.id = d.lander_id
		WHERE d.workspace_id = $1 AND d.`+o.col+` = $2 AND d.removed_at IS NULL AND d.path_id IS NOT DISTINCT FROM $4::uuid
		ORDER BY d.position, d.created_at`, ws, o.id, day, path)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (linkDestinationView, error) {
		var v linkDestinationView
		err := row.Scan(&v.ID, &v.Name, &v.URL, &v.Weight, &v.DailyClickCap, &v.ClicksToday, &v.OfferID, &v.OfferName, &v.LanderID, &v.LanderName)
		v.Open = v.Weight > 0 && (v.DailyClickCap == nil || v.ClicksToday < int64(*v.DailyClickCap))
		return v, err
	})
	if list == nil {
		list = []linkDestinationView{}
	}
	return list, err
}

func (s *Server) listLinkDestinations(w http.ResponseWriter, r *http.Request) {
	c, link, url, _, ok := s.findRedirectLink(w, r)
	if !ok {
		return
	}
	v, err := s.readLinkDestinations(r.Context(), accessFrom(r.Context()).WorkspaceID, link, url)
	if err == nil {
		v.NeedsDomain, err = s.needsDomain(r.Context(), c)
	}
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
	// OfferID sends it to one of the brand's offers, instead of url, and
	// LanderID to one of its landers.
	OfferID  string `json:"offer_id"`
	LanderID string `json:"lander_id"`
}

// checkDestinations cleans and checks a list of destinations; what names
// the list in messages. It returns how many have a cap.
func checkDestinations(what string, list []linkDestinationInput) (capped int, err error) {
	if len(list) > maxLinkDestinations {
		return 0, errInvalid(what + " has at most 20 destinations")
	}
	seen := map[string]bool{}
	for i := range list {
		d := &list[i]
		d.ID, d.Name = strings.TrimSpace(d.ID), strings.TrimSpace(d.Name)
		if d.Weight == nil {
			hundred := 100
			d.Weight = &hundred
		}
		at := what + "[" + strconv.Itoa(i) + "]"
		switch {
		case d.ID != "" && (!validUUID(d.ID) || seen[d.ID]):
			return 0, errInvalid(at + ".id is one of the link's destinations, once")
		case len(d.Name) > 120:
			return 0, errInvalid(at + ".name is at most 120 characters")
		case *d.Weight < 0 || *d.Weight > 10000:
			return 0, errInvalid(at + ".weight is 0 to 10,000; 0 sends it no clicks")
		case d.DailyClickCap != nil && (*d.DailyClickCap < 0 || *d.DailyClickCap > 100_000_000):
			return 0, errInvalid(at + ".daily_click_cap is 0 (stopped) or more; leave it out for no cap")
		}
		seen[d.ID] = d.ID != ""
		d.OfferID, d.LanderID = strings.TrimSpace(d.OfferID), strings.TrimSpace(d.LanderID)
		if d.OfferID != "" || d.LanderID != "" {
			switch {
			case d.OfferID != "" && d.LanderID != "":
				return 0, errInvalid(at + " is an offer or a lander, not both")
			case d.OfferID != "" && !validUUID(d.OfferID):
				return 0, errInvalid(at + ".offer_id is one of the brand's offers")
			case d.LanderID != "" && !validUUID(d.LanderID):
				return 0, errInvalid(at + ".lander_id is one of the brand's landers")
			}
			// Its URL is the offer's, read when it is saved.
			d.URL = ""
			if d.DailyClickCap != nil {
				capped++
			}
			continue
		}
		if err := checkDestinationTokens(at+".url", d.URL); err != nil {
			return 0, err
		}
		dest, err := utm.Destination(d.URL)
		if err != nil {
			return 0, errInvalid(at + ".url: " + err.Error())
		}
		d.URL = dest
		if d.DailyClickCap != nil {
			capped++
		}
	}
	return capped, nil
}

// saveDestinations replaces the destinations of one of a link's or flow's
// paths (nil for its default ones) with list, inside tx. Destinations left
// out are marked removed; it returns how many.
func saveDestinations(ctx context.Context, tx pgx.Tx, ws string, o routeOwner, path *string, what string, list []linkDestinationInput) (int, error) {
	// Lock the path's destinations so two saves don't interleave.
	rows, err := tx.Query(ctx, `SELECT id::text FROM tracking_link_destinations
		WHERE workspace_id = $1 AND `+o.col+` = $2 AND removed_at IS NULL AND path_id IS NOT DISTINCT FROM $3::uuid FOR UPDATE`, ws, o.id, path)
	if err != nil {
		return 0, err
	}
	have, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	keep := map[string]bool{}
	for i, d := range list {
		var offer, lander *string
		if d.LanderID != "" {
			// A lander of the owner's brand, active; its URL goes with it.
			err := tx.QueryRow(ctx, `SELECT x.id::text, x.url FROM tracker_landers x, `+o.source()+`
				WHERE x.workspace_id = o.w AND x.brand_id = o.b AND x.id = $3 AND x.status = 'active'`, ws, o.id, d.LanderID).Scan(&lander, &d.URL)
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, errInvalid(what + "[" + strconv.Itoa(i) + "].lander_id isn't one of the brand's active landers")
			}
			if err != nil {
				return 0, err
			}
		}
		if d.OfferID != "" {
			// An offer of the owner's brand, active; its URL goes with it.
			err := tx.QueryRow(ctx, `SELECT x.id::text, x.url FROM tracker_offers x, `+o.source()+`
				WHERE x.workspace_id = o.w AND x.brand_id = o.b AND x.id = $3 AND x.status = 'active'`, ws, o.id, d.OfferID).Scan(&offer, &d.URL)
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, errInvalid(what + "[" + strconv.Itoa(i) + "].offer_id isn't one of the brand's active offers")
			}
			if err != nil {
				return 0, err
			}
		}
		if d.ID == "" {
			if _, err := tx.Exec(ctx, `INSERT INTO tracking_link_destinations (workspace_id, brand_id, `+o.col+`, path_id, name, url, weight, daily_click_cap, position,
					offer_id, lander_id)
				SELECT o.w, o.b, $2, $3, $4, $5, $6, $7, $8, $9, $10 FROM `+o.source(),
				ws, o.id, path, d.Name, d.URL, *d.Weight, d.DailyClickCap, i, offer, lander); err != nil {
				return 0, err
			}
			continue
		}
		tag, err := tx.Exec(ctx, `UPDATE tracking_link_destinations SET name = $5, url = $6, weight = $7, daily_click_cap = $8, position = $9, offer_id = $10,
				lander_id = $11, updated_at = now()
			WHERE workspace_id = $1 AND `+o.col+` = $2 AND id = $3 AND removed_at IS NULL AND path_id IS NOT DISTINCT FROM $4::uuid`,
			ws, o.id, d.ID, path, d.Name, d.URL, *d.Weight, d.DailyClickCap, i, offer, lander)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() == 0 {
			return 0, errInvalid(what + "[" + strconv.Itoa(i) + "].id isn't one of these destinations; leave it out to add a new one")
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
			WHERE workspace_id = $1 AND `+o.col+` = $2 AND id = ANY ($3::uuid[])`, ws, o.id, gone); err != nil {
			return 0, err
		}
	}
	return len(gone), nil
}

// putLinkDestinations replaces a link's default destinations with the list
// given. Destinations left out are taken off the link (their clicks keep
// them).
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
	capped, err := checkDestinations("destinations", in.Destinations)
	if s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		removed, err := saveDestinations(ctx, tx, a.WorkspaceID, linkOwner(link), nil, "destinations", in.Destinations)
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracking_link.destinations_updated", "tracking_link", link,
			map[string]any{"campaign_id": c.ID, "destinations": len(in.Destinations), "capped": capped, "removed": removed})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.redirects.clear()
	v, err := s.readLinkDestinations(ctx, a.WorkspaceID, link, url)
	if err == nil {
		v.NeedsDomain, err = s.needsDomain(ctx, c)
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// needsDomain says a campaign's brand has no verified tracking domain for
// its tracker features to run on.
func (s *Server) needsDomain(ctx context.Context, c *trackingCampaignView) (bool, error) {
	has, err := s.brandHasOwnDomain(ctx, accessFrom(ctx).WorkspaceID, c.BrandID)
	return !has, err
}
