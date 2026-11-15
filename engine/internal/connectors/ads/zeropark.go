package ads

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ZeroparkAds reads a Zeropark advertiser account through the Stats API
// (panel.zeropark.com/api): its campaigns, and what each spent and got per
// day. The workspace pastes an API access token it makes in Zeropark (My
// account, Security), sent in the api-token header. Zeropark pays per
// visit (a redirect), so a day's clicks are its redirects; it reports no
// impressions. Days are in the account's own time zone (My account), which
// Zeropark also uses for the dates it is asked about. Read-only.
//
// Docs: the Zeropark Stats API user guide and the API reference in the
// panel (My account, API documentation; /v3/api-docs).
type ZeroparkAds struct{}

const (
	zeroparkBase = "https://panel.zeropark.com"
	// zeroparkPage is how many campaigns one page asks for (Zeropark
	// takes up to 10,000).
	zeroparkPage = 1000
)

func (ZeroparkAds) Definition() c.Definition {
	d := def("zeropark_ads", "Zeropark", "https://zeropark.com",
		"https://doc.zeropark.com/article/zeropark-api-usage-guide", "",
		"Spend, visits, conversions and payout per campaign and day from your Zeropark account.",
		"Uses your own API access token: in Zeropark, open My account, then Security, and add an access token. Zeropark shows it once, so paste it here straight away; it recommends making a new one every 30 days. Read-only.")
	d.Auth = c.AuthAPIKey
	d.Capabilities = append(d.Capabilities, c.CapAdsTargets)
	d.Fields = []c.Field{
		{Name: "api_token", Label: "API access token", Secret: true, Help: "My account, Security, Access tokens, Add new."},
	}
	return d
}

func (ZeroparkAds) base(cl *c.Client) string { return cl.Base("zeropark_ads", zeroparkBase) }

func (z ZeroparkAds) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) error {
	token := strings.TrimSpace(creds["api_token"])
	if token == "" {
		return c.Errf(c.ErrAuth, "Zeropark needs an API access token")
	}
	u := z.base(cl) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return newCall(cl, "Zeropark").set("api-token", token).get(ctx, u, out)
}

type zeroparkAccount struct {
	ClientID string   `json:"clientId"`
	Balance  *flexNum `json:"accountBalance"`
	Timezone string   `json:"timezone"`
}

func (z ZeroparkAds) account(ctx context.Context, cl *c.Client, creds c.Creds) (zeroparkAccount, error) {
	var a zeroparkAccount
	if err := z.get(ctx, cl, creds, "/api/account/details", nil, &a); err != nil {
		return a, err
	}
	if a.ClientID == "" {
		return a, c.Errf(c.ErrInvalid, "Zeropark returned no account for this token")
	}
	return a, nil
}

// Test reads the account the token belongs to. Its owner's name and email
// address aren't kept.
func (z ZeroparkAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	a, err := z.account(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	acct := c.Account{ID: a.ClientID, Name: "Zeropark " + a.ClientID, Details: map[string]string{}}
	if a.Balance != nil {
		acct.Details["balance_usd"] = strconv.FormatFloat(float64(*a.Balance), 'f', 2, 64)
	}
	if tz := zeroparkZone(a.Timezone); tz != "" {
		acct.Details["time_zone"] = tz
	}
	return acct, nil
}

// zeroparkZone is the account's time zone as an IANA name, or "" when
// Zeropark gives one Go doesn't know.
func zeroparkZone(tz string) string {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return ""
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return ""
	}
	return tz
}

// AdAccounts is the token's one account. Zeropark bills in US dollars.
func (z ZeroparkAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	a, err := z.account(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	return []c.AdAccount{{ID: a.ClientID, Name: "Zeropark " + a.ClientID, Currency: "USD", TimeZone: zeroparkZone(a.Timezone)}}, nil
}

// zeroparkCampaign is one element of /api/stats/campaign/all.
type zeroparkCampaign struct {
	Details struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		DailyBudget *struct {
			Type   string   `json:"type"`
			Amount *flexNum `json:"amount"`
		} `json:"dailyBudget"`
		State *struct {
			State string `json:"state"`
		} `json:"state"`
	} `json:"details"`
	Stats struct {
		Spent       flexNum `json:"spent"`
		Payout      flexNum `json:"payout"`
		Redirects   flexInt `json:"redirects"`
		Conversions flexInt `json:"conversions"`
	} `json:"stats"`
}

// campaigns reads every page of /api/stats/campaign/all for an interval;
// CUSTOM takes a start and end day (YYYY-MM-DD here, dd/mm/yyyy to
// Zeropark).
func (z ZeroparkAds) campaigns(ctx context.Context, cl *c.Client, creds c.Creds, interval, from, to string) ([]zeroparkCampaign, error) {
	var out []zeroparkCampaign
	for page := 0; page < maxPages; page++ {
		q := url.Values{"interval": {interval}, "page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(zeroparkPage)},
			"sortColumn": {"SPENT"}, "sortOrder": {"DESC"}}
		if interval == "CUSTOM" {
			q.Set("startDate", zeroparkDate(from))
			q.Set("endDate", zeroparkDate(to))
		}
		var resp struct {
			Elements []zeroparkCampaign `json:"elements"`
			Total    int                `json:"total"`
		}
		if err := z.get(ctx, cl, creds, "/api/stats/campaign/all", q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Elements...)
		if len(resp.Elements) < zeroparkPage || (page+1)*zeroparkPage >= resp.Total {
			break
		}
	}
	return out, nil
}

