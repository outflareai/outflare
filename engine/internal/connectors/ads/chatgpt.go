package ads

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ChatGPTAds reads and manages ads in ChatGPT through OpenAI's Advertiser
// API (api.ads.openai.com/v1), and sends conversions through its
// Conversions API (bzr.openai.com). An Advertiser API key belongs to one ad
// account; the workspace makes its own in Ads Manager, Settings, and a
// separate conversion key for sending conversions.
type ChatGPTAds struct{}

const (
	chatgptBase       = "https://api.ads.openai.com/v1"
	chatgptEventsBase = "https://bzr.openai.com/v1"
	// chatgptDays is the longest range one insights call takes.
	chatgptDays = 365
	// chatgptConvRows is the most rows a conversions report returns; it
	// has no cursor, so a call names at most that many campaign days.
	chatgptConvRows = 2000
	// chatgptBatch is the most events one Conversions API call takes.
	chatgptBatch = 1000
)

var (
	_ c.AdsManager       = ChatGPTAds{}
	_ c.ConversionSender = ChatGPTAds{}
	_ c.SetupNeeder      = ChatGPTAds{}
)

func (ChatGPTAds) Definition() c.Definition {
	d := def("chatgpt_ads", "ChatGPT Ads", "https://ads.openai.com",
		"https://developers.openai.com/ads", "https://ads.openai.com/settings",
		"Spend, impressions, clicks and conversions per campaign from your ChatGPT ad account, and pause, resume and budget changes.",
		"Uses your own Advertiser API key from OpenAI Ads Manager (Settings). A key reaches one ad account, so connect each account with its own key. Add a pixel ID and a conversion key to send conversions back through OpenAI's Conversions API.")
	d.Auth = c.AuthAPIKey
	d.Capabilities = append(d.Capabilities, c.CapAdsConversions, c.CapAdsManage)
	d.Fields = []c.Field{
		{Name: "api_key", Label: "Advertiser API key", Secret: true,
			Help: "In OpenAI Ads Manager (ads.openai.com), Settings, create an API key for this ad account."},
	}
	return d
}

func (ChatGPTAds) base(cl *c.Client) string { return cl.Base("chatgpt_ads", chatgptBase) }

func (ChatGPTAds) call(cl *c.Client, creds c.Creds) *call {
	return newCall(cl, "ChatGPT Ads").bearer(creds["api_key"])
}

// chatgptIDRe matches OpenAI's ids (cmpn_…, adacct_…).
var chatgptIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

type chatgptAccount struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Timezone     string `json:"timezone"`
	CurrencyCode string `json:"currency_code"`
}

func (g ChatGPTAds) account(ctx context.Context, cl *c.Client, creds c.Creds) (chatgptAccount, error) {
	var a chatgptAccount
	if creds["api_key"] == "" {
		return a, c.Errf(c.ErrAuth, "add your Advertiser API key from OpenAI Ads Manager, Settings")
	}
	if err := g.call(cl, creds).get(ctx, g.base(cl)+"/ad_account", &a); err != nil {
		return a, err
	}
	if a.ID == "" {
		return a, c.Errf(c.ErrUnavailable, "OpenAI returned no ad account for this key")
	}
	return a, nil
}

func (g ChatGPTAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	a, err := g.account(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: a.ID, Name: a.Name, Details: map[string]string{"accounts": "1", "status": a.Status}}, nil
}

// AdAccounts is the key's one ad account.
func (g ChatGPTAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	a, err := g.account(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	return []c.AdAccount{{ID: a.ID, Name: a.Name, Currency: strings.ToUpper(a.CurrencyCode), TimeZone: a.Timezone}}, nil
}

type chatgptCampaign struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	BiddingType string `json:"bidding_type"`
	Objective   string `json:"objective"`
	Budget      struct {
		Daily    *int64 `json:"daily_spend_limit_micros"`
		Lifetime *int64 `json:"lifetime_spend_limit_micros"`
	} `json:"budget"`
}

