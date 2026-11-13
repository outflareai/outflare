package ads

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// XAds reads and manages X (Twitter) ad accounts through the X Ads API. X
// grants Ads API access per developer app, so the workspace connects its
// own app's keys and the access token for the account that runs its ads;
// the Ads API signs requests with OAuth 1.0a. It pauses, resumes and sets
// campaign budgets (AdsManager) and sends conversions to the account's
// pixel through the Conversions API (ConversionSender).
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
		"Spend, impressions, clicks and conversions per campaign from your X ad accounts.",
		"Uses your own X developer app, once X has approved it for the Ads API (apply in the developer portal; approval can take a few weeks). Paste the app's API key and secret and the access token and secret of the account that runs your ads. It changes a campaign only when you, or an agent within the grants you set, pause or resume it or set its daily budget, which needs the access token made with Read and write permission. Add your pixel ID and conversion event IDs to send conversions back.")
	d.Auth = c.AuthAPIKey
	d.Capabilities = append(d.Capabilities, c.CapAdsConversions, c.CapAdsManage)
	d.Fields = []c.Field{
		{Name: "consumer_key", Label: "API key", Secret: false, Help: "Your developer app's API key (consumer key)."},
		{Name: "consumer_secret", Label: "API key secret", Secret: true},
		{Name: "access_token", Label: "Access token", Secret: true, Optional: true, Help: "Leave empty and sign in to X from Paid ads, Accounts, instead. Or paste the token of the X account that runs your ads, made after setting the app's permissions to Read and write."},
		{Name: "access_token_secret", Label: "Access token secret", Secret: true, Optional: true},
	}
	return d
}

// SetupFields: the Conversions API needs the account's pixel and the
// events to send to, both shown in X Ads Manager under Events Manager.
// Reading and managing need neither, so both are optional.
func (XAds) SetupFields() []c.Field {
	return []c.Field{
		{Name: "pixel_id", Label: "Pixel ID", Optional: true,
			Help: "Only to send conversions to X: in X Ads Manager, Tools, Events Manager, your pixel's ID. It looks like o8z6j."},
		{Name: "conversion_events", Label: "Conversion events", Optional: true,
			Help: "A name and the Event ID from Events Manager for each event, separated by commas: Sign-up = o8z2x, Purchase = o8z2y"},
		{Name: "conversions_token", Label: "Conversion API access token", Secret: true, Optional: true,
			Help: "Optional: in Events Manager, Install Pixel, Manual, Conversion API, Generate access token. Without it, conversions are sent with the connection's own keys."},
	}
}

func (XAds) base(cl *c.Client) string { return cl.Base("x_ads", xAdsBase) + "/" + xAdsVersion }

// get signs and sends one GET.
func (x XAds) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) error {
	return x.send(ctx, cl, creds, http.MethodGet, path, q, nil, out)
}

// send signs and sends one request. Query parameters are signed; a JSON
// body is not, as OAuth 1.0a only signs form bodies.
func (x XAds) send(ctx context.Context, cl *c.Client, creds c.Creds, method, path string, q url.Values, body, out any) error {
	u := x.base(cl) + path
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return c.Errf(c.ErrInvalid, "bad X Ads address: %v", err)
	}
	if q == nil {
		q = url.Values{}
	}
	req.URL.RawQuery = q.Encode()
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", oauth1Header(method, u, q, creds, oauthNonce(), time.Now()))
	_, err = c.DoJSON(ctx, cl, "X Ads", req, out)
	return err
}

