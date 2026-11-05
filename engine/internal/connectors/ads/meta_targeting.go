package ads

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Targeting (step 5 of the paid ads plan) on Meta lives on each ad set:
// its custom audiences and the countries in its geo_locations. A change to
// a campaign is made on every one of its ad sets, each keeping the rest of
// its targeting as Meta returned it. Bidding is set per ad set on Meta and
// isn't changed here.

var _ c.AdTargetingManager = MetaAds{}

func (MetaAds) BiddingMethods() []string { return nil }

type metaAdSet struct {
	ID        string         `json:"id"`
	Status    string         `json:"effective_status"`
	Targeting map[string]any `json:"targeting"`
}

func (m MetaAds) adSets(ctx context.Context, cl *c.Client, creds c.Creds, campaignID string) ([]metaAdSet, error) {
	if !metaID(campaignID) {
		return nil, c.Errf(c.ErrInvalid, "Meta campaign ids are numbers")
	}
	var out []metaAdSet
	err := m.pages(ctx, cl, creds, m.base(cl)+"/"+esc(campaignID)+"/adsets?fields=id,effective_status,targeting&limit=100", func(raw json.RawMessage) error {
		var s metaAdSet
		if err := json.Unmarshal(raw, &s); err != nil {
			return c.Errf(c.ErrUnavailable, "Meta answered an ad set Outflare can't read")
		}
		if metaID(s.ID) && s.Status != "DELETED" && s.Status != "ARCHIVED" {
			out = append(out, s)
		}
		return nil
	})
	return out, err
}

func metaAudiences(t map[string]any) []c.AdAudience {
	var out []c.AdAudience
	list, _ := t["custom_audiences"].([]any)
	for _, v := range list {
		a, _ := v.(map[string]any)
		id, _ := a["id"].(string)
		name, _ := a["name"].(string)
		if metaID(id) {
			out = append(out, c.AdAudience{ID: id, Name: name})
		}
	}
	return out
}

func metaCountries(t map[string]any) []string {
	geo, _ := t["geo_locations"].(map[string]any)
	list, _ := geo["countries"].([]any)
	var out []string
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (m MetaAds) CampaignTargeting(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) (c.AdTargeting, error) {
	sets, err := m.adSets(ctx, cl, creds, campaignID)
	if err != nil {
		return c.AdTargeting{}, err
	}
	t := c.AdTargeting{Bidding: c.AdBidding{Strategy: "set_per_ad_set"}, Audiences: []c.AdAudience{}, Countries: []string{}, Notes: []string{}}
	audiences, countries := map[string]int{}, map[string]int{}
	names := map[string]string{}
	other := 0
	for _, s := range sets {
		for _, a := range metaAudiences(s.Targeting) {
			audiences[a.ID]++
			names[a.ID] = a.Name
		}
		for _, cc := range metaCountries(s.Targeting) {
			countries[cc]++
		}
		if geo, _ := s.Targeting["geo_locations"].(map[string]any); len(geo) > 0 {
			for k := range geo {
				if k != "countries" && k != "location_types" {
					other++
					break
				}
			}
		}
	}
	for id, n := range audiences {
		t.Audiences = append(t.Audiences, c.AdAudience{ID: id, Name: names[id]})
		if n < len(sets) {
			t.Notes = append(t.Notes, fmt.Sprintf("%s is on %d of %d ad sets", orID(names[id], id), n, len(sets)))
		}
	}
	for cc, n := range countries {
		t.Countries = append(t.Countries, cc)
		if n < len(sets) {
			t.Notes = append(t.Notes, fmt.Sprintf("%s is on %d of %d ad sets", cc, n, len(sets)))
		}
	}
	if other > 0 {
		t.Notes = append(t.Notes, fmt.Sprintf("%d ad sets also target regions or cities, changed in Ads Manager", other))
	}
	sort.Slice(t.Audiences, func(i, j int) bool { return t.Audiences[i].ID < t.Audiences[j].ID })
	sort.Strings(t.Countries)
	sort.Strings(t.Notes)
	return t, nil
}

func orID(name, id string) string {
	if name != "" {
		return name
	}
	return "audience " + id
}

func (m MetaAds) SetBidding(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, b c.AdBidding) error {
	return c.Errf(c.ErrInvalid, "Meta bids per ad set; change bidding in Ads Manager")
}

// setTargeting writes each ad set's targeting back after change edits it,
// skipping ad sets change leaves as they were.
func (m MetaAds) setTargeting(ctx context.Context, cl *c.Client, creds c.Creds, campaignID string, change func(t map[string]any) (bool, error)) error {
	sets, err := m.adSets(ctx, cl, creds, campaignID)
	if err != nil {
		return err
	}
	if len(sets) == 0 {
		return c.Errf(c.ErrInvalid, "the campaign has no ad sets to change")
	}
	for _, s := range sets {
		if s.Targeting == nil {
			s.Targeting = map[string]any{}
		}
		changed, err := change(s.Targeting)
		if err != nil {
			return err
		}
		if !changed {
			continue
		}
		spec, _ := json.Marshal(s.Targeting)
		if err := m.call(cl, creds).post(ctx, m.base(cl)+"/"+esc(s.ID), map[string]any{"targeting": string(spec)}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (m MetaAds) SetCampaignAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, audienceID string, add bool) error {
	if !metaID(audienceID) {
		return c.Errf(c.ErrInvalid, "Meta audience ids are numbers")
	}
	return m.setTargeting(ctx, cl, creds, campaignID, func(t map[string]any) (bool, error) {
		list, _ := t["custom_audiences"].([]any)
		var kept []any
		has := false
		for _, v := range list {
			a, _ := v.(map[string]any)
			if id, _ := a["id"].(string); id == audienceID {
				has = true
				continue
			}
			kept = append(kept, v)
		}
		switch {
		case add && has, !add && !has:
			return false, nil
		case add:
			t["custom_audiences"] = append(list, map[string]any{"id": audienceID})
		case len(kept) == 0:
			delete(t, "custom_audiences")
		default:
			t["custom_audiences"] = kept
		}
		return true, nil
	})
}

func (m MetaAds) SetCampaignCountry(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, country string, add bool) error {
	code, ok := c.CountryCode(country)
	if !ok {
		return c.Errf(c.ErrInvalid, "a country is a two-letter code such as AU")
	}
	return m.setTargeting(ctx, cl, creds, campaignID, func(t map[string]any) (bool, error) {
		geo, _ := t["geo_locations"].(map[string]any)
		if geo == nil {
			geo = map[string]any{}
		}
		have := metaCountries(t)
		var kept []any
		has := false
		for _, cc := range have {
			if cc == code {
				has = true
				continue
			}
			kept = append(kept, cc)
		}
		switch {
		case add && has, !add && !has:
			return false, nil
		case add:
			all := make([]any, 0, len(have)+1)
			for _, cc := range have {
				all = append(all, cc)
			}
			geo["countries"] = append(all, code)
		case len(kept) == 0 && !metaOtherPlaces(geo):
			return false, c.Errf(c.ErrInvalid, "%s is an ad set's only place; Meta needs at least one", code)
		case len(kept) == 0:
			delete(geo, "countries")
		default:
			geo["countries"] = kept
		}
		t["geo_locations"] = geo
		return true, nil
	})
}

// metaOtherPlaces reports whether geo names places besides countries.
func metaOtherPlaces(geo map[string]any) bool {
	for k, v := range geo {
		if l, ok := v.([]any); ok && len(l) > 0 && k != "countries" && k != "location_types" {
			return true
		}
	}
	return false
}