func (cp chatgptCampaign) campaign() c.AdCampaign {
	camp := c.AdCampaign{ID: cp.ID, Name: cp.Name, Status: status(cp.Status), ProviderStatus: cp.Status,
		Objective: strings.ToLower(firstText(cp.Objective, cp.BiddingType))}
	if cp.Budget.Daily != nil && *cp.Budget.Daily > 0 {
		camp.DailyBudgetMicros = ptrI(*cp.Budget.Daily)
	}
	switch cp.BiddingType {
	case "clicks":
		camp.BiddingStrategy = "fixed_bid"
	case "conversions":
		camp.BiddingStrategy = "optimized_cpc"
	}
	return camp
}

// checkKey refuses a key that doesn't reach the account a change names.
func (g ChatGPTAds) checkKey(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) error {
	a, err := g.account(ctx, cl, creds)
	if err != nil {
		return err
	}
	if acct.ID != "" && a.ID != acct.ID {
		return c.Errf(c.ErrInvalid, "this connection's key reaches ad account %s, not %s", a.ID, acct.ID)
	}
	return nil
}

func (g ChatGPTAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	if err := g.checkKey(ctx, cl, creds, acct); err != nil {
		return nil, err
	}
	var out []c.AdCampaign
	q := url.Values{"limit": {"500"}}
	for i := 0; i < maxPages; i++ {
		var resp struct {
			Data    []chatgptCampaign `json:"data"`
			LastID  *string           `json:"last_id"`
			HasMore bool              `json:"has_more"`
		}
		if err := g.call(cl, creds).get(ctx, g.base(cl)+"/campaigns?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, cp := range resp.Data {
			out = append(out, cp.campaign())
		}
		if !resp.HasMore || resp.LastID == nil || *resp.LastID == "" {
			break
		}
		q.Set("after", *resp.LastID)
	}
	return out, nil
}

// micros turns a decimal amount in the account's currency into micros.
func chatgptMicros(v float64) int64 { return int64(math.Round(v * 1e6)) }

// dateRange is an insights time range of whole local days, until inclusive.
func chatgptRange(from, to string) string {
	raw, _ := json.Marshal(map[string]string{"type": "date_range", "since": from, "until": to})
	return string(raw)
}

// AdDays reads spend, impressions and clicks per campaign and day, then
// the campaigns' goal conversions and purchase value from the conversions
// report (30-day click, 1-day view, by the ad's day). A refused
// conversions report leaves the spend as it is.
func (g ChatGPTAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	all := days(from, to)
	var out []c.AdDay
	index := map[string]int{}
	for start := 0; start < len(all); start += chatgptDays {
		window := all[start:min(start+chatgptDays, len(all))]
		q := url.Values{
			"time_granularity":  {"daily"},
			"aggregation_level": {"campaign"},
			"limit":             {"2000"},
			"time_ranges[]":     {chatgptRange(window[0], window[len(window)-1])},
			"fields[]":          {"metadata.readable_time", "campaign.id", "campaign.name", "campaign.impressions", "campaign.clicks", "campaign.spend"},
		}
		for i := 0; i < maxPages; i++ {
			var resp struct {
				Data []struct {
					ReadableTime string   `json:"readable_time"`
					CampaignID   string   `json:"campaign_id"`
					CampaignName string   `json:"campaign_name"`
					Impressions  flexInt  `json:"impressions"`
					Clicks       flexInt  `json:"clicks"`
					Spend        *flexNum `json:"spend"`
				} `json:"data"`
				LastID  *string `json:"last_id"`
				HasMore bool    `json:"has_more"`
			}
			if err := g.call(cl, creds).get(ctx, g.base(cl)+"/ad_account/insights?"+q.Encode(), &resp); err != nil {
				return nil, err
			}
			for _, r := range resp.Data {
				var spend int64
				if r.Spend != nil {
					spend = chatgptMicros(float64(*r.Spend))
				}
				if r.CampaignID == "" || len(r.ReadableTime) < 10 || spend == 0 && r.Impressions == 0 && r.Clicks == 0 {
					continue
				}
				d := c.AdDay{CampaignID: r.CampaignID, CampaignName: r.CampaignName, Date: r.ReadableTime[:10],
					SpendMicros: spend, Impressions: int64(r.Impressions), Clicks: int64(r.Clicks)}
				index[d.CampaignID+"|"+d.Date] = len(out)
				out = append(out, d)
			}
			if !resp.HasMore || resp.LastID == nil || *resp.LastID == "" {
				break
			}
			q.Set("after", *resp.LastID)
		}
	}
	if len(out) == 0 {
		return out, nil
	}
	if err := g.addConversions(ctx, cl, creds, all, out, index); err != nil && c.KindOf(err) != c.ErrInvalid {
		return nil, err
	}
	return out, nil
}