// zeroparkDate turns 2026-11-14 into Zeropark's 14/11/2026.
func zeroparkDate(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.Format("02/01/2006")
}

// zeroparkStatus maps Zeropark's campaign state. A campaign that is on but
// held (out of budget, outside its day parting) is active: it runs again
// on its own.
func zeroparkStatus(state string) string {
	switch state {
	case "ACTIVE", "OUT_OF_BUDGET", "OUT_OF_DAILY_BUDGET", "OUT_OF_CREDIT", "TOP_UP_NEEDED", "DAY_PARTING", "CONVERSION_CAPPING":
		return c.AdActive
	case "PAUSED", "DEAL_PAUSED":
		return c.AdPaused
	case "DELETED", "ARCHIVED", "REJECTED", "DEAL_EXPIRED", "NONEXISTENT":
		return c.AdRemoved
	}
	return c.AdOther
}

// AdCampaigns lists the account's campaigns.
func (z ZeroparkAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	list, err := z.campaigns(ctx, cl, creds, "TODAY", "", "")
	if err != nil {
		return nil, err
	}
	out := make([]c.AdCampaign, 0, len(list))
	for _, e := range list {
		d := e.Details
		if d.ID == "" {
			continue
		}
		state := ""
		if d.State != nil {
			state = d.State.State
		}
		camp := c.AdCampaign{ID: d.ID, Name: d.Name, Status: zeroparkStatus(state), ProviderStatus: state}
		if b := d.DailyBudget; b != nil && strings.EqualFold(b.Type, "LIMITED") && b.Amount != nil && *b.Amount > 0 {
			camp.DailyBudgetMicros = ptrI(c.Micros(float64(*b.Amount)))
		}
		out = append(out, camp)
	}
	return out, nil
}

// AdDays asks for each day on its own (Zeropark's statistics add a whole
// interval up). Conversions and payout are what Zeropark counted from its
// own postbacks.
func (z ZeroparkAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	var out []c.AdDay
	for _, day := range days(from, to) {
		list, err := z.campaigns(ctx, cl, creds, "CUSTOM", day, day)
		if err != nil {
			return nil, err
		}
		for _, e := range list {
			s := e.Stats
			if e.Details.ID == "" || s.Spent == 0 && s.Redirects == 0 && s.Conversions == 0 {
				continue
			}
			d := c.AdDay{CampaignID: e.Details.ID, CampaignName: e.Details.Name, Date: day,
				SpendMicros: c.Micros(float64(s.Spent)), Clicks: int64(s.Redirects), Conversions: ptrF(float64(s.Conversions))}
			if s.Payout > 0 {
				d.ConversionValueMicros = ptrI(c.Micros(float64(s.Payout)))
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// AdTargetDays reads one campaign's targets for one day, every page, and
// leaves out those that neither spent nor sent a visit.
func (z ZeroparkAds) AdTargetDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, day string) ([]c.AdTargetDay, error) {
	if campaignID == "" || strings.ContainsAny(campaignID, "/?#") {
		return nil, c.Errf(c.ErrInvalid, "not a Zeropark campaign id: %q", campaignID)
	}
	var out []c.AdTargetDay
	for page := 0; page < maxPages; page++ {
		q := url.Values{"interval": {"CUSTOM"}, "startDate": {zeroparkDate(day)}, "endDate": {zeroparkDate(day)},
			"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(zeroparkPage)}, "sortColumn": {"SPENT"}, "sortOrder": {"DESC"}}
		var resp struct {
			Elements []struct {
				ID      string `json:"id"`
				Target  string `json:"target"`
				Address string `json:"address"`
				Source  string `json:"source"`
				Stats   struct {
					Spent     flexNum `json:"spent"`
					Redirects flexInt `json:"redirects"`
				} `json:"stats"`
			} `json:"elements"`
			Total int `json:"total"`
		}
		if err := z.get(ctx, cl, creds, "/api/stats/campaign/"+esc(campaignID)+"/targets", q, &resp); err != nil {
			return nil, err
		}
		for _, e := range resp.Elements {
			if e.Stats.Spent == 0 && e.Stats.Redirects == 0 {
				continue
			}
			var keys []string
			for _, k := range []string{e.Target, e.Address, e.ID} {
				if k = strings.TrimSpace(k); k != "" && !slices.Contains(keys, k) {
					keys = append(keys, k)
				}
			}
			out = append(out, c.AdTargetDay{CampaignID: campaignID, Date: day, Keys: keys, Source: e.Source,
				SpendMicros: c.Micros(float64(e.Stats.Spent)), Clicks: int64(e.Stats.Redirects)})
		}
		if len(resp.Elements) < zeroparkPage || (page+1)*zeroparkPage >= resp.Total {
			break
		}
	}
	return out, nil
}
