package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/geoip"
	"github.com/outflareai/outflare/engine/internal/trafficsources"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// A destination preview shows the address a tracker click would go on to
// for a URL as someone wrote it: what Outflare adds, what it replaces and
// what it leaves alone. It runs the redirect's own builder
// (clickDestination) on a made-up click, so the two can't drift apart, and
// it writes nothing: no click, no visit, no count.

// previewClickID stands in for a click's key. It has the shape of a real
// one, and no click ever gets it: real keys never hold the letter "l" or
// the digit "0" (utm.NewShortCode).
const previewClickID = "previewclickid00"

func (s *Server) routesTrackerPreview(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/destination-preview", s.onBrand(auth.ScopeAnalyticsRead, s.previewDestination))
}

type destinationPreview struct {
	// ConfiguredURL is the URL as it was given.
	ConfiguredURL string `json:"configured_url"`
	// SavedURL is what saving it on a lander, an offer or a link keeps:
	// without the parameters Outflare owns.
	SavedURL string `json:"saved_url"`
	// EffectiveURL is where a click would go, for the sample click.
	EffectiveURL string `json:"effective_url"`
	// CampaignID is the campaign whose tags it carries, nil when none was
	// named (then it carries no utm_* and no ofc).
	CampaignID *string `json:"campaign_id"`
	// SampleClickID is the made-up click id in EffectiveURL.
	SampleClickID string `json:"sample_click_id"`
	// Parameters are EffectiveURL's query parameters, in order.
	Parameters []previewParameter `json:"parameters"`
	// Replaced are the parameters the URL set that Outflare owns, so their
	// written values were dropped.
	Replaced []string `json:"replaced"`
}

type previewParameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Source says who set it: click (ofclk, the click's id), campaign
	// (utm_source, utm_medium, utm_campaign, ofc), link (utm_content,
	// utm_term), token (the URL's own parameter, with a token filled in for
	// the click) or url (as written).
	Source string `json:"source"`
}

// previewClick is the made-up click a preview is built for.
func previewClick() bufferedClick {
	c := bufferedClick{key: previewClickID, sourceClickID: "sample-source-click-id", fields: map[string]string{}}
	for i := 1; i <= trafficsources.Fields; i++ {
		c.fields[strconv.Itoa(i)] = "sample-field" + strconv.Itoa(i)
	}
	c.detail = clickDetail{place: geoip.Place{Country: "US", Region: "California", City: "San Francisco", ASN: 64500, Org: "Example ISP"},
		device: "desktop", os: "macOS", browser: "Chrome", language: "en"}
	return c
}

// ownedParam says whether Outflare sets a destination parameter itself.
func ownedParam(name string) bool {
	return name == clickKeyParam || name == "ofc" || strings.HasPrefix(name, "utm_")
}

func queryParts(raw string) [][2]string {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	var out [][2]string
	for _, part := range strings.Split(u.RawQuery, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		if n, err := url.QueryUnescape(k); err == nil {
			k = n
		}
		if n, err := url.QueryUnescape(v); err == nil {
			v = n
		}
		out = append(out, [2]string{k, v})
	}
	return out
}

// buildDestinationPreview previews raw for a campaign (the zero value for
// none) and a link's content and term.
func buildDestinationPreview(c trackingCampaignView, raw, content, term string) (destinationPreview, error) {
	if err := checkDestinationTokens("destination", raw); err != nil {
		return destinationPreview{}, err
	}
	saved, err := utm.Destination(raw)
	if err != nil {
		return destinationPreview{}, errInvalid("destination: " + err.Error())
	}
	link := trackingLinkView{URL: saved, Content: content, Term: term}
	t := redirectTarget{campaign: c, link: link}
	p := destinationPreview{ConfiguredURL: raw, SavedURL: saved, SampleClickID: previewClickID, Parameters: []previewParameter{}, Replaced: []string{}}
	p.EffectiveURL = clickDestination(t, link, previewClick(), "0.25", previewClickID)
	if c.ID != "" {
		p.CampaignID = &c.ID
	}
	seen := map[string]bool{}
	for _, kv := range queryParts(raw) {
		if ownedParam(kv[0]) && !seen[kv[0]] {
			seen[kv[0]] = true
			p.Replaced = append(p.Replaced, kv[0])
		}
	}
	written, i := queryParts(saved), 0
	for _, kv := range queryParts(p.EffectiveURL) {
		par := previewParameter{Name: kv[0], Value: kv[1], Source: "url"}
		switch {
		case kv[0] == clickKeyParam:
			par.Source = "click"
		case kv[0] == "utm_content" || kv[0] == "utm_term":
			par.Source = "link"
		case ownedParam(kv[0]):
			par.Source = "campaign"
		default:
			if i < len(written) && written[i][1] != kv[1] {
				par.Source = "token"
			}
			i++
		}
		p.Parameters = append(p.Parameters, par)
	}
	return p, nil
}

func (s *Server) previewDestination(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	raw := strings.TrimSpace(q.Get("destination"))
	if raw == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "destination is the URL to preview")
		return
	}
	var c trackingCampaignView
	if ref := strings.TrimSpace(q.Get("campaign")); ref != "" {
		v, ok := s.findTrackingCampaign(w, r, a.WorkspaceID, ref)
		if !ok {
			return
		}
		c = *v
	}
	p, err := buildDestinationPreview(c, raw, strings.TrimSpace(q.Get("content")), strings.TrimSpace(q.Get("term")))
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, p)
}
