package ads

import (
	"context"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ResolveLocations names each place asked for: a two-letter country code
// (AU), Google's id for a place (2036) or its resource name
// (geoTargetConstants/2036). A place Google doesn't have is an error that
// names it, so a change never drops one quietly.
func (g GoogleAds) ResolveLocations(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, refs []string) ([]c.AdLocation, error) {
	out := make([]c.AdLocation, 0, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	var codes, ids []string
	keys := make([]string, len(refs))
	for i, ref := range refs {
		ref = strings.TrimSpace(ref)
		id := strings.TrimPrefix(ref, "geoTargetConstants/")
		if _, err := strconv.ParseInt(id, 10, 64); err == nil && len(id) <= 12 {
			keys[i] = "geoTargetConstants/" + id
			ids = append(ids, "'"+keys[i]+"'")
			continue
		}
		code, ok := c.CountryCode(ref)
		if !ok {
			return nil, c.Errf(c.ErrInvalid, "%q is not a place: give a two-letter country code such as AU, or Google's id for a place such as geoTargetConstants/2036", ref)
		}
		keys[i] = code
		codes = append(codes, "'"+code+"'")
	}
	const sel = "SELECT geo_target_constant.resource_name, geo_target_constant.name, geo_target_constant.canonical_name, geo_target_constant.country_code, geo_target_constant.target_type FROM geo_target_constant WHERE "
	login := acct.Meta["login_customer_id"]
	found := map[string]c.AdLocation{}
	read := func(where string, byCode bool) error {
		rows, err := g.search(ctx, cl, creds, acct.ID, login, sel+where)
		if err != nil {
			return err
		}
		for _, r := range rows {
			gc := r.GeoTargetConstant
			if !strings.HasPrefix(gc.ResourceName, "geoTargetConstants/") {
				continue
			}
			l := c.AdLocation{GeoTargetConstant: gc.ResourceName, Name: gc.Name, TargetType: gc.TargetType, CountryCode: strings.ToUpper(gc.CountryCode)}
			country := strings.EqualFold(gc.TargetType, "Country")
			if !country && gc.CanonicalName != "" {
				l.Name = gc.CanonicalName
			}
			if byCode {
				if country && l.CountryCode != "" {
					found[l.CountryCode] = l
				}
			} else {
				found[gc.ResourceName] = l
			}
		}
		return nil
	}
	if len(ids) > 0 {
		if err := read("geo_target_constant.resource_name IN ("+strings.Join(ids, ", ")+")", false); err != nil {
			return nil, err
		}
	}
	if len(codes) > 0 {
		if err := read("geo_target_constant.country_code IN ("+strings.Join(codes, ", ")+") AND geo_target_constant.target_type = 'Country'", true); err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	for i, k := range keys {
		l, ok := found[k]
		if !ok {
			return nil, c.Errf(c.ErrInvalid, "Google Ads has no place %s", strings.TrimSpace(refs[i]))
		}
		if seen[l.GeoTargetConstant] {
			continue
		}
		seen[l.GeoTargetConstant] = true
		out = append(out, l)
	}
	return out, nil
}

// SetCampaignLocations makes the change's removals and additions in one
// campaignCriteria:mutate request. Google takes a mutate request whole or
// not at all (partial failure is off), so the campaign never sits halfway
// between two sets of places.
func (g GoogleAds) SetCampaignLocations(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, ch c.AdLocationChange) error {
	if err := googleCampaignID(campaignID); err != nil {
		return err
	}
	prefix := "customers/" + acct.ID + "/campaignCriteria/"
	campaign := g.resource(acct, "campaigns", campaignID)
	var ops []any
	for _, l := range append(append([]c.AdLocation{}, ch.RemoveIncluded...), ch.RemoveExcluded...) {
		if !strings.HasPrefix(l.ResourceName, prefix) {
			return c.Errf(c.ErrInvalid, "the change names a place that is not one of this campaign's")
		}
		ops = append(ops, map[string]any{"remove": l.ResourceName})
	}
	add := func(list []c.AdLocation, negative bool) error {
		for _, l := range list {
			if !strings.HasPrefix(l.GeoTargetConstant, "geoTargetConstants/") {
				return c.Errf(c.ErrInvalid, "a place to add is one of Google's (geoTargetConstants/N)")
			}
			ops = append(ops, map[string]any{"create": map[string]any{"campaign": campaign, "negative": negative, "location": map[string]any{"geoTargetConstant": l.GeoTargetConstant}}})
		}
		return nil
	}
	if err := add(ch.AddIncluded, false); err != nil {
		return err
	}
	if err := add(ch.AddExcluded, true); err != nil {
		return err
	}
	if len(ops) == 0 {
		return nil
	}
	return g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/campaignCriteria:mutate",
		map[string]any{"operations": ops}, nil)
}

// RenameCampaign gives the campaign another name.
func (g GoogleAds) RenameCampaign(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, name string) error {
	if err := googleCampaignID(campaignID); err != nil {
		return err
	}
	op := map[string]any{"update": map[string]any{"resourceName": g.resource(acct, "campaigns", campaignID), "name": name}, "updateMask": "name"}
	return g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/campaigns:mutate",
		map[string]any{"operations": []any{op}}, nil)
}

var (
	_ c.AdLocationSetter  = GoogleAds{}
	_ c.AdCampaignRenamer = GoogleAds{}
)
