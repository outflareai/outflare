package ads

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// New ads (step 6 of the paid ads plan): a draft written by the brand
// agent or a person becomes, once a person approves it, a responsive
// search ad in an ad group, a new ad group with keywords and an ad, or a
// new search campaign with its budget, bidding, countries and an ad group,
// all made paused in one batch mutate (all or nothing), and turned on
// later when a person says so.

var _ c.AdCreator = GoogleAds{}

func (GoogleAds) DraftKinds() []string { return []string{c.DraftAd, c.DraftAdGroup, c.DraftCampaign} }

// draftURL checks an ad's link: a full http or https address.
func draftURL(s string) error {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(s) > 2048 {
		return c.Errf(c.ErrInvalid, "final_url is the full address the ad links to, starting with https://")
	}
	return nil
}

// draftTexts checks a list of ad texts: from min to max of them, each
// with text and at most limit characters, none twice.
func draftTexts(what string, list []string, min, max, limit int) error {
	if len(list) < min || len(list) > max {
		return c.Errf(c.ErrInvalid, "a search ad has %d to %d %s", min, max, what)
	}
	seen := map[string]bool{}
	for _, t := range list {
		t = strings.TrimSpace(t)
		if t == "" || utf8.RuneCountInString(t) > limit {
			return c.Errf(c.ErrInvalid, "each of the %s has text and at most %d characters: %q", what, limit, t)
		}
		if seen[strings.ToLower(t)] {
			return c.Errf(c.ErrInvalid, "the %s %q is there twice", what, t)
		}
		seen[strings.ToLower(t)] = true
	}
	return nil
}

func (g GoogleAds) CheckDraft(d c.AdDraft) error {
	if err := draftURL(d.FinalURL); err != nil {
		return err
	}
	if err := draftTexts("headlines", d.Headlines, 3, 15, 30); err != nil {
		return err
	}
	if err := draftTexts("descriptions", d.Descriptions, 2, 4, 90); err != nil {
		return err
	}
	for _, p := range []string{d.Path1, d.Path2} {
		if utf8.RuneCountInString(p) > 15 || strings.ContainsAny(p, "/ ") {
			return c.Errf(c.ErrInvalid, "a display path part is at most 15 characters, without spaces or slashes")
		}
	}
	if d.Path2 != "" && d.Path1 == "" {
		return c.Errf(c.ErrInvalid, "path2 needs path1")
	}
	if d.ImageURL != "" || d.PrimaryText != "" {
		return c.Errf(c.ErrInvalid, "Google search ads are text only; an image or main text is for Meta")
	}
	switch d.Kind {
	case c.DraftAd:
		if _, err := strconv.ParseInt(d.AdGroupID, 10, 64); err != nil {
			return c.Errf(c.ErrInvalid, "a new ad goes in one of the campaign's ad groups")
		}
		return nil
	case c.DraftAdGroup, c.DraftCampaign:
	default:
		return c.Errf(c.ErrInvalid, "a Google Ads draft is an ad, an ad_group or a campaign")
	}
	name := strings.TrimSpace(d.Name)
	if name == "" || utf8.RuneCountInString(name) > 255 {
		return c.Errf(c.ErrInvalid, "a new %s has a name of at most 255 characters", strings.ReplaceAll(d.Kind, "_", " "))
	}
	if len(d.Keywords) == 0 || len(d.Keywords) > 50 {
		return c.Errf(c.ErrInvalid, "a new ad group has 1 to 50 keywords")
	}
	for _, k := range d.Keywords {
		if _, err := keywordText(k.Text); err != nil {
			return err
		}
		if k.MatchType != "" {
			if _, err := googleMatch(k.MatchType); err != nil {
				return err
			}
		}
	}
	if d.Kind == c.DraftAdGroup {
		if _, err := strconv.ParseInt(d.CampaignID, 10, 64); err != nil {
			return c.Errf(c.ErrInvalid, "a new ad group goes in one of the account's campaigns")
		}
		if d.DailyBudgetMicros != nil || d.Bidding != nil || len(d.Countries) > 0 {
			return c.Errf(c.ErrInvalid, "a new ad group takes its budget, bidding and countries from its campaign")
		}
		return nil
	}
	if d.DailyBudgetMicros == nil || *d.DailyBudgetMicros < 10_000 {
		return c.Errf(c.ErrInvalid, "a new campaign needs a daily budget (daily_budget_micros)")
	}
	if len(d.Countries) == 0 || len(d.Countries) > 50 {
		return c.Errf(c.ErrInvalid, "a new campaign shows in 1 to 50 countries, so it never shows everywhere by mistake")
	}
	for _, cc := range d.Countries {
		if _, ok := c.CountryCode(cc); !ok {
			return c.Errf(c.ErrInvalid, "a country is a two-letter code such as AU, not %q", cc)
		}
	}
	if d.Bidding != nil {
		if _, err := googleBiddingFields(*d.Bidding); err != nil {
			return err
		}
	}
	return nil
}

