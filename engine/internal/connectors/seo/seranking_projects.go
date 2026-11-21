package seo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// SE Ranking's Project API: the keywords an account tracks in its own
// projects, checked by SE Ranking on the project's schedule. Reading them
// takes no Data API credits, so Outflare keeps SE Ranking's positions
// instead of paying for its own checks of the same keywords.

// seRankingCountries are SE Ranking's country names in search engine names
// ("Google USA", "Google Mobile Germany"), by two-letter code. An engine
// whose country is not here is skipped and reported, never guessed.
var seRankingCountries = map[string]string{
	"usa": "US", "united states": "US", "uk": "GB", "united kingdom": "GB", "great britain": "GB",
	"canada": "CA", "australia": "AU", "new zealand": "NZ", "ireland": "IE", "india": "IN",
	"singapore": "SG", "south africa": "ZA", "germany": "DE", "austria": "AT", "switzerland": "CH",
	"france": "FR", "belgium": "BE", "netherlands": "NL", "spain": "ES", "portugal": "PT",
	"italy": "IT", "poland": "PL", "czech republic": "CZ", "czechia": "CZ", "slovakia": "SK",
	"hungary": "HU", "romania": "RO", "bulgaria": "BG", "greece": "GR", "turkey": "TR",
	"ukraine": "UA", "sweden": "SE", "norway": "NO", "denmark": "DK", "finland": "FI",
	"brazil": "BR", "mexico": "MX", "argentina": "AR", "chile": "CL", "colombia": "CO",
	"peru": "PE", "japan": "JP", "south korea": "KR", "korea": "KR", "china": "CN",
	"hong kong": "HK", "taiwan": "TW", "indonesia": "ID", "malaysia": "MY", "philippines": "PH",
	"thailand": "TH", "vietnam": "VN", "united arab emirates": "AE", "uae": "AE",
	"saudi arabia": "SA", "israel": "IL", "egypt": "EG", "nigeria": "NG", "kenya": "KE",
	"pakistan": "PK", "bangladesh": "BD", "kazakhstan": "KZ", "lithuania": "LT", "latvia": "LV",
	"estonia": "EE", "croatia": "HR", "serbia": "RS", "slovenia": "SI",
}

// seRankingEngine reads an SE Ranking search engine name: the engine
// (google, bing, ...), the device and the country code, or ok false when
// the country is unknown.
func seRankingEngine(name string) (engine, device, country string, ok bool) {
	words := strings.Fields(strings.ToLower(name))
	if len(words) == 0 {
		return "", "", "", false
	}
	engine, device = words[0], "desktop"
	rest := words[1:]
	if len(rest) > 0 && rest[0] == "mobile" {
		device, rest = "mobile", rest[1:]
	}
	country, ok = seRankingCountries[strings.Join(rest, " ")]
	return engine, device, country, ok
}

func seRankingHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func (s SERanking) TrackedRanks(ctx context.Context, cl *c.Client, creds c.Creds, q c.TrackedRankQuery) (c.TrackedRanks, error) {
	out := c.TrackedRanks{}
	var sites []struct {
		ID     flexInt `json:"id"`
		Title  string  `json:"title"`
		Name   string  `json:"name"`
		Active flexInt `json:"is_active"`
	}
	if err := s.project(ctx, cl, creds, "/project-management/sites", url.Values{}, &sites, "reading the projects"); err != nil {
		return out, err
	}
	site := -1
	for _, x := range sites {
		h := seRankingHost(x.Name)
		if h != "" && len(out.Seen) < 50 {
			out.Seen = append(out.Seen, h)
		}
		for _, want := range q.Hosts {
			if h != "" && h == strings.ToLower(want) {
				site, out.Project = int(x.ID), x.Title
				break
			}
		}
		if site >= 0 {
			break
		}
	}
	if site < 0 {
		return out, nil
	}
	id := url.Values{"site_id": {strconv.Itoa(site)}}

	var all []struct {
		ID   any    `json:"id"`
		Name string `json:"name"`
	}
	if err := s.project(ctx, cl, creds, "/project-management/system/search-engines", url.Values{}, &all, "reading the search engines"); err != nil {
		return out, err
	}
	names := map[string]string{}
	for _, e := range all {
		names[idOf(e.ID)] = e.Name
	}
	var engines []struct {
		SiteEngine   flexInt `json:"site_engine_id"`
		SearchEngine any     `json:"search_engine_id"`
	}
	if err := s.project(ctx, cl, creds, "/project-management/sites/search-engines", id, &engines, "reading the project's search engines"); err != nil {
		return out, err
	}
	type where struct{ engine, device, country string }
	at := map[int]where{}
	taken := map[where]bool{}
	for _, e := range engines {
		name := names[idOf(e.SearchEngine)]
		en, dev, country, ok := seRankingEngine(name)
		w := where{en, dev, country}
		switch {
		case !ok:
			out.Skipped = append(out.Skipped, "search engine "+strconv.Quote(name)+": its country is not known")
		case taken[w]:
			// Two of the project's engines in one country and device (two
			// cities, say) would write the same keyword's day twice.
			out.Skipped = append(out.Skipped, "search engine "+strconv.Quote(name)+": another of the project's engines already covers "+country+" "+dev)
		default:
			at[int(e.SiteEngine)], taken[w] = w, true
		}
	}

	var kws []struct {
		ID   any    `json:"id"`
		Name string `json:"name"`
	}
	if err := s.project(ctx, cl, creds, "/project-management/keywords", id, &kws, "reading the project's keywords"); err != nil {
		return out, err
	}
	words := map[string]string{}
	for _, k := range kws {
		words[idOf(k.ID)] = k.Name
	}

	v := url.Values{"site_id": id["site_id"], "date_from": {q.From}, "date_to": {q.To}, "with_landing_pages": {"1"}}
	var stats []struct {
		SiteEngine flexInt `json:"site_engine_id"`
		Keywords   []struct {
			ID        any `json:"id"`
			Positions []struct {
				Date string   `json:"date"`
				Pos  *flexInt `json:"pos"`
				Map  flexInt  `json:"is_map"`
			} `json:"positions"`
			Landing []struct {
				URL  string `json:"url"`
				Date string `json:"date"`
			} `json:"landing_pages"`
		} `json:"keywords"`
	}
	if err := s.project(ctx, cl, creds, "/project-management/sites/positions", v, &stats, "reading the positions"); err != nil {
		return out, err
	}
	for _, st := range stats {
		w, ok := at[int(st.SiteEngine)]
		if !ok {
			continue
		}
		for _, k := range st.Keywords {
			word := words[idOf(k.ID)]
			if word == "" {
				continue
			}
			// A landing page is listed from the day it started ranking, so
			// each day takes the latest one listed on or before it.
			sort.Slice(k.Landing, func(i, j int) bool { return k.Landing[i].Date < k.Landing[j].Date })
			page := func(day string) string {
				u := ""
				for _, l := range k.Landing {
					if l.Date > day {
						break
					}
					u = l.URL
				}
				return u
			}
			r := c.TrackedRank{Keyword: word, Country: w.country, Device: w.device, Engine: w.engine}
			for _, p := range k.Positions {
				if p.Date == "" {
					continue
				}
				d := c.TrackedDay{Day: p.Date}
				// 0 is "not found", and a maps-block place is not an
				// organic position.
				if p.Pos != nil && *p.Pos > 0 && p.Map == 0 {
					pos := int(*p.Pos)
					d.Position, d.URL = &pos, page(p.Date)
				}
				r.Days = append(r.Days, d)
			}
			if len(r.Days) > 0 {
				out.Ranks = append(out.Ranks, r)
			}
		}
	}
	sort.Slice(out.Ranks, func(i, j int) bool {
		a, b := out.Ranks[i], out.Ranks[j]
		if a.Keyword != b.Keyword {
			return a.Keyword < b.Keyword
		}
		return a.Country+a.Device+a.Engine < b.Country+b.Device+b.Engine
	})
	return out, nil
}

// idOf is an id SE Ranking sends as a number or a string.
func idOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

// project reads one Project API answer into out. A failure says which
// step it was, and an answer of an unexpected shape says what did not
// fit and how it began, so a failed import can be told apart on the
// Rankings page instead of only reading "unavailable".
func (s SERanking) project(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any, step string) error {
	var raw json.RawMessage
	if err := s.get(ctx, cl, creds, path, q, &raw); err != nil {
		var e *c.Error
		if errors.As(err, &e) {
			return c.Errf(e.Kind, "SE Ranking projects, %s: %s", step, e.Message)
		}
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		start := strings.Join(strings.Fields(string(raw)), " ")
		if len(start) > 120 {
			start = start[:120] + "…"
		}
		return c.Errf(c.ErrUnavailable, "SE Ranking projects, %s: the answer is not in the shape Outflare reads (%v); it begins %s", step, err, start)
	}
	return nil
}

// flexInt reads a whole number SE Ranking may send as a number, a numeric
// string ("86012"), a boolean or null, so a change of type in its answers
// does not fail the whole import.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	t := strings.Trim(strings.TrimSpace(string(b)), `"`)
	switch t {
	case "", "null":
		*f = 0
		return nil
	case "true":
		*f = 1
		return nil
	case "false":
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return fmt.Errorf("%q is not a number", t)
	}
	*f = flexInt(v)
	return nil
}
