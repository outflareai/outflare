package ads

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Targeting (step 5 of the paid ads plan): how a Google Ads campaign bids,
// the Customer Match lists it targets, and the countries it shows in, each
// read with GAQL and changed with one mutate. On Search, a list added to a
// campaign is observed (Google bids with it in mind) unless the campaign's
// own targeting setting narrows to it, as in Google Ads itself.

var _ c.AdTargetingManager = GoogleAds{}

func (GoogleAds) BiddingMethods() []string {
	return []string{c.BidMaxConversions, c.BidMaxValue, c.BidImpressionShare, c.BidMaxClicks, c.BidPortfolio}
}

func googleCampaignID(id string) error {
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return c.Errf(c.ErrInvalid, "Google Ads campaign ids are numbers")
	}
	return nil
}

// criteria lists the campaign's audience and location criteria.
func (g GoogleAds) criteria(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) ([]gaqlRow, error) {
	return g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT campaign_criterion.resource_name, campaign_criterion.criterion_id, campaign_criterion.type, campaign_criterion.negative, campaign_criterion.user_list.user_list, "+
			"campaign_criterion.location.geo_target_constant, campaign_criterion.proximity.radius, campaign_criterion.proximity.radius_units, "+
			"campaign_criterion.proximity.address.city_name, campaign_criterion.proximity.address.country_code FROM campaign_criterion WHERE campaign.id = "+campaignID+
			" AND campaign_criterion.type IN ('USER_LIST', 'LOCATION', 'PROXIMITY') AND campaign_criterion.status != 'REMOVED'")
}

func lastPart(resource string) string {
	return resource[strings.LastIndex(resource, "/")+1:]
}

