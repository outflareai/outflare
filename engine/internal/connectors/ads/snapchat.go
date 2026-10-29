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

// SnapchatAds reads Snapchat ad accounts through the Snapchat Marketing
// API (v1) and sends conversions to a Snap Pixel through the Conversions
// API (v3). The sign-in reaches every ad account of every organization the
// person belongs to.
type SnapchatAds struct{}

const (
	snapBase        = "https://adsapi.snapchat.com/v1"
	snapConversions = "https://tr.snapchat.com/v3"
	// snapStatsDays is the longest range a daily stats call takes.
	snapStatsDays = 31
)

func (SnapchatAds) Definition() c.Definition {
	d := def("snapchat_ads", "Snapchat Ads", "https://ads.snapchat.com",
		"https://developers.snap.com/api/marketing-api/Ads-API/introduction", "",
		"Spend, impressions and swipes per campaign from your Snapchat ad accounts.",
		"Signs in with Snapchat and reads every ad account of every organization you belong to, read-only; conversions go back to your Snap Pixel through the Conversions API.")
	d.Capabilities = append(d.Capabilities, c.CapAdsConversions)
	d.OAuth = &c.OAuth{
		AuthURL:  "https://accounts.snapchat.com/login/oauth2/authorize",
		TokenURL: "https://accounts.snapchat.com/login/oauth2/access_token",
		Scopes:   []string{"snapchat-marketing-api"},
	}
	return d
}

func (SnapchatAds) base(cl *c.Client) string { return cl.Base("snapchat_ads", snapBase) }

func (SnapchatAds) call(cl *c.Client, creds c.Creds) *call {
	return newCall(cl, "Snapchat Ads").bearer(creds["access_token"])
}

// snapPaging is how Snapchat lists continue: a full next link on the API host.
type snapPaging struct {
	NextLink string `json:"next_link"`
}

// list follows paging.next_link on the same host.
func (s SnapchatAds) list(ctx context.Context, cl *c.Client, creds c.Creds, first string, each func(page json.RawMessage) error) error {
	b, _ := url.Parse(s.base(cl))
	next := first
	for i := 0; i < maxPages && next != ""; i++ {
		var raw json.RawMessage
		if err := s.call(cl, creds).get(ctx, next, &raw); err != nil {
			return err
		}
		if err := each(raw); err != nil {
			return err
		}
		var p struct {
			Paging snapPaging `json:"paging"`
		}
		_ = json.Unmarshal(raw, &p)
		next = ""
		if u, err := url.Parse(p.Paging.NextLink); err == nil && p.Paging.NextLink != "" && b != nil && u.Host == b.Host {
			next = p.Paging.NextLink
		}
	}
	return nil
}

