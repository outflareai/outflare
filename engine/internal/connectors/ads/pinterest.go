package ads

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// PinterestAds reads Pinterest ad accounts through the Pinterest API (v5)
// and sends conversions through its Conversions API. The sign-in only
// reads; sending takes a conversion access token the workspace generates
// in Ads Manager, so Outflare never asks for write access to the ads.
type PinterestAds struct{}

const (
	pinterestBase = "https://api.pinterest.com/v5"
	// pinterestDays is the longest range one analytics call takes, and
	// pinterestIDs the most campaigns it names.
	pinterestDays = 90
	pinterestIDs  = 250
)

func (PinterestAds) Definition() c.Definition {
	d := def("pinterest_ads", "Pinterest Ads", "https://ads.pinterest.com",
		"https://developers.pinterest.com/docs/api/v5/", "",
		"Spend, impressions, clicks and conversions per campaign from your Pinterest ad accounts.",
		"Signs in with Pinterest and reads every ad account you can see, read-only; add a conversion access token to send conversions back through the Conversions API.")
	d.Capabilities = append(d.Capabilities, c.CapAdsConversions)
	d.OAuth = &c.OAuth{
		AuthURL:   "https://www.pinterest.com/oauth/",
		TokenURL:  pinterestBase + "/oauth/token",
		Scopes:    []string{"ads:read", "user_accounts:read"},
		BasicAuth: true,
	}
	return d
}

func (PinterestAds) base(cl *c.Client) string { return cl.Base("pinterest_ads", pinterestBase) }

func (PinterestAds) call(cl *c.Client, token string) *call {
	return newCall(cl, "Pinterest Ads").bearer(token)
}

// pages follows a v5 list through its bookmark.
func (p PinterestAds) pages(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, each func(json.RawMessage) error) error {
	q.Set("page_size", "250")
	for i := 0; i < maxPages; i++ {
		var resp struct {
			Items    json.RawMessage `json:"items"`
			Bookmark string          `json:"bookmark"`
		}
		if err := p.call(cl, creds["access_token"]).get(ctx, p.base(cl)+path+"?"+q.Encode(), &resp); err != nil {
			return err
		}
		if err := each(resp.Items); err != nil {
			return err
		}
		if resp.Bookmark == "" {
			return nil
		}
		q.Set("bookmark", resp.Bookmark)
	}
	return nil
}

func (p PinterestAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var me struct {
		Username string `json:"username"`
	}
	if err := p.call(cl, creds["access_token"]).get(ctx, p.base(cl)+"/user_account", &me); err != nil {
		return c.Account{}, err
	}
	accts, err := p.AdAccounts(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: me.Username, Name: me.Username, Details: map[string]string{"accounts": fmt.Sprint(len(accts))}}, nil
}

func (p PinterestAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	var out []c.AdAccount
	err := p.pages(ctx, cl, creds, "/ad_accounts", url.Values{}, func(raw json.RawMessage) error {
		var list []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Currency string `json:"currency"`
		}
		_ = json.Unmarshal(raw, &list)
		for _, a := range list {
			out = append(out, c.AdAccount{ID: a.ID, Name: a.Name, Currency: strings.ToUpper(a.Currency)})
		}
		return nil
	})
	return out, err
}

func (p PinterestAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	var out []c.AdCampaign
	q := url.Values{"entity_statuses": {"ACTIVE,PAUSED,ARCHIVED"}}
	err := p.pages(ctx, cl, creds, "/ad_accounts/"+esc(acct.ID)+"/campaigns", q, func(raw json.RawMessage) error {
		var list []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Status        string `json:"status"`
			ObjectiveType string `json:"objective_type"`
			DailySpendCap *int64 `json:"daily_spend_cap"` // micros
		}
		_ = json.Unmarshal(raw, &list)
		for _, cp := range list {
			camp := c.AdCampaign{ID: cp.ID, Name: cp.Name, Status: status(cp.Status), ProviderStatus: cp.Status, Objective: strings.ToLower(cp.ObjectiveType)}
			if cp.DailySpendCap != nil && *cp.DailySpendCap > 0 {
				camp.DailyBudgetMicros = ptrI(*cp.DailySpendCap)
			}
			out = append(out, camp)
		}
		return nil
	})
	return out, err
}