// addConversions fills in conversions for the campaign days in out. The
// report has no cursor and stops at 2,000 rows, so each call names a
// month and no more campaigns than fit.
func (g ChatGPTAds) addConversions(ctx context.Context, cl *c.Client, creds c.Creds, all []string, out []c.AdDay, index map[string]int) error {
	const month = 30
	for start := 0; start < len(all); start += month {
		window := all[start:min(start+month, len(all))]
		seen := map[string]bool{}
		var ids []string
		for _, d := range out {
			if d.Date >= window[0] && d.Date <= window[len(window)-1] && !seen[d.CampaignID] {
				seen[d.CampaignID] = true
				ids = append(ids, d.CampaignID)
			}
		}
		per := max(1, chatgptConvRows/len(window))
		for i := 0; i < len(ids); i += per {
			batch := ids[i:min(i+per, len(ids))]
			var resp struct {
				Data []struct {
					EntityID    string   `json:"entity_id"`
					Date        string   `json:"date"`
					Conversions *flexNum `json:"conversions"`
					Sales       *string  `json:"order_created_attributed_sales"`
				} `json:"data"`
			}
			body := map[string]any{"aggregation_level": "campaign", "time_granularity": "daily", "entity_ids": batch,
				"time_ranges": []string{chatgptRange(window[0], window[len(window)-1])}}
			if err := g.call(cl, creds).post(ctx, g.base(cl)+"/conversions/insights", body, &resp); err != nil {
				return err
			}
			for _, r := range resp.Data {
				if len(r.Date) < 10 {
					continue
				}
				k, ok := index[r.EntityID+"|"+r.Date[:10]]
				if !ok {
					continue
				}
				if r.Conversions != nil {
					out[k].Conversions = ptrF(float64(*r.Conversions))
				}
				if r.Sales != nil {
					if v, err := strconv.ParseFloat(*r.Sales, 64); err == nil && v > 0 {
						out[k].ConversionValueMicros = ptrI(chatgptMicros(v))
					}
				}
			}
		}
	}
	return nil
}

// ---- managing campaigns ----

func (g ChatGPTAds) campaign(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, id string) (chatgptCampaign, error) {
	var cp chatgptCampaign
	if !chatgptIDRe.MatchString(id) {
		return cp, c.Errf(c.ErrInvalid, "ChatGPT Ads campaign ids are letters, digits and underscores")
	}
	if err := g.checkKey(ctx, cl, creds, acct); err != nil {
		return cp, err
	}
	if err := g.call(cl, creds).get(ctx, g.base(cl)+"/campaigns/"+esc(id), &cp); err != nil {
		return cp, err
	}
	if cp.ID != id {
		return cp, c.Errf(c.ErrInvalid, "ChatGPT Ads has no campaign %s in account %s", id, acct.ID)
	}
	return cp, nil
}

func (g ChatGPTAds) AdCampaign(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) (c.AdCampaign, error) {
	cp, err := g.campaign(ctx, cl, creds, acct, campaignID)
	if err != nil {
		return c.AdCampaign{}, err
	}
	return cp.campaign(), nil
}

// SetCampaignStatus activates or pauses a campaign. Activating starts
// delivery only once its ad groups and ads are active and approved.
func (g ChatGPTAds) SetCampaignStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, active bool) error {
	cp, err := g.campaign(ctx, cl, creds, acct, campaignID)
	if err != nil {
		return err
	}
	if cp.Status == "archived" {
		return c.Errf(c.ErrInvalid, "the campaign is archived; OpenAI can't run it again")
	}
	verb := "pause"
	if active {
		verb = "activate"
	}
	return g.update(ctx, cl, creds, "/campaigns/"+esc(campaignID)+"/"+verb, nil)
}

