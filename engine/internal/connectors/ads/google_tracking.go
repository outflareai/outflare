package ads

import (
	"context"
	"fmt"

	"github.com/outflareai/outflare/engine/internal/adtrack"
	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Google Ads keeps a Final URL suffix on the account, each campaign, each
// ad group and each ad, and an ad uses the lowest one that is set. So the
// template on the account alone isn't enough when a campaign has a suffix
// of its own: that one replaces it. TrackingPlan adds the template to the
// account's suffix and to every lower suffix that is set without it,
// keeping the parameters each already has (adtrack.AddTemplate).

// trackingChunk is how many updates go in one mutate request.
const trackingChunk = 200

func (g GoogleAds) TrackingPlan(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, template string) ([]c.TrackingChange, error) {
	if template == "" {
		return nil, c.Errf(c.ErrInvalid, "there is no tracking template to add")
	}
	login := acct.Meta["login_customer_id"]
	var out []c.TrackingChange
	add := func(level, id, name, suffix string, always bool) {
		if adtrack.HasTemplate(suffix) || (suffix == "" && !always) {
			return
		}
		out = append(out, c.TrackingChange{Level: level, ID: id, Name: name, Before: suffix, After: adtrack.AddTemplate(suffix, template)})
	}
	rows, err := g.search(ctx, cl, creds, acct.ID, login, "SELECT customer.id, customer.descriptive_name, customer.final_url_suffix FROM customer")
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		// The account's suffix is what new campaigns get, so it carries
		// the template even when it is empty now.
		add(c.TrackingAccount, acct.ID, firstText(r.Customer.DescriptiveName, acct.Name), r.Customer.FinalURLSuffix, true)
	}
	rows, err = g.search(ctx, cl, creds, acct.ID, login, "SELECT campaign.id, campaign.name, campaign.final_url_suffix FROM campaign WHERE campaign.status != 'REMOVED'")
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		add(c.TrackingCampaign, fmt.Sprint(int64(r.Campaign.ID)), r.Campaign.Name, r.Campaign.FinalURLSuffix, false)
	}
	rows, err = g.search(ctx, cl, creds, acct.ID, login, "SELECT ad_group.id, ad_group.name, campaign.name, ad_group.final_url_suffix FROM ad_group WHERE ad_group.status != 'REMOVED' AND campaign.status != 'REMOVED'")
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		add(c.TrackingAdGroup, fmt.Sprint(int64(r.AdGroup.ID)), r.AdGroup.Name+" · "+r.Campaign.Name, r.AdGroup.FinalURLSuffix, false)
	}
	rows, err = g.search(ctx, cl, creds, acct.ID, login, "SELECT ad_group_ad.ad.id, ad_group_ad.ad.name, ad_group_ad.ad.responsive_search_ad.headlines, ad_group.name, ad_group_ad.ad.final_url_suffix "+
		"FROM ad_group_ad WHERE ad_group_ad.status != 'REMOVED' AND ad_group.status != 'REMOVED' AND campaign.status != 'REMOVED'")
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		ad := r.AdGroupAd.Ad
		name := ad.Name
		if name == "" && len(ad.ResponsiveSearchAd.Headlines) > 0 {
			name = ad.ResponsiveSearchAd.Headlines[0].Text
		}
		add(c.TrackingAd, fmt.Sprint(int64(ad.ID)), firstText(name, "Ad "+fmt.Sprint(int64(ad.ID)))+" · "+r.AdGroup.Name, ad.FinalURLSuffix, false)
	}
	return out, nil
}

// trackingResource is where each level's updates go, and how a resource
// of it is named.
var trackingResource = map[string]string{
	c.TrackingCampaign: "campaigns",
	c.TrackingAdGroup:  "adGroups",
	c.TrackingAd:       "ads",
}

func (g GoogleAds) ApplyTracking(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, changes []c.TrackingChange) ([]c.TrackingChange, error) {
	login := acct.Meta["login_customer_id"]
	base := g.base(cl) + "/customers/" + esc(acct.ID)
	out := make([]c.TrackingChange, len(changes))
	copy(out, changes)
	mutate := func(path string, ops []any) error {
		return g.call(cl, creds, login).post(ctx, base+path, map[string]any{"operations": ops}, nil)
	}
	byLevel := map[string][]int{}
	for i, ch := range out {
		if ch.Level == c.TrackingAccount {
			op := map[string]any{"update": map[string]any{"resourceName": "customers/" + acct.ID, "finalUrlSuffix": ch.After}, "updateMask": "final_url_suffix"}
			if err := g.call(cl, creds, login).post(ctx, base+":mutate", map[string]any{"operation": op}, nil); err != nil {
				if c.KindOf(err) == c.ErrUnavailable || c.KindOf(err) == c.ErrAuth {
					return nil, err
				}
				out[i].Error = trimErr(err)
			}
			continue
		}
		if _, ok := trackingResource[ch.Level]; !ok {
			out[i].Error = "unknown level " + ch.Level
			continue
		}
		byLevel[ch.Level] = append(byLevel[ch.Level], i)
	}
	op := func(ch c.TrackingChange) any {
		return map[string]any{"update": map[string]any{"resourceName": "customers/" + acct.ID + "/" + trackingResource[ch.Level] + "/" + ch.ID, "finalUrlSuffix": ch.After},
			"updateMask": "final_url_suffix"}
	}
	for _, level := range []string{c.TrackingCampaign, c.TrackingAdGroup, c.TrackingAd} {
		idx := byLevel[level]
		path := "/" + trackingResource[level] + ":mutate"
		for start := 0; start < len(idx); start += trackingChunk {
			chunk := idx[start:min(start+trackingChunk, len(idx))]
			ops := make([]any, len(chunk))
			for k, i := range chunk {
				ops[k] = op(out[i])
			}
			err := mutate(path, ops)
			if err == nil {
				continue
			}
			if k := c.KindOf(err); k == c.ErrUnavailable || k == c.ErrAuth {
				return nil, err
			}
			// Google refuses a whole request for one bad update; try them
			// one by one so the others still go through.
			for _, i := range chunk {
				if err := mutate(path, []any{op(out[i])}); err != nil {
					out[i].Error = trimErr(err)
				}
			}
		}
	}
	return out, nil
}

func trimErr(err error) string {
	s := err.Error()
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