func (x XAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if creds["access_token"] == "" {
		// The app's keys alone reach nothing until an account signs in.
		return c.Account{Name: "X app", Details: map[string]string{"sign_in": "needed"}}, nil
	}
	accts, err := x.AdAccounts(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	return c.Account{Details: map[string]string{"accounts": fmt.Sprint(len(accts))}}, nil
}

func (x XAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	if creds["access_token"] == "" {
		return nil, c.Errf(c.ErrAuth, "Sign in to X first: in Paid ads, Accounts, press Sign in to X on this connection")
	}
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

// xCampaign is a campaign as the Ads API returns it.
type xCampaign struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	EntityStatus      string  `json:"entity_status"`
	Deleted           bool    `json:"deleted"`
	DailyBudgetMicros flexInt `json:"daily_budget_amount_local_micro"`
}

func (cp xCampaign) campaign() c.AdCampaign {
	st := cp.EntityStatus
	if cp.Deleted {
		st = "DELETED"
	}
	camp := c.AdCampaign{ID: cp.ID, Name: cp.Name, Status: status(st), ProviderStatus: st}
	if cp.DailyBudgetMicros > 0 {
		camp.DailyBudgetMicros = ptrI(int64(cp.DailyBudgetMicros))
	}
	return camp
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
			Data       []xCampaign `json:"data"`
			NextCursor string      `json:"next_cursor"`
		}
		if err := x.get(ctx, cl, creds, "/accounts/"+esc(acct.ID)+"/campaigns", q, &resp); err != nil {
			return nil, err
		}
		for _, cp := range resp.Data {
			out = append(out, cp.campaign())
		}
		if cursor = resp.NextCursor; cursor == "" {
			break
		}
	}
	return out, nil
}

// xID is an X Ads id: short, lowercase letters and digits.
func xID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

// AdCampaign reads one campaign, deleted ones included.
func (x XAds) AdCampaign(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) (c.AdCampaign, error) {
	if !xID(campaignID) {
		return c.AdCampaign{}, c.Errf(c.ErrInvalid, "an X campaign id is required")
	}
	var resp struct {
		Data xCampaign `json:"data"`
	}
	if err := x.get(ctx, cl, creds, "/accounts/"+esc(acct.ID)+"/campaigns/"+esc(campaignID), url.Values{"with_deleted": {"true"}}, &resp); err != nil {
		return c.AdCampaign{}, err
	}
	if resp.Data.ID != campaignID {
		return c.AdCampaign{}, c.Errf(c.ErrInvalid, "X Ads has no campaign %s in account %s", campaignID, acct.ID)
	}
	return resp.Data.campaign(), nil
}

// SetCampaignStatus pauses or resumes a campaign (entity_status).
func (x XAds) SetCampaignStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, active bool) error {
	if !xID(campaignID) {
		return c.Errf(c.ErrInvalid, "an X campaign id is required")
	}
	st := "PAUSED"
	if active {
		st = "ACTIVE"
	}
	return x.update(ctx, cl, creds, acct, campaignID, url.Values{"entity_status": {st}})
}

// SetCampaignDailyBudget sets the campaign's daily budget in the account's
// currency, in micros as X takes it, rounded to the cent.
func (x XAds) SetCampaignDailyBudget(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, micros int64) error {
	if !xID(campaignID) {
		return c.Errf(c.ErrInvalid, "an X campaign id is required")
	}
	if micros <= 0 {
		return c.Errf(c.ErrInvalid, "the daily budget must be more than nothing")
	}
	cents := (micros + 5_000) / 10_000
	return x.update(ctx, cl, creds, acct, campaignID, url.Values{"daily_budget_amount_local_micro": {strconv.FormatInt(cents*10_000, 10)}})
}

func (x XAds) update(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, q url.Values) error {
	err := x.send(ctx, cl, creds, http.MethodPut, "/accounts/"+esc(acct.ID)+"/campaigns/"+esc(campaignID), q, nil, nil)
	if c.KindOf(err) == c.ErrAuth {
		return c.Errf(c.ErrAuth, "X refused the change. Make a new access token after setting your developer app's permissions to Read and write, and paste it on the connection.")
	}
	return err
}

// xConversion is one WEB_CONVERSION metric: per day, the conversions
// (metric, or after an engagement and after a view) and their value in
// micros (sale_amount). X sends it as such an object, or as a bare series
// of counts; anything else reads as nothing.
type xConversion map[string][]*flexInt

func (m *xConversion) UnmarshalJSON(b []byte) error {
	var obj map[string]json.RawMessage
	if json.Unmarshal(b, &obj) == nil {
		out := xConversion{}
		for k, raw := range obj {
			var s []*flexInt
			if json.Unmarshal(raw, &s) == nil {
				out[k] = s
			}
		}
		*m = out
		return nil
	}
	var s []*flexInt
	if json.Unmarshal(b, &s) == nil {
		*m = xConversion{"metric": s}
	}
	return nil
}

