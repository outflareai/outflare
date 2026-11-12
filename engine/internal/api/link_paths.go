package api

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/trafficsources"
)

// Rule-based paths (plans/ad-tracker.md, T4b). A path has conditions on the
// click and its own weighted destinations. A click takes the first path,
// in order, whose conditions all match and that has an open destination;
// otherwise the link's default destinations, then the link's own page.
// Conditions combine with AND, and the values in one condition with OR, as
// in Voluum.

const (
	maxLinkPaths      = 10
	maxPathConditions = 20
	maxConditionVals  = 100
	// visitorWindow is how long a click's visitor counts as returning.
	visitorWindow = 24 * time.Hour
	// visitorsMax bounds the server's memory of recent visitors.
	visitorsMax = 1_000_000
)

// PathCondition is one condition of a path.
type pathCondition struct {
	Field  string   `json:"field"`
	Op     string   `json:"op"`
	Values []string `json:"values"`
}

// conditionFields are what a condition can test.
var conditionFields = append([]string{"country", "region", "city", "isp", "asn", "device", "device_brand", "os", "os_version",
	"browser", "browser_version", "language", "referrer", "ip", "weekday", "hour", "visitor"}, fieldNames()...)

func fieldNames() []string {
	out := make([]string, trafficsources.Fields)
	for i := range out {
		out[i] = trafficsources.FieldToken(i + 1)
	}
	return out
}

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// checkConditions cleans and checks a path's conditions.
func checkConditions(what string, conds []pathCondition) error {
	if len(conds) == 0 {
		return errInvalid(what + " needs at least one condition; clicks that match none go to the default destinations")
	}
	if len(conds) > maxPathConditions {
		return errInvalid(what + " has at most 20 conditions")
	}
	for i := range conds {
		c := &conds[i]
		at := what + "[" + strconv.Itoa(i) + "]"
		c.Field, c.Op = strings.TrimSpace(c.Field), strings.TrimSpace(c.Op)
		if !slices.Contains(conditionFields, c.Field) {
			return errInvalid(at + ".field is one of " + strings.Join(conditionFields, ", "))
		}
		if c.Op != "is" && c.Op != "is_not" {
			return errInvalid(at + ".op is is or is_not")
		}
		if len(c.Values) == 0 || len(c.Values) > maxConditionVals {
			return errInvalid(at + ".values has 1 to 100 values; the condition matches any of them")
		}
		for j, v := range c.Values {
			v = strings.TrimSpace(v)
			bad := ""
			switch c.Field {
			case "country":
				v = strings.ToUpper(v)
				if !countryRe.MatchString(v) {
					bad = "a two-letter country code such as US"
				}
			case "ip":
				if _, err := parsePrefix(v); err != nil {
					bad = "an IP address or range, such as 203.0.113.0/24"
				}
			case "hour":
				if n, err := strconv.Atoi(v); err != nil || n < 0 || n > 23 {
					bad = "an hour from 0 to 23 (UTC)"
				}
			case "weekday":
				v = strings.ToLower(v)
				if !slices.Contains(weekdays, v) {
					bad = "a day: mon, tue, wed, thu, fri, sat or sun"
				}
			case "visitor":
				v = strings.ToLower(v)
				if v != "unique" && v != "returning" {
					bad = "unique or returning"
				}
			case "asn":
				if _, err := strconv.ParseUint(strings.TrimPrefix(strings.ToUpper(v), "AS"), 10, 32); err != nil {
					bad = "a network number such as 64500"
				}
				v = strings.TrimPrefix(strings.ToUpper(v), "AS")
			default:
				v = strings.ToLower(v)
				if v == "" || len(v) > 200 {
					bad = "1 to 200 characters"
				}
			}
			if bad != "" {
				return errInvalid(at + ".values[" + strconv.Itoa(j) + "] is " + bad)
			}
			c.Values[j] = v
		}
	}
	return nil
}

