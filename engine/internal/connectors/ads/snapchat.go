package ads

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/adtrack"
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

// ---- ad squads and ads ----

var _ c.AdItemsReader = SnapchatAds{}

// snapAd is one ad as the account's list gives it.
type snapAd struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	AdSquadID  string `json:"ad_squad_id"`
	CreativeID string `json:"creative_id"`
	Status     string `json:"status"`
}

// ads lists every ad of the account.
func (s SnapchatAds) ads(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]snapAd, error) {
	var out []snapAd
	err := s.list(ctx, cl, creds, s.base(cl)+"/adaccounts/"+esc(acct.ID)+"/ads", func(page json.RawMessage) error {
		var data struct {
			Ads []struct {
				Ad snapAd `json:"ad"`
			} `json:"ads"`
		}
		_ = json.Unmarshal(page, &data)
		for _, w := range data.Ads {
			if w.Ad.ID != "" {
				out = append(out, w.Ad)
			}
		}
		return nil
	})
	return out, err
}

// AdItems lists the account's ad squads (Snapchat's ad groups, with
// their own daily budget where they have one) or its ads, each ad with
// its creative's headline and the page it opens.
func (s SnapchatAds) AdItems(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, level string) ([]c.AdItem, error) {
	switch level {
	case c.AdLevelGroup:
		var out []c.AdItem
		err := s.list(ctx, cl, creds, s.base(cl)+"/adaccounts/"+esc(acct.ID)+"/adsquads", func(page json.RawMessage) error {
			var data struct {
				AdSquads []struct {
					AdSquad struct {
						ID               string `json:"id"`
						Name             string `json:"name"`
						Status           string `json:"status"`
						CampaignID       string `json:"campaign_id"`
						DailyBudgetMicro *int64 `json:"daily_budget_micro"`
					} `json:"adsquad"`
				} `json:"adsquads"`
			}
			_ = json.Unmarshal(page, &data)
			for _, w := range data.AdSquads {
				q := w.AdSquad
				if q.ID == "" {
					continue
				}
				it := c.AdItem{ID: q.ID, ParentID: q.CampaignID, Name: q.Name, Status: status(q.Status), ProviderStatus: q.Status}
				if q.DailyBudgetMicro != nil && *q.DailyBudgetMicro > 0 {
					it.DailyBudgetMicros = ptrI(*q.DailyBudgetMicro)
				}
				out = append(out, it)
			}
			return nil
		})
		return out, err
	case c.AdLevelAd:
		ads, err := s.ads(ctx, cl, creds, acct)
		if err != nil {
			return nil, err
		}
		// What each ad shows is on its creative.
		type creative struct{ headline, url string }
		creatives := map[string]creative{}
		err = s.list(ctx, cl, creds, s.base(cl)+"/adaccounts/"+esc(acct.ID)+"/creatives", func(page json.RawMessage) error {
			var data struct {
				Creatives []struct {
					Creative struct {
						ID                string `json:"id"`
						Headline          string `json:"headline"`
						WebViewProperties struct {
							URL string `json:"url"`
						} `json:"web_view_properties"`
					} `json:"creative"`
				} `json:"creatives"`
			}
			_ = json.Unmarshal(page, &data)
			for _, w := range data.Creatives {
				creatives[w.Creative.ID] = creative{w.Creative.Headline, w.Creative.WebViewProperties.URL}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		out := make([]c.AdItem, 0, len(ads))
		for _, a := range ads {
			it := c.AdItem{ID: a.ID, ParentID: a.AdSquadID, Name: a.Name, Status: status(a.Status), ProviderStatus: a.Status}
			if cr, ok := creatives[a.CreativeID]; ok {
				it.Details = adDetails(map[string]string{c.AdDetailHeadline: cr.headline, c.AdDetailURL: cr.url})
				if cr.url != "" {
					it.Details[c.AdDetailTemplated] = strconv.FormatBool(adtrack.HasTemplate(cr.url))
				}
			}
			out = append(out, it)
		}
		return out, nil
	}
	return nil, c.Errf(c.ErrInvalid, "level is %s or %s", c.AdLevelGroup, c.AdLevelAd)
}

// AdItemDays reads the account's daily stats broken down by ad. Snapchat
// breaks an account's stats down by campaign or by ad, not by ad squad,
// so an ad squad's day is the sum of its ads' (each ad is in one squad).
func (s SnapchatAds) AdItemDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, level, from, to string) ([]c.AdItemDay, error) {
	if level != c.AdLevelGroup && level != c.AdLevelAd {
		return nil, c.Errf(c.ErrInvalid, "level is %s or %s", c.AdLevelGroup, c.AdLevelAd)
	}
	ads, err := s.ads(ctx, cl, creds, acct)
	if err != nil {
		return nil, err
	}
	squad := map[string]string{}
	name := map[string]string{}
	for _, a := range ads {
		squad[a.ID], name[a.ID] = a.AdSquadID, a.Name
	}
	loc, err := time.LoadLocation(acct.TimeZone)
	if err != nil || acct.TimeZone == "" {
		loc = time.UTC
	}
	all := days(from, to)
	var out []c.AdItemDay
	// An ad squad's days, summed as its ads' come in.
	type key struct{ squad, date string }
	sums := map[key]*c.AdItemDay{}
	var order []key
	for start := 0; start < len(all); start += snapStatsDays {
		window := all[start:min(start+snapStatsDays, len(all))]
		t0 := mustDate(window[0])
		t1 := mustDate(window[len(window)-1]).AddDate(0, 0, 1)
		q := url.Values{
			"granularity": {"DAY"}, "breakdown": {"ad"}, "fields": {"impressions,swipes,spend,conversion_purchases"},
			"start_time": {time.Date(t0.Year(), t0.Month(), t0.Day(), 0, 0, 0, 0, loc).Format(time.RFC3339)},
			"end_time":   {time.Date(t1.Year(), t1.Month(), t1.Day(), 0, 0, 0, 0, loc).Format(time.RFC3339)},
		}
		var resp struct {
			TimeseriesStats []struct {
				TimeseriesStat struct {
					BreakdownStats struct {
						Ad []struct {
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
						} `json:"ad"`
					} `json:"breakdown_stats"`
				} `json:"timeseries_stat"`
			} `json:"timeseries_stats"`
		}
		if err := s.call(cl, creds).get(ctx, s.base(cl)+"/adaccounts/"+esc(acct.ID)+"/stats?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, ts := range resp.TimeseriesStats {
			for _, ad := range ts.TimeseriesStat.BreakdownStats.Ad {
				for _, p := range ad.Timeseries {
					st := p.Stats
					if ad.ID == "" || len(p.StartTime) < 10 || st.Spend == 0 && st.Impressions == 0 && st.Swipes == 0 {
						continue
					}
					day := c.AdItemDay{ID: ad.ID, ParentID: squad[ad.ID], Name: name[ad.ID], Date: p.StartTime[:10],
						SpendMicros: int64(st.Spend), Impressions: int64(st.Impressions), Clicks: int64(st.Swipes)}
					if st.Purchases != nil {
						day.Conversions = ptrF(float64(*st.Purchases))
					}
					if level == c.AdLevelAd {
						out = append(out, day)
						continue
					}
					// An ad that is no longer listed has no squad to add to.
					k := key{squad[ad.ID], day.Date}
					if k.squad == "" {
						continue
					}
					sum, ok := sums[k]
					if !ok {
						sum = &c.AdItemDay{ID: k.squad, Date: k.date}
						sums[k], order = sum, append(order, k)
					}
					sum.SpendMicros += day.SpendMicros
					sum.Impressions += day.Impressions
					sum.Clicks += day.Clicks
					if day.Conversions != nil {
						if sum.Conversions == nil {
							sum.Conversions = ptrF(0)
						}
						*sum.Conversions += *day.Conversions
					}
				}
			}
		}
	}
	for _, k := range order {
		out = append(out, *sums[k])
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
		// Where the person was, beside the identifiers above and never the
		// only thing sent: the platform matches on them unhashed.
		if cv.IPAddress != "" {
			user["client_ip_address"], keys = cv.IPAddress, append(keys, "ip_address")
		}
		if cv.UserAgent != "" {
			user["client_user_agent"], keys = cv.UserAgent, append(keys, "user_agent")
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
