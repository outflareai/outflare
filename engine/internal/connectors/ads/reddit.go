package ads

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// RedditAds reads Reddit ad accounts through the Reddit Ads API (v3). It
// is its own connection, separate from organic Reddit, because the Ads API
// signs in through ads.reddit.com with its own permissions.
type RedditAds struct{}

const redditAdsBase = "https://ads-api.reddit.com/api/v3"

func (RedditAds) Definition() c.Definition {
	d := def("reddit_ads", "Reddit Ads", "https://ads.reddit.com",
		"https://ads-api.reddit.com/docs/v3/", "",
		"Spend, impressions and clicks per campaign from your Reddit ad accounts.",
		"Signs in with Reddit and reads every ad account of every business the account belongs to, with read-only access. Separate from organic Reddit, which posts and listens from your brand's own account.")
	d.OAuth = &c.OAuth{
		AuthURL:   "https://www.reddit.com/api/v1/authorize",
		TokenURL:  "https://www.reddit.com/api/v1/access_token",
		Scopes:    []string{"adsread"},
		Params:    map[string]string{"duration": "permanent"},
		BasicAuth: true,
	}
	return d
}

func (RedditAds) base(cl *c.Client) string { return cl.Base("reddit_ads", redditAdsBase) }

func (RedditAds) call(cl *c.Client, creds c.Creds) *call {
	return newCall(cl, "Reddit Ads").bearer(creds["access_token"])
}

func (r RedditAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var me struct {
		Data struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := r.call(cl, creds).get(ctx, r.base(cl)+"/me", &me); err != nil {
		return c.Account{}, err
	}
	accts, err := r.AdAccounts(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: me.Data.ID, Name: me.Data.Username, Details: map[string]string{"accounts": fmt.Sprint(len(accts))}}, nil
}

// list follows a v3 list through pagination.next_url on the same host.
func (r RedditAds) list(ctx context.Context, cl *c.Client, creds c.Creds, method, first string, body any, each func(json.RawMessage) error) error {
	next := first
	b, _ := url.Parse(r.base(cl))
	for i := 0; i < maxPages && next != ""; i++ {
		var resp struct {
			Data       json.RawMessage `json:"data"`
			Pagination struct {
				NextURL string `json:"next_url"`
			} `json:"pagination"`
		}
		var err error
		if method == "POST" {
			err = r.call(cl, creds).post(ctx, next, body, &resp)
		} else {
			err = r.call(cl, creds).get(ctx, next, &resp)
		}
		if err != nil {
			return err
		}
		if err := each(resp.Data); err != nil {
			return err
		}
		next = ""
		if u, err := url.Parse(resp.Pagination.NextURL); err == nil && resp.Pagination.NextURL != "" && b != nil && u.Host == b.Host {
			next = resp.Pagination.NextURL
		}
	}
	return nil
}

func (r RedditAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	var businesses []string
	err := r.list(ctx, cl, creds, "GET", r.base(cl)+"/me/businesses", nil, func(raw json.RawMessage) error {
		var list []struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &list)
		for _, b := range list {
			businesses = append(businesses, b.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []c.AdAccount
	for _, biz := range businesses {
		err := r.list(ctx, cl, creds, "GET", r.base(cl)+"/businesses/"+esc(biz)+"/ad_accounts", nil, func(raw json.RawMessage) error {
			var list []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				Currency   string `json:"currency"`
				TimeZoneID string `json:"time_zone_id"`
			}
			_ = json.Unmarshal(raw, &list)
			for _, a := range list {
				if a.ID != "" && !seen[a.ID] {
					seen[a.ID] = true
					out = append(out, c.AdAccount{ID: a.ID, Name: a.Name, Currency: strings.ToUpper(a.Currency), TimeZone: a.TimeZoneID,
						Meta: map[string]string{"business_id": biz}})
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r RedditAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	var out []c.AdCampaign
	err := r.list(ctx, cl, creds, "GET", r.base(cl)+"/ad_accounts/"+esc(acct.ID)+"/campaigns", nil, func(raw json.RawMessage) error {
		var list []struct {
			ID               string `json:"id"`
			Name             string `json:"name"`
			EffectiveStatus  string `json:"effective_status"`
			ConfiguredStatus string `json:"configured_status"`
			Objective        string `json:"objective"`
		}
		_ = json.Unmarshal(raw, &list)
		for _, cp := range list {
			st := cp.EffectiveStatus
			if st == "" {
				st = cp.ConfiguredStatus
			}
			out = append(out, c.AdCampaign{ID: cp.ID, Name: cp.Name, Status: status(st), ProviderStatus: st, Objective: strings.ToLower(cp.Objective)})
		}
		return nil
	})
	return out, err
}

func (r RedditAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	end := mustDate(to).AddDate(0, 0, 1)
	body := map[string]any{"data": map[string]any{
		"breakdowns":   []string{"DATE", "CAMPAIGN_ID"},
		"fields":       []string{"SPEND", "IMPRESSIONS", "CLICKS"},
		"starts_at":    from + "T00:00:00Z",
		"ends_at":      end.Format("2006-01-02") + "T00:00:00Z",
		"time_zone_id": acct.TimeZone,
	}}
	var out []c.AdDay
	err := r.list(ctx, cl, creds, "POST", r.base(cl)+"/ad_accounts/"+esc(acct.ID)+"/reports", body, func(raw json.RawMessage) error {
		var data struct {
			Metrics []struct {
				CampaignID  string  `json:"campaign_id"`
				Date        string  `json:"date"`
				Spend       flexInt `json:"spend"` // micros
				Impressions flexInt `json:"impressions"`
				Clicks      flexInt `json:"clicks"`
			} `json:"metrics"`
		}
		_ = json.Unmarshal(raw, &data)
		for _, m := range data.Metrics {
			if m.CampaignID == "" || len(m.Date) < 10 {
				continue
			}
			out = append(out, c.AdDay{CampaignID: m.CampaignID, Date: m.Date[:10], SpendMicros: int64(m.Spend),
				Impressions: int64(m.Impressions), Clicks: int64(m.Clicks)})
		}
		return nil
	})
	return out, err
}
