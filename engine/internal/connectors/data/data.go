// Package data connects lead data providers: people search, enrichment and
// email finding. Each provider maps its own fields onto connectors.Person
// and connectors.PeopleFilter, so list building works the same whichever
// provider a workspace connects.
//
// Request shapes follow each provider's public API reference. Base URLs can
// be pointed at a fake in tests through connectors.Client.BaseURLs.
package data

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// All returns every available data connector.
func All() []c.Connector {
	return []c.Connector{Apollo{}, PDL{}, MoltSets{}, Prospeo{}, LeadMagic{}, Findymail{}, Snitcher{}, RB2B{}, Vector{}, Warmly{}}
}

// Planned lists data and signal providers we intend to add. Listing them
// shows people what is coming; they cannot be connected yet.
func Planned() []c.Definition {
	p := func(id, name, category, site, summary string, caps ...string) c.Definition {
		return c.Definition{ID: id, Name: name, Category: category, Auth: c.AuthAPIKey, Website: site, Summary: summary, Capabilities: caps}
	}
	data, signals := c.CategoryData, c.CategorySignals
	return []c.Definition{
		p("clay", "Clay", data, "https://www.clay.com", "Send enriched rows from a Clay table straight into a list."),
		p("zoominfo", "ZoomInfo", data, "https://www.zoominfo.com", "Search ZoomInfo contacts and companies with your own licence.", c.CapSearchPeople, c.CapEnrichPerson),
		p("lusha", "Lusha", data, "https://www.lusha.com", "Find emails and phone numbers with your Lusha credits.", c.CapEnrichPerson, c.CapFindEmail),
		p("cognism", "Cognism", data, "https://www.cognism.com", "Search Cognism's European-strong contact data.", c.CapSearchPeople, c.CapEnrichPerson),
		p("rocketreach", "RocketReach", data, "https://rocketreach.co", "Search people and reveal emails and phones.", c.CapSearchPeople, c.CapEnrichPerson),
		p("contactout", "ContactOut", data, "https://contactout.com", "Emails and phones from LinkedIn profiles.", c.CapEnrichPerson, c.CapFindEmail),
		p("icypeas", "Icypeas", data, "https://www.icypeas.com", "Low-cost email finding and verification.", c.CapFindEmail),
		p("dropcontact", "Dropcontact", data, "https://www.dropcontact.com", "GDPR-friendly email finding and enrichment.", c.CapFindEmail),
		p("fullenrich", "FullEnrich", data, "https://fullenrich.com", "Waterfall email and phone finding across many sources.", c.CapFindEmail),
		p("bettercontact", "BetterContact", data, "https://bettercontact.rocks", "Waterfall email and phone finding.", c.CapFindEmail),
		p("wiza", "Wiza", data, "https://wiza.co", "Emails and phones for LinkedIn and Sales Navigator lists.", c.CapEnrichPerson, c.CapFindEmail),
		p("crunchbase", "Crunchbase", signals, "https://www.crunchbase.com", "Funding rounds and acquisitions as reasons to reach out."),
		p("theirstack", "TheirStack", signals, "https://theirstack.com", "Companies hiring for roles that signal a need, and the tech they use."),
		p("predictleads", "PredictLeads", signals, "https://predictleads.com", "Hiring, news and technology changes at target companies."),
		p("usergems", "UserGems", signals, "https://www.usergems.com", "Past customers and champions who changed jobs."),
		p("commonroom", "Common Room", signals, "https://www.commonroom.io", "Community, product and website signals in one place."),
		p("trigify", "Trigify", signals, "https://trigify.io", "LinkedIn engagement and job-change signals."),
	}
}

func apiKeyField(help string) []c.Field {
	return []c.Field{{Name: "api_key", Label: "API key", Secret: true, Help: help}}
}

// call sends req and decodes a JSON body into v. A non-2xx status becomes a
// connectors.Error, except statuses listed in ok, which are returned for
// the caller to interpret (many providers answer "no match" with 404).
func call(ctx context.Context, cl *c.Client, name string, req *http.Request, v any, ok ...int) (int, error) {
	req = req.WithContext(ctx)
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return 0, c.Errf(c.ErrUnavailable, "could not reach %s: %v", name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, c.Errf(c.ErrUnavailable, "reading the %s response: %v", name, err)
	}
	for _, s := range ok {
		if resp.StatusCode == s {
			if v != nil {
				_ = json.Unmarshal(body, v)
			}
			return resp.StatusCode, nil
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, c.HTTPError(name, resp.StatusCode, string(body))
	}
	if v != nil {
		if err := json.Unmarshal(body, v); err != nil {
			return resp.StatusCode, c.Errf(c.ErrUnavailable, "%s returned something that is not JSON", name)
		}
	}
	return resp.StatusCode, nil
}

func getReq(rawURL string, q url.Values) *http.Request {
	if len(q) > 0 {
		rawURL += "?" + q.Encode()
	}
	req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
	return req
}

func postReq(rawURL string, body any) *http.Request {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// domainOf returns the bare host of a website or email domain.
func domainOf(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// splitRange reads a shared employee range ("11-50", "10001+") as bounds;
// max is 0 for open-ended ranges.
func splitRange(r string) (lo, hi int, ok bool) {
	r = strings.TrimSpace(r)
	if strings.HasSuffix(r, "+") {
		n, err := strconv.Atoi(strings.TrimSuffix(r, "+"))
		return n, 0, err == nil
	}
	a, b, found := strings.Cut(r, "-")
	if !found {
		return 0, 0, false
	}
	x, err1 := strconv.Atoi(strings.TrimSpace(a))
	y, err2 := strconv.Atoi(strings.TrimSpace(b))
	return x, y, err1 == nil && err2 == nil && x <= y
}

func ptr[T any](v T) *T { return &v }

// num reads JSON numbers some providers send as strings.
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	*n = num(f)
	return nil
}
