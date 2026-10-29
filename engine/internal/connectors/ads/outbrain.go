package ads

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// OutbrainAds reads Outbrain marketers (ad accounts) through the Amplify
// API (v0.1). Amplify has no sign-in app: the workspace gives its own
// Outbrain username and password, which are traded for an OB-TOKEN-V1
// token at GET /login. A token lasts 30 days and Outbrain allows two
// logins per hour per user, so the token is stored with the sealed
// credentials and renewed through CredsRefresher well before it expires.
// It only reads: the Amplify API has no endpoint that takes conversions.
type OutbrainAds struct{}

const (
	outbrainBase = "https://api.outbrain.com/amplify/v0.1"
	// outbrainTokenLife is how long a stored token is used before a new
	// one is fetched; Outbrain's last 30 days.
	outbrainTokenLife = 25 * 24 * time.Hour
	// outbrainReportDays is the longest range one daily report call asks
	// for, so one page holds every day of every campaign.
	outbrainReportDays = 31
	// outbrainCampaignPage is the most campaigns one list call returns.
	outbrainCampaignPage = 50
)

func (OutbrainAds) Definition() c.Definition {
	d := def("outbrain_ads", "Outbrain Ads", "https://www.outbrain.com/advertisers/",
		"https://developer.outbrain.com/docs/amplify/api/overview/", "https://my.outbrain.com/create-token",
		"Spend, impressions, clicks and conversions per campaign from your Outbrain marketers.",
		"Uses your own Outbrain login, once Outbrain has given it Amplify API access (apply on Outbrain's developer site). Outflare trades the username and password for an API token every few weeks and reads every marketer the login can see, read-only.")
	d.Auth = c.AuthAPIKey
	d.Fields = []c.Field{
		{Name: "username", Label: "Username", Help: "The Outbrain login with Amplify API access."},
		{Name: "password", Label: "Password", Secret: true, Help: "Outbrain allows two token requests an hour, so connecting again right away can be refused for an hour."},
	}
	return d
}

func (OutbrainAds) base(cl *c.Client) string { return cl.Base("outbrain_ads", outbrainBase) }

// login trades the username and password for a token.
func (o OutbrainAds) login(ctx context.Context, cl *c.Client, creds c.Creds) (string, error) {
	user, pass := strings.TrimSpace(creds["username"]), creds["password"]
	if user == "" || pass == "" {
		return "", c.Errf(c.ErrAuth, "add the Outbrain username and password")
	}
	req, err := http.NewRequest(http.MethodGet, o.base(cl)+"/login", nil)
	if err != nil {
		return "", c.Errf(c.ErrInvalid, "bad Outbrain Ads address: %v", err)
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	var out map[string]string
	status, err := c.DoJSON(ctx, cl, "Outbrain Ads", req, &out)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "", c.Errf(c.ErrAuth, "Outbrain refused the username or password (HTTP %d). Check them, and that the login has Amplify API access.", status)
	case status == http.StatusTooManyRequests:
		return "", c.Errf(c.ErrQuota, "Outbrain allows two token requests an hour per login. Try again in an hour.")
	case err != nil:
		return "", err
	}
	token := out["OB-TOKEN-V1"]
	if token == "" {
		return "", c.Errf(c.ErrUnavailable, "Outbrain did not return a token")
	}
	return token, nil
}

// RefreshDue reports whether there is no stored token or it is old enough
// to renew.
func (OutbrainAds) RefreshDue(creds c.Creds) bool {
	at, err := strconv.ParseInt(creds["token_at"], 10, 64)
	return creds["token"] == "" || err != nil || time.Since(time.Unix(at, 0)) > outbrainTokenLife
}

// RefreshCreds fetches a new token; older tokens stay valid until they
// expire.
func (o OutbrainAds) RefreshCreds(ctx context.Context, cl *c.Client, creds c.Creds) (c.Creds, error) {
	token, err := o.login(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	out := c.Creds{}
	for k, v := range creds {
		out[k] = v
	}
	out["token"] = token
	out["token_at"] = strconv.FormatInt(time.Now().Unix(), 10)
	return out, nil
}

// token is the stored token, or a new one when there is none yet (a
// connection being tested before it is saved).
func (o OutbrainAds) token(ctx context.Context, cl *c.Client, creds c.Creds) (string, error) {
	if t := creds["token"]; t != "" {
		return t, nil
	}
	return o.login(ctx, cl, creds)
}

func (o OutbrainAds) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) error {
	token, err := o.token(ctx, cl, creds)
	if err != nil {
		return err
	}
	u := o.base(cl) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return newCall(cl, "Outbrain Ads").set("OB-TOKEN-V1", token).get(ctx, u, out)
}