func (s SnapchatAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var me struct {
		Me struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"me"`
	}
	if err := s.call(cl, creds).get(ctx, s.base(cl)+"/me", &me); err != nil {
		return c.Account{}, err
	}
	accts, err := s.AdAccounts(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: me.Me.ID, Name: me.Me.DisplayName, Details: map[string]string{"accounts": fmt.Sprint(len(accts))}}, nil
}

func (s SnapchatAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	seen := map[string]bool{}
	var out []c.AdAccount
	err := s.list(ctx, cl, creds, s.base(cl)+"/me/organizations?with_ad_accounts=true", func(page json.RawMessage) error {
		var data struct {
			Organizations []struct {
				Organization struct {
					ID         string `json:"id"`
					AdAccounts []struct {
						ID       string `json:"id"`
						Name     string `json:"name"`
						Currency string `json:"currency"`
						Timezone string `json:"timezone"`
						Status   string `json:"status"`
					} `json:"ad_accounts"`
				} `json:"organization"`
			} `json:"organizations"`
		}
		_ = json.Unmarshal(page, &data)
		for _, o := range data.Organizations {
			for _, a := range o.Organization.AdAccounts {
				if a.ID != "" && !seen[a.ID] {
					seen[a.ID] = true
					out = append(out, c.AdAccount{ID: a.ID, Name: a.Name, Currency: strings.ToUpper(a.Currency), TimeZone: a.Timezone,
						Meta: map[string]string{"organization_id": o.Organization.ID}})
				}
			}
		}
		return nil
	})
	return out, err
}

func (s SnapchatAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	var out []c.AdCampaign
	err := s.list(ctx, cl, creds, s.base(cl)+"/adaccounts/"+esc(acct.ID)+"/campaigns", func(page json.RawMessage) error {
		var data struct {
			Campaigns []struct {
				Campaign struct {
					ID               string  `json:"id"`
					Name             string  `json:"name"`
					Status           string  `json:"status"`
					Objective        string  `json:"objective"`
					DailyBudgetMicro *int64  `json:"daily_budget_micro"`
					EndTime          *string `json:"end_time"`
				} `json:"campaign"`
			} `json:"campaigns"`
		}
		_ = json.Unmarshal(page, &data)
		for _, w := range data.Campaigns {
			cp := w.Campaign
			camp := c.AdCampaign{ID: cp.ID, Name: cp.Name, Status: status(cp.Status), ProviderStatus: cp.Status, Objective: strings.ToLower(cp.Objective)}
			if cp.DailyBudgetMicro != nil && *cp.DailyBudgetMicro > 0 {
				camp.DailyBudgetMicros = ptrI(*cp.DailyBudgetMicro)
			}
			out = append(out, camp)
		}
		return nil
	})
	return out, err
}

// AdDays reads the account's daily stats broken down by campaign. Days
// start at midnight in the account's time zone, and one call covers at
// most 31 days, so longer ranges go in windows. Swipes are the clicks.
func (s SnapchatAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	loc, err := time.LoadLocation(acct.TimeZone)
	if err != nil || acct.TimeZone == "" {
		loc = time.UTC
	}
	all := days(from, to)
	var out []c.AdDay
	for start := 0; start < len(all); start += snapStatsDays {
		window := all[start:min(start+snapStatsDays, len(all))]
		t0 := mustDate(window[0])
		t1 := mustDate(window[len(window)-1]).AddDate(0, 0, 1)
		q := url.Values{
			"granularity": {"DAY"}, "breakdown": {"campaign"}, "fields": {"impressions,swipes,spend,conversion_purchases"},
			"start_time": {time.Date(t0.Year(), t0.Month(), t0.Day(), 0, 0, 0, 0, loc).Format(time.RFC3339)},
			"end_time":   {time.Date(t1.Year(), t1.Month(), t1.Day(), 0, 0, 0, 0, loc).Format(time.RFC3339)},
		}
		var resp struct {
			TimeseriesStats []struct {
				TimeseriesStat struct {
					BreakdownStats struct {
						Campaign []struct {
							ID         string `json:"id"`
							Timeseries []struct {
								StartTime string `json:"start_time"`
								Stats     struct {
									Impressions flexInt  `json:"impressions"`
									Swipes      flexInt  `json:"swipes"`
									Spend       flexInt  `json:"spend"` // micros
									Purchases   *flexNum `json:"conversion_purchases"`
								} `json:"stats"`
							} `json:"timeseries"`
						} `json:"campaign"`
					} `json:"breakdown_stats"`
				} `json:"timeseries_stat"`
			} `json:"timeseries_stats"`
		}
		if err := s.call(cl, creds).get(ctx, s.base(cl)+"/adaccounts/"+esc(acct.ID)+"/stats?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, ts := range resp.TimeseriesStats {
			for _, cp := range ts.TimeseriesStat.BreakdownStats.Campaign {
				for _, p := range cp.Timeseries {
					st := p.Stats
					if len(p.StartTime) < 10 || st.Spend == 0 && st.Impressions == 0 && st.Swipes == 0 {
						continue
					}
					day := c.AdDay{CampaignID: cp.ID, Date: p.StartTime[:10], SpendMicros: int64(st.Spend), Impressions: int64(st.Impressions), Clicks: int64(st.Swipes)}
					if st.Purchases != nil {
						day.Conversions = ptrF(float64(*st.Purchases))
					}
					out = append(out, day)
				}
			}
		}
	}
	return out, nil
}

// ---- conversions ----

// snapEvents are Snapchat's standard events a rule can send as.
var snapEvents = []string{"PURCHASE", "SIGN_UP", "SUBSCRIBE", "START_TRIAL", "ADD_CART", "START_CHECKOUT", "VIEW_CONTENT", "CUSTOM_EVENT_1"}

// SetupFields: the Conversions API takes the sign-in token; a token
// generated in Ads Manager (Business Details, Conversions API Tokens) is
// used instead when it is given.
func (SnapchatAds) SetupFields() []c.Field {
	return []c.Field{{Name: "conversions_token", Label: "Conversions API token", Secret: true, Optional: true,
		Help: "Only if sending conversions with the sign-in fails: in Snapchat Ads Manager, Business Details, generate a Conversions API token."}}
}

// ConversionTargets lists the account's Snap Pixels.
func (s SnapchatAds) ConversionTargets(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.ConversionTarget, error) {
	var out []c.ConversionTarget
	err := s.list(ctx, cl, creds, s.base(cl)+"/adaccounts/"+esc(acct.ID)+"/pixels", func(page json.RawMessage) error {
		var data struct {
			Pixels []struct {
				Pixel struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"pixel"`
			} `json:"pixels"`
		}
		_ = json.Unmarshal(page, &data)
		for _, p := range data.Pixels {
			if p.Pixel.ID != "" {
				out = append(out, c.ConversionTarget{ID: p.Pixel.ID, Name: p.Pixel.Name, EventTypes: snapEvents})
			}
		}
		return nil
	})
	return out, err
}

