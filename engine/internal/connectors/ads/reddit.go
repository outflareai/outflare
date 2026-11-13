package ads

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// RedditAds reads and manages Reddit ad accounts through the Reddit Ads
// API (v3). It is its own connection, separate from organic Reddit, because
// the Ads API signs in through ads.reddit.com with its own permissions. It
// pauses, resumes and sets campaign budgets (AdsManager) and sends
// conversions to the account's pixel (ConversionSender).
type RedditAds struct{}

const redditAdsBase = "https://ads-api.reddit.com/api/v3"

func (RedditAds) Definition() c.Definition {
	d := def("reddit_ads", "Reddit Ads", "https://ads.reddit.com",
		"https://ads-api.reddit.com/docs/v3/", "",
		"Spend, impressions, clicks and conversions per campaign from your Reddit ad accounts.",
		"Signs in with Reddit and reads every ad account of every business the account belongs to. It changes a campaign only when you, or an agent within the grants you set, pause or resume it or set its daily budget. Add your pixel ID and a Conversions API token to send conversions back. Separate from organic Reddit, which posts and listens from your brand's own account.")
	d.Capabilities = append(d.Capabilities, c.CapAdsConversions, c.CapAdsManage)
	d.OAuth = &c.OAuth{
		AuthURL:   "https://www.reddit.com/api/v1/authorize",
		TokenURL:  "https://www.reddit.com/api/v1/access_token",
		Scopes:    []string{"adsread", "adsedit"},
		Params:    map[string]string{"duration": "permanent"},
		BasicAuth: true,
	}
	return d
}

func (RedditAds) base(cl *c.Client) string { return cl.Base("reddit_ads", redditAdsBase) }

func (RedditAds) call(cl *c.Client, creds c.Creds) *call {
	return newCall(cl, "Reddit Ads").bearer(creds["access_token"])
}

