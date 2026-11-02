package ads

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/outflareai/outflare/engine/internal/adtrack"
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

// metaConversions maps Meta's standard conversion actions to the kind of
// conversion they count. Meta reports one purchase several times (as
// purchase, omni_purchase and the pixel's own), so each kind counts once,
// at its largest. Everything else (link clicks, video views) is
// engagement; custom conversions are listed by kind but not counted.
var metaConversions = map[string]string{
	"purchase": "purchase", "omni_purchase": "purchase", "offsite_conversion.fb_pixel_purchase": "purchase",
	"lead": "lead", "offsite_conversion.fb_pixel_lead": "lead", "onsite_web_lead": "lead",
	"complete_registration": "complete_registration", "omni_complete_registration": "complete_registration",
	"offsite_conversion.fb_pixel_complete_registration": "complete_registration",
	"start_trial": "start_trial", "omni_start_trial": "start_trial", "start_trial_total": "start_trial",
	"offsite_conversion.fb_pixel_start_trial": "start_trial",
	"subscribe": "subscribe", "omni_subscribe": "subscribe", "subscribe_total": "subscribe",
	"offsite_conversion.fb_pixel_subscribe": "subscribe",
}

// metaCustom is the prefix of a custom conversion's action type.
const metaCustom = "offsite_conversion.custom."

func (MetaAds) Definition() c.Definition {
	d := def("meta_ads", "Meta Ads", "https://www.facebook.com/business/ads",
		"https://developers.facebook.com/docs/marketing-api/insights", "https://www.facebook.com/business/help/755172054692222",
		"Spend, clicks and conversions per campaign from your Facebook and Instagram ad accounts.",
		"Reads every ad account a token can see, with read-only access. The simplest way: in your Meta Business Settings, Users, System users, create a system user, give it the ad accounts, and generate a token with the ads_read permission. Conversions are Meta's standard events (purchases, leads, registrations, trials, subscriptions). To send conversions back through the Conversions API, also assign the system user to your pixel (Business Settings, Data sources, Datasets). For journeys to add people to your customer list audiences, give the token ads_management too and accept the Custom Audience terms in your ad account.")
	d.Capabilities = append(d.Capabilities, c.CapAdsConversions, c.CapAdsAudiences)
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
		"fields":         {"campaign_id,campaign_name,spend,impressions,clicks,reach,actions,action_values"},
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
			Reach        *flexInt     `json:"reach"`
			Actions      []metaAction `json:"actions"`
			ActionValues []metaAction `json:"action_values"`
		}
		if json.Unmarshal(raw, &r) != nil || r.CampaignID == "" {
			return nil
		}
		conv, value, extra := metaResults(r.Actions, r.ActionValues, r.Reach)
		out = append(out, c.AdDay{CampaignID: r.CampaignID, CampaignName: r.CampaignName, Date: r.DateStart,
			SpendMicros: c.MicrosOf(r.Spend), Impressions: int64(r.Impressions), Clicks: int64(r.Clicks),
			Conversions: ptrF(conv), ConversionValueMicros: ptrI(c.Micros(value)), AdExtra: extra})
		return nil
	})
	return out, err
}