func (m xConversion) get(name string, k int) (float64, bool) {
	s, ok := m[name]
	if !ok {
		return 0, false
	}
	if k < len(s) && s[k] != nil {
		return float64(*s[k]), true
	}
	return 0, true
}

func (m xConversion) at(k int) float64 {
	if v, ok := m.get("metric", k); ok {
		return v
	}
	a, _ := m.get("post_engagement", k)
	b, _ := m.get("post_view", k)
	return a + b
}

// value is the day's sale amount in micros.
func (m xConversion) value(k int) int64 {
	v, _ := m.get("sale_amount", k)
	return int64(v)
}

// xConversionNames are the WEB_CONVERSION metrics read, by the name the
// day's actions carry: the kinds Outflare sends back.
var xConversionNames = map[string]string{"conversion_sign_ups": "Sign-up", "conversion_purchases": "Purchase"}

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
	// X's own conversions come with the WEB_CONVERSION group; an account
	// that can't have it (no pixel) gets spend without them.
	conversions := true
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
			var resp struct {
				Data []struct {
					ID     string `json:"id"`
					IDData []struct {
						Metrics struct {
							Billed      []*flexInt  `json:"billed_charge_local_micro"`
							Impressions []*flexInt  `json:"impressions"`
							Clicks      []*flexInt  `json:"clicks"`
							SignUps     xConversion `json:"conversion_sign_ups"`
							Purchases   xConversion `json:"conversion_purchases"`
						} `json:"metrics"`
					} `json:"id_data"`
				} `json:"data"`
			}
			for {
				groups := "BILLING,ENGAGEMENT"
				if conversions {
					groups += ",WEB_CONVERSION"
				}
				q := url.Values{
					"entity": {"CAMPAIGN"}, "entity_ids": {strings.Join(ids, ",")}, "granularity": {"DAY"},
					"metric_groups": {groups}, "placement": {"ALL_ON_TWITTER"},
					"start_time": {begin.Format(time.RFC3339)}, "end_time": {end.Format(time.RFC3339)},
				}
				err := x.get(ctx, cl, creds, "/stats/accounts/"+esc(acct.ID), q, &resp)
				if err != nil && conversions && c.KindOf(err) == c.ErrInvalid {
					conversions = false
					continue
				}
				if err != nil {
					return nil, err
				}
				break
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
						su, pu := m.SignUps.at(k), m.Purchases.at(k)
						if spend == 0 && imp == 0 && clk == 0 && su == 0 && pu == 0 {
							continue
						}
						ad := c.AdDay{CampaignID: d.ID, CampaignName: names[d.ID], Date: day, SpendMicros: spend, Impressions: imp, Clicks: clk}
						if m.SignUps != nil || m.Purchases != nil {
							total := su + pu
							ad.Conversions = &total
							if v := m.SignUps.value(k) + m.Purchases.value(k); v > 0 {
								ad.ConversionValueMicros = &v
							}
							for _, a := range []struct {
								n string
								v float64
							}{{xConversionNames["conversion_sign_ups"], su}, {xConversionNames["conversion_purchases"], pu}} {
								if a.v > 0 {
									ad.Actions = append(ad.Actions, c.AdAction{Name: a.n, Conversions: a.v})
								}
							}
						}
						out = append(out, ad)
					}
				}
			}
		}
	}
	return out, nil
}

// ---- conversions ----

// xConversionsMax is the most conversions one Conversions API call takes.
const xConversionsMax = 500

// xEvents reads the connection's conversion events, "name = id"
// separated by commas or lines; a bare id is its own name. An id copied as
// the pixel snippet shows it (tw-<pixel>-<event>) keeps only the event
// part, as the Conversions API takes it.
func xEvents(raw, pixel string) []c.ConversionTarget {
	var out []c.ConversionTarget
	seen := map[string]bool{}
	for _, line := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == ',' || r == ';' }) {
		name, id, ok := strings.Cut(line, "=")
		if !ok {
			id, name = name, ""
		}
		id, name = strings.TrimSpace(id), strings.TrimSpace(name)
		if pixel != "" {
			id = strings.TrimPrefix(id, "tw-"+pixel+"-")
		}
		if !xID(strings.ToLower(id)) || seen[id] {
			continue
		}
		seen[id] = true
		if name == "" {
			name = id
		}
		out = append(out, c.ConversionTarget{ID: id, Name: name})
	}
	return out
}