type outbrainMarketer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func (o OutbrainAds) marketers(ctx context.Context, cl *c.Client, creds c.Creds) ([]outbrainMarketer, error) {
	var resp struct {
		Marketers []outbrainMarketer `json:"marketers"`
	}
	if err := o.get(ctx, cl, creds, "/marketers", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Marketers, nil
}

func (o OutbrainAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if creds["token"] == "" {
		// Log in once and use the token for the calls below.
		token, err := o.login(ctx, cl, creds)
		if err != nil {
			return c.Account{}, err
		}
		creds = c.Creds{"token": token}
	}
	ms, err := o.marketers(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	acct := c.Account{Details: map[string]string{"accounts": fmt.Sprint(len(ms))}}
	if len(ms) == 1 {
		acct.ID, acct.Name = ms[0].ID, ms[0].Name
	}
	return acct, nil
}

// AdAccounts lists the marketers the login can see. Spend is in the
// marketer's currency, which Amplify gives on its budgets; a marketer
// without a budget has spent nothing yet and is listed in USD.
func (o OutbrainAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	ms, err := o.marketers(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdAccount, 0, len(ms))
	for _, m := range ms {
		if m.ID == "" {
			continue
		}
		acct := c.AdAccount{ID: m.ID, Name: m.Name, Currency: "USD"}
		var b struct {
			Budgets []struct {
				Currency string `json:"currency"`
			} `json:"budgets"`
		}
		if err := o.get(ctx, cl, creds, "/marketers/"+esc(m.ID)+"/budgets", nil, &b); err != nil {
			return nil, err
		}
		for _, x := range b.Budgets {
			if x.Currency != "" {
				acct.Currency = strings.ToUpper(x.Currency)
				break
			}
		}
		out = append(out, acct)
	}
	return out, nil
}

// AdCampaigns lists the marketer's campaigns, archived ones included.
func (o OutbrainAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	var out []c.AdCampaign
	for page := 0; page < maxPages; page++ {
		q := url.Values{"includeArchived": {"true"}, "fetch": {"all"},
			"limit": {strconv.Itoa(outbrainCampaignPage)}, "offset": {strconv.Itoa(page * outbrainCampaignPage)}}
		var resp struct {
			Campaigns []struct {
				ID           string          `json:"id"`
				Name         string          `json:"name"`
				Enabled      bool            `json:"enabled"`
				AutoArchived bool            `json:"autoArchived"`
				Objective    json.RawMessage `json:"objective"`
				Budget       *struct {
					Type        string   `json:"type"`
					Pacing      string   `json:"pacing"`
					Amount      *flexNum `json:"amount"`
					DailyTarget *flexNum `json:"dailyTarget"`
				} `json:"budget"`
			} `json:"campaigns"`
			TotalCount int `json:"totalCount"`
		}
		if err := o.get(ctx, cl, creds, "/marketers/"+esc(acct.ID)+"/campaigns", q, &resp); err != nil {
			return nil, err
		}
		for _, cp := range resp.Campaigns {
			camp := c.AdCampaign{ID: cp.ID, Name: cp.Name}
			switch {
			case cp.AutoArchived:
				camp.Status, camp.ProviderStatus = c.AdRemoved, "ARCHIVED"
			case cp.Enabled:
				camp.Status, camp.ProviderStatus = c.AdActive, "ENABLED"
			default:
				camp.Status, camp.ProviderStatus = c.AdPaused, "DISABLED"
			}
			var objective string
			if json.Unmarshal(cp.Objective, &objective) == nil {
				camp.Objective = strings.ToLower(objective)
			}
			if b := cp.Budget; b != nil {
				switch {
				case strings.EqualFold(b.Type, "DAILY") && b.Amount != nil && *b.Amount > 0:
					camp.DailyBudgetMicros = ptrI(c.Micros(float64(*b.Amount)))
				case strings.EqualFold(b.Pacing, "DAILY_TARGET") && b.DailyTarget != nil && *b.DailyTarget > 0:
					camp.DailyBudgetMicros = ptrI(c.Micros(float64(*b.DailyTarget)))
				}
			}
			out = append(out, camp)
		}
		if len(resp.Campaigns) < outbrainCampaignPage || (page+1)*outbrainCampaignPage >= resp.TotalCount {
			break
		}
	}
	return out, nil
}

// AdDays reads the daily report by campaign. Each call covers at most 31
// days and asks for that many results per campaign, so nothing is left on
// a second page. Conversions are Outbrain's conversions from clicks, by
// conversion date, with their value.
func (o OutbrainAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	all := days(from, to)
	var out []c.AdDay
	for start := 0; start < len(all); start += outbrainReportDays {
		window := all[start:min(start+outbrainReportDays, len(all))]
		q := url.Values{"from": {window[0]}, "to": {window[len(window)-1]}, "breakdown": {"daily"},
			"limit": {strconv.Itoa(len(window))}, "includeArchivedCampaigns": {"true"}}
		var resp struct {
			CampaignResults []struct {
				CampaignID string `json:"campaignId"`
				Results    []struct {
					Metadata struct {
						ID       string `json:"id"`
						FromDate string `json:"fromDate"`
					} `json:"metadata"`
					Metrics struct {
						Impressions flexInt  `json:"impressions"`
						Clicks      flexInt  `json:"clicks"`
						Spend       flexNum  `json:"spend"`
						Conversions *flexNum `json:"conversions"`
						SumValue    *flexNum `json:"sumValue"`
					} `json:"metrics"`
				} `json:"results"`
			} `json:"campaignResults"`
		}
		if err := o.get(ctx, cl, creds, "/reports/marketers/"+esc(acct.ID)+"/campaigns/periodic", q, &resp); err != nil {
			return nil, err
		}
		for _, cr := range resp.CampaignResults {
			for _, r := range cr.Results {
				date := r.Metadata.FromDate
				if date == "" {
					date = r.Metadata.ID
				}
				m := r.Metrics
				if len(date) < 10 || m.Spend == 0 && m.Impressions == 0 && m.Clicks == 0 {
					continue
				}
				day := c.AdDay{CampaignID: cr.CampaignID, Date: date[:10], SpendMicros: c.Micros(float64(m.Spend)),
					Impressions: int64(m.Impressions), Clicks: int64(m.Clicks)}
				if m.Conversions != nil {
					day.Conversions = ptrF(float64(*m.Conversions))
				}
				if m.SumValue != nil && *m.SumValue > 0 {
					day.ConversionValueMicros = ptrI(c.Micros(float64(*m.SumValue)))
				}
				out = append(out, day)
			}
		}
	}
	return out, nil
}
