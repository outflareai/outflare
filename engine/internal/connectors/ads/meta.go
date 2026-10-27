package ads

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// MetaAds reads Facebook and Instagram ad accounts through the Marketing
// API. It connects with a system user access token the workspace creates
// in its own Meta Business Settings, so no Meta app review stands in the
// way, or with Facebook sign-in once a sign-in app is set up.
type MetaAds struct{}

const (
	metaBase    = "https://graph.facebook.com"
	metaVersion = "v24.0"
)

// metaConversions are Meta's standard conversion actions, counted as
// conversions. Everything else (link clicks, video views) is engagement.
var metaConversions = map[string]bool{
	"purchase": true, "omni_purchase": true, "lead": true, "complete_registration": true,
	"start_trial": true, "subscribe": true, "offsite_conversion.fb_pixel_purchase": true,
	"offsite_conversion.fb_pixel_lead": true, "offsite_conversion.fb_pixel_complete_registration": true,
}

func (MetaAds) Definition() c.Definition {
	d := def("meta_ads", "Meta Ads", "https://www.facebook.com/business/ads",
		"https://developers.facebook.com/docs/marketing-api/insights", "https://www.facebook.com/business/help/755172054692222",
		"Spend, clicks and conversions per campaign from your Facebook and Instagram ad accounts.",
		"Reads every ad account a token can see, with read-only access. The simplest way: in your Meta Business Settings, Users, System users, create a system user, give it the ad accounts, and generate a token with the ads_read permission. Conversions are Meta's standard events (purchases, leads, registrations, trials, subscriptions).")
	d.Auth, d.AuthMethods = c.AuthAPIKey, []string{c.AuthAPIKey, c.AuthOAuth2}
	d.Fields = []c.Field{{Name: "api_key", Label: "System user access token", Secret: true, Placeholder: "EAAB…",
		Help: "Business Settings, Users, System users: generate a token with ads_read for the ad accounts you want to read."}}
	d.OAuth = &c.OAuth{
		AuthURL:  "https://www.facebook.com/" + metaVersion + "/dialog/oauth",
		TokenURL: "https://graph.facebook.com/" + metaVersion + "/oauth/access_token",
		Scopes:   []string{"ads_read", "business_management"},
	}
	return d
}

func (m MetaAds) base(cl *c.Client) string { return cl.Base("meta_ads", metaBase) + "/" + metaVersion }

func (MetaAds) call(cl *c.Client, creds c.Creds) *call {
	return newCall(cl, "Meta").bearer(c.Token(creds))
}

func (m MetaAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var me struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := m.call(cl, creds).get(ctx, m.base(cl)+"/me?fields=id,name", &me); err != nil {
		return c.Account{}, err
	}
	accts, err := m.AdAccounts(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: me.ID, Name: me.Name, Details: map[string]string{"accounts": fmt.Sprint(len(accts))}}, nil
}

