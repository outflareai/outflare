package ads

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// LinkedInAds reads LinkedIn ad accounts through the Advertising API
// (Marketing API, versioned REST). LinkedIn grants Advertising API access
// per app, so it works once the sign-in app is approved for it.
type LinkedInAds struct{}

const (
	linkedInBase    = "https://api.linkedin.com/rest"
	linkedInVersion = "202609"
)

func (LinkedInAds) Definition() c.Definition {
	d := def("linkedin_ads", "LinkedIn Ads", "https://business.linkedin.com/marketing-solutions/ads",
		"https://learn.microsoft.com/linkedin/marketing/integrations/ads-reporting/ads-reporting", "",
		"Spend, impressions, clicks and conversions per campaign from your LinkedIn ad accounts.",
		"Signs in with LinkedIn and reads every ad account the member can see. Conversions are LinkedIn's website conversions as LinkedIn counts them. It changes a campaign only when you, or an agent within the grants you set, pause or resume it or set its daily budget (the rw_ads permission; a connection made before it was asked for is connected again to manage campaigns).")
	d.Capabilities = append(d.Capabilities, c.CapAdsManage)
	d.OAuth = &c.OAuth{
		AuthURL:  "https://www.linkedin.com/oauth/v2/authorization",
		TokenURL: "https://www.linkedin.com/oauth/v2/accessToken",
		Scopes:   []string{"r_ads", "r_ads_reporting", "rw_ads"},
	}
	return d
}

func (LinkedInAds) base(cl *c.Client) string { return cl.Base("linkedin_ads", linkedInBase) }

func (LinkedInAds) call(cl *c.Client, creds c.Creds) *call {
	return newCall(cl, "LinkedIn").bearer(creds["access_token"]).
		set("LinkedIn-Version", linkedInVersion).set("X-Restli-Protocol-Version", "2.0.0")
}

func (l LinkedInAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	accts, err := l.AdAccounts(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	return c.Account{Details: map[string]string{"accounts": fmt.Sprint(len(accts))}}, nil
}

const sponsoredAccount = "urn:li:sponsoredAccount:"

func (l LinkedInAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	var ids []string
	for start := 0; start < 100*maxPages; start += 100 {
		var resp struct {
			Elements []struct {
				Account string `json:"account"`
			} `json:"elements"`
		}
		if err := l.call(cl, creds).get(ctx, fmt.Sprintf("%s/adAccountUsers?q=authenticatedUser&start=%d&count=100", l.base(cl), start), &resp); err != nil {
			return nil, err
		}
		for _, e := range resp.Elements {
			if id := strings.TrimPrefix(e.Account, sponsoredAccount); id != "" {
				ids = append(ids, id)
			}
		}
		if len(resp.Elements) < 100 {
			break
		}
	}
	var out []c.AdAccount
	for _, id := range ids {
		var a struct {
			ID       flexInt `json:"id"`
			Name     string  `json:"name"`
			Currency string  `json:"currency"`
			Status   string  `json:"status"`
		}
		if err := l.call(cl, creds).get(ctx, l.base(cl)+"/adAccounts/"+esc(id), &a); err != nil {
			return nil, err
		}
		if a.Status == "CANCELED" || a.Status == "REMOVED" {
			continue
		}
		out = append(out, c.AdAccount{ID: id, Name: a.Name, Currency: a.Currency, TimeZone: "UTC"})
	}
	return out, nil
}

func (l LinkedInAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	var out []c.AdCampaign
	token := ""
	for i := 0; i < maxPages; i++ {
		u := l.base(cl) + "/adAccounts/" + esc(acct.ID) + "/adCampaigns?q=search&pageSize=1000"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		var resp struct {
			Elements []struct {
				ID          flexInt `json:"id"`
				Name        string  `json:"name"`
				Status      string  `json:"status"`
				Objective   string  `json:"objectiveType"`
				DailyBudget *struct {
					Amount string `json:"amount"`
				} `json:"dailyBudget"`
			} `json:"elements"`
			Metadata struct {
				NextPageToken string `json:"nextPageToken"`
			} `json:"metadata"`
		}
		if err := l.call(cl, creds).get(ctx, u, &resp); err != nil {
			return nil, err
		}
		for _, e := range resp.Elements {
			cp := c.AdCampaign{ID: fmt.Sprint(int64(e.ID)), Name: e.Name, Status: status(e.Status), ProviderStatus: e.Status,
				Objective: strings.ToLower(e.Objective)}
			if e.DailyBudget != nil {
				if b := c.MicrosOf(e.DailyBudget.Amount); b > 0 {
					cp.DailyBudgetMicros = ptrI(b)
				}
			}
			out = append(out, cp)
		}
		if token = resp.Metadata.NextPageToken; token == "" {
			break
		}
	}
	return out, nil
}

// ---- managing campaigns ----

func (l LinkedInAds) AdCampaign(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) (c.AdCampaign, error) {
	if atoi64(campaignID) <= 0 {
		return c.AdCampaign{}, c.Errf(c.ErrInvalid, "LinkedIn campaign ids are numbers")
	}
	var e struct {
		ID          flexInt `json:"id"`
		Name        string  `json:"name"`
		Status      string  `json:"status"`
		Objective   string  `json:"objectiveType"`
		DailyBudget *struct {
			Amount string `json:"amount"`
		} `json:"dailyBudget"`
	}
	if err := l.call(cl, creds).get(ctx, l.base(cl)+"/adAccounts/"+esc(acct.ID)+"/adCampaigns/"+esc(campaignID), &e); err != nil {
		return c.AdCampaign{}, err
	}
	if fmt.Sprint(int64(e.ID)) != campaignID {
		return c.AdCampaign{}, c.Errf(c.ErrInvalid, "LinkedIn has no campaign %s in account %s", campaignID, acct.ID)
	}
	cp := c.AdCampaign{ID: campaignID, Name: e.Name, Status: status(e.Status), ProviderStatus: e.Status, Objective: strings.ToLower(e.Objective)}
	if e.DailyBudget != nil {
		if b := c.MicrosOf(e.DailyBudget.Amount); b > 0 {
			cp.DailyBudgetMicros = ptrI(b)
		}
	}
	return cp, nil
}

// liPatch sends a Rest.li partial update that sets the fields given.
func (l LinkedInAds) liPatch(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, set map[string]any) error {
	return l.call(cl, creds).set("X-RestLi-Method", "PARTIAL_UPDATE").post(ctx, l.base(cl)+"/adAccounts/"+esc(acct.ID)+"/adCampaigns/"+esc(campaignID),
		map[string]any{"patch": map[string]any{"$set": set}}, nil)
}

func (l LinkedInAds) SetCampaignStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, active bool) error {
	if atoi64(campaignID) <= 0 {
		return c.Errf(c.ErrInvalid, "LinkedIn campaign ids are numbers")
	}
	st := "PAUSED"
	if active {
		st = "ACTIVE"
	}
	return l.liPatch(ctx, cl, creds, acct, campaignID, map[string]any{"status": st})
}

