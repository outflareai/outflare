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
	rows, err := l.analytics(ctx, cl, creds, acct, "CAMPAIGN", from, to)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdDay, 0, len(rows))
	for _, r := range rows {
		out = append(out, c.AdDay{CampaignID: r.id, Date: r.date, SpendMicros: r.spend, Impressions: r.impressions, Clicks: r.clicks,
			Conversions: ptrF(r.conversions), ConversionValueMicros: ptrI(r.value)})
	}
	return out, nil
}

// liDay is one campaign's or creative's numbers for a day.
type liDay struct {
	id, date                          string
	spend, impressions, clicks, value int64
	conversions                       float64
}

// analytics reads the account's numbers by day and pivot (CAMPAIGN or
// CREATIVE).
func (l LinkedInAds) analytics(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, pivot, from, to string) ([]liDay, error) {
	// Rest.li wants its own syntax left as is; only the URN is escaped.
	q := "q=analytics&pivot=" + pivot + "&timeGranularity=DAILY" +
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
	out := make([]liDay, 0, len(resp.Elements))
	for _, e := range resp.Elements {
		if len(e.PivotValues) == 0 {
			continue
		}
		s := e.DateRange.Start
		out = append(out, liDay{id: liID(e.PivotValues[0]), date: fmt.Sprintf("%04d-%02d-%02d", s.Year, s.Month, s.Day),
			spend: c.MicrosOf(e.Cost), impressions: int64(e.Impressions), clicks: int64(e.Clicks),
			conversions: float64(e.Conversions), value: c.MicrosOf(e.Value)})
	}
	return out, nil
}

// liID is the id at the end of a URN.
func liID(urn string) string { return urn[strings.LastIndexByte(urn, ':')+1:] }

// ---- ads (creatives) ----

var _ c.AdCampaignAdsReader = LinkedInAds{}

// liCreativeDays is how many days one analytics call by creative covers:
// LinkedIn returns at most 15,000 rows a call, with no next page.
const liCreativeDays = 31

// CampaignAds reads the account's ads. What Outflare calls a LinkedIn
// campaign holds its ads (LinkedIn's creatives) directly, so there is no
// ad group between. A creative carries its name, status and review; what
// it shows is on the post it sponsors, which isn't read.
func (l LinkedInAds) CampaignAds(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdItem, []c.AdItemDay, error) {
	var items []c.AdItem
	parent := map[string]string{}
	names := map[string]string{}
	token := ""
	for i := 0; i < maxPages; i++ {
		u := l.base(cl) + "/adAccounts/" + esc(acct.ID) + "/creatives?q=criteria&pageSize=100"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		var resp struct {
			Elements []struct {
				ID             string `json:"id"`
				Campaign       string `json:"campaign"`
				Name           string `json:"name"`
				IntendedStatus string `json:"intendedStatus"`
				Review         *struct {
					Status string `json:"status"`
				} `json:"review"`
			} `json:"elements"`
			Metadata struct {
				NextPageToken string `json:"nextPageToken"`
			} `json:"metadata"`
		}
		if err := l.call(cl, creds).set("X-RestLi-Method", "FINDER").get(ctx, u, &resp); err != nil {
			return nil, nil, err
		}
		for _, e := range resp.Elements {
			id, camp := liID(e.ID), liID(e.Campaign)
			if id == "" || camp == "" {
				continue
			}
			it := c.AdItem{ID: id, ParentID: camp, Name: e.Name, Status: status(e.IntendedStatus), ProviderStatus: e.IntendedStatus}
			if e.Review != nil {
				policy := map[string]string{"APPROVED": "approved", "REJECTED": "disapproved", "PENDING": "under_review", "NEEDS_REVIEW": "under_review"}[e.Review.Status]
				it.Details = adDetails(map[string]string{c.AdDetailPolicy: policy})
			}
			items = append(items, it)
			parent[id], names[id] = camp, e.Name
		}
		if token = resp.Metadata.NextPageToken; token == "" {
			break
		}
	}
	var out []c.AdItemDay
	all := days(from, to)
	for start := 0; start < len(all); start += liCreativeDays {
		window := all[start:min(start+liCreativeDays, len(all))]
		rows, err := l.analytics(ctx, cl, creds, acct, "CREATIVE", window[0], window[len(window)-1])
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			// A creative the list doesn't have has no campaign to go under.
			camp, ok := parent[r.id]
			if !ok || r.spend == 0 && r.impressions == 0 && r.clicks == 0 {
				continue
			}
			out = append(out, c.AdItemDay{ID: r.id, ParentID: camp, Name: names[r.id], Date: r.date, SpendMicros: r.spend, Impressions: r.impressions,
				Clicks: r.clicks, Conversions: ptrF(r.conversions), ConversionValueMicros: ptrI(r.value)})
		}
	}
	return items, out, nil
}