// AdItems lists ad sets (AdLevelGroup) or ads (AdLevelAd), with each ad's
// creative: its title, text, link, URL parameters and preview image.
func (m MetaAds) AdItems(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, level string) ([]c.AdItem, error) {
	u := m.base(cl) + "/act_" + esc(acct.ID)
	switch level {
	case c.AdLevelGroup:
		u += "/adsets?fields=id,name,effective_status,campaign_id,daily_budget&limit=200"
	case c.AdLevelAd:
		u += "/ads?" + url.Values{"fields": {"id,name,effective_status,adset_id,creative{title,body,thumbnail_url,url_tags,object_story_spec}"}, "limit": {"200"}}.Encode()
	default:
		return nil, c.Errf(c.ErrInvalid, "level is %s or %s", c.AdLevelGroup, c.AdLevelAd)
	}
	var out []c.AdItem
	err := m.pages(ctx, cl, creds, u, func(raw json.RawMessage) error {
		var x struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			EffectiveStatus string `json:"effective_status"`
			CampaignID      string `json:"campaign_id"`
			AdsetID         string `json:"adset_id"`
			DailyBudget     string `json:"daily_budget"`
			Creative        struct {
				Title           string `json:"title"`
				Body            string `json:"body"`
				ThumbnailURL    string `json:"thumbnail_url"`
				URLTags         string `json:"url_tags"`
				ObjectStorySpec struct {
					LinkData struct {
						Link    string `json:"link"`
						Name    string `json:"name"`
						Message string `json:"message"`
					} `json:"link_data"`
					VideoData struct {
						Title        string `json:"title"`
						Message      string `json:"message"`
						CallToAction struct {
							Value struct {
								Link string `json:"link"`
							} `json:"value"`
						} `json:"call_to_action"`
					} `json:"video_data"`
				} `json:"object_story_spec"`
			} `json:"creative"`
		}
		if json.Unmarshal(raw, &x) != nil || x.ID == "" {
			return nil
		}
		it := c.AdItem{ID: x.ID, Name: x.Name, Status: status(x.EffectiveStatus), ProviderStatus: x.EffectiveStatus}
		if level == c.AdLevelGroup {
			it.ParentID = x.CampaignID
			if b := atoi64(x.DailyBudget); b > 0 {
				it.DailyBudgetMicros = ptrI(b * 1e6 / minorUnits(acct.Currency))
			}
		} else {
			cr, story := x.Creative, x.Creative.ObjectStorySpec
			it.ParentID = x.AdsetID
			it.Details = adDetails(map[string]string{
				c.AdDetailHeadline:  firstText(cr.Title, story.LinkData.Name, story.VideoData.Title),
				c.AdDetailText:      firstText(cr.Body, story.LinkData.Message, story.VideoData.Message),
				c.AdDetailURL:       firstText(story.LinkData.Link, story.VideoData.CallToAction.Value.Link),
				c.AdDetailURLTags:   cr.URLTags,
				c.AdDetailThumbnail: cr.ThumbnailURL,
			})
			// A creative whose link Meta keeps elsewhere (dynamic creative,
			// catalog ads) says nothing either way.
			if it.Details[c.AdDetailURL] != "" || cr.URLTags != "" {
				it.Details[c.AdDetailTemplated] = strconv.FormatBool(adtrack.HasTemplate(cr.URLTags, it.Details[c.AdDetailURL]))
			}
		}
		out = append(out, it)
		return nil
	})
	return out, err
}

