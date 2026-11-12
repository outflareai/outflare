package ads

import (
	"context"
	"fmt"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Click harvest (plans/paid-ads/plan.md, sync and alerts): Google's
// click_view has one row per click with its gclid and the ad group, ad,
// keyword and device it came from. Google answers one day per query and
// keeps 90 days, so adsync reads yesterday once a day and backfills the
// rest on an account's first sync. With it a landing page that carried
// only a gclid resolves to its ad group, ad and keyword.

// AdClicks lists an account's clicks on one day (the account's time zone).
func (g GoogleAds) AdClicks(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, day string) ([]c.AdClick, error) {
	d, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return nil, c.Errf(c.ErrInvalid, "day is YYYY-MM-DD")
	}
	q := fmt.Sprintf("SELECT click_view.gclid, click_view.ad_group_ad, click_view.keyword, click_view.keyword_info.text, click_view.keyword_info.match_type, "+
		"segments.device, segments.date, campaign.id, ad_group.id FROM click_view WHERE segments.date = '%s'", d.Format(time.DateOnly))
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], q)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdClick, 0, len(rows))
	for _, r := range rows {
		if r.ClickView.Gclid == "" {
			continue
		}
		k := c.AdClick{ClickID: r.ClickView.Gclid, Kind: "gclid", CampaignID: fmt.Sprint(int64(r.Campaign.ID)),
			AdGroupID: fmt.Sprint(int64(r.AdGroup.ID)), AdID: googleResourceTail(r.ClickView.AdGroupAd),
			Keyword: r.ClickView.KeywordInfo.Text, MatchType: strings.ToLower(r.ClickView.KeywordInfo.MatchType), Device: strings.ToLower(r.Segments.Device)}
		if k.CampaignID == "0" {
			k.CampaignID = ""
		}
		if k.AdGroupID == "0" {
			k.AdGroupID = ""
		}
		out = append(out, k)
	}
	return out, nil
}

// googleResourceTail is the last id of a resource name: the ad of
// customers/1/adGroupAds/71~81, the criterion of .../adGroupCriteria/71~91.
func googleResourceTail(name string) string {
	name = name[strings.LastIndex(name, "/")+1:]
	if i := strings.LastIndex(name, "~"); i >= 0 {
		name = name[i+1:]
	}
	return name
}