func (g GoogleAds) CampaignTargeting(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) (c.AdTargeting, error) {
	if err := googleCampaignID(campaignID); err != nil {
		return c.AdTargeting{}, err
	}
	login := acct.Meta["login_customer_id"]
	rows, err := g.search(ctx, cl, creds, acct.ID, login,
		"SELECT campaign.id, campaign.resource_name, campaign.geo_target_type_setting.positive_geo_target_type, campaign.geo_target_type_setting.negative_geo_target_type, campaign.bidding_strategy_type, campaign.bidding_strategy, campaign.target_cpa.target_cpa_micros, campaign.maximize_conversions.target_cpa_micros, "+
			"campaign.target_roas.target_roas, campaign.maximize_conversion_value.target_roas, campaign.target_impression_share.location, "+
			"campaign.target_impression_share.location_fraction_micros, campaign.target_impression_share.cpc_bid_ceiling_micros, campaign.target_spend.cpc_bid_ceiling_micros "+
			"FROM campaign WHERE campaign.id = "+campaignID)
	if err != nil {
		return c.AdTargeting{}, err
	}
	if len(rows) == 0 {
		return c.AdTargeting{}, c.Errf(c.ErrInvalid, "Google Ads has no campaign %s in account %s", campaignID, acct.ID)
	}
	var cp c.AdCampaign
	googleBidding(&cp, rows[0])
	cg := rows[0].Campaign
	t := c.AdTargeting{Bidding: c.AdBidding{Strategy: cp.BiddingStrategy, TargetCPAMicros: cp.TargetCPAMicros, TargetROAS: cp.TargetROAS},
		Audiences: []c.AdAudience{}, Countries: []string{}, IncludedLocations: []c.AdLocation{}, ExcludedLocations: []c.AdLocation{}, ExcludedCountries: []string{},
		ExcludedAudiences: []c.AdAudience{}, Notes: []string{}, ResourceName: firstText(cg.ResourceName, g.resource(acct, "campaigns", campaignID)),
		PositiveGeoTargetType: cg.GeoTargetTypeSetting.PositiveGeoTargetType, NegativeGeoTargetType: cg.GeoTargetTypeSetting.NegativeGeoTargetType}
	t.Bidding.ImpressionShareLocation, t.Bidding.ImpressionShare, t.Bidding.MaxCPCMicros = cg.TargetImpressionShare.read()
	if v := cg.TargetSpend.CpcBidCeilingMicros; v > 0 && t.Bidding.MaxCPCMicros == nil {
		t.Bidding.MaxCPCMicros = ptrI(int64(v))
	}
	// A portfolio strategy keeps its target on itself (AdPortfolios).
	if ps := cg.BiddingStrategy; ps != "" {
		t.Bidding.BiddingStrategyID = lastPart(ps)
	}
	crit, err := g.criteria(ctx, cl, creds, acct, campaignID)
	if err != nil {
		return t, err
	}
	var lists, noLists, places []string
	for _, r := range crit {
		cc := r.CampaignCriterion
		loc := c.AdLocation{CriterionID: fmt.Sprint(int64(cc.CriterionID)), ResourceName: cc.ResourceName}
		switch {
		case cc.Type == "USER_LIST" && cc.UserList.UserList != "":
			if cc.Negative {
				noLists = append(noLists, lastPart(cc.UserList.UserList))
			} else {
				lists = append(lists, lastPart(cc.UserList.UserList))
			}
			continue
		case cc.Type == "LOCATION" && cc.Location.GeoTargetConstant != "":
			loc.GeoTargetConstant = cc.Location.GeoTargetConstant
			places = append(places, cc.Location.GeoTargetConstant)
		case cc.Type == "PROXIMITY":
			radius := cc.Proximity.Radius
			loc.TargetType, loc.Radius, loc.RadiusUnits = "Proximity", &radius, strings.ToLower(cc.Proximity.RadiusUnits)
			loc.Name, loc.CountryCode = cc.Proximity.Address.CityName, strings.ToUpper(cc.Proximity.Address.CountryCode)
		default:
			continue
		}
		if cc.Negative {
			t.ExcludedLocations = append(t.ExcludedLocations, loc)
		} else {
			t.IncludedLocations = append(t.IncludedLocations, loc)
		}
	}
	t.TargetAllLocations = len(t.IncludedLocations) == 0
	if len(lists)+len(noLists) > 0 {
		names := map[string]string{}
		if all, err := g.Audiences(ctx, cl, creds, acct); err == nil {
			for _, a := range all {
				names[a.ID] = a.Name
			}
		}
		for _, id := range lists {
			t.Audiences = append(t.Audiences, c.AdAudience{ID: id, Name: names[id]})
		}
		for _, id := range noLists {
			t.ExcludedAudiences = append(t.ExcludedAudiences, c.AdAudience{ID: id, Name: names[id]})
		}
		if len(noLists) > 0 {
			t.Notes = append(t.Notes, fmt.Sprintf("%d excluded audiences, changed in Google Ads", len(noLists)))
		}
	}
	// Each place in words: its name, what kind of place and its country.
	if len(places) > 0 {
		quoted := make([]string, len(places))
		for i, p := range places {
			quoted[i] = "'" + strings.ReplaceAll(p, "'", "") + "'"
		}
		geo, err := g.search(ctx, cl, creds, acct.ID, login, "SELECT geo_target_constant.resource_name, geo_target_constant.name, geo_target_constant.canonical_name, "+
			"geo_target_constant.country_code, geo_target_constant.target_type FROM geo_target_constant WHERE geo_target_constant.resource_name IN ("+strings.Join(quoted, ", ")+")")
		if err != nil {
			return t, err
		}
		known := map[string]gaqlRow{}
		for _, r := range geo {
			known[r.GeoTargetConstant.ResourceName] = r
		}
		name := func(l *c.AdLocation) bool {
			gc, ok := known[l.GeoTargetConstant]
			if !ok {
				return false
			}
			l.TargetType, l.CountryCode = gc.GeoTargetConstant.TargetType, strings.ToUpper(gc.GeoTargetConstant.CountryCode)
			l.Name = gc.GeoTargetConstant.Name
			// A region or city is told apart by its full name ("Lisbon,Lisbon,Portugal").
			if !strings.EqualFold(l.TargetType, "Country") && gc.GeoTargetConstant.CanonicalName != "" {
				l.Name = gc.GeoTargetConstant.CanonicalName
			}
			return strings.EqualFold(l.TargetType, "Country") && l.CountryCode != ""
		}
		other := 0
		for i := range t.IncludedLocations {
			l := &t.IncludedLocations[i]
			if l.GeoTargetConstant == "" {
				continue
			}
			if name(l) {
				t.Countries = append(t.Countries, l.CountryCode)
			} else {
				other++
			}
		}
		for i := range t.ExcludedLocations {
			if l := &t.ExcludedLocations[i]; l.GeoTargetConstant != "" && name(l) {
				t.ExcludedCountries = append(t.ExcludedCountries, l.CountryCode)
			}
		}
		if other > 0 {
			t.Notes = append(t.Notes, fmt.Sprintf("%d regions or cities, changed in Google Ads", other))
		}
		sort.Strings(t.Countries)
		sort.Strings(t.ExcludedCountries)
	}
	byName := func(l []c.AdLocation) {
		sort.SliceStable(l, func(i, j int) bool { return l[i].Name < l[j].Name })
	}
	byName(t.IncludedLocations)
	byName(t.ExcludedLocations)
	if t.TargetAllLocations {
		t.Notes = append(t.Notes, "No place is targeted, so the campaign shows everywhere it isn't excluded from")
	}
	if n := len(t.ExcludedLocations); n > 0 {
		t.Notes = append(t.Notes, fmt.Sprintf("%d excluded places, listed under excluded_locations", n))
	}
	return t, nil
}

