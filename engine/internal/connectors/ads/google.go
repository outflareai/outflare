package ads

import (
	"context"
	"fmt"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// GoogleAds reads Google Ads accounts through the Google Ads API. Every
// call needs a developer token as well as the sign-in; the workspace adds
// its own, from its own Google Ads manager account.
type GoogleAds struct{}

const (
	googleAdsBase    = "https://googleads.googleapis.com"
	googleAdsVersion = "v23"
	scopeAdWords     = "https://www.googleapis.com/auth/adwords"
)

func (GoogleAds) Definition() c.Definition {
	d := def("google_ads", "Google Ads", "https://ads.google.com",
		"https://developers.google.com/google-ads/api/docs/start", "https://developers.google.com/google-ads/api/docs/get-started/dev-token",
		"Spend, clicks and conversions per campaign from your Google Ads accounts.",
		"Signs in with Google and reads every Google Ads account the sign-in can see, including accounts under a manager account. Google also asks for a developer token: create one in your own Google Ads manager account (Admin, API Center) and add it after signing in. Read-only: nothing changes in your accounts.")
	d.OAuth = &c.OAuth{
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		Scopes:   []string{scopeAdWords, "openid", "email"},
		Params:   map[string]string{"access_type": "offline", "prompt": "consent"},
	}
	return d
}

func (GoogleAds) SetupFields() []c.Field {
	return []c.Field{{Name: "developer_token", Label: "Developer token", Secret: true,
		Help: "From your own Google Ads manager account: Admin, API Center. Basic access is enough."}}
}

func (g GoogleAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	acct := c.Account{Details: map[string]string{}}
	if creds["developer_token"] == "" {
		acct.Details["setup"] = "add your developer token"
		return acct, nil
	}
	ids, err := g.accessible(ctx, cl, creds)
	if err != nil {
		return acct, err
	}
	acct.Details["accounts"] = fmt.Sprint(len(ids))
	return acct, nil
}

func (GoogleAds) base(cl *c.Client) string {
	return cl.Base("google_ads", googleAdsBase) + "/" + googleAdsVersion
}

func (GoogleAds) call(cl *c.Client, creds c.Creds, loginID string) *call {
	return newCall(cl, "Google Ads").bearer(creds["access_token"]).
		set("developer-token", creds["developer_token"]).set("login-customer-id", loginID)
}

func (g GoogleAds) accessible(ctx context.Context, cl *c.Client, creds c.Creds) ([]string, error) {
	var resp struct {
		ResourceNames []string `json:"resourceNames"`
	}
	if err := g.call(cl, creds, "").get(ctx, g.base(cl)+"/customers:listAccessibleCustomers", &resp); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.ResourceNames))
	for _, n := range resp.ResourceNames {
		out = append(out, strings.TrimPrefix(n, "customers/"))
	}
	return out, nil
}

type gaqlRow struct {
	Customer struct {
		ID              flexInt `json:"id"`
		DescriptiveName string  `json:"descriptiveName"`
		CurrencyCode    string  `json:"currencyCode"`
		TimeZone        string  `json:"timeZone"`
		Manager         bool    `json:"manager"`
	} `json:"customer"`
	CustomerClient struct {
		ID              flexInt `json:"id"`
		DescriptiveName string  `json:"descriptiveName"`
		CurrencyCode    string  `json:"currencyCode"`
		TimeZone        string  `json:"timeZone"`
		Manager         bool    `json:"manager"`
		Status          string  `json:"status"`
	} `json:"customerClient"`
	Campaign struct {
		ID                     flexInt `json:"id"`
		Name                   string  `json:"name"`
		Status                 string  `json:"status"`
		AdvertisingChannelType string  `json:"advertisingChannelType"`
	} `json:"campaign"`
	CampaignBudget struct {
		AmountMicros flexInt `json:"amountMicros"`
	} `json:"campaignBudget"`
	Segments struct {
		Date string `json:"date"`
	} `json:"segments"`
	Metrics struct {
		CostMicros       flexInt `json:"costMicros"`
		Impressions      flexInt `json:"impressions"`
		Clicks           flexInt `json:"clicks"`
		Conversions      flexNum `json:"conversions"`
		ConversionsValue flexNum `json:"conversionsValue"`
	} `json:"metrics"`
}