// SetCampaignDailyBudget sets a campaign's daily spend limit, in micros,
// rounded to the cent. OpenAI takes at least 1 of the account's currency.
func (g ChatGPTAds) SetCampaignDailyBudget(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, micros int64) error {
	if micros <= 0 {
		return c.Errf(c.ErrInvalid, "the daily budget must be more than nothing")
	}
	micros = (micros + 5_000) / 10_000 * 10_000
	if micros < 1_000_000 {
		return c.Errf(c.ErrInvalid, "ChatGPT Ads takes a daily budget of at least 1 %s", firstText(acct.Currency, "unit of the account's currency"))
	}
	cp, err := g.campaign(ctx, cl, creds, acct, campaignID)
	if err != nil {
		return err
	}
	if cp.Budget.Daily == nil && cp.Budget.Lifetime != nil {
		return c.Errf(c.ErrInvalid, "the campaign has a lifetime budget only; give it a daily budget in OpenAI Ads Manager first")
	}
	return g.update(ctx, cl, creds, "/campaigns/"+esc(campaignID), map[string]any{"budget": map[string]any{"daily_spend_limit_micros": micros}})
}

// update posts a change, and says what to fix when the key can't write.
func (g ChatGPTAds) update(ctx context.Context, cl *c.Client, creds c.Creds, path string, body any) error {
	if body == nil {
		body = map[string]any{}
	}
	err := g.call(cl, creds).post(ctx, g.base(cl)+path, body, nil)
	if c.KindOf(err) == c.ErrAuth {
		return c.Errf(c.ErrAuth, "OpenAI refused the change: check that the Advertiser API key on the connection is still active in Ads Manager, Settings (%v)", err)
	}
	return err
}

// ---- conversions ----

// chatgptEvents are the Conversions API's standard events a rule can send
// as, with the data shape each takes.
var chatgptEvents = map[string]string{
	"registration_completed": "customer_action",
	"order_created":          "contents",
	"lead_created":           "customer_action",
	"subscription_created":   "plan_enrollment",
	"trial_started":          "plan_enrollment",
	"checkout_started":       "contents",
	"appointment_scheduled":  "customer_action",
	"items_added":            "contents",
	"contents_viewed":        "contents",
	"page_viewed":            "contents",
}

var chatgptEventOrder = []string{"registration_completed", "order_created", "lead_created", "subscription_created", "trial_started",
	"checkout_started", "appointment_scheduled", "items_added", "contents_viewed", "page_viewed"}

// SetupFields: sending conversions takes the pixel ID and a conversion key
// from Ads Manager's conversions tab; the website address is the
// conversions' source URL, which OpenAI asks for on web events.
func (ChatGPTAds) SetupFields() []c.Field {
	return []c.Field{
		{Name: "pixel_id", Label: "Pixel ID", Optional: true,
			Help: "Only to send conversions to ChatGPT Ads: in OpenAI Ads Manager, Conversions, your data source's Pixel ID."},
		{Name: "conversions_key", Label: "Conversion key", Secret: true, Optional: true,
			Help: "In OpenAI Ads Manager, Conversions, Manage conversion keys, create a key. The Advertiser API key can't send conversions."},
		{Name: "website", Label: "Website address", Optional: true,
			Help: "The site the conversions happen on, such as https://acme.example. OpenAI asks for it on every web conversion."},
	}
}

var chatgptPixelRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func chatgptConvCreds(creds c.Creds) (pixel, key string, err error) {
	pixel, key = strings.TrimSpace(creds["pixel_id"]), creds["conversions_key"]
	if pixel == "" || key == "" {
		return "", "", c.Errf(c.ErrAuth, "ChatGPT Ads needs the pixel ID and a conversion key to send conversions: add them on the connection")
	}
	if !chatgptPixelRe.MatchString(pixel) {
		return "", "", c.Errf(c.ErrInvalid, "the ChatGPT Ads pixel ID is letters, digits and underscores")
	}
	return pixel, key, nil
}

// ConversionTargets: an account's conversions go to its pixel.
func (ChatGPTAds) ConversionTargets(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.ConversionTarget, error) {
	pixel, _, err := chatgptConvCreds(creds)
	if err != nil {
		return nil, err
	}
	return []c.ConversionTarget{{ID: pixel, Name: "Conversions API (pixel " + pixel + ")", EventTypes: chatgptEventOrder}}, nil
}