// pages follows a Graph API list through paging.next.
func (m MetaAds) pages(ctx context.Context, cl *c.Client, creds c.Creds, first string, each func(json.RawMessage) error) error {
	next := first
	for i := 0; i < maxPages && next != ""; i++ {
		var resp struct {
			Data   []json.RawMessage `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := m.call(cl, creds).get(ctx, next, &resp); err != nil {
			return err
		}
		for _, d := range resp.Data {
			if err := each(d); err != nil {
				return err
			}
		}
		next = ""
		// Only follow pages on the same host, so a reply can't send the
		// token somewhere else.
		if u, err := url.Parse(resp.Paging.Next); err == nil && resp.Paging.Next != "" {
			if b, _ := url.Parse(m.base(cl)); b != nil && u.Host == b.Host {
				next = resp.Paging.Next
			}
		}
	}
	return nil
}

func (m MetaAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	var out []c.AdAccount
	err := m.pages(ctx, cl, creds, m.base(cl)+"/me/adaccounts?fields=account_id,name,currency,timezone_name,account_status&limit=100", func(raw json.RawMessage) error {
		var a struct {
			AccountID    string `json:"account_id"`
			Name         string `json:"name"`
			Currency     string `json:"currency"`
			TimezoneName string `json:"timezone_name"`
		}
		if json.Unmarshal(raw, &a) == nil && a.AccountID != "" {
			out = append(out, c.AdAccount{ID: a.AccountID, Name: a.Name, Currency: a.Currency, TimeZone: a.TimezoneName})
		}
		return nil
	})
	return out, err
}

func (m MetaAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	var out []c.AdCampaign
	u := m.base(cl) + "/act_" + esc(acct.ID) + "/campaigns?fields=id,name,effective_status,objective,daily_budget&limit=200"
	err := m.pages(ctx, cl, creds, u, func(raw json.RawMessage) error {
		var cp struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			EffectiveStatus string `json:"effective_status"`
			Objective       string `json:"objective"`
			DailyBudget     string `json:"daily_budget"` // in the currency's smallest unit
		}
		if json.Unmarshal(raw, &cp) != nil || cp.ID == "" {
			return nil
		}
		x := c.AdCampaign{ID: cp.ID, Name: cp.Name, Status: status(cp.EffectiveStatus), ProviderStatus: cp.EffectiveStatus,
			Objective: strings.ToLower(strings.TrimPrefix(cp.Objective, "OUTCOME_"))}
		if b := atoi64(cp.DailyBudget); b > 0 {
			x.DailyBudgetMicros = ptrI(b * 1e6 / minorUnits(acct.Currency))
		}
		out = append(out, x)
		return nil
	})
	return out, err
}

type metaAction struct {
	ActionType string  `json:"action_type"`
	Value      flexNum `json:"value"`
}

func (m MetaAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	tr, _ := json.Marshal(map[string]string{"since": from, "until": to})
	q := url.Values{
		"level":          {"campaign"},
		"time_increment": {"1"},
		"time_range":     {string(tr)},
		"fields":         {"campaign_id,campaign_name,spend,impressions,clicks,actions,action_values"},
		"limit":          {"500"},
	}
	var out []c.AdDay
	err := m.pages(ctx, cl, creds, m.base(cl)+"/act_"+esc(acct.ID)+"/insights?"+q.Encode(), func(raw json.RawMessage) error {
		var r struct {
			CampaignID   string       `json:"campaign_id"`
			CampaignName string       `json:"campaign_name"`
			DateStart    string       `json:"date_start"`
			Spend        string       `json:"spend"`
			Impressions  flexInt      `json:"impressions"`
			Clicks       flexInt      `json:"clicks"`
			Actions      []metaAction `json:"actions"`
			ActionValues []metaAction `json:"action_values"`
		}
		if json.Unmarshal(raw, &r) != nil || r.CampaignID == "" {
			return nil
		}
		var conv, value float64
		for _, a := range r.Actions {
			if metaConversions[a.ActionType] {
				conv += float64(a.Value)
			}
		}
		for _, a := range r.ActionValues {
			if metaConversions[a.ActionType] {
				value += float64(a.Value)
			}
		}
		out = append(out, c.AdDay{CampaignID: r.CampaignID, CampaignName: r.CampaignName, Date: r.DateStart,
			SpendMicros: c.MicrosOf(r.Spend), Impressions: int64(r.Impressions), Clicks: int64(r.Clicks),
			Conversions: ptrF(conv), ConversionValueMicros: ptrI(c.Micros(value))})
		return nil
	})
	return out, err
}

// minorUnits is how many of a currency's smallest unit make one: 100 for
// most, 1 for currencies without cents.
func minorUnits(currency string) int64 {
	switch strings.ToUpper(currency) {
	case "JPY", "KRW", "VND", "CLP", "ISK", "HUF", "TWD", "COP", "IDR", "PYG", "UGX":
		return 1
	}
	return 100
}