// SendConversions posts web events to the Conversions API with the click
// id (ScCid), the _scid cookie and the hashed email, phone and external
// id; event_id dedupes against the pixel. Snapchat takes or refuses a
// batch whole.
func (s SnapchatAds) SendConversions(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, target c.ConversionTarget, convs []c.AdConversion) ([]c.ConversionResult, error) {
	results := make([]c.ConversionResult, len(convs))
	var events []map[string]any
	var sentIdx []int
	for i, cv := range convs {
		results[i] = c.ConversionResult{Index: cv.Index}
		user := map[string]any{}
		var keys []string
		if v := firstOf(cv.ClickIDs, "ScCid", "sccid"); v != "" {
			user["sc_click_id"], keys = v, append(keys, "sccid")
		}
		if v := cv.ClickIDs["scid"]; v != "" {
			user["sc_cookie1"], keys = v, append(keys, "scid")
		}
		if len(cv.Emails) > 0 {
			user["em"], keys = cv.Emails, append(keys, "email")
		}
		if len(cv.PhoneDigits) > 0 {
			// Snapchat hashes the number without the plus.
			user["ph"], keys = cv.PhoneDigits, append(keys, "phone")
		}
		if cv.ExternalID != "" {
			user["external_id"], keys = []string{cv.ExternalID}, append(keys, "external_id")
		}
		if len(keys) == 0 {
			results[i].Skipped, results[i].Reason = true, "no Snapchat click id or identifier to match on"
			continue
		}
		kind := cv.EventType
		if kind == "" {
			kind = "SIGN_UP"
		}
		ev := map[string]any{"event_name": kind, "event_time": cv.Time.Unix(), "event_id": cv.EventID, "action_source": "WEB", "user_data": user}
		if cv.ValueMicros != nil {
			ev["custom_data"] = map[string]any{"value": float64(*cv.ValueMicros) / 1e6, "currency": cv.Currency}
		}
		results[i].MatchKeys = keys
		events = append(events, ev)
		sentIdx = append(sentIdx, i)
	}
	if len(events) == 0 {
		return results, nil
	}
	token := creds["conversions_token"]
	if token == "" {
		token = creds["access_token"]
	}
	u := cl.Base("snapchat_ads.conversions", snapConversions) + "/" + esc(target.ID) + "/events"
	if err := newCall(cl, "Snapchat Ads").bearer(token).post(ctx, u, map[string]any{"data": events}, nil); err != nil {
		return nil, err
	}
	for _, i := range sentIdx {
		results[i].OK = true
	}
	return results, nil
}

// firstOf returns the first non-empty value among keys.
func firstOf(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}