// googleBiddingFields is a campaign's bidding as Google's create takes it.
func googleBiddingFields(b c.AdBidding) (map[string]any, error) {
	switch b.Strategy {
	case c.BidMaxConversions:
		mc := map[string]any{}
		if b.TargetCPAMicros != nil {
			if *b.TargetCPAMicros <= 0 {
				return nil, c.Errf(c.ErrInvalid, "the target cost per conversion must be more than nothing")
			}
			mc["targetCpaMicros"] = strconv.FormatInt(*b.TargetCPAMicros, 10)
		}
		return map[string]any{"maximizeConversions": mc}, nil
	case c.BidMaxValue:
		mv := map[string]any{}
		if b.TargetROAS != nil {
			if *b.TargetROAS <= 0 {
				return nil, c.Errf(c.ErrInvalid, "the target return on spend must be more than nothing")
			}
			mv["targetRoas"] = *b.TargetROAS
		}
		return map[string]any{"maximizeConversionValue": mv}, nil
	}
	return nil, c.Errf(c.ErrInvalid, "Google Ads bidding here is maximize_conversions or maximize_conversion_value")
}

// googleRSA is a draft's responsive search ad.
func googleRSA(d c.AdDraft) map[string]any {
	var hs, ds []any
	for _, h := range d.Headlines {
		hs = append(hs, map[string]any{"text": strings.TrimSpace(h)})
	}
	for _, t := range d.Descriptions {
		ds = append(ds, map[string]any{"text": strings.TrimSpace(t)})
	}
	rsa := map[string]any{"headlines": hs, "descriptions": ds}
	if d.Path1 != "" {
		rsa["path1"] = d.Path1
	}
	if d.Path2 != "" {
		rsa["path2"] = d.Path2
	}
	return map[string]any{"finalUrls": []string{strings.TrimSpace(d.FinalURL)}, "responsiveSearchAd": rsa}
}

type googleMutateResult struct {
	ResourceName string `json:"resourceName"`
}

// batch runs operations in one googleAds:mutate, all or nothing, and
// returns each one's resource name in order.
func (g GoogleAds) batch(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, ops []any) ([]string, error) {
	var resp struct {
		Responses []map[string]googleMutateResult `json:"mutateOperationResponses"`
	}
	if err := g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/googleAds:mutate",
		map[string]any{"mutateOperations": ops}, &resp); err != nil {
		return nil, err
	}
	out := make([]string, len(resp.Responses))
	for i, r := range resp.Responses {
		for _, v := range r {
			out[i] = v.ResourceName
		}
	}
	return out, nil
}

// lastID is the id at the end of a resource name; for an ad group ad
// (adGroupAds/{group}~{ad}) the ad's own.
func lastID(resource string) string {
	id := resource[strings.LastIndex(resource, "/")+1:]
	return id[strings.LastIndex(id, "~")+1:]
}