// SendConversions posts events matched on the click's oppref, the pixel's
// browser reference (obref) and the hashed email, phone and record id. The
// event id dedupes against the pixel. OpenAI takes events from the last 7
// days only, and refuses a whole batch for one bad event.
func (g ChatGPTAds) SendConversions(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, target c.ConversionTarget, convs []c.AdConversion) ([]c.ConversionResult, error) {
	pixel, key, err := chatgptConvCreds(creds)
	if err != nil {
		return nil, err
	}
	site := strings.TrimRight(strings.TrimSpace(creds["website"]), "/")
	if site != "" {
		if u, err := url.Parse(site); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, c.Errf(c.ErrInvalid, "the website address on the ChatGPT Ads connection must start with https://")
		}
	}
	results := make([]c.ConversionResult, len(convs))
	var events []map[string]any
	var sentIdx []int
	oldest := time.Now().Add(-7 * 24 * time.Hour)
	for i, cv := range convs {
		results[i] = c.ConversionResult{Index: cv.Index}
		if cv.Time.Before(oldest) {
			results[i].Skipped, results[i].Reason = true, "OpenAI takes conversions from the last 7 days only"
			continue
		}
		kind := cv.EventType
		shape, ok := chatgptEvents[kind]
		if kind == "" {
			kind, shape, ok = "registration_completed", "customer_action", true
		}
		if !ok {
			results[i].Skipped, results[i].Reason = true, fmt.Sprintf("ChatGPT Ads has no event %q", kind)
			continue
		}
		user := map[string]any{}
		var keys []string
		ev := map[string]any{"id": cv.EventID, "type": kind, "timestamp_ms": cv.Time.UnixMilli()}
		if v := cv.ClickIDs["oppref"]; v != "" {
			ev["oppref"], keys = v, append(keys, "oppref")
		}
		if v := cv.ClickIDs["obref"]; v != "" {
			user["obref"], keys = v, append(keys, "obref")
		}
		if len(cv.Emails) > 0 {
			user["emails_sha256"], keys = firstThree(cv.Emails), append(keys, "email")
		}
		if len(cv.PhoneDigits) > 0 {
			user["phone_numbers_sha256"], keys = firstThree(cv.PhoneDigits), append(keys, "phone")
		}
		if cv.ExternalID != "" {
			user["external_ids_sha256"], keys = []string{cv.ExternalID}, append(keys, "external_id")
		}
		if len(keys) == 0 {
			results[i].Skipped, results[i].Reason = true, "no ChatGPT click (oppref) or identifier to match on"
			continue
		}
		if len(user) > 0 {
			ev["user"] = user
		}
		if site != "" {
			ev["action_source"], ev["source_url"] = "web", site
		} else {
			ev["action_source"] = "other"
		}
		data := map[string]any{"type": shape}
		if cv.ValueMicros != nil && cv.Currency != "" {
			// An integer in the currency's minor unit (4200 for 42.00 USD).
			data["amount"] = int64(math.Round(float64(*cv.ValueMicros) * float64(minorUnits(cv.Currency)) / 1e6))
			data["currency"] = strings.ToUpper(cv.Currency)
		}
		ev["data"] = data
		results[i].MatchKeys = keys
		events = append(events, ev)
		sentIdx = append(sentIdx, i)
	}
	u := cl.Base("chatgpt_ads.events", chatgptEventsBase) + "/events?" + url.Values{"pid": {pixel}}.Encode()
	for start := 0; start < len(events); start += chatgptBatch {
		end := min(start+chatgptBatch, len(events))
		body := map[string]any{"integration_source": "outflare", "events": events[start:end]}
		if err := newCall(cl, "ChatGPT Ads").bearer(key).post(ctx, u, body, nil); err != nil {
			if c.KindOf(err) != c.ErrInvalid {
				return nil, err
			}
			// One bad event refuses the batch: report it on each.
			for _, i := range sentIdx[start:end] {
				results[i].Reason = err.Error()
			}
			continue
		}
		for _, i := range sentIdx[start:end] {
			results[i].OK = true
		}
	}
	return results, nil
}

// firstThree keeps at most three values, the most OpenAI takes per field.
func firstThree(v []string) []string {
	if len(v) > 3 {
		return v[:3]
	}
	return v
}
