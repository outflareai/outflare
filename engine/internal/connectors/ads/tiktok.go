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

// TikTokAds reads TikTok ad accounts (advertisers) through the TikTok
// Marketing API (v1.3) and sends conversions to a TikTok pixel through the
// Events API. The sign-in grants a set of advertisers, which come back
// with the token; the token has no expiry until the person revokes it.
type TikTokAds struct{}

const tiktokBase = "https://business-api.tiktok.com/open_api/v1.3"

func (TikTokAds) Definition() c.Definition {
	d := def("tiktok_ads", "TikTok Ads", "https://ads.tiktok.com",
		"https://business-api.tiktok.com/portal/docs", "",
		"Spend, impressions, clicks and conversions per campaign from your TikTok ad accounts.",
		"Signs in with TikTok For Business and reads the ad accounts you grant, read-only; conversions go back to your TikTok pixel through the Events API. To add more ad accounts later, connect again and grant them.")
	d.Capabilities = append(d.Capabilities, c.CapAdsConversions)
	d.OAuth = &c.OAuth{
		AuthURL:       "https://business-api.tiktok.com/portal/auth",
		TokenURL:      tiktokBase + "/oauth2/access_token/",
		Scopes:        []string{}, // set on the app in TikTok's developer portal, not per sign-in
		AppIDAuthCode: true,
	}
	return d
}

func (TikTokAds) base(cl *c.Client) string { return cl.Base("tiktok_ads", tiktokBase) }

// envelope is how every TikTok answer comes: code 0 is success, even
// though a refusal also arrives with HTTP 200.
type tiktokEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// get calls a v1.3 endpoint with query parameters; list and object
// values are sent JSON-encoded, as TikTok asks.
func (t TikTokAds) get(ctx context.Context, cl *c.Client, token, path string, params map[string]any, out any) error {
	q := url.Values{}
	for k, v := range params {
		switch x := v.(type) {
		case string:
			q.Set(k, x)
		case int:
			q.Set(k, strconv.Itoa(x))
		default:
			raw, _ := json.Marshal(x)
			q.Set(k, string(raw))
		}
	}
	var env tiktokEnvelope
	if err := newCall(cl, "TikTok Ads").set("Access-Token", token).get(ctx, t.base(cl)+path+"?"+q.Encode(), &env); err != nil {
		return err
	}
	return tiktokData(env, out)
}

