package ads

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/outflareai/outflare/engine/internal/adtrack"
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
		"Signs in with TikTok For Business and reads the ad accounts you grant; conversions go back to your TikTok pixel through the Events API. It changes a campaign only when you, or an agent within the grants you set, pause or resume it or set its daily budget (Ad Account Management write access on the app). To add more ad accounts later, connect again and grant them.")
	d.Capabilities = append(d.Capabilities, c.CapAdsConversions, c.CapAdsManage)
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

// ---- managing campaigns ----

func (t TikTokAds) AdCampaign(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) (c.AdCampaign, error) {
	if campaignID == "" || len(campaignID) > 40 {
		return c.AdCampaign{}, c.Errf(c.ErrInvalid, "a TikTok campaign id is required")
	}
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
	}
	err := t.get(ctx, cl, creds["access_token"], "/campaign/get/", map[string]any{
		"advertiser_id": acct.ID, "page": 1, "page_size": 10, "filtering": map[string]any{"campaign_ids": []string{campaignID}},
		"fields": []string{"campaign_id", "campaign_name", "operation_status", "secondary_status", "objective_type", "budget", "budget_mode"},
	}, &data)
	if err != nil {
		return c.AdCampaign{}, err
	}
	for _, cp := range data.List {
		if cp.CampaignID != campaignID {
			continue
		}
		camp := c.AdCampaign{ID: cp.CampaignID, Name: cp.CampaignName, Status: tiktokStatus(cp.OperationStatus, cp.SecondaryStatus),
			ProviderStatus: cp.OperationStatus, Objective: strings.ToLower(cp.ObjectiveType)}
		if cp.BudgetMode == "BUDGET_MODE_DAY" && cp.Budget > 0 {
			camp.DailyBudgetMicros = ptrI(c.Micros(float64(cp.Budget)))
		}
		return camp, nil
	}
	return c.AdCampaign{}, c.Errf(c.ErrInvalid, "TikTok Ads has no campaign %s in account %s", campaignID, acct.ID)
}

// post calls a v1.3 endpoint with a JSON body.
func (t TikTokAds) post(ctx context.Context, cl *c.Client, token, path string, body any) error {
	var env tiktokEnvelope
	if err := newCall(cl, "TikTok Ads").set("Access-Token", token).post(ctx, t.base(cl)+path, body, &env); err != nil {
		return err
	}
	return tiktokData(env, nil)
}

func (t TikTokAds) SetCampaignStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, active bool) error {
	if campaignID == "" || len(campaignID) > 40 {
		return c.Errf(c.ErrInvalid, "a TikTok campaign id is required")
	}
	op := "DISABLE"
	if active {
		op = "ENABLE"
	}
	return t.post(ctx, cl, creds["access_token"], "/campaign/status/update/", map[string]any{"advertiser_id": acct.ID, "campaign_ids": []string{campaignID}, "operation_status": op})
}