func parsePrefix(v string) (netip.Prefix, error) {
	if strings.Contains(v, "/") {
		p, err := netip.ParsePrefix(v)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(v)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
}

// compiledCondition is a condition ready to test clicks.
type compiledCondition struct {
	field  string
	negate bool
	values map[string]bool
	ranges []netip.Prefix
}

// linkPath is a path as a redirect needs it.
type linkPath struct {
	id    string
	conds []compiledCondition
	dests []linkDestination
	// visitor says a condition asks for a first or returning visitor.
	visitor bool
}

func compileConditions(conds []pathCondition) (out []compiledCondition, visitor bool) {
	for _, c := range conds {
		cc := compiledCondition{field: c.Field, negate: c.Op == "is_not", values: map[string]bool{}}
		for _, v := range c.Values {
			if c.Field == "ip" {
				if p, err := parsePrefix(v); err == nil {
					cc.ranges = append(cc.ranges, p)
				}
				continue
			}
			cc.values[v] = true
		}
		visitor = visitor || c.Field == "visitor"
		out = append(out, cc)
	}
	return out, visitor
}

// clickFacts are what conditions test about a click.
type clickFacts struct {
	values    map[string]string
	ip        netip.Addr
	returning bool
}

func (f clickFacts) matches(conds []compiledCondition) bool {
	for _, c := range conds {
		var hit bool
		switch c.field {
		case "ip":
			for _, p := range c.ranges {
				hit = hit || (f.ip.IsValid() && p.Contains(f.ip))
			}
		case "visitor":
			hit = c.values["returning"] && f.returning || c.values["unique"] && !f.returning
		default:
			v := f.values[c.field]
			hit = v != "" && c.values[v]
		}
		if hit == c.negate {
			return false
		}
	}
	return true
}

// factsFor gathers a click's facts for conditions. Strings are lowercased
// as conditions' values are; the country is in capitals; the hour and
// weekday are the brand's, in its reporting time zone (B4a).
func factsFor(r *http.Request, c bufferedClick, now time.Time, loc *time.Location) clickFacts {
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	d := c.detail
	v := map[string]string{
		"country": d.place.Country, "region": strings.ToLower(d.place.Region), "city": strings.ToLower(d.place.City),
		"isp": strings.ToLower(d.place.Org), "device": strings.ToLower(d.device), "device_brand": strings.ToLower(d.detail.brand),
		"os": strings.ToLower(d.os), "os_version": strings.ToLower(d.detail.osVersion), "browser": strings.ToLower(d.browser),
		"browser_version": strings.ToLower(d.detail.browserVersion), "language": strings.ToLower(d.language),
		"hour": strconv.Itoa(local.Hour()), "weekday": weekdays[local.Weekday()],
	}
	if d.place.ASN != 0 {
		v["asn"] = strconv.FormatUint(uint64(d.place.ASN), 10)
	}
	if ref := r.Referer(); ref != "" {
		if h := hostOf(ref); h != "" {
			v["referrer"] = h
		}
	}
	for n, f := range c.fields {
		v["field"+n] = strings.ToLower(f)
	}
	f := clickFacts{values: v}
	if ip, err := parseClientAddr(clientIP(r)); err == nil {
		f.ip = ip.Unmap()
	}
	return f
}

// visitorMemory remembers which visitors (by the keyed address hash)
// clicked a link in the last day, so a condition can tell a first visit
// from a returning one. It is this server's memory, so a returning visitor
// whose clicks reach different servers can count as new.
type visitorMemory struct {
	mu sync.Mutex
	m  map[uint64]int64
}

// returning records a visit and reports whether there was one in the last
// day.
func (v *visitorMemory) returning(link, ipHash string, now time.Time) bool {
	if ipHash == "" {
		return false
	}
	h := fnv.New64a()
	h.Write([]byte(link + "|" + ipHash))
	key := h.Sum64()
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.m == nil {
		v.m = map[uint64]int64{}
	}
	last, ok := v.m[key]
	if len(v.m) >= visitorsMax {
		cutoff := now.Add(-visitorWindow).Unix()
		for k, t := range v.m {
			if t < cutoff {
				delete(v.m, k)
			}
		}
		if len(v.m) >= visitorsMax {
			v.m = map[uint64]int64{}
		}
	}
	v.m[key] = now.Unix()
	return ok && now.Unix()-last < int64(visitorWindow/time.Second)
}

// choose picks a click's destination: the first matching path's, the
// default destinations', or none for the link's own page. It returns the
// path taken ("" for none).
func (s *Server) choose(r *http.Request, t redirectTarget, c bufferedClick, now time.Time) (*linkDestination, string) {
	if len(t.paths) == 0 && len(t.dests) == 0 {
		return nil, ""
	}
	if len(t.paths) > 0 {
		f := factsFor(r, c, now, t.loc)
		if slices.ContainsFunc(t.paths, func(p linkPath) bool { return p.visitor }) {
			f.returning = s.visitors.returning(t.link.ID, c.ipHash, now)
		}
		for _, p := range t.paths {
			if f.matches(p.conds) {
				if d := s.route(entry(p.dests), c.day, c.uncounted()); d != nil {
					routeMetric("path")
					return d, p.id
				}
			}
		}
	}
	if d := s.route(entry(t.dests), c.day, c.uncounted()); d != nil {
		routeMetric("destination")
		return d, ""
	}
	routeMetric("fallback")
	return nil, ""
}

// loadPaths reads a link's or flow's active paths with their destinations.
func (s *Server) loadPaths(ctx context.Context, o routeOwner, day string) ([]linkPath, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, conditions FROM tracking_link_paths
		WHERE `+o.col+` = $1 AND removed_at IS NULL AND active ORDER BY position, created_at`, o.id)
	if err != nil {
		return nil, err
	}
	var out []linkPath
	for rows.Next() {
		var p linkPath
		var raw []byte
		if err := rows.Scan(&p.id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var conds []pathCondition
		_ = json.Unmarshal(raw, &conds)
		p.conds, p.visitor = compileConditions(conds)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].dests, err = s.loadDestinationsOf(ctx, o, &out[i].id, day); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// API: a link's rule-based paths, read and replaced as one list.

func (s *Server) routesLinkPaths(mux *http.ServeMux) {
	path := "/v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/paths"
	mux.HandleFunc("GET "+path, s.onBrand(auth.ScopeAnalyticsRead, s.listLinkPaths))
	mux.HandleFunc("PUT "+path, s.onBrand(auth.ScopeAnalyticsManage, s.putLinkPaths))
}

type linkPathView struct {
	ID           string                `json:"id"`
	Name         string                `json:"name"`
	Active       bool                  `json:"active"`
	Conditions   []pathCondition       `json:"conditions"`
	Destinations []linkDestinationView `json:"destinations"`
}

type linkPathsView struct {
	LinkID string         `json:"link_id"`
	Paths  []linkPathView `json:"paths"`
	// NeedsDomain says the brand has no verified tracking domain, so the
	// paths aren't used.
	NeedsDomain bool `json:"needs_domain"`
}

func (s *Server) readLinkPaths(ctx context.Context, ws, link string) (linkPathsView, error) {
	list, err := s.pathViews(ctx, ws, linkOwner(link))
	return linkPathsView{LinkID: link, Paths: list}, err
}

// pathViews are a link's or flow's paths, in order, with their
// destinations.
func (s *Server) pathViews(ctx context.Context, ws string, o routeOwner) ([]linkPathView, error) {
	out := []linkPathView{}
	rows, err := s.pool.Query(ctx, `SELECT id::text, name, active, conditions FROM tracking_link_paths
		WHERE workspace_id = $1 AND `+o.col+` = $2 AND removed_at IS NULL ORDER BY position, created_at`, ws, o.id)
	if err != nil {
		return out, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (linkPathView, error) {
		var v linkPathView
		var raw []byte
		err := row.Scan(&v.ID, &v.Name, &v.Active, &raw)
		if err == nil {
			err = json.Unmarshal(raw, &v.Conditions)
		}
		return v, err
	})
	if err != nil {
		return out, err
	}
	for i := range list {
		if list[i].Destinations, err = s.destinationViews(ctx, ws, o, &list[i].ID); err != nil {
			return out, err
		}
		if list[i].Conditions == nil {
			list[i].Conditions = []pathCondition{}
		}
	}
	if list != nil {
		out = list
	}
	return out, nil
}

func (s *Server) listLinkPaths(w http.ResponseWriter, r *http.Request) {
	c, link, _, _, ok := s.findRedirectLink(w, r)
	if !ok {
		return
	}
	v, err := s.readLinkPaths(r.Context(), accessFrom(r.Context()).WorkspaceID, link)
	if err == nil {
		v.NeedsDomain, err = s.needsDomain(r.Context(), c)
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type linkPathInput struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Active       *bool                  `json:"active"`
	Conditions   []pathCondition        `json:"conditions"`
	Destinations []linkDestinationInput `json:"destinations"`
}

// putLinkPaths replaces a link's paths, in order. A path left out is taken
// off the link with its destinations (their clicks keep them).
func (s *Server) putLinkPaths(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	c, link, _, code, ok := s.findRedirectLink(w, r)
	if !ok {
		return
	}
	var in struct {
		Paths []linkPathInput `json:"paths"`
	}
	if !decode(w, r, &in) {
		return
	}
	if code == nil && len(in.Paths) > 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "paths route a redirect link's clicks, and this link has none; make it again with redirect: true first")
		return
	}
	conditions, err := checkPaths(in.Paths)
	if s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		gone, err := savePaths(ctx, tx, a.WorkspaceID, linkOwner(link), in.Paths)
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracking_link.paths_updated", "tracking_link", link,
			map[string]any{"campaign_id": c.ID, "paths": len(in.Paths), "conditions": conditions, "removed": gone})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.redirects.clear()
	v, err := s.readLinkPaths(ctx, a.WorkspaceID, link)
	if err == nil {
		v.NeedsDomain, err = s.needsDomain(ctx, c)
	}
	if errors.Is(err, pgx.ErrNoRows) || err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// checkPaths cleans and checks a list of paths; it returns how many
// conditions they have.
func checkPaths(paths []linkPathInput) (int, error) {
	if len(paths) > maxLinkPaths {
		return 0, errInvalid("a link or flow has at most 10 paths")
	}
	seen := map[string]bool{}
	conditions := 0
	for i := range paths {
		p := &paths[i]
		at := "paths[" + strconv.Itoa(i) + "]"
		p.ID, p.Name = strings.TrimSpace(p.ID), strings.TrimSpace(p.Name)
		switch {
		case p.ID != "" && (!validUUID(p.ID) || seen[p.ID]):
			return 0, errInvalid(at + ".id is one of these paths, once")
		case len(p.Name) > 120:
			return 0, errInvalid(at + ".name is at most 120 characters")
		case len(p.Destinations) == 0:
			return 0, errInvalid(at + ".destinations needs at least one page for the clicks that match")
		}
		seen[p.ID] = p.ID != ""
		if err := checkConditions(at+".conditions", p.Conditions); err != nil {
			return 0, err
		}
		if _, err := checkDestinations(at+".destinations", p.Destinations); err != nil {
			return 0, err
		}
		conditions += len(p.Conditions)
	}
	return conditions, nil
}

// savePaths replaces a link's or flow's paths, in order, inside tx. A path
// left out is removed with its destinations (their clicks keep them); it
// returns how many were.
func savePaths(ctx context.Context, tx pgx.Tx, ws string, o routeOwner, paths []linkPathInput) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM tracking_link_paths WHERE workspace_id = $1 AND `+o.col+` = $2 AND removed_at IS NULL FOR UPDATE`, ws, o.id)
	if err != nil {
		return 0, err
	}
	have, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	keep := map[string]bool{}
	for i, p := range paths {
		conds, err := json.Marshal(p.Conditions)
		if err != nil {
			return 0, err
		}
		active := p.Active == nil || *p.Active
		id := p.ID
		if id == "" {
			if err := tx.QueryRow(ctx, `INSERT INTO tracking_link_paths (workspace_id, brand_id, `+o.col+`, name, conditions, position, active)
				SELECT o.w, o.b, $2, $3, $4, $5, $6 FROM `+o.source()+` RETURNING id::text`, ws, o.id, p.Name, conds, i, active).Scan(&id); err != nil {
				return 0, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE tracking_link_paths SET name = $4, conditions = $5, position = $6, active = $7, updated_at = now()
				WHERE workspace_id = $1 AND `+o.col+` = $2 AND id = $3 AND removed_at IS NULL`, ws, o.id, id, p.Name, conds, i, active)
			if err != nil {
				return 0, err
			}
			if tag.RowsAffected() == 0 {
				return 0, errInvalid("paths[" + strconv.Itoa(i) + "].id isn't one of these paths; leave it out to add a new one")
			}
			keep[id] = true
		}
		if _, err := saveDestinations(ctx, tx, ws, o, &id, "paths["+strconv.Itoa(i)+"].destinations", p.Destinations); err != nil {
			return 0, err
		}
	}
	var gone []string
	for _, id := range have {
		if !keep[id] {
			gone = append(gone, id)
		}
	}
	if len(gone) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE tracking_link_paths SET removed_at = now(), updated_at = now() WHERE workspace_id = $1 AND id = ANY ($2::uuid[])`,
			ws, gone); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE tracking_link_destinations SET removed_at = now(), updated_at = now()
			WHERE workspace_id = $1 AND path_id = ANY ($2::uuid[]) AND removed_at IS NULL`, ws, gone); err != nil {
			return 0, err
		}
	}
	return len(gone), nil
}