// AdItemDays reads each ad set's or ad's insights per day.
func (m MetaAds) AdItemDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, level, from, to string) ([]c.AdItemDay, error) {
	var metaLevel, fields string
	switch level {
	case c.AdLevelGroup:
		metaLevel, fields = "adset", "adset_id,adset_name,campaign_id"
	case c.AdLevelAd:
		metaLevel, fields = "ad", "ad_id,ad_name,adset_id"
	default:
		return nil, c.Errf(c.ErrInvalid, "level is %s or %s", c.AdLevelGroup, c.AdLevelAd)
	}
	tr, _ := json.Marshal(map[string]string{"since": from, "until": to})
	q := url.Values{
		"level":          {metaLevel},
		"time_increment": {"1"},
		"time_range":     {string(tr)},
		"fields":         {fields + ",spend,impressions,clicks,reach,actions,action_values"},
		"limit":          {"500"},
	}
	var out []c.AdItemDay
	err := m.pages(ctx, cl, creds, m.base(cl)+"/act_"+esc(acct.ID)+"/insights?"+q.Encode(), func(raw json.RawMessage) error {
		var r struct {
			AdsetID      string       `json:"adset_id"`
			AdsetName    string       `json:"adset_name"`
			CampaignID   string       `json:"campaign_id"`
			AdID         string       `json:"ad_id"`
			AdName       string       `json:"ad_name"`
			DateStart    string       `json:"date_start"`
			Spend        string       `json:"spend"`
			Impressions  flexInt      `json:"impressions"`
			Clicks       flexInt      `json:"clicks"`
			Reach        *flexInt     `json:"reach"`
			Actions      []metaAction `json:"actions"`
			ActionValues []metaAction `json:"action_values"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return nil
		}
		d := c.AdItemDay{ID: r.AdsetID, ParentID: r.CampaignID, Name: r.AdsetName}
		if level == c.AdLevelAd {
			d = c.AdItemDay{ID: r.AdID, ParentID: r.AdsetID, Name: r.AdName}
		}
		if d.ID == "" {
			return nil
		}
		conv, value, extra := metaResults(r.Actions, r.ActionValues, r.Reach)
		d.AdExtra = extra
		d.Date, d.SpendMicros, d.Impressions, d.Clicks = r.DateStart, c.MicrosOf(r.Spend), int64(r.Impressions), int64(r.Clicks)
		d.Conversions, d.ConversionValueMicros = ptrF(conv), ptrI(c.Micros(value))
		out = append(out, d)
		return nil
	})
	return out, err
}

// metaResults reads a row's actions: its standard conversions and their
// value, each kind once, the conversions by kind (custom conversions
// too), and its link clicks, landing page views, 3-second video views and
// reach.
func metaResults(actions, values []metaAction, reach *flexInt) (conv, value float64, extra c.AdExtra) {
	kind := func(t string) string {
		if k := metaConversions[t]; k != "" {
			return k
		}
		if strings.HasPrefix(t, metaCustom) {
			return "custom." + strings.TrimPrefix(t, metaCustom)
		}
		return ""
	}
	counts, worth := map[string]float64{}, map[string]float64{}
	var order []string
	for _, a := range actions {
		switch a.ActionType {
		case "link_click":
			extra.LinkClicks = ptrI(int64(a.Value))
		case "landing_page_view":
			extra.LandingPageViews = ptrI(int64(a.Value))
		case "video_view":
			extra.VideoViews = ptrI(int64(a.Value))
		}
		if k := kind(a.ActionType); k != "" {
			if _, ok := counts[k]; !ok {
				order = append(order, k)
			}
			counts[k] = max(counts[k], float64(a.Value))
		}
	}
	for _, a := range values {
		if k := kind(a.ActionType); k != "" {
			worth[k] = max(worth[k], float64(a.Value))
		}
	}
	for _, k := range order {
		x := c.AdAction{Name: k, Conversions: counts[k]}
		if v, ok := worth[k]; ok {
			x.ValueMicros = ptrI(c.Micros(v))
		}
		extra.Actions = append(extra.Actions, x)
		if !strings.HasPrefix(k, "custom.") {
			conv += counts[k]
			value += worth[k]
		}
	}
	if reach != nil {
		extra.Reach = ptrI(int64(*reach))
	}
	return conv, value, extra
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

// ---- conversions ----

// metaEvents are Meta's standard events a rule can send as.
var metaEvents = []string{"Purchase", "Lead", "CompleteRegistration", "StartTrial", "Subscribe", "Contact", "SubmitApplication", "Schedule"}

// ConversionTargets lists the pixels (datasets) the account can use.
func (m MetaAds) ConversionTargets(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.ConversionTarget, error) {
	var out []c.ConversionTarget
	err := m.pages(ctx, cl, creds, m.base(cl)+"/act_"+esc(acct.ID)+"/adspixels?fields=id,name&limit=100", func(raw json.RawMessage) error {
		var p struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &p) == nil && p.ID != "" {
			out = append(out, c.ConversionTarget{ID: p.ID, Name: p.Name, EventTypes: metaEvents})
		}
		return nil
	})
	return out, err
}

// SendConversions sends server events through the Conversions API. The
// event id matches the browser pixel's eventID, so Meta counts a
// conversion once when both send it. Meta takes or refuses a batch whole.
func (m MetaAds) SendConversions(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, target c.ConversionTarget, convs []c.AdConversion) ([]c.ConversionResult, error) {
	results := make([]c.ConversionResult, len(convs))
	var data []map[string]any
	var sentIdx []int
	for i, cv := range convs {
		results[i] = c.ConversionResult{Index: cv.Index}
		user := map[string]any{}
		var keys []string
		if len(cv.Emails) > 0 {
			user["em"], keys = cv.Emails, append(keys, "email")
		}
		if len(cv.PhoneDigits) > 0 {
			user["ph"], keys = cv.PhoneDigits, append(keys, "phone")
		}
		if cv.ExternalID != "" {
			user["external_id"], keys = []string{cv.ExternalID}, append(keys, "external_id")
		}
		switch {
		case cv.ClickIDs["fbc"] != "":
			user["fbc"], keys = cv.ClickIDs["fbc"], append(keys, "fbc")
		case cv.ClickIDs["fbclid"] != "":
			at := cv.ClickTime
			if at.IsZero() {
				at = cv.Time
			}
			user["fbc"], keys = fmt.Sprintf("fb.1.%d.%s", at.UnixMilli(), cv.ClickIDs["fbclid"]), append(keys, "fbc")
		}
		if v := cv.ClickIDs["fbp"]; v != "" {
			user["fbp"], keys = v, append(keys, "fbp")
		}
		if len(keys) == 0 {
			results[i].Skipped, results[i].Reason = true, "no identifier or click id to match on"
			continue
		}
		event := cv.EventType
		if event == "" {
			event = "Lead"
		}
		ev := map[string]any{"event_name": event, "event_time": cv.Time.Unix(), "event_id": cv.EventID,
			"action_source": "website", "user_data": user}
		if cv.ValueMicros != nil {
			ev["custom_data"] = map[string]any{"value": float64(*cv.ValueMicros) / 1e6, "currency": cv.Currency}
		}
		results[i].MatchKeys = keys
		data = append(data, ev)
		sentIdx = append(sentIdx, i)
	}
	if len(data) == 0 {
		return results, nil
	}
	var resp struct {
		EventsReceived int `json:"events_received"`
	}
	if err := m.call(cl, creds).post(ctx, m.base(cl)+"/"+esc(target.ID)+"/events", map[string]any{"data": data}, &resp); err != nil {
		return nil, err
	}
	for _, i := range sentIdx {
		results[i].OK = true
	}
	return results, nil
}

// ---- audiences ----

// Audiences lists the account's custom audiences made from a customer
// list, the only kind people can be added to by hand.
func (m MetaAds) Audiences(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdAudience, error) {
	var out []c.AdAudience
	err := m.pages(ctx, cl, creds, m.base(cl)+"/act_"+esc(acct.ID)+"/customaudiences?fields=id,name,subtype&limit=100", func(raw json.RawMessage) error {
		var a struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Subtype string `json:"subtype"`
		}
		if json.Unmarshal(raw, &a) == nil && a.ID != "" && a.Subtype == "CUSTOM" {
			out = append(out, c.AdAudience{ID: a.ID, Name: a.Name})
		}
		return nil
	})
	return out, err
}

// SyncAudience adds the person's hashed emails and phone numbers to a
// custom audience, or removes them, one identifier kind per call. Meta
// matches them on its side and never says whether they matched.
func (m MetaAds) SyncAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, audienceID, change string, p c.AudienceMember) error {
	method := http.MethodPost
	if change == c.AudienceRemove {
		method = http.MethodDelete
	}
	for _, k := range []struct {
		schema string
		ids    []string
	}{{"EMAIL_SHA256", p.Emails}, {"PHONE_SHA256", p.PhoneDigits}} {
		if len(k.ids) == 0 {
			continue
		}
		rows := make([][]string, len(k.ids))
		for i, id := range k.ids {
			rows[i] = []string{id}
		}
		body := map[string]any{"payload": map[string]any{"schema": []string{k.schema}, "data": rows}}
		if err := m.call(cl, creds).do(ctx, method, m.base(cl)+"/"+esc(audienceID)+"/users", body, nil); err != nil {
			return err
		}
	}
	return nil
}