// search runs a GAQL query against one customer, every page.
func (g GoogleAds) search(ctx context.Context, cl *c.Client, creds c.Creds, customer, loginID, query string) ([]gaqlRow, error) {
	var out []gaqlRow
	token := ""
	for i := 0; i < maxPages; i++ {
		body := map[string]any{"query": query}
		if token != "" {
			body["pageToken"] = token
		}
		var resp struct {
			Results       []gaqlRow `json:"results"`
			NextPageToken string    `json:"nextPageToken"`
		}
		if err := g.call(cl, creds, loginID).post(ctx, g.base(cl)+"/customers/"+esc(customer)+"/googleAds:search", body, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Results...)
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	return out, nil
}

func (g GoogleAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	if creds["developer_token"] == "" {
		return nil, c.Errf(c.ErrAuth, "Google Ads needs your developer token: add it on the connection")
	}
	ids, err := g.accessible(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []c.AdAccount
	for _, id := range ids {
		rows, err := g.search(ctx, cl, creds, id, "", "SELECT customer.id, customer.descriptive_name, customer.currency_code, customer.time_zone, customer.manager FROM customer")
		if err != nil {
			// A suspended or cancelled account refuses queries; the others
			// still sync.
			if c.KindOf(err) == c.ErrInvalid || c.KindOf(err) == c.ErrAuth {
				continue
			}
			return nil, err
		}
		if len(rows) == 0 {
			continue
		}
		cu := rows[0].Customer
		if !cu.Manager {
			if !seen[id] {
				seen[id] = true
				out = append(out, c.AdAccount{ID: id, Name: cu.DescriptiveName, Currency: cu.CurrencyCode, TimeZone: cu.TimeZone})
			}
			continue
		}
		// A manager account: read the accounts under it through it.
		clients, err := g.search(ctx, cl, creds, id, id, "SELECT customer_client.id, customer_client.descriptive_name, customer_client.currency_code, customer_client.time_zone, customer_client.manager, customer_client.status FROM customer_client WHERE customer_client.manager = FALSE")
		if err != nil {
			return nil, err
		}
		for _, r := range clients {
			cc := r.CustomerClient
			cid := fmt.Sprint(int64(cc.ID))
			if seen[cid] || cc.Status == "CANCELED" || cc.Status == "CLOSED" {
				continue
			}
			seen[cid] = true
			out = append(out, c.AdAccount{ID: cid, Name: cc.DescriptiveName, Currency: cc.CurrencyCode, TimeZone: cc.TimeZone,
				Meta: map[string]string{"login_customer_id": id}})
		}
	}
	return out, nil
}

func (g GoogleAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT campaign.id, campaign.name, campaign.status, campaign.advertising_channel_type, campaign_budget.amount_micros FROM campaign")
	if err != nil {
		return nil, err
	}
	out := make([]c.AdCampaign, 0, len(rows))
	for _, r := range rows {
		cp := c.AdCampaign{ID: fmt.Sprint(int64(r.Campaign.ID)), Name: r.Campaign.Name, Status: status(r.Campaign.Status),
			ProviderStatus: r.Campaign.Status, Objective: strings.ToLower(r.Campaign.AdvertisingChannelType)}
		if b := int64(r.CampaignBudget.AmountMicros); b > 0 {
			cp.DailyBudgetMicros = ptrI(b)
		}
		out = append(out, cp)
	}
	return out, nil
}

func (g GoogleAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	q := fmt.Sprintf("SELECT campaign.id, campaign.name, segments.date, metrics.cost_micros, metrics.impressions, metrics.clicks, metrics.conversions, metrics.conversions_value FROM campaign WHERE segments.date BETWEEN '%s' AND '%s'",
		mustDate(from).Format("2006-01-02"), mustDate(to).Format("2006-01-02"))
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], q)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdDay, 0, len(rows))
	for _, r := range rows {
		m := r.Metrics
		out = append(out, c.AdDay{CampaignID: fmt.Sprint(int64(r.Campaign.ID)), CampaignName: r.Campaign.Name, Date: r.Segments.Date,
			SpendMicros: int64(m.CostMicros), Impressions: int64(m.Impressions), Clicks: int64(m.Clicks),
			Conversions: ptrF(float64(m.Conversions)), ConversionValueMicros: ptrI(c.Micros(float64(m.ConversionsValue)))})
	}
	return out, nil
}