// SetCampaignDailyBudget sets the daily budget in the account's currency,
// as a decimal string with two places.
func (l LinkedInAds) SetCampaignDailyBudget(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, micros int64) error {
	if atoi64(campaignID) <= 0 {
		return c.Errf(c.ErrInvalid, "LinkedIn campaign ids are numbers")
	}
	if micros <= 0 {
		return c.Errf(c.ErrInvalid, "the daily budget must be more than nothing")
	}
	cents := (micros + 5_000) / 10_000
	return l.liPatch(ctx, cl, creds, acct, campaignID, map[string]any{"dailyBudget": map[string]any{"amount": fmt.Sprintf("%d.%02d", cents/100, cents%100), "currencyCode": acct.Currency}})
}

func liDate(s string) string {
	t := mustDate(s)
	return fmt.Sprintf("(year:%d,month:%d,day:%d)", t.Year(), int(t.Month()), t.Day())
}

func (l LinkedInAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	// Rest.li wants its own syntax left as is; only the URN is escaped.
	q := "q=analytics&pivot=CAMPAIGN&timeGranularity=DAILY" +
		"&dateRange=(start:" + liDate(from) + ",end:" + liDate(to) + ")" +
		"&accounts=List(" + url.QueryEscape(sponsoredAccount+acct.ID) + ")" +
		"&fields=dateRange,pivotValues,costInLocalCurrency,impressions,clicks,externalWebsiteConversions,conversionValueInLocalCurrency"
	var resp struct {
		Elements []struct {
			PivotValues []string `json:"pivotValues"`
			DateRange   struct {
				Start struct {
					Year  int `json:"year"`
					Month int `json:"month"`
					Day   int `json:"day"`
				} `json:"start"`
			} `json:"dateRange"`
			Cost        string  `json:"costInLocalCurrency"`
			Impressions flexInt `json:"impressions"`
			Clicks      flexInt `json:"clicks"`
			Conversions flexNum `json:"externalWebsiteConversions"`
			Value       string  `json:"conversionValueInLocalCurrency"`
		} `json:"elements"`
	}
	if err := l.call(cl, creds).get(ctx, l.base(cl)+"/adAnalytics?"+q, &resp); err != nil {
		return nil, err
	}
	out := make([]c.AdDay, 0, len(resp.Elements))
	for _, e := range resp.Elements {
		if len(e.PivotValues) == 0 {
			continue
		}
		id := e.PivotValues[0][strings.LastIndexByte(e.PivotValues[0], ':')+1:]
		s := e.DateRange.Start
		out = append(out, c.AdDay{CampaignID: id, Date: fmt.Sprintf("%04d-%02d-%02d", s.Year, s.Month, s.Day),
			SpendMicros: c.MicrosOf(e.Cost), Impressions: int64(e.Impressions), Clicks: int64(e.Clicks),
			Conversions: ptrF(float64(e.Conversions)), ConversionValueMicros: ptrI(c.MicrosOf(e.Value))})
	}
	return out, nil
}
