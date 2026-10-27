package ads

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// XAds reads X (Twitter) ad accounts through the X Ads API. X grants Ads
// API access per developer app, so the workspace connects its own app's
// keys and the access token for the account that runs its ads; the Ads API
// signs requests with OAuth 1.0a.
type XAds struct{}

const (
	xAdsBase    = "https://ads-api.x.com"
	xAdsVersion = "12"
	// xStatsDays and xStatsIDs are the most one stats request may ask for.
	xStatsDays = 7
	xStatsIDs  = 20
)

func (XAds) Definition() c.Definition {
	d := def("x_ads", "X Ads", "https://ads.x.com",
		"https://developer.x.com/en/docs/x-ads-api", "https://developer.x.com/en/portal/dashboard",
		"Spend, impressions and clicks per campaign from your X ad accounts.",
		"Uses your own X developer app, once X has approved it for the Ads API (apply in the developer portal; approval can take a few weeks). Paste the app's API key and secret and the access token and secret of the account that runs your ads. Read-only.")
	d.Auth = c.AuthAPIKey
	d.Fields = []c.Field{
		{Name: "consumer_key", Label: "API key", Secret: false, Help: "Your developer app's API key (consumer key)."},
		{Name: "consumer_secret", Label: "API key secret", Secret: true},
		{Name: "access_token", Label: "Access token", Secret: true, Help: "For the X account that runs your ads, with read access."},
		{Name: "access_token_secret", Label: "Access token secret", Secret: true},
	}
	return d
}

func (XAds) base(cl *c.Client) string { return cl.Base("x_ads", xAdsBase) + "/" + xAdsVersion }

// get signs and sends one GET.
func (x XAds) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) error {
	u := x.base(cl) + path
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return c.Errf(c.ErrInvalid, "bad X Ads address: %v", err)
	}
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Authorization", oauth1Header(http.MethodGet, u, q, creds, oauthNonce(), time.Now()))
	_, err = c.DoJSON(ctx, cl, "X Ads", req, out)
	return err
}