// SetBidding switches the campaign to Maximise conversions (with a target
// cost per conversion, or none), Maximise conversion value (with a target
// return, or none) or a portfolio strategy of the account. The strategy is
// one oneof on the campaign, so setting a standard one takes the campaign
// off its portfolio strategy, and setting a portfolio one replaces its
// standard strategy.
func (g GoogleAds) SetBidding(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, b c.AdBidding) error {
	if err := googleCampaignID(campaignID); err != nil {
		return err
	}
	update := map[string]any{"resourceName": g.resource(acct, "campaigns", campaignID)}
	var mask string
	switch b.Strategy {
	case c.BidMaxConversions:
		mc := map[string]any{}
		mask = "maximize_conversions"
		if b.TargetCPAMicros != nil {
			if *b.TargetCPAMicros <= 0 {
				return c.Errf(c.ErrInvalid, "the target cost per conversion must be more than nothing")
			}
			mc["targetCpaMicros"] = strconv.FormatInt(*b.TargetCPAMicros, 10)
			mask = "maximize_conversions.target_cpa_micros"
		}
		update["maximizeConversions"] = mc
	case c.BidMaxValue:
		mv := map[string]any{}
		mask = "maximize_conversion_value"
		if b.TargetROAS != nil {
			if *b.TargetROAS <= 0 {
				return c.Errf(c.ErrInvalid, "the target return on spend must be more than nothing")
			}
			mv["targetRoas"] = *b.TargetROAS
			mask = "maximize_conversion_value.target_roas"
		}
		update["maximizeConversionValue"] = mv
	case c.BidPortfolio:
		if _, err := strconv.ParseInt(b.BiddingStrategyID, 10, 64); err != nil {
			return c.Errf(c.ErrInvalid, "a portfolio strategy is named by its id, from the account's bidding strategies")
		}
		update["biddingStrategy"] = g.resource(acct, "biddingStrategies", b.BiddingStrategyID)
		mask = "bidding_strategy"
	case c.BidImpressionShare:
		body, err := googleShareBody(b.ImpressionShareLocation, b.ImpressionShare, b.MaxCPCMicros)
		if err != nil {
			return err
		}
		update["targetImpressionShare"] = body
		mask = "target_impression_share.location,target_impression_share.location_fraction_micros,target_impression_share.cpc_bid_ceiling_micros"
	case c.BidMaxClicks:
		ts := map[string]any{}
		mask = "target_spend"
		if b.MaxCPCMicros != nil {
			if *b.MaxCPCMicros <= 0 {
				return c.Errf(c.ErrInvalid, "the most to pay for a click must be more than nothing")
			}
			ts["cpcBidCeilingMicros"] = strconv.FormatInt(*b.MaxCPCMicros, 10)
			mask = "target_spend.cpc_bid_ceiling_micros"
		}
		update["targetSpend"] = ts
	default:
		return c.Errf(c.ErrInvalid, "Google Ads bidding here is maximize_conversions, maximize_conversion_value, target_impression_share, target_spend or portfolio")
	}
	return g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/campaigns:mutate",
		map[string]any{"operations": []any{map[string]any{"update": update, "updateMask": mask}}}, nil)
}