func tiktokData(env tiktokEnvelope, out any) error {
	if env.Code != 0 {
		// 40100 to 40199 are the access token's problems.
		if env.Code >= 40100 && env.Code < 40200 {
			return c.Errf(c.ErrAuth, "TikTok no longer accepts this connection (%d %s); connect it again", env.Code, env.Message)
		}
		return c.Errf(c.ErrInvalid, "TikTok refused: %d %s", env.Code, env.Message)
	}
	if out == nil || len(env.Data) == 0 {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

func (TikTokAds) advertiserIDs(creds c.Creds) []string {
	var out []string
	for _, id := range strings.Split(creds["advertiser_ids"], ",") {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func (t TikTokAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	accts, err := t.AdAccounts(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	name := "TikTok Ads"
	if len(accts) == 1 {
		name = accts[0].Name
	}
	return c.Account{ID: accts[0].ID, Name: name, Details: map[string]string{"accounts": fmt.Sprint(len(accts))}}, nil
}

func (t TikTokAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	ids := t.advertiserIDs(creds)
	if len(ids) == 0 {
		return nil, c.Errf(c.ErrAuth, "the TikTok sign-in granted no ad accounts; connect again and choose at least one")
	}
	var out []c.AdAccount
	// advertiser/info takes up to 100 ids at a time.
	for start := 0; start < len(ids); start += 100 {
		end := min(start+100, len(ids))
		var data struct {
			List []struct {
				AdvertiserID    string `json:"advertiser_id"`
				Name            string `json:"name"`
				Currency        string `json:"currency"`
				Timezone        string `json:"timezone"`
				DisplayTimezone string `json:"display_timezone"`
			} `json:"list"`
		}
		err := t.get(ctx, cl, creds["access_token"], "/advertiser/info/", map[string]any{
			"advertiser_ids": ids[start:end],
			"fields":         []string{"advertiser_id", "name", "currency", "timezone", "display_timezone"},
		}, &data)
		if err != nil {
			return nil, err
		}
		for _, a := range data.List {
			tz := a.DisplayTimezone
			if tz == "" {
				tz = a.Timezone
			}
			out = append(out, c.AdAccount{ID: a.AdvertiserID, Name: a.Name, Currency: strings.ToUpper(a.Currency), TimeZone: tz})
		}
	}
	if len(out) == 0 {
		return nil, c.Errf(c.ErrAuth, "TikTok returned none of the ad accounts this sign-in granted; connect it again")
	}
	return out, nil
}

// tiktokPage is TikTok's page_info.
type tiktokPage struct {
	Page      int `json:"page"`
	TotalPage int `json:"total_page"`
}

func (t TikTokAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	var out []c.AdCampaign
	for page := 1; page <= maxPages; page++ {
		var data struct {
			List []struct {
				CampaignID      string  `json:"campaign_id"`
				CampaignName    string  `json:"campaign_name"`
				OperationStatus string  `json:"operation_status"`
				SecondaryStatus string  `json:"secondary_status"`
				ObjectiveType   string  `json:"objective_type"`
				Budget          flexNum `json:"budget"`
				BudgetMode      string  `json:"budget_mode"`
			} `json:"list"`
			PageInfo tiktokPage `json:"page_info"`
		}
		err := t.get(ctx, cl, creds["access_token"], "/campaign/get/", map[string]any{
			"advertiser_id": acct.ID, "page": page, "page_size": 1000,
			"fields": []string{"campaign_id", "campaign_name", "operation_status", "secondary_status", "objective_type", "budget", "budget_mode"},
		}, &data)
		if err != nil {
			return nil, err
		}
		for _, cp := range data.List {
			camp := c.AdCampaign{ID: cp.CampaignID, Name: cp.CampaignName, Status: tiktokStatus(cp.OperationStatus, cp.SecondaryStatus),
				ProviderStatus: cp.OperationStatus, Objective: strings.ToLower(cp.ObjectiveType)}
			if cp.BudgetMode == "BUDGET_MODE_DAY" && cp.Budget > 0 {
				camp.DailyBudgetMicros = ptrI(c.Micros(float64(cp.Budget)))
			}
			out = append(out, camp)
		}
		if data.PageInfo.TotalPage <= page {
			break
		}
	}
	return out, nil
}

// tiktokStatus reads ENABLE, DISABLE and DELETE; a deleted campaign can
// also show only in its secondary status.
func tiktokStatus(op, secondary string) string {
	if strings.Contains(secondary, "DELETE") {
		return c.AdRemoved
	}
	switch op {
	case "ENABLE":
		return c.AdActive
	case "DISABLE":
		return c.AdPaused
	case "DELETE":
		return c.AdRemoved
	}
	return c.AdOther
}

// AdDays reads the integrated report by campaign and day. A daily report
// spans at most 30 days, so longer ranges go in windows.
func (t TikTokAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	var out []c.AdDay
	end := mustDate(to)
	for start := mustDate(from); !start.After(end); start = start.AddDate(0, 0, 30) {
		stop := start.AddDate(0, 0, 29)
		if stop.After(end) {
			stop = end
		}
		for page := 1; page <= maxPages; page++ {
			var data struct {
				List []struct {
					Dimensions struct {
						CampaignID  string `json:"campaign_id"`
						StatTimeDay string `json:"stat_time_day"`
					} `json:"dimensions"`
					Metrics struct {
						CampaignName string  `json:"campaign_name"`
						Spend        string  `json:"spend"`
						Impressions  flexInt `json:"impressions"`
						Clicks       flexInt `json:"clicks"`
						Conversion   *string `json:"conversion"`
					} `json:"metrics"`
				} `json:"list"`
				PageInfo tiktokPage `json:"page_info"`
			}
			err := t.get(ctx, cl, creds["access_token"], "/report/integrated/get/", map[string]any{
				"advertiser_id": acct.ID, "report_type": "BASIC", "data_level": "AUCTION_CAMPAIGN",
				"dimensions": []string{"campaign_id", "stat_time_day"},
				"metrics":    []string{"campaign_name", "spend", "impressions", "clicks", "conversion"},
				"start_date": start.Format("2006-01-02"), "end_date": stop.Format("2006-01-02"),
				"page": page, "page_size": 1000,
			}, &data)
			if err != nil {
				return nil, err
			}
			for _, row := range data.List {
				d := row.Dimensions
				if d.CampaignID == "" || len(d.StatTimeDay) < 10 {
					continue
				}
				day := c.AdDay{CampaignID: d.CampaignID, CampaignName: row.Metrics.CampaignName, Date: d.StatTimeDay[:10],
					SpendMicros: c.MicrosOf(row.Metrics.Spend), Impressions: int64(row.Metrics.Impressions), Clicks: int64(row.Metrics.Clicks)}
				if row.Metrics.Conversion != nil {
					if n := c.ParseNum(*row.Metrics.Conversion); n != nil {
						day.Conversions = ptrF(*n)
					}
				}
				out = append(out, day)
			}
			if data.PageInfo.TotalPage <= page {
				break
			}
		}
	}
	return out, nil
}

// ---- conversions ----

// tiktokEvents are TikTok's standard web events a rule can send as.
var tiktokEvents = []string{"CompletePayment", "CompleteRegistration", "SubmitForm", "Contact", "Subscribe", "PlaceAnOrder", "InitiateCheckout", "AddToCart", "ViewContent"}

// SetupFields: the sign-in token can send events; an Events API access
// token generated in Events Manager is used instead when it is given.
func (TikTokAds) SetupFields() []c.Field {
	return []c.Field{{Name: "events_token", Label: "Events API access token", Secret: true, Optional: true,
		Help: "Only if sending conversions with the sign-in fails: in TikTok Events Manager, open your pixel, Settings, Events API, and generate an access token."}}
}

// ConversionTargets lists the account's pixels.
func (t TikTokAds) ConversionTargets(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.ConversionTarget, error) {
	var out []c.ConversionTarget
	for page := 1; page <= maxPages; page++ {
		var data struct {
			Pixels []struct {
				PixelCode string `json:"pixel_code"`
				PixelName string `json:"pixel_name"`
			} `json:"pixels"`
			PageInfo tiktokPage `json:"page_info"`
		}
		if err := t.get(ctx, cl, creds["access_token"], "/pixel/list/", map[string]any{"advertiser_id": acct.ID, "page": page, "page_size": 20}, &data); err != nil {
			return nil, err
		}
		for _, p := range data.Pixels {
			if p.PixelCode != "" {
				out = append(out, c.ConversionTarget{ID: p.PixelCode, Name: p.PixelName, EventTypes: tiktokEvents})
			}
		}
		if data.PageInfo.TotalPage <= page {
			break
		}
	}
	return out, nil
}

// SendConversions posts web events to the Events API with the click id,
// the _ttp cookie and the hashed email, phone and external id; event_id
// dedupes against the pixel. TikTok takes or refuses a batch whole.
func (t TikTokAds) SendConversions(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, target c.ConversionTarget, convs []c.AdConversion) ([]c.ConversionResult, error) {
	results := make([]c.ConversionResult, len(convs))
	var events []map[string]any
	var sentIdx []int
	for i, cv := range convs {
		results[i] = c.ConversionResult{Index: cv.Index}
		user := map[string]any{}
		var keys []string
		if v := cv.ClickIDs["ttclid"]; v != "" {
			user["ttclid"], keys = v, append(keys, "ttclid")
		}
		if v := cv.ClickIDs["ttp"]; v != "" {
			user["ttp"], keys = v, append(keys, "ttp")
		}
		if len(cv.Emails) > 0 {
			user["email"], keys = cv.Emails[0], append(keys, "email")
		}
		if len(cv.Phones) > 0 {
			user["phone"], keys = cv.Phones[0], append(keys, "phone")
		}
		if cv.ExternalID != "" {
			user["external_id"], keys = cv.ExternalID, append(keys, "external_id")
		}
		if len(keys) == 0 {
			results[i].Skipped, results[i].Reason = true, "no TikTok click id or identifier to match on"
			continue
		}
		kind := cv.EventType
		if kind == "" {
			kind = "CompleteRegistration"
		}
		ev := map[string]any{"event": kind, "event_time": cv.Time.Unix(), "event_id": cv.EventID, "user": user}
		if cv.ValueMicros != nil {
			ev["properties"] = map[string]any{"value": float64(*cv.ValueMicros) / 1e6, "currency": cv.Currency}
		}
		results[i].MatchKeys = keys
		events = append(events, ev)
		sentIdx = append(sentIdx, i)
	}
	if len(events) == 0 {
		return results, nil
	}
	token := creds["events_token"]
	if token == "" {
		token = creds["access_token"]
	}
	var env tiktokEnvelope
	err := newCall(cl, "TikTok Ads").set("Access-Token", token).post(ctx, t.base(cl)+"/event/track/",
		map[string]any{"event_source": "web", "event_source_id": target.ID, "data": events}, &env)
	if err != nil {
		return nil, err
	}
	if err := tiktokData(env, nil); err != nil {
		return nil, err
	}
	for _, i := range sentIdx {
		results[i].OK = true
	}
	return results, nil
}