func (x XAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	accts, err := x.AdAccounts(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	return c.Account{Details: map[string]string{"accounts": fmt.Sprint(len(accts))}}, nil
}

func (x XAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	var out []c.AdAccount
	cursor := ""
	for i := 0; i < maxPages; i++ {
		q := url.Values{"count": {"1000"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var resp struct {
			Data []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Timezone string `json:"timezone"`
				Deleted  bool   `json:"deleted"`
			} `json:"data"`
			NextCursor string `json:"next_cursor"`
		}
		if err := x.get(ctx, cl, creds, "/accounts", q, &resp); err != nil {
			return nil, err
		}
		for _, a := range resp.Data {
			if a.Deleted {
				continue
			}
			acct := c.AdAccount{ID: a.ID, Name: a.Name, TimeZone: a.Timezone, Currency: "USD"}
			var fi struct {
				Data []struct {
					Currency string `json:"currency"`
				} `json:"data"`
			}
			if err := x.get(ctx, cl, creds, "/accounts/"+esc(a.ID)+"/funding_instruments", url.Values{}, &fi); err == nil && len(fi.Data) > 0 && fi.Data[0].Currency != "" {
				acct.Currency = strings.ToUpper(fi.Data[0].Currency)
			}
			out = append(out, acct)
		}
		if cursor = resp.NextCursor; cursor == "" {
			break
		}
	}
	return out, nil
}

func (x XAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	var out []c.AdCampaign
	cursor := ""
	for i := 0; i < maxPages; i++ {
		q := url.Values{"count": {"1000"}, "with_deleted": {"true"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var resp struct {
			Data []struct {
				ID                string  `json:"id"`
				Name              string  `json:"name"`
				EntityStatus      string  `json:"entity_status"`
				Deleted           bool    `json:"deleted"`
				DailyBudgetMicros flexInt `json:"daily_budget_amount_local_micro"`
			} `json:"data"`
			NextCursor string `json:"next_cursor"`
		}
		if err := x.get(ctx, cl, creds, "/accounts/"+esc(acct.ID)+"/campaigns", q, &resp); err != nil {
			return nil, err
		}
		for _, cp := range resp.Data {
			st := cp.EntityStatus
			if cp.Deleted {
				st = "DELETED"
			}
			camp := c.AdCampaign{ID: cp.ID, Name: cp.Name, Status: status(st), ProviderStatus: st}
			if cp.DailyBudgetMicros > 0 {
				camp.DailyBudgetMicros = ptrI(int64(cp.DailyBudgetMicros))
			}
			out = append(out, camp)
		}
		if cursor = resp.NextCursor; cursor == "" {
			break
		}
	}
	return out, nil
}

func (x XAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	camps, err := x.AdCampaigns(ctx, cl, creds, acct)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(acct.TimeZone)
	if err != nil || acct.TimeZone == "" {
		loc = time.UTC
	}
	all := days(from, to)
	var out []c.AdDay
	for start := 0; start < len(all); start += xStatsDays {
		window := all[start:min(start+xStatsDays, len(all))]
		t0 := mustDate(window[0])
		t1 := mustDate(window[len(window)-1]).AddDate(0, 0, 1)
		begin := time.Date(t0.Year(), t0.Month(), t0.Day(), 0, 0, 0, 0, loc)
		end := time.Date(t1.Year(), t1.Month(), t1.Day(), 0, 0, 0, 0, loc)
		for i := 0; i < len(camps); i += xStatsIDs {
			batch := camps[i:min(i+xStatsIDs, len(camps))]
			ids := make([]string, len(batch))
			names := map[string]string{}
			for j, cp := range batch {
				ids[j], names[cp.ID] = cp.ID, cp.Name
			}
			q := url.Values{
				"entity": {"CAMPAIGN"}, "entity_ids": {strings.Join(ids, ",")}, "granularity": {"DAY"},
				"metric_groups": {"BILLING,ENGAGEMENT"}, "placement": {"ALL_ON_TWITTER"},
				"start_time": {begin.Format(time.RFC3339)}, "end_time": {end.Format(time.RFC3339)},
			}
			var resp struct {
				Data []struct {
					ID     string `json:"id"`
					IDData []struct {
						Metrics struct {
							Billed      []*flexInt `json:"billed_charge_local_micro"`
							Impressions []*flexInt `json:"impressions"`
							Clicks      []*flexInt `json:"clicks"`
						} `json:"metrics"`
					} `json:"id_data"`
				} `json:"data"`
			}
			if err := x.get(ctx, cl, creds, "/stats/accounts/"+esc(acct.ID), q, &resp); err != nil {
				return nil, err
			}
			at := func(s []*flexInt, k int) int64 {
				if k < len(s) && s[k] != nil {
					return int64(*s[k])
				}
				return 0
			}
			for _, d := range resp.Data {
				for _, seg := range d.IDData {
					m := seg.Metrics
					for k, day := range window {
						spend, imp, clk := at(m.Billed, k), at(m.Impressions, k), at(m.Clicks, k)
						if spend == 0 && imp == 0 && clk == 0 {
							continue
						}
						out = append(out, c.AdDay{CampaignID: d.ID, CampaignName: names[d.ID], Date: day, SpendMicros: spend, Impressions: imp, Clicks: clk})
					}
				}
			}
		}
	}
	return out, nil
}

// ---- OAuth 1.0a ----

func oauthNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// pct is RFC 3986 percent-encoding, as OAuth 1.0a requires.
func pct(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '.' || ch == '_' || ch == '~' {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

// oauth1Header signs a request with HMAC-SHA1 (RFC 5849).
func oauth1Header(method, rawURL string, query url.Values, creds c.Creds, nonce string, now time.Time) string {
	oauth := map[string]string{
		"oauth_consumer_key":     creds["consumer_key"],
		"oauth_nonce":            nonce,
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_timestamp":        strconv.FormatInt(now.Unix(), 10),
		"oauth_token":            creds["access_token"],
		"oauth_version":          "1.0",
	}
	var pairs []string
	for k, v := range oauth {
		pairs = append(pairs, pct(k)+"="+pct(v))
	}
	for k, vs := range query {
		for _, v := range vs {
			pairs = append(pairs, pct(k)+"="+pct(v))
		}
	}
	sort.Strings(pairs)
	base := strings.ToUpper(method) + "&" + pct(rawURL) + "&" + pct(strings.Join(pairs, "&"))
	mac := hmac.New(sha1.New, []byte(pct(creds["consumer_secret"])+"&"+pct(creds["access_token_secret"])))
	mac.Write([]byte(base))
	oauth["oauth_signature"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	keys := make([]string, 0, len(oauth))
	for k := range oauth {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = pct(k) + `="` + pct(oauth[k]) + `"`
	}
	return "OAuth " + strings.Join(parts, ", ")
}