func (g GoogleAds) CreateAds(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, d c.AdDraft) (c.AdMade, error) {
	if err := g.CheckDraft(d); err != nil {
		return c.AdMade{}, err
	}
	var ops []any
	made := c.AdMade{CampaignID: d.CampaignID, AdGroupID: d.AdGroupID}
	group := g.resource(acct, "adGroups", d.AdGroupID)
	if d.Kind == c.DraftCampaign {
		budget, campaign := g.resource(acct, "campaignBudgets", "-1"), g.resource(acct, "campaigns", "-2")
		ops = append(ops, map[string]any{"campaignBudgetOperation": map[string]any{"create": map[string]any{
			"resourceName": budget, "name": strings.TrimSpace(d.Name) + " budget", "amountMicros": strconv.FormatInt(*d.DailyBudgetMicros, 10),
			"deliveryMethod": "STANDARD", "explicitlyShared": false}}})
		cp := map[string]any{"resourceName": campaign, "name": strings.TrimSpace(d.Name), "status": "PAUSED", "advertisingChannelType": "SEARCH",
			"campaignBudget": budget, "containsEuPoliticalAdvertising": "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING",
			"networkSettings": map[string]any{"targetGoogleSearch": true, "targetSearchNetwork": false, "targetContentNetwork": false, "targetPartnerSearchNetwork": false}}
		bid := c.AdBidding{Strategy: c.BidMaxConversions}
		if d.Bidding != nil {
			bid = *d.Bidding
		}
		fields, _ := googleBiddingFields(bid)
		for k, v := range fields {
			cp[k] = v
		}
		ops = append(ops, map[string]any{"campaignOperation": map[string]any{"create": cp}})
		for _, cc := range d.Countries {
			code, _ := c.CountryCode(cc)
			place, err := g.country(ctx, cl, creds, acct, code)
			if err != nil {
				return c.AdMade{}, err
			}
			ops = append(ops, map[string]any{"campaignCriterionOperation": map[string]any{"create": map[string]any{
				"campaign": campaign, "location": map[string]any{"geoTargetConstant": place}}}})
		}
		d.CampaignID = "-2"
		made.Made = append(made.Made, c.DraftCampaign)
	}
	if d.Kind != c.DraftAd {
		group = g.resource(acct, "adGroups", "-3")
		ops = append(ops, map[string]any{"adGroupOperation": map[string]any{"create": map[string]any{
			"resourceName": group, "campaign": g.resource(acct, "campaigns", d.CampaignID), "name": strings.TrimSpace(d.Name),
			"status": "PAUSED", "type": "SEARCH_STANDARD"}}})
		for _, k := range d.Keywords {
			text, _ := keywordText(k.Text)
			m := "PHRASE"
			if k.MatchType != "" {
				m, _ = googleMatch(k.MatchType)
			}
			ops = append(ops, map[string]any{"adGroupCriterionOperation": map[string]any{"create": map[string]any{
				"adGroup": group, "status": "ENABLED", "keyword": map[string]any{"text": text, "matchType": m}}}})
		}
		made.Made = append(made.Made, c.DraftAdGroup)
	}
	ops = append(ops, map[string]any{"adGroupAdOperation": map[string]any{"create": map[string]any{
		"adGroup": group, "status": "PAUSED", "ad": googleRSA(d)}}})
	made.Made = append(made.Made, c.DraftAd)

	names, err := g.batch(ctx, cl, creds, acct, ops)
	if err != nil {
		return c.AdMade{}, err
	}
	prefix := "customers/" + acct.ID + "/"
	for _, n := range names {
		if !strings.HasPrefix(n, prefix) {
			continue
		}
		switch kind := strings.SplitN(strings.TrimPrefix(n, prefix), "/", 2)[0]; kind {
		case "campaigns":
			made.CampaignID = lastID(n)
		case "adGroups":
			made.AdGroupID = lastID(n)
		case "adGroupAds":
			made.AdID = lastID(n)
		}
	}
	if made.AdID == "" || made.AdGroupID == "" || made.CampaignID == "" || made.CampaignID == "-2" || made.AdGroupID == "-3" {
		return made, c.Errf(c.ErrUnavailable, "Google Ads made the ads but did not say their ids")
	}
	return made, nil
}

func (g GoogleAds) GoLive(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, made c.AdMade) error {
	for _, id := range []string{made.CampaignID, made.AdGroupID, made.AdID} {
		if _, err := strconv.ParseInt(id, 10, 64); err != nil {
			return c.Errf(c.ErrInvalid, "Google Ads ids are numbers")
		}
	}
	enable := func(service, resource string) map[string]any {
		return map[string]any{service: map[string]any{"update": map[string]any{"resourceName": resource, "status": "ENABLED"}, "updateMask": "status"}}
	}
	// The ad first and the campaign last, so nothing serves half made.
	ops := []any{enable("adGroupAdOperation", g.resource(acct, "adGroupAds", made.AdGroupID+"~"+made.AdID))}
	if made.Has(c.DraftAdGroup) {
		ops = append(ops, enable("adGroupOperation", g.resource(acct, "adGroups", made.AdGroupID)))
	}
	if made.Has(c.DraftCampaign) {
		ops = append(ops, enable("campaignOperation", g.resource(acct, "campaigns", made.CampaignID)))
	}
	_, err := g.batch(ctx, cl, creds, acct, ops)
	return err
}