// AdDays reads campaign analytics by day. A call names at most 250
// campaigns and spans at most 90 days, so both are split. Despite its
// name, SPEND_IN_MICRO_DOLLAR is in micros of the account's currency.
func (p PinterestAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	camps, err := p.AdCampaigns(ctx, cl, creds, acct)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, cp := range camps {
		names[cp.ID] = cp.Name
	}
	all := days(from, to)
	var out []c.AdDay
	for start := 0; start < len(all); start += pinterestDays {
		window := all[start:min(start+pinterestDays, len(all))]
		for i := 0; i < len(camps); i += pinterestIDs {
			batch := camps[i:min(i+pinterestIDs, len(camps))]
			ids := make([]string, len(batch))
			for j, cp := range batch {
				ids[j] = cp.ID
			}
			q := url.Values{
				"start_date": {window[0]}, "end_date": {window[len(window)-1]}, "granularity": {"DAY"},
				"campaign_ids": {strings.Join(ids, ",")},
				"columns":      {"SPEND_IN_MICRO_DOLLAR,IMPRESSION_1,CLICKTHROUGH_1,TOTAL_CONVERSIONS"},
			}
			var rows []struct {
				CampaignID  flexID   `json:"CAMPAIGN_ID"`
				Date        string   `json:"DATE"`
				Spend       flexInt  `json:"SPEND_IN_MICRO_DOLLAR"`
				Impressions flexInt  `json:"IMPRESSION_1"`
				Clicks      flexInt  `json:"CLICKTHROUGH_1"`
				Conversions *flexNum `json:"TOTAL_CONVERSIONS"`
			}
			if err := p.call(cl, creds["access_token"]).get(ctx, p.base(cl)+"/ad_accounts/"+esc(acct.ID)+"/campaigns/analytics?"+q.Encode(), &rows); err != nil {
				return nil, err
			}
			for _, r := range rows {
				id := string(r.CampaignID)
				if id == "" || len(r.Date) < 10 || r.Spend == 0 && r.Impressions == 0 && r.Clicks == 0 {
					continue
				}
				day := c.AdDay{CampaignID: id, CampaignName: names[id], Date: r.Date[:10], SpendMicros: int64(r.Spend), Impressions: int64(r.Impressions), Clicks: int64(r.Clicks)}
				if r.Conversions != nil {
					day.Conversions = ptrF(float64(*r.Conversions))
				}
				out = append(out, day)
			}
		}
	}
	return out, nil
}

// flexID reads an id sent as a JSON number or a string.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" {
		s = ""
	}
	*f = flexID(s)
	return nil
}

// ---- conversions ----

// pinterestEvents are the Conversions API's event names a rule can send as.
var pinterestEvents = []string{"checkout", "signup", "lead", "add_to_cart", "page_visit", "view_category", "search", "custom"}

// SetupFields: sending conversions takes a conversion access token, which
// the workspace generates in Ads Manager. Reading needs only the sign-in.
func (PinterestAds) SetupFields() []c.Field {
	return []c.Field{{Name: "conversions_token", Label: "Conversion access token", Secret: true, Optional: true,
		Help: "Only to send conversions to Pinterest: in Pinterest Ads Manager, Conversions, Conversion access token, generate a token."}}
}

func pinterestToken(creds c.Creds) (string, error) {
	if creds["conversions_token"] == "" {
		return "", c.Errf(c.ErrAuth, "Pinterest needs a conversion access token to send conversions: add it on the connection")
	}
	return creds["conversions_token"], nil
}

// ConversionTargets: an account's conversions all go to one place.
func (PinterestAds) ConversionTargets(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.ConversionTarget, error) {
	if _, err := pinterestToken(creds); err != nil {
		return nil, err
	}
	return []c.ConversionTarget{{ID: acct.ID, Name: "Conversions API", EventTypes: pinterestEvents}}, nil
}

// SendConversions posts web events with the click id (epik) and the
// hashed email, phone (digits only) and external id; event_id dedupes
// against the Pinterest tag. Pinterest says how each event went.
func (p PinterestAds) SendConversions(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, target c.ConversionTarget, convs []c.AdConversion) ([]c.ConversionResult, error) {
	token, err := pinterestToken(creds)
	if err != nil {
		return nil, err
	}
	results := make([]c.ConversionResult, len(convs))
	var events []map[string]any
	var sentIdx []int
	for i, cv := range convs {
		results[i] = c.ConversionResult{Index: cv.Index}
		user := map[string]any{}
		var keys []string
		if v := cv.ClickIDs["epik"]; v != "" {
			user["click_id"], keys = v, append(keys, "epik")
		}
		if len(cv.Emails) > 0 {
			user["em"], keys = cv.Emails, append(keys, "email")
		}
		if len(cv.PhoneDigits) > 0 {
			user["ph"], keys = cv.PhoneDigits, append(keys, "phone")
		}
		if cv.ExternalID != "" {
			user["external_id"], keys = []string{cv.ExternalID}, append(keys, "external_id")
		}
		if len(keys) == 0 {
			results[i].Skipped, results[i].Reason = true, "no Pinterest click id or identifier to match on"
			continue
		}
		kind := cv.EventType
		if kind == "" {
			kind = "signup"
		}
		ev := map[string]any{"event_name": kind, "action_source": "web", "event_time": cv.Time.Unix(), "event_id": cv.EventID, "user_data": user}
		if cv.ValueMicros != nil {
			// Pinterest takes the value as a decimal string.
			ev["custom_data"] = map[string]any{"value": strconv.FormatFloat(float64(*cv.ValueMicros)/1e6, 'f', -1, 64), "currency": cv.Currency}
		}
		results[i].MatchKeys = keys
		events = append(events, ev)
		sentIdx = append(sentIdx, i)
	}
	if len(events) == 0 {
		return results, nil
	}
	var resp struct {
		Events []struct {
			Status       string `json:"status"`
			ErrorMessage string `json:"error_message"`
		} `json:"events"`
	}
	if err := p.call(cl, token).post(ctx, p.base(cl)+"/ad_accounts/"+esc(target.ID)+"/events", map[string]any{"data": events}, &resp); err != nil {
		return nil, err
	}
	for k, i := range sentIdx {
		if k < len(resp.Events) && resp.Events[k].Status != "" && resp.Events[k].Status != "processed" {
			results[i].Reason = resp.Events[k].ErrorMessage
			continue
		}
		results[i].OK = true
	}
	return results, nil
}
