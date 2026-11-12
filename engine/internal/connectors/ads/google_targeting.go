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
	return []string{c.BidMaxConversions, c.BidMaxValue, c.BidPortfolio}
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
		"SELECT campaign_criterion.resource_name, campaign_criterion.type, campaign_criterion.negative, campaign_criterion.user_list.user_list, "+
			"campaign_criterion.location.geo_target_constant FROM campaign_criterion WHERE campaign.id = "+campaignID+
			" AND campaign_criterion.type IN ('USER_LIST', 'LOCATION') AND campaign_criterion.status != 'REMOVED'")
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
		"SELECT campaign.id, campaign.bidding_strategy_type, campaign.bidding_strategy, campaign.target_cpa.target_cpa_micros, campaign.maximize_conversions.target_cpa_micros, "+
			"campaign.target_roas.target_roas, campaign.maximize_conversion_value.target_roas FROM campaign WHERE campaign.id = "+campaignID)
	if err != nil {
		return c.AdTargeting{}, err
	}
	if len(rows) == 0 {
		return c.AdTargeting{}, c.Errf(c.ErrInvalid, "Google Ads has no campaign %s in account %s", campaignID, acct.ID)
	}
	var cp c.AdCampaign
	googleBidding(&cp, rows[0])
	t := c.AdTargeting{Bidding: c.AdBidding{Strategy: cp.BiddingStrategy, TargetCPAMicros: cp.TargetCPAMicros, TargetROAS: cp.TargetROAS},
		Audiences: []c.AdAudience{}, Countries: []string{}, Notes: []string{}}
	// A portfolio strategy keeps its target on itself (AdPortfolios).
	if ps := rows[0].Campaign.BiddingStrategy; ps != "" {
		t.Bidding.BiddingStrategyID = lastPart(ps)
	}
	crit, err := g.criteria(ctx, cl, creds, acct, campaignID)
	if err != nil {
		return t, err
	}
	var lists, places []string
	excluded := 0
	for _, r := range crit {
		cc := r.CampaignCriterion
		switch {
		case cc.Negative:
			excluded++
		case cc.Type == "USER_LIST" && cc.UserList.UserList != "":
			lists = append(lists, lastPart(cc.UserList.UserList))
		case cc.Type == "LOCATION" && cc.Location.GeoTargetConstant != "":
			places = append(places, cc.Location.GeoTargetConstant)
		}
	}
	if excluded > 0 {
		t.Notes = append(t.Notes, fmt.Sprintf("%d excluded audiences or places, changed in Google Ads", excluded))
	}
	if len(lists) > 0 {
		names := map[string]string{}
		if all, err := g.Audiences(ctx, cl, creds, acct); err == nil {
			for _, a := range all {
				names[a.ID] = a.Name
			}
		}
		for _, id := range lists {
			t.Audiences = append(t.Audiences, c.AdAudience{ID: id, Name: names[id]})
		}
	}
	if len(places) > 0 {
		quoted := make([]string, len(places))
		for i, p := range places {
			quoted[i] = "'" + strings.ReplaceAll(p, "'", "") + "'"
		}
		geo, err := g.search(ctx, cl, creds, acct.ID, login, "SELECT geo_target_constant.resource_name, geo_target_constant.country_code, geo_target_constant.target_type "+
			"FROM geo_target_constant WHERE geo_target_constant.resource_name IN ("+strings.Join(quoted, ", ")+")")
		if err != nil {
			return t, err
		}
		other := 0
		for _, r := range geo {
			if strings.EqualFold(r.GeoTargetConstant.TargetType, "Country") && r.GeoTargetConstant.CountryCode != "" {
				t.Countries = append(t.Countries, strings.ToUpper(r.GeoTargetConstant.CountryCode))
			} else {
				other++
			}
		}
		if other > 0 {
			t.Notes = append(t.Notes, fmt.Sprintf("%d regions or cities, changed in Google Ads", other))
		}
		sort.Strings(t.Countries)
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
	default:
		return c.Errf(c.ErrInvalid, "Google Ads bidding here is maximize_conversions, maximize_conversion_value or portfolio")
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