func (r RedditAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var me struct {
		Data struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := r.call(cl, creds).get(ctx, r.base(cl)+"/me", &me); err != nil {
		return c.Account{}, err
	}
	accts, err := r.AdAccounts(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: me.Data.ID, Name: me.Data.Username, Details: map[string]string{"accounts": fmt.Sprint(len(accts))}}, nil
}

// list follows a v3 list through pagination.next_url on the same host.
func (r RedditAds) list(ctx context.Context, cl *c.Client, creds c.Creds, method, first string, body any, each func(json.RawMessage) error) error {
	next := first
	b, _ := url.Parse(r.base(cl))
	for i := 0; i < maxPages && next != ""; i++ {
		var resp struct {
			Data       json.RawMessage `json:"data"`
			Pagination struct {
				NextURL string `json:"next_url"`
			} `json:"pagination"`
		}
		var err error
		if method == "POST" {
			err = r.call(cl, creds).post(ctx, next, body, &resp)
		} else {
			err = r.call(cl, creds).get(ctx, next, &resp)
		}
		if err != nil {
			return err
		}
		if err := each(resp.Data); err != nil {
			return err
		}
		next = ""
		if u, err := url.Parse(resp.Pagination.NextURL); err == nil && resp.Pagination.NextURL != "" && b != nil && u.Host == b.Host {
			next = resp.Pagination.NextURL
		}
	}
	return nil
}

func (r RedditAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	var businesses []string
	err := r.list(ctx, cl, creds, "GET", r.base(cl)+"/me/businesses", nil, func(raw json.RawMessage) error {
		var list []struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &list)
		for _, b := range list {
			businesses = append(businesses, b.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []c.AdAccount
	for _, biz := range businesses {
		err := r.list(ctx, cl, creds, "GET", r.base(cl)+"/businesses/"+esc(biz)+"/ad_accounts", nil, func(raw json.RawMessage) error {
			var list []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				Currency   string `json:"currency"`
				TimeZoneID string `json:"time_zone_id"`
			}
			_ = json.Unmarshal(raw, &list)
			for _, a := range list {
				if a.ID != "" && !seen[a.ID] {
					seen[a.ID] = true
					out = append(out, c.AdAccount{ID: a.ID, Name: a.Name, Currency: strings.ToUpper(a.Currency), TimeZone: a.TimeZoneID,
						Meta: map[string]string{"business_id": biz}})
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// redditCampaign is a campaign as the v3 API returns it. A campaign with
// campaign budget optimisation carries its budget (goal_type DAILY_SPEND,
// goal_value in micros); otherwise the budget is on its ad groups.
type redditCampaign struct {
	ID               string  `json:"id"`
	AdAccountID      string  `json:"ad_account_id"`
	Name             string  `json:"name"`
	EffectiveStatus  string  `json:"effective_status"`
	ConfiguredStatus string  `json:"configured_status"`
	Objective        string  `json:"objective"`
	GoalType         string  `json:"goal_type"`
	GoalValue        flexInt `json:"goal_value"`
}

func (cp redditCampaign) campaign() c.AdCampaign {
	st := cp.EffectiveStatus
	if st == "" {
		st = cp.ConfiguredStatus
	}
	camp := c.AdCampaign{ID: cp.ID, Name: cp.Name, Status: status(st), ProviderStatus: st, Objective: strings.ToLower(cp.Objective)}
	if cp.GoalType == "DAILY_SPEND" && cp.GoalValue > 0 {
		camp.DailyBudgetMicros = ptrI(int64(cp.GoalValue))
	}
	return camp
}

func (r RedditAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	var out []c.AdCampaign
	err := r.list(ctx, cl, creds, "GET", r.base(cl)+"/ad_accounts/"+esc(acct.ID)+"/campaigns", nil, func(raw json.RawMessage) error {
		var list []redditCampaign
		_ = json.Unmarshal(raw, &list)
		for _, cp := range list {
			out = append(out, cp.campaign())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// A campaign without its own budget gets the sum of its ad groups'
	// daily budgets, so budget rules and changes see one number.
	groups, err := r.adGroups(ctx, cl, creds, acct, "")
	if err != nil {
		return out, nil
	}
	sums := map[string]int64{}
	for _, g := range groups {
		if g.daily() {
			sums[g.CampaignID] += int64(g.GoalValue)
		}
	}
	for i := range out {
		if out[i].DailyBudgetMicros == nil && sums[out[i].ID] > 0 {
			out[i].DailyBudgetMicros = ptrI(sums[out[i].ID])
		}
	}
	return out, nil
}

// redditAdGroup is an ad group as the v3 API returns it, with the budget
// a campaign without campaign budget optimisation keeps on its ad groups.
type redditAdGroup struct {
	ID               string  `json:"id"`
	CampaignID       string  `json:"campaign_id"`
	ConfiguredStatus string  `json:"configured_status"`
	GoalType         string  `json:"goal_type"`
	GoalValue        flexInt `json:"goal_value"`
}

// daily: the ad group has a daily budget and isn't archived or deleted.
func (g redditAdGroup) daily() bool {
	return g.GoalType == "DAILY_SPEND" && g.GoalValue > 0 && g.ConfiguredStatus != "ARCHIVED" && g.ConfiguredStatus != "DELETED"
}

// adGroups lists an account's ad groups, or one campaign's.
func (r RedditAds) adGroups(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) ([]redditAdGroup, error) {
	u := r.base(cl) + "/ad_accounts/" + esc(acct.ID) + "/ad_groups"
	if campaignID != "" {
		u += "?campaign_id=" + url.QueryEscape(campaignID)
	}
	var out []redditAdGroup
	err := r.list(ctx, cl, creds, "GET", u, nil, func(raw json.RawMessage) error {
		var list []redditAdGroup
		_ = json.Unmarshal(raw, &list)
		for _, g := range list {
			if campaignID == "" || g.CampaignID == campaignID {
				out = append(out, g)
			}
		}
		return nil
	})
	return out, err
}

// redditID is a v3 id: letters, digits, dashes and underscores.
func redditID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

func (r RedditAds) campaign(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) (redditCampaign, error) {
	if !redditID(campaignID) {
		return redditCampaign{}, c.Errf(c.ErrInvalid, "a Reddit campaign id is required")
	}
	var resp struct {
		Data redditCampaign `json:"data"`
	}
	if err := r.call(cl, creds).get(ctx, r.base(cl)+"/campaigns/"+esc(campaignID), &resp); err != nil {
		return redditCampaign{}, err
	}
	// A campaign of another account is not this account's to change.
	if resp.Data.ID != campaignID || (resp.Data.AdAccountID != "" && resp.Data.AdAccountID != acct.ID) {
		return redditCampaign{}, c.Errf(c.ErrInvalid, "Reddit Ads has no campaign %s in account %s", campaignID, acct.ID)
	}
	return resp.Data, nil
}

// AdCampaign reads one campaign; without campaign budget optimisation,
// its budget is the sum of its ad groups' daily budgets.
func (r RedditAds) AdCampaign(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) (c.AdCampaign, error) {
	cp, err := r.campaign(ctx, cl, creds, acct, campaignID)
	if err != nil {
		return c.AdCampaign{}, err
	}
	camp := cp.campaign()
	if camp.DailyBudgetMicros == nil {
		groups, err := r.adGroups(ctx, cl, creds, acct, campaignID)
		if err != nil {
			return c.AdCampaign{}, err
		}
		var sum int64
		for _, g := range groups {
			if g.daily() {
				sum += int64(g.GoalValue)
			}
		}
		if sum > 0 {
			camp.DailyBudgetMicros = ptrI(sum)
		}
	}
	return camp, nil
}

// SetCampaignStatus pauses or resumes a campaign (configured_status).
func (r RedditAds) SetCampaignStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, active bool) error {
	if _, err := r.campaign(ctx, cl, creds, acct, campaignID); err != nil {
		return err
	}
	st := "PAUSED"
	if active {
		st = "ACTIVE"
	}
	return r.update(ctx, cl, creds, "/campaigns/"+esc(campaignID), map[string]any{"configured_status": st})
}

// SetCampaignDailyBudget sets a campaign's daily budget, in micros,
// rounded to the cent. A campaign with campaign budget optimisation holds
// it itself; otherwise its ad groups' daily budgets are scaled together
// so they add up to the new amount, each keeping its share. A campaign
// with no daily budget anywhere (lifetime budgets) is refused with a
// reason.
func (r RedditAds) SetCampaignDailyBudget(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, micros int64) error {
	if micros <= 0 {
		return c.Errf(c.ErrInvalid, "the daily budget must be more than nothing")
	}
	cp, err := r.campaign(ctx, cl, creds, acct, campaignID)
	if err != nil {
		return err
	}
	cents := (micros + 5_000) / 10_000
	if cp.GoalType == "DAILY_SPEND" {
		return r.update(ctx, cl, creds, "/campaigns/"+esc(campaignID), map[string]any{"goal_type": "DAILY_SPEND", "goal_value": cents * 10_000})
	}
	groups, err := r.adGroups(ctx, cl, creds, acct, campaignID)
	if err != nil {
		return err
	}
	var daily []redditAdGroup
	var total int64
	for _, g := range groups {
		if g.daily() && redditID(g.ID) {
			daily = append(daily, g)
			total += int64(g.GoalValue)
		}
	}
	if len(daily) == 0 || total <= 0 {
		return c.Errf(c.ErrInvalid, "this Reddit campaign has no daily budget to change (its budgets are lifetime budgets): set a daily budget in Reddit Ads Manager first")
	}
	if cents < int64(len(daily)) {
		return c.Errf(c.ErrInvalid, "the daily budget is too small to share between this campaign's %d ad groups", len(daily))
	}
	// Each ad group keeps its share, to the cent; the last takes what
	// rounding left, so the total is exact.
	left := cents
	for i, g := range daily {
		share := left
		if i < len(daily)-1 {
			share = max(1, (cents*int64(g.GoalValue)+total/2)/total)
			share = min(share, left-int64(len(daily)-1-i))
		}
		left -= share
		if err := r.update(ctx, cl, creds, "/ad_groups/"+esc(g.ID), map[string]any{"goal_value": share * 10_000}); err != nil {
			return err
		}
	}
	return nil
}

// update patches one campaign or ad group (path from the API's base).
func (r RedditAds) update(ctx context.Context, cl *c.Client, creds c.Creds, path string, data map[string]any) error {
	err := r.call(cl, creds).patch(ctx, r.base(cl)+path, map[string]any{"data": data}, nil)
	if c.KindOf(err) == c.ErrAuth {
		return c.Errf(c.ErrAuth, "Reddit refused the change. Connect Reddit Ads again so it grants edit access, with an account that can manage this ad account.")
	}
	return err
}

func (r RedditAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	out, err := r.adDays(ctx, cl, creds, acct, from, to, true)
	// An account that can't report conversions (no pixel) gets spend
	// without them.
	if c.KindOf(err) == c.ErrInvalid {
		out, err = r.adDays(ctx, cl, creds, acct, from, to, false)
	}
	return out, err
}

func (r RedditAds) adDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string, conversions bool) ([]c.AdDay, error) {
	end := mustDate(to).AddDate(0, 0, 1)
	fields := []string{"SPEND", "IMPRESSIONS", "CLICKS"}
	if conversions {
		fields = append(fields, "KEY_CONVERSION_TOTAL_COUNT")
	}
	body := map[string]any{"data": map[string]any{
		"breakdowns":   []string{"DATE", "CAMPAIGN_ID"},
		"fields":       fields,
		"starts_at":    from + "T00:00:00Z",
		"ends_at":      end.Format("2006-01-02") + "T00:00:00Z",
		"time_zone_id": acct.TimeZone,
	}}
	var out []c.AdDay
	err := r.list(ctx, cl, creds, "POST", r.base(cl)+"/ad_accounts/"+esc(acct.ID)+"/reports", body, func(raw json.RawMessage) error {
		var data struct {
			Metrics []struct {
				CampaignID  string   `json:"campaign_id"`
				Date        string   `json:"date"`
				Spend       flexInt  `json:"spend"` // micros
				Impressions flexInt  `json:"impressions"`
				Clicks      flexInt  `json:"clicks"`
				Conversions *flexNum `json:"key_conversion_total_count"`
			} `json:"metrics"`
		}
		_ = json.Unmarshal(raw, &data)
		for _, m := range data.Metrics {
			if m.CampaignID == "" || len(m.Date) < 10 {
				continue
			}
			d := c.AdDay{CampaignID: m.CampaignID, Date: m.Date[:10], SpendMicros: int64(m.Spend),
				Impressions: int64(m.Impressions), Clicks: int64(m.Clicks)}
			if m.Conversions != nil {
				v := float64(*m.Conversions)
				d.Conversions = &v
			}
			out = append(out, d)
		}
		return nil
	})
	return out, err
}

// ---- conversions ----

// redditConversionsBase is the retired v2.0 Conversions API, kept for
// connections that have a token but no pixel ID yet.
const redditConversionsBase = "https://ads-api.reddit.com/api/v2.0"

// redditConversionsMax is the most events one v3 call takes.
const redditConversionsMax = 1000

// redditEvents are Reddit's tracking types a rule can send as, by their
// v2.0 names; v3 spells them in capitals (redditV3Types).
var redditEvents = []string{"SignUp", "Lead", "Purchase", "AddToCart", "ViewContent"}

// redditV3Types are the v3 Conversions API's names for the same types.
var redditV3Types = map[string]string{"SignUp": "SIGN_UP", "Lead": "LEAD", "Purchase": "PURCHASE", "AddToCart": "ADD_TO_CART", "ViewContent": "VIEW_CONTENT"}

// SetupFields: the Conversions API takes the account's pixel and its own
// access token, which the workspace generates in its Reddit Ads Manager.
// Reading and managing need neither, so both are optional.
func (RedditAds) SetupFields() []c.Field {
	return []c.Field{
		{Name: "pixel_id", Label: "Pixel ID", Optional: true,
			Help: "Only to send conversions to Reddit: in Reddit Ads Manager, Events Manager, your pixel's ID (it looks like a2_abc123)."},
		{Name: "conversions_token", Label: "Conversions API access token", Secret: true, Optional: true,
			Help: "Only to send conversions to Reddit: in Reddit Ads Manager, Events Manager, Conversions API, generate an access token."},
	}
}

// ConversionTargets: an account's conversions all go to one place, its
// pixel.
func (RedditAds) ConversionTargets(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.ConversionTarget, error) {
	if creds["conversions_token"] == "" {
		return nil, c.Errf(c.ErrAuth, "Reddit needs a Conversions API access token to send conversions: add it on the connection")
	}
	if p := strings.TrimSpace(creds["pixel_id"]); p != "" {
		return []c.ConversionTarget{{ID: p, Name: "Pixel " + p, EventTypes: redditEvents}}, nil
	}
	return []c.ConversionTarget{{ID: acct.ID, Name: "Conversions API", EventTypes: redditEvents}}, nil
}

// SendConversions posts events to the Conversions API with the click id
// and the hashed email and external id; conversion_id dedupes against the
// pixel. With a pixel ID it uses v3 (pixels/{id}/conversion_events);
// without one, the v2.0 endpoint the connection used before. Reddit takes
// or refuses a batch whole.
func (r RedditAds) SendConversions(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, target c.ConversionTarget, convs []c.AdConversion) ([]c.ConversionResult, error) {
	if creds["conversions_token"] == "" {
		return nil, c.Errf(c.ErrAuth, "Reddit needs a Conversions API access token to send conversions: add it on the connection")
	}
	pixel := strings.TrimSpace(creds["pixel_id"])
	results := make([]c.ConversionResult, len(convs))
	var events []map[string]any
	var sentIdx []int
	for i, cv := range convs {
		results[i] = c.ConversionResult{Index: cv.Index}
		user := map[string]any{}
		ev := map[string]any{}
		var keys []string
		if v := cv.ClickIDs["rdt_cid"]; v != "" {
			ev["click_id"], keys = v, append(keys, "rdt_cid")
		}
		if len(cv.Emails) > 0 {
			user["email"], keys = cv.Emails[0], append(keys, "email")
		}
		if cv.ExternalID != "" {
			user["external_id"], keys = cv.ExternalID, append(keys, "external_id")
		}
		if len(keys) == 0 {
			results[i].Skipped, results[i].Reason = true, "no Reddit click id or identifier to match on"
			continue
		}
		kind := cv.EventType
		if kind == "" {
			kind = "SignUp"
		}
		meta := map[string]any{"conversion_id": cv.EventID}
		if pixel != "" {
			if cv.ValueMicros != nil {
				meta["value"] = float64(*cv.ValueMicros) / 1e6
				meta["currency"] = cv.Currency
			}
			ev["event_at"] = cv.Time.UTC().UnixMilli()
			ev["action_source"] = "WEBSITE"
			v3 := redditV3Types[kind]
			if v3 == "" {
				v3 = kind
			}
			ev["type"] = map[string]string{"tracking_type": v3}
			ev["metadata"] = meta
		} else {
			if cv.ValueMicros != nil {
				meta["value_decimal"] = float64(*cv.ValueMicros) / 1e6
				meta["currency"] = cv.Currency
			}
			ev["event_at"] = cv.Time.UTC().Format(time.RFC3339)
			ev["event_type"] = map[string]string{"tracking_type": kind}
			ev["event_metadata"] = meta
		}
		ev["user"] = user
		results[i].MatchKeys = keys
		events = append(events, ev)
		sentIdx = append(sentIdx, i)
	}
	if len(events) == 0 {
		return results, nil
	}
	call := newCall(cl, "Reddit Ads").bearer(creds["conversions_token"])
	if pixel == "" {
		u := cl.Base("reddit_ads.conversions", redditConversionsBase) + "/conversions/events/" + esc(acct.ID)
		if err := call.post(ctx, u, map[string]any{"test_mode": false, "events": events}, nil); err != nil {
			return nil, err
		}
	} else {
		u := r.base(cl) + "/pixels/" + esc(pixel) + "/conversion_events"
		for start := 0; start < len(events); start += redditConversionsMax {
			part := events[start:min(start+redditConversionsMax, len(events))]
			if err := call.post(ctx, u, map[string]any{"data": map[string]any{"events": part}}, nil); err != nil {
				return nil, err
			}
		}
	}
	for _, i := range sentIdx {
		results[i].OK = true
	}
	return results, nil
}