// removeCriterion removes one campaign criterion by its resource name.
func (g GoogleAds) removeCriterion(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, resource string) error {
	if !strings.HasPrefix(resource, "customers/"+acct.ID+"/campaignCriteria/") {
		return c.Errf(c.ErrUnavailable, "Google Ads answered an unexpected criterion")
	}
	return g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/campaignCriteria:mutate",
		map[string]any{"operations": []any{map[string]any{"remove": resource}}}, nil)
}

func (g GoogleAds) SetCampaignAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, audienceID string, add bool) error {
	if err := googleCampaignID(campaignID); err != nil {
		return err
	}
	if _, err := strconv.ParseInt(audienceID, 10, 64); err != nil {
		return c.Errf(c.ErrInvalid, "Google Ads audience ids are numbers")
	}
	list := g.resource(acct, "userLists", audienceID)
	crit, err := g.criteria(ctx, cl, creds, acct, campaignID)
	if err != nil {
		return err
	}
	for _, r := range crit {
		cc := r.CampaignCriterion
		if cc.Type == "USER_LIST" && !cc.Negative && cc.UserList.UserList == list {
			if add {
				return nil
			}
			return g.removeCriterion(ctx, cl, creds, acct, cc.ResourceName)
		}
	}
	if !add {
		return nil
	}
	_, err = g.mutate(ctx, cl, creds, acct, "campaignCriteria", map[string]any{"campaign": g.resource(acct, "campaigns", campaignID), "userList": map[string]any{"userList": list}})
	return err
}

func (g GoogleAds) SetCampaignCountry(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, country string, add bool) error {
	if err := googleCampaignID(campaignID); err != nil {
		return err
	}
	code, ok := c.CountryCode(country)
	if !ok {
		return c.Errf(c.ErrInvalid, "a country is a two-letter code such as AU")
	}
	place, err := g.country(ctx, cl, creds, acct, code)
	if err != nil {
		return err
	}
	crit, err := g.criteria(ctx, cl, creds, acct, campaignID)
	if err != nil {
		return err
	}
	found, locations := "", 0
	for _, r := range crit {
		cc := r.CampaignCriterion
		if cc.Type != "LOCATION" || cc.Negative {
			continue
		}
		locations++
		if cc.Location.GeoTargetConstant == place {
			found = cc.ResourceName
		}
	}
	switch {
	case add && found != "":
		return nil
	case add:
		_, err = g.mutate(ctx, cl, creds, acct, "campaignCriteria", map[string]any{"campaign": g.resource(acct, "campaigns", campaignID), "location": map[string]any{"geoTargetConstant": place}})
		return err
	case found == "":
		return nil
	case locations == 1:
		return c.Errf(c.ErrInvalid, "%s is the campaign's only place; without it the campaign would show everywhere", code)
	}
	return g.removeCriterion(ctx, cl, creds, acct, found)
}

// country is Google's place for a country code (geoTargetConstants/N).
func (g GoogleAds) country(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, code string) (string, error) {
	geo, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], "SELECT geo_target_constant.resource_name, geo_target_constant.country_code, geo_target_constant.target_type "+
		"FROM geo_target_constant WHERE geo_target_constant.country_code = '"+code+"' AND geo_target_constant.target_type = 'Country'")
	if err != nil {
		return "", err
	}
	for _, r := range geo {
		if strings.EqualFold(r.GeoTargetConstant.CountryCode, code) && strings.HasPrefix(r.GeoTargetConstant.ResourceName, "geoTargetConstants/") {
			return r.GeoTargetConstant.ResourceName, nil
		}
	}
	return "", c.Errf(c.ErrInvalid, "Google Ads has no country %s", code)
}