// ConversionTargets are the events listed on the connection.
func (XAds) ConversionTargets(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.ConversionTarget, error) {
	if strings.TrimSpace(creds["pixel_id"]) == "" {
		return nil, c.Errf(c.ErrInvalid, "X needs your pixel ID to send conversions: add it on the connection")
	}
	ts := xEvents(creds["conversion_events"], strings.TrimSpace(creds["pixel_id"]))
	if len(ts) == 0 {
		return nil, c.Errf(c.ErrInvalid, "X needs at least one conversion Event ID from Events Manager to send conversions: add it on the connection")
	}
	return ts, nil
}

// SendConversions posts conversions to the pixel's event, matched on the
// click id and the hashed email and phone; conversion_id dedupes against
// the pixel. X takes or refuses a batch whole.
func (x XAds) SendConversions(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, target c.ConversionTarget, convs []c.AdConversion) ([]c.ConversionResult, error) {
	pixel := strings.TrimSpace(creds["pixel_id"])
	if pixel == "" {
		return nil, c.Errf(c.ErrInvalid, "X needs your pixel ID to send conversions: add it on the connection")
	}
	if !xID(strings.ToLower(target.ID)) {
		return nil, c.Errf(c.ErrInvalid, "an X conversion Event ID is letters and digits, as Events Manager shows it")
	}
	results := make([]c.ConversionResult, len(convs))
	var batch []map[string]any
	var idx []int
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		body := map[string]any{"conversions": batch}
		var err error
		if tok := strings.TrimSpace(creds["conversions_token"]); tok != "" {
			// Events Manager's own token, sent as X-Pixel-Token instead of
			// signing with the app's keys.
			err = newCall(cl, "X Ads").set("X-Pixel-Token", tok).post(ctx, x.base(cl)+"/measurement/conversions/"+esc(pixel), body, nil)
		} else {
			err = x.send(ctx, cl, creds, http.MethodPost, "/measurement/conversions/"+esc(pixel), nil, body, nil)
		}
		if err != nil {
			return err
		}
		for _, i := range idx {
			results[i].OK = true
		}
		batch, idx = nil, nil
		return nil
	}
	for i, cv := range convs {
		results[i] = c.ConversionResult{Index: cv.Index}
		var ids []map[string]string
		var keys []string
		if v := cv.ClickIDs["twclid"]; v != "" {
			ids, keys = append(ids, map[string]string{"twclid": v}), append(keys, "twclid")
		}
		if len(cv.Emails) > 0 {
			ids, keys = append(ids, map[string]string{"hashed_email": cv.Emails[0]}), append(keys, "email")
		}
		if len(cv.Phones) > 0 {
			ids, keys = append(ids, map[string]string{"hashed_phone_number": cv.Phones[0]}), append(keys, "phone")
		}
		if len(ids) == 0 {
			results[i].Skipped, results[i].Reason = true, "no X click id, email or phone to match on"
			continue
		}
		ev := map[string]any{
			"conversion_time": cv.Time.UTC().Format("2006-01-02T15:04:05.000Z"),
			"event_id":        target.ID,
			"identifiers":     ids,
			"conversion_id":   cv.EventID,
		}
		if cv.ValueMicros != nil {
			ev["value"] = fmt.Sprintf("%.2f", float64(*cv.ValueMicros)/1e6)
			ev["price_currency"] = cv.Currency
		}
		results[i].MatchKeys = keys
		batch, idx = append(batch, ev), append(idx, i)
		if len(batch) == xConversionsMax {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return results, nil
}

// ---- Signing in (3-legged OAuth 1.0a) ----

// xAuthBase is where X's OAuth 1.0a endpoints live.
const xAuthBase = "https://api.x.com"

var _ c.AccountSignInOAuth1 = XAds{}

// oauthForm signs and posts one OAuth 1.0a step and reads its form answer.
func (x XAds) oauthForm(ctx context.Context, cl *c.Client, creds c.Creds, path string, extra map[string]string) (url.Values, error) {
	u := cl.Base("x_ads.auth", xAuthBase) + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return nil, c.Errf(c.ErrInvalid, "bad X address: %v", err)
	}
	req.Header.Set("Authorization", oauth1HeaderWith(http.MethodPost, u, url.Values{}, creds, oauthNonce(), time.Now(), extra))
	resp, err := cl.Do(req)
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "could not reach X: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if strings.Contains(string(body), "415") || strings.Contains(strings.ToLower(string(body)), "callback") {
			return nil, c.Errf(c.ErrAuth, "X refused the sign-in: add this server's callback URL to your X app (User authentication settings, Callback URI)")
		}
		return nil, c.Errf(c.ErrAuth, "X refused the app's API key and secret (HTTP %d); check them on the connection", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, c.HTTPError("X", resp.StatusCode, string(body))
	}
	v, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "X answered the sign-in with something unexpected")
	}
	return v, nil
}

