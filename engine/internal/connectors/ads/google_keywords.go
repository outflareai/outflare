package ads

import (
	"context"
	"fmt"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Keywords (step 3 of the paid ads plan): adding a keyword, a negative
// keyword, splitting a keyword into its own ad group, and reading Google's
// own recommendations, all on the workspace's own account.

var _ c.AdKeywordManager = GoogleAds{}

type googleImpact struct {
	Clicks      flexNum `json:"clicks"`
	Conversions flexNum `json:"conversions"`
	CostMicros  flexInt `json:"costMicros"`
}

// googleMatch is Google's word for a match type.
func googleMatch(m string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "exact":
		return "EXACT", nil
	case "phrase":
		return "PHRASE", nil
	case "broad":
		return "BROAD", nil
	}
	return "", c.Errf(c.ErrInvalid, "match type is exact, phrase or broad, not %q", m)
}

func keywordText(t string) (string, error) {
	t = strings.Join(strings.Fields(t), " ")
	if t == "" || len(t) > 80 || len(strings.Fields(t)) > 10 {
		return "", c.Errf(c.ErrInvalid, "a keyword is 1 to 80 characters and at most 10 words")
	}
	return t, nil
}

func (g GoogleAds) mutate(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, service string, create map[string]any) (string, error) {
	var resp struct {
		Results []struct {
			ResourceName string `json:"resourceName"`
		} `json:"results"`
	}
	err := g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/"+service+":mutate",
		map[string]any{"operations": []any{map[string]any{"create": create}}}, &resp)
	if err != nil {
		return "", err
	}
	if len(resp.Results) == 0 {
		return "", nil
	}
	return resp.Results[0].ResourceName, nil
}

func (g GoogleAds) resource(acct c.AdAccount, kind, id string) string {
	return "customers/" + acct.ID + "/" + kind + "/" + id
}

func (g GoogleAds) AddKeyword(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, adGroupID, text, matchType string) error {
	m, err := googleMatch(matchType)
	if err != nil {
		return err
	}
	if text, err = keywordText(text); err != nil {
		return err
	}
	_, err = g.mutate(ctx, cl, creds, acct, "adGroupCriteria", map[string]any{"adGroup": g.resource(acct, "adGroups", adGroupID), "status": "ENABLED",
		"keyword": map[string]any{"text": text, "matchType": m}})
	return err
}

func (g GoogleAds) AddNegativeKeyword(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, adGroupID, text, matchType string) error {
	m, err := googleMatch(matchType)
	if err != nil {
		return err
	}
	if text, err = keywordText(text); err != nil {
		return err
	}
	kw := map[string]any{"text": text, "matchType": m}
	if adGroupID != "" {
		_, err = g.mutate(ctx, cl, creds, acct, "adGroupCriteria", map[string]any{"adGroup": g.resource(acct, "adGroups", adGroupID), "negative": true, "keyword": kw})
		return err
	}
	_, err = g.mutate(ctx, cl, creds, acct, "campaignCriteria", map[string]any{"campaign": g.resource(acct, "campaigns", campaignID), "negative": true, "keyword": kw})
	return err
}