// SetCampaignDailyBudget sets a daily budget (BUDGET_MODE_DAY) in the
// account's currency, to the cent.
func (t TikTokAds) SetCampaignDailyBudget(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, micros int64) error {
	if campaignID == "" || len(campaignID) > 40 {
		return c.Errf(c.ErrInvalid, "a TikTok campaign id is required")
	}
	if micros <= 0 {
		return c.Errf(c.ErrInvalid, "the daily budget must be more than nothing")
	}
	return t.post(ctx, cl, creds["access_token"], "/campaign/update/", map[string]any{"advertiser_id": acct.ID, "campaign_id": campaignID,
		"budget_mode": "BUDGET_MODE_DAY", "budget": float64((micros+5_000)/10_000) / 100})
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

// tiktokVideoMetrics say how far a video ad was watched: plays, plays of 2
// and of 6 seconds or more, and plays that reached each quarter.
var tiktokVideoMetrics = []string{"video_play_actions", "video_watched_2s", "video_watched_6s", "video_views_p25", "video_views_p50", "video_views_p75", "video_views_p100"}

// ---- ad groups and ads ----

var _ c.AdItemsReader = TikTokAds{}

// AdItems lists the account's ad groups (/adgroup/get/) or ads
// (/ad/get/), with each ad's text and landing page. TikTok leaves deleted
// ones out unless asked.
func (t TikTokAds) AdItems(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, level string) ([]c.AdItem, error) {
	path, fields := "/adgroup/get/", []string{"adgroup_id", "adgroup_name", "campaign_id", "operation_status", "secondary_status", "budget", "budget_mode"}
	switch level {
	case c.AdLevelGroup:
	case c.AdLevelAd:
		path, fields = "/ad/get/", []string{"ad_id", "ad_name", "adgroup_id", "operation_status", "secondary_status", "ad_text", "landing_page_url"}
	default:
		return nil, c.Errf(c.ErrInvalid, "level is %s or %s", c.AdLevelGroup, c.AdLevelAd)
	}
	var out []c.AdItem
	for page := 1; page <= maxPages; page++ {
		var data struct {
			List []struct {
				AdgroupID       string  `json:"adgroup_id"`
				AdgroupName     string  `json:"adgroup_name"`
				CampaignID      string  `json:"campaign_id"`
				AdID            string  `json:"ad_id"`
				AdName          string  `json:"ad_name"`
				OperationStatus string  `json:"operation_status"`
				SecondaryStatus string  `json:"secondary_status"`
				Budget          flexNum `json:"budget"`
				BudgetMode      string  `json:"budget_mode"`
				AdText          string  `json:"ad_text"`
				LandingPageURL  string  `json:"landing_page_url"`
			} `json:"list"`
			PageInfo tiktokPage `json:"page_info"`
		}
		if err := t.get(ctx, cl, creds["access_token"], path, map[string]any{"advertiser_id": acct.ID, "page": page, "page_size": 1000, "fields": fields}, &data); err != nil {
			return nil, err
		}
		for _, x := range data.List {
			it := c.AdItem{Status: tiktokStatus(x.OperationStatus, x.SecondaryStatus), ProviderStatus: x.OperationStatus}
			if level == c.AdLevelGroup {
				it.ID, it.Name, it.ParentID = x.AdgroupID, x.AdgroupName, x.CampaignID
				if x.BudgetMode == "BUDGET_MODE_DAY" && x.Budget > 0 {
					it.DailyBudgetMicros = ptrI(c.Micros(float64(x.Budget)))
				}
			} else {
				it.ID, it.Name, it.ParentID = x.AdID, x.AdName, x.AdgroupID
				it.Details = adDetails(map[string]string{c.AdDetailText: x.AdText, c.AdDetailURL: x.LandingPageURL})
				if x.LandingPageURL != "" {
					it.Details[c.AdDetailTemplated] = strconv.FormatBool(adtrack.HasTemplate(x.LandingPageURL))
				}
			}
			if it.ID != "" {
				out = append(out, it)
			}
		}
		if data.PageInfo.TotalPage <= page {
			break
		}
	}
	return out, nil
}

// AdItemDays reads the integrated report by ad group or ad and day, in
// windows of at most 30 days like AdDays.
func (t TikTokAds) AdItemDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, level, from, to string) ([]c.AdItemDay, error) {
	dataLevel, dim, name, parent := "AUCTION_ADGROUP", "adgroup_id", "adgroup_name", "campaign_id"
	switch level {
	case c.AdLevelGroup:
	case c.AdLevelAd:
		dataLevel, dim, name, parent = "AUCTION_AD", "ad_id", "ad_name", "adgroup_id"
	default:
		return nil, c.Errf(c.ErrInvalid, "level is %s or %s", c.AdLevelGroup, c.AdLevelAd)
	}
	var out []c.AdItemDay
	end := mustDate(to)
	for start := mustDate(from); !start.After(end); start = start.AddDate(0, 0, 30) {
		stop := start.AddDate(0, 0, 29)
		if stop.After(end) {
			stop = end
		}
		for page := 1; page <= maxPages; page++ {
			var data struct {
				List []struct {
					Dimensions map[string]string `json:"dimensions"`
					Metrics    map[string]any    `json:"metrics"`
				} `json:"list"`
				PageInfo tiktokPage `json:"page_info"`
			}
			err := t.get(ctx, cl, creds["access_token"], "/report/integrated/get/", map[string]any{
				"advertiser_id": acct.ID, "report_type": "BASIC", "data_level": dataLevel,
				"dimensions": []string{dim, "stat_time_day"},
				"metrics":    append([]string{name, parent, "spend", "impressions", "clicks", "conversion"}, tiktokVideoMetrics...),
				"start_date": start.Format("2006-01-02"), "end_date": stop.Format("2006-01-02"),
				"page": page, "page_size": 1000,
			}, &data)
			if err != nil {
				return nil, err
			}
			for _, row := range data.List {
				id, date := row.Dimensions[dim], row.Dimensions["stat_time_day"]
				if id == "" || len(date) < 10 {
					continue
				}
				// TikTok sends numbers as strings, or as numbers.
				text := func(k string) string {
					switch v := row.Metrics[k].(type) {
					case string:
						return v
					case float64:
						return strconv.FormatFloat(v, 'f', -1, 64)
					}
					return ""
				}
				day := c.AdItemDay{ID: id, ParentID: text(parent), Name: text(name), Date: date[:10], SpendMicros: c.MicrosOf(text("spend"))}
				if n := c.ParseNum(text("impressions")); n != nil {
					day.Impressions = int64(*n)
				}
				if n := c.ParseNum(text("clicks")); n != nil {
					day.Clicks = int64(*n)
				}
				if n := c.ParseNum(text("conversion")); n != nil {
					day.Conversions = ptrF(*n)
				}
				// How far the video was watched. A row with no plays at all
				// is an ad without a video, and keeps these empty.
				watched := func(k string) *int64 {
					if n := c.ParseNum(text(k)); n != nil && *n >= 0 {
						return ptrI(int64(*n))
					}
					return nil
				}
				if started, plays := watched("video_play_actions"), watched("video_watched_2s"); plays != nil && (*plays > 0 || (started != nil && *started > 0)) {
					day.VideoViews, day.VideoThruPlays = plays, watched("video_watched_6s")
					day.VideoP25, day.VideoP50, day.VideoP75, day.VideoP100 = watched("video_views_p25"), watched("video_views_p50"), watched("video_views_p75"), watched("video_views_p100")
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

// Changes below the campaign: an ad group or an ad is paused or resumed
// (/adgroup/status/update/, /ad/status/update/), and an ad group's own
// daily budget set (/adgroup/update/).
var (
	_ c.AdGroupManager       = TikTokAds{}
	_ c.AdGroupBudgetManager = TikTokAds{}
	_ c.AdStatusManager      = TikTokAds{}
)

func tiktokOp(active bool) string {
	if active {
		return "ENABLE"
	}
	return "DISABLE"
}

// tiktokID is an id TikTok gave: digits, checked before it is sent back.
func tiktokID(id string) bool {
	if id == "" || len(id) > 40 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (t TikTokAds) SetAdGroupStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, adGroupID string, active bool) error {
	if !tiktokID(adGroupID) {
		return c.Errf(c.ErrInvalid, "TikTok ad group ids are numbers")
	}
	return t.post(ctx, cl, creds["access_token"], "/adgroup/status/update/", map[string]any{"advertiser_id": acct.ID, "adgroup_ids": []string{adGroupID}, "operation_status": tiktokOp(active)})
}

func (t TikTokAds) SetAdStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, adGroupID, adID string, active bool) error {
	if !tiktokID(adID) {
		return c.Errf(c.ErrInvalid, "TikTok ad ids are numbers")
	}
	return t.post(ctx, cl, creds["access_token"], "/ad/status/update/", map[string]any{"advertiser_id": acct.ID, "ad_ids": []string{adID}, "operation_status": tiktokOp(active)})
}

// SetAdGroupDailyBudget sets an ad group's own daily budget, to the cent.
// TikTok refuses it for an ad group whose campaign holds the budget.
func (t TikTokAds) SetAdGroupDailyBudget(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, adGroupID string, micros int64) error {
	if !tiktokID(adGroupID) {
		return c.Errf(c.ErrInvalid, "TikTok ad group ids are numbers")
	}
	if micros <= 0 {
		return c.Errf(c.ErrInvalid, "the daily budget must be more than nothing")
	}
	return t.post(ctx, cl, creds["access_token"], "/adgroup/update/", map[string]any{"advertiser_id": acct.ID, "adgroup_id": adGroupID, "budget": float64((micros+5_000)/10_000) / 100})
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
		// Where the person was, beside the identifiers above and never the
		// only thing sent: the platform matches on them unhashed.
		if cv.IPAddress != "" {
			user["ip"], keys = cv.IPAddress, append(keys, "ip_address")
		}
		if cv.UserAgent != "" {
			user["user_agent"], keys = cv.UserAgent, append(keys, "user_agent")
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