// SignInRequest gets a request token for the callback and the consent page
// for it.
func (x XAds) SignInRequest(ctx context.Context, cl *c.Client, creds c.Creds, callback string) (string, string, string, error) {
	if creds["consumer_key"] == "" || creds["consumer_secret"] == "" {
		return "", "", "", c.Errf(c.ErrInvalid, "add your X app's API key and secret to the connection first")
	}
	app := c.Creds{"consumer_key": creds["consumer_key"], "consumer_secret": creds["consumer_secret"]}
	v, err := x.oauthForm(ctx, cl, app, "/oauth/request_token", map[string]string{"oauth_callback": callback})
	if err != nil {
		return "", "", "", err
	}
	tok, sec := v.Get("oauth_token"), v.Get("oauth_token_secret")
	if tok == "" || sec == "" || v.Get("oauth_callback_confirmed") != "true" {
		return "", "", "", c.Errf(c.ErrAuth, "X did not accept the sign-in's callback: add this server's callback URL to your X app")
	}
	return cl.Base("x_ads.auth", xAuthBase) + "/oauth/authorize?" + url.Values{"oauth_token": {tok}}.Encode(), tok, sec, nil
}

// SignInAccess trades the request token and verifier for the account's
// access token, and reads the ad accounts it reaches.
func (x XAds) SignInAccess(ctx context.Context, cl *c.Client, creds c.Creds, token, secret, verifier string) (c.Creds, c.Account, error) {
	if token == "" || verifier == "" {
		return nil, c.Account{}, c.Errf(c.ErrAuth, "X sent no sign-in back; try again")
	}
	step := c.Creds{"consumer_key": creds["consumer_key"], "consumer_secret": creds["consumer_secret"], "access_token": token, "access_token_secret": secret}
	v, err := x.oauthForm(ctx, cl, step, "/oauth/access_token", map[string]string{"oauth_verifier": verifier})
	if err != nil {
		return nil, c.Account{}, err
	}
	if v.Get("oauth_token") == "" || v.Get("oauth_token_secret") == "" {
		return nil, c.Account{}, c.Errf(c.ErrAuth, "X gave no lasting sign-in; try again")
	}
	out := c.Creds{}
	for k, val := range creds {
		if k != c.OAuth1RequestToken && k != c.OAuth1RequestSecret {
			out[k] = val
		}
	}
	out["access_token"], out["access_token_secret"] = v.Get("oauth_token"), v.Get("oauth_token_secret")
	if n := v.Get("screen_name"); n != "" {
		out["username"] = n
	}
	acct, err := x.Test(ctx, cl, out)
	if err != nil {
		return nil, c.Account{}, err
	}
	acct.ID, acct.Name = v.Get("user_id"), "@"+v.Get("screen_name")
	return out, acct, nil
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
	return oauth1HeaderWith(method, rawURL, query, creds, nonce, now, nil)
}

// oauth1HeaderWith signs with extra oauth_ parameters (the callback or the
// verifier of a sign-in); without an access token, oauth_token is left out.
func oauth1HeaderWith(method, rawURL string, query url.Values, creds c.Creds, nonce string, now time.Time, extra map[string]string) string {
	oauth := map[string]string{
		"oauth_consumer_key":     creds["consumer_key"],
		"oauth_nonce":            nonce,
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_timestamp":        strconv.FormatInt(now.Unix(), 10),
		"oauth_version":          "1.0",
	}
	if creds["access_token"] != "" {
		oauth["oauth_token"] = creds["access_token"]
	}
	for k, v := range extra {
		oauth[k] = v
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