func (g GoogleAds) SplitKeyword(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, fromAdGroupID, name, text string) (string, error) {
	text, err := keywordText(text)
	if err != nil {
		return "", err
	}
	// The ads to copy, read first: a group with none to copy would show nothing.
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], fmt.Sprintf(
		"SELECT ad_group_ad.ad.final_urls, ad_group_ad.ad.responsive_search_ad.headlines, ad_group_ad.ad.responsive_search_ad.descriptions, "+
			"ad_group_ad.ad.responsive_search_ad.path1, ad_group_ad.ad.responsive_search_ad.path2 FROM ad_group_ad "+
			"WHERE ad_group.id = %d AND ad_group_ad.status = 'ENABLED' AND ad_group_ad.ad.type = 'RESPONSIVE_SEARCH_AD'", atoi64(fromAdGroupID)))
	if err != nil {
		return "", err
	}
	var ads []map[string]any
	for _, r := range rows {
		rsa := r.AdGroupAd.Ad.ResponsiveSearchAd
		if len(rsa.Headlines) < 3 || len(rsa.Descriptions) < 2 || len(r.AdGroupAd.Ad.FinalURLs) == 0 {
			continue
		}
		var hs, ds []any
		for _, h := range rsa.Headlines {
			hs = append(hs, map[string]any{"text": h.Text})
		}
		for _, d := range rsa.Descriptions {
			ds = append(ds, map[string]any{"text": d.Text})
		}
		ad := map[string]any{"headlines": hs, "descriptions": ds}
		if rsa.Path1 != "" {
			ad["path1"] = rsa.Path1
		}
		if rsa.Path2 != "" {
			ad["path2"] = rsa.Path2
		}
		ads = append(ads, map[string]any{"finalUrls": r.AdGroupAd.Ad.FinalURLs, "responsiveSearchAd": ad})
	}
	if len(ads) == 0 {
		return "", c.Errf(c.ErrInvalid, "ad group %s has no active responsive search ad to copy into the new group", fromAdGroupID)
	}
	if len([]rune(name)) > 255 {
		name = string([]rune(name)[:255])
	}
	group, err := g.mutate(ctx, cl, creds, acct, "adGroups", map[string]any{"campaign": g.resource(acct, "campaigns", campaignID), "name": name,
		"status": "ENABLED", "type": "SEARCH_STANDARD"})
	if err != nil {
		return "", err
	}
	if group == "" {
		return "", c.Errf(c.ErrUnavailable, "Google Ads made the ad group but did not say its id")
	}
	for _, ad := range ads {
		if _, err := g.mutate(ctx, cl, creds, acct, "adGroupAds", map[string]any{"adGroup": group, "status": "ENABLED", "ad": ad}); err != nil {
			return "", err
		}
	}
	if _, err := g.mutate(ctx, cl, creds, acct, "adGroupCriteria", map[string]any{"adGroup": group, "status": "ENABLED",
		"keyword": map[string]any{"text": text, "matchType": "EXACT"}}); err != nil {
		return "", err
	}
	if err := g.AddNegativeKeyword(ctx, cl, creds, acct, campaignID, fromAdGroupID, text, "exact"); err != nil {
		return "", err
	}
	return group[strings.LastIndex(group, "/")+1:], nil
}

func (g GoogleAds) Recommendations(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdRecommendation, error) {
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT recommendation.resource_name, recommendation.type, recommendation.campaign, "+
			"recommendation.impact.base_metrics.clicks, recommendation.impact.base_metrics.conversions, recommendation.impact.base_metrics.cost_micros, "+
			"recommendation.impact.potential_metrics.clicks, recommendation.impact.potential_metrics.conversions, recommendation.impact.potential_metrics.cost_micros "+
			"FROM recommendation WHERE recommendation.dismissed = FALSE")
	if err != nil {
		return nil, err
	}
	out := make([]c.AdRecommendation, 0, len(rows))
	for _, r := range rows {
		rec := r.Recommendation
		if rec.ResourceName == "" {
			continue
		}
		x := c.AdRecommendation{ID: rec.ResourceName, Type: rec.Type}
		if i := strings.LastIndex(rec.Campaign, "/"); i >= 0 {
			x.CampaignID = rec.Campaign[i+1:]
		}
		b, p := rec.Impact.BaseMetrics, rec.Impact.PotentialMetrics
		if p.Clicks != 0 || p.Conversions != 0 || p.CostMicros != 0 {
			x.Impact = map[string]float64{"clicks": float64(b.Clicks), "conversions": float64(b.Conversions), "cost_micros": float64(b.CostMicros),
				"potential_clicks": float64(p.Clicks), "potential_conversions": float64(p.Conversions), "potential_cost_micros": float64(p.CostMicros)}
		}
		out = append(out, x)
	}
	return out, nil
}
