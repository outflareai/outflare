package ads

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// TaboolaAds reads Taboola advertiser accounts through the Backstage API
// and sends conversions back as server-to-server (S2S) actions. Taboola
// issues API credentials (a client id and secret) to the advertiser, so
// the workspace pastes its own; they are traded for a 12-hour token with
// the client credentials flow.
//
// Docs: developers.taboola.com/backstage-api (token, accounts, campaigns,
// the campaign summary report) and developers.taboola.com/pixel (bulk S2S
// conversions).
type TaboolaAds struct{}

const (
	taboolaBase = "https://backstage.taboola.com"
	taboolaAPI  = "/backstage/api/1.0"
	// taboolaS2S takes conversions; it needs no token.
	taboolaS2S = "https://trc.taboola.com"
	// taboolaReportDays is how many days one report call covers.
	taboolaReportDays = 31
	// taboolaPageSize is how many campaigns one page asks for.
	taboolaPageSize = 500
	// taboolaBatch is the most conversions one bulk S2S call takes.
	taboolaBatch = 1000
)

func (TaboolaAds) Definition() c.Definition {
	d := def("taboola_ads", "Taboola Ads", "https://www.taboola.com",
		"https://developers.taboola.com/backstage-api/reference/welcome", "",
		"Spend, impressions, clicks and conversions per campaign from your Taboola advertiser accounts.",
		"Uses your own Backstage API credentials: ask your Taboola account manager for API access, and paste the client id and secret they issue. A network's credentials read every advertiser account under it. Outflare changes a campaign only when you ask: pause, resume, its daily cap, blocking a publisher site on it, and pausing or resuming one of its ads. Conversions go back to your event-based conversions as server-to-server actions, matched on Taboola's click id.")
	d.Auth = c.AuthAPIKey
	d.Capabilities = append(d.Capabilities, c.CapAdsConversions, c.CapAdsManage)
	d.Fields = []c.Field{
		{Name: "client_id", Label: "Client ID", Secret: false, Help: "From your Taboola account manager, issued with Backstage API access."},
		{Name: "client_secret", Label: "Client secret", Secret: true},
		{Name: "account_id", Label: "Account ID", Secret: false, Optional: true,
			Help: "Only to read one account: the alphabetic account id Taboola gave you with the credentials. A network account id reads every advertiser under it. Leave empty to read the credentials' own account."},
	}
	return d
}

func (TaboolaAds) base(cl *c.Client) string { return cl.Base("taboola_ads", taboolaBase) }

// ---- token ----

type taboolaToken struct {
	value   string
	expires time.Time
}

// taboolaTokens keeps each credential's token until shortly before it
// expires, keyed by the token URL, client id and a hash of the secret.
var taboolaTokens = struct {
	sync.Mutex
	m map[string]taboolaToken
}{m: map[string]taboolaToken{}}

func taboolaKey(tokenURL string, creds c.Creds) string {
	sum := sha256.Sum256([]byte(tokenURL + "\x00" + creds["client_id"] + "\x00" + creds["client_secret"]))
	return hex.EncodeToString(sum[:])
}

func (t TaboolaAds) token(ctx context.Context, cl *c.Client, creds c.Creds, fresh bool) (string, error) {
	u := t.base(cl) + "/backstage/oauth/token" // no trailing slash: Taboola answers 403 with one
	key := taboolaKey(u, creds)
	taboolaTokens.Lock()
	tok, ok := taboolaTokens.m[key]
	taboolaTokens.Unlock()
	if ok && !fresh && time.Now().Before(tok.expires) {
		return tok.value, nil
	}
	if creds["client_id"] == "" || creds["client_secret"] == "" {
		return "", c.Errf(c.ErrAuth, "Taboola Ads needs the client id and secret")
	}
	form := url.Values{"client_id": {creds["client_id"]}, "client_secret": {creds["client_secret"]}, "grant_type": {"client_credentials"}}
	req, err := http.NewRequest(http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return "", c.Errf(c.ErrInvalid, "bad Taboola Ads address: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	status, err := c.DoJSON(ctx, cl, "Taboola Ads", req, &out)
	if err != nil {
		// A wrong id or secret is a 400 (or 401) with an XML body.
		if status == http.StatusBadRequest || status == http.StatusUnauthorized {
			return "", c.Errf(c.ErrAuth, "Taboola Ads refused the client id and secret (HTTP %d). Check them, or ask your Taboola account manager for new ones.", status)
		}
		return "", err
	}
	if out.AccessToken == "" {
		return "", c.Errf(c.ErrAuth, "Taboola Ads gave no access token for these credentials")
	}
	life := time.Duration(out.ExpiresIn) * time.Second
	if life <= 0 {
		life = time.Hour
	}
	taboolaTokens.Lock()
	taboolaTokens.m[key] = taboolaToken{value: out.AccessToken, expires: time.Now().Add(life - min(life/10, 10*time.Minute))}
	taboolaTokens.Unlock()
	return out.AccessToken, nil
}

// get sends one GET to the Backstage API.
func (t TaboolaAds) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) (int, error) {
	return t.send(ctx, cl, creds, http.MethodGet, path, q, nil, out)
}

// send sends one call to the Backstage API, trading the credentials for a
// token first; a refused token is traded again once. body, when not nil,
// goes as JSON.
func (t TaboolaAds) send(ctx context.Context, cl *c.Client, creds c.Creds, method, path string, q url.Values, body, out any) (int, error) {
	u := t.base(cl) + taboolaAPI + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return 0, c.Errf(c.ErrInvalid, "Taboola Ads can't take that request")
		}
	}
	var status int
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := t.token(ctx, cl, creds, attempt > 0)
		if err != nil {
			return 0, err
		}
		var rd io.Reader
		if raw != nil {
			rd = bytes.NewReader(raw)
		}
		req, err := http.NewRequest(method, u, rd)
		if err != nil {
			return 0, c.Errf(c.ErrInvalid, "bad Taboola Ads address: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if raw != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		status, err = c.DoJSON(ctx, cl, "Taboola Ads", req, out)
		if status == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		return status, err
	}
	return status, nil
}

// ---- accounts ----

type taboolaAccount struct {
	ID           flexInt  `json:"id"`
	Name         string   `json:"name"`
	AccountID    string   `json:"account_id"`
	PartnerTypes []string `json:"partner_types"`
	Currency     string   `json:"currency"`
	TimeZone     string   `json:"time_zone_name"`
}

func (a taboolaAccount) adAccount() c.AdAccount {
	acct := c.AdAccount{ID: a.AccountID, Name: a.Name, Currency: strings.ToUpper(a.Currency), TimeZone: a.TimeZone}
	if a.ID > 0 {
		// The numeric id is what the S2S endpoint takes.
		acct.Meta = map[string]string{"numeric_id": strconv.FormatInt(int64(a.ID), 10)}
	}
	return acct
}

// advertises reports whether the account runs ads (a publisher-only
// account does not).
func (a taboolaAccount) advertises() bool {
	return len(a.PartnerTypes) == 0 || slices.Contains(a.PartnerTypes, "ADVERTISER")
}

func (t TaboolaAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var me taboolaAccount
	if _, err := t.get(ctx, cl, creds, "/users/current/account", nil, &me); err != nil {
		return c.Account{}, err
	}
	accts, err := t.AdAccounts(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: me.AccountID, Name: me.Name, Details: map[string]string{"accounts": fmt.Sprint(len(accts))}}, nil
}

// AdAccounts reads the account the credentials name (account_id, or the
// credentials' own account). A network account stands for every advertiser
// under it; any other account is read on its own.
func (t TaboolaAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	root := strings.TrimSpace(creds["account_id"])
	if root == "" {
		var me taboolaAccount
		if _, err := t.get(ctx, cl, creds, "/users/current/account", nil, &me); err != nil {
			return nil, err
		}
		root = me.AccountID
	}
	if root == "" {
		return nil, c.Errf(c.ErrInvalid, "Taboola Ads named no account for these credentials; add the account id")
	}
	// A network lists its advertisers; any other account answers 404.
	var network struct {
		Results []taboolaAccount `json:"results"`
	}
	status, err := t.get(ctx, cl, creds, "/"+esc(root)+"/advertisers", nil, &network)
	switch {
	case err == nil:
		var out []c.AdAccount
		for _, a := range network.Results {
			if a.AccountID != "" && a.advertises() {
				out = append(out, a.adAccount())
			}
		}
		return out, nil
	case status != http.StatusNotFound:
		return nil, err
	}
	var allowed struct {
		Results []taboolaAccount `json:"results"`
	}
	if _, err := t.get(ctx, cl, creds, "/users/current/allowed-accounts", nil, &allowed); err != nil {
		return nil, err
	}
	for _, a := range allowed.Results {
		if a.AccountID == root {
			if !a.advertises() {
				return nil, c.Errf(c.ErrInvalid, "Taboola account %s is not an advertiser account", root)
			}
			return []c.AdAccount{a.adAccount()}, nil
		}
	}
	return nil, c.Errf(c.ErrInvalid, "these Taboola credentials can't read account %s; check the account id", root)
}

// ---- campaigns ----

// taboolaStatus maps a campaign's status onto the shared statuses. A
// campaign the advertiser paused still reads RUNNING: Taboola says so with
// is_active false ("When status = RUNNING and is_active = false, then the
// campaign is PAUSED"), which active carries; nil when it wasn't sent.
func taboolaStatus(word string, active *bool) string {
	switch strings.ToUpper(strings.TrimSpace(word)) {
	case "RUNNING":
		if active != nil && !*active {
			return c.AdPaused
		}
		return c.AdActive
	case "PAUSED":
		return c.AdPaused
	case "TERMINATED", "EXPIRED":
		return c.AdRemoved
	}
	return c.AdOther
}

func (t TaboolaAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	var out []c.AdCampaign
	for page := 1; page <= maxPages; page++ {
		q := url.Values{"page_size": {strconv.Itoa(taboolaPageSize)}, "page": {strconv.Itoa(page)}}
		var resp struct {
			Results []struct {
				ID                 string  `json:"id"`
				Name               string  `json:"name"`
				Status             string  `json:"status"`
				IsActive           *bool   `json:"is_active"`
				MarketingObjective string  `json:"marketing_objective"`
				DailyCap           flexNum `json:"daily_cap"`
			} `json:"results"`
			Metadata struct {
				Total flexInt `json:"total"`
			} `json:"metadata"`
		}
		if _, err := t.get(ctx, cl, creds, "/"+esc(acct.ID)+"/campaigns/", q, &resp); err != nil {
			return nil, err
		}
		for _, cp := range resp.Results {
			camp := c.AdCampaign{ID: cp.ID, Name: cp.Name, Status: taboolaStatus(cp.Status, cp.IsActive), ProviderStatus: cp.Status,
				Objective: strings.ToLower(cp.MarketingObjective)}
			// daily_cap is in the account's currency; 0 means none.
			if cp.DailyCap > 0 {
				camp.DailyBudgetMicros = ptrI(micros(float64(cp.DailyCap)))
			}
			out = append(out, camp)
		}
		// metadata.total says how many there are; without it, a short
		// page is the last.
		if len(resp.Results) == 0 || resp.Metadata.Total > 0 && int64(len(out)) >= int64(resp.Metadata.Total) ||
			resp.Metadata.Total == 0 && len(resp.Results) < taboolaPageSize {
			break
		}
	}
	return out, nil
}

// micros turns an amount in major units into micros.
func micros(v float64) int64 { return int64(math.Round(v * 1e6)) }

// ---- daily numbers ----

// AdDays reads the campaign summary report by campaign and day
// (campaign_day_breakdown). Days are in the account's time zone; money is
// in the account's currency, in major units. Conversions are every action
// Taboola counted (cpa_actions_num), clicks through and views through.
func (t TaboolaAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	all := days(from, to)
	var out []c.AdDay
	for start := 0; start < len(all); start += taboolaReportDays {
		window := all[start:min(start+taboolaReportDays, len(all))]
		q := url.Values{"start_date": {window[0]}, "end_date": {window[len(window)-1]}, "exclude_empty_campaigns": {"true"}}
		var resp struct {
			Results []struct {
				Date             string   `json:"date"` // "2026-10-01 00:00:00.0"
				Campaign         flexStr  `json:"campaign"`
				CampaignName     string   `json:"campaign_name"`
				Spent            flexNum  `json:"spent"`
				Impressions      flexInt  `json:"impressions"`
				Clicks           flexInt  `json:"clicks"`
				Actions          *flexNum `json:"cpa_actions_num"`
				ConversionsValue *flexNum `json:"conversions_value"`
			} `json:"results"`
		}
		if _, err := t.get(ctx, cl, creds, "/"+esc(acct.ID)+"/reports/campaign-summary/dimensions/campaign_day_breakdown", q, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Results {
			if len(r.Date) < 10 || r.Campaign == "" || r.Spent == 0 && r.Impressions == 0 && r.Clicks == 0 {
				continue
			}
			day := c.AdDay{CampaignID: string(r.Campaign), CampaignName: r.CampaignName, Date: r.Date[:10],
				SpendMicros: micros(float64(r.Spent)), Impressions: int64(r.Impressions), Clicks: int64(r.Clicks)}
			if r.Actions != nil {
				day.Conversions = ptrF(float64(*r.Actions))
			}
			if r.ConversionsValue != nil && *r.ConversionsValue > 0 {
				day.ConversionValueMicros = ptrI(micros(float64(*r.ConversionsValue)))
			}
			out = append(out, day)
		}
	}
	return out, nil
}

// ---- ads (items) ----

var _ c.AdCampaignAdsReader = TaboolaAds{}

const (
	// taboolaItemDays is the most days of ads one sync reads: the content
	// report has no day breakdown, so each day is a call of its own.
	taboolaItemDays = 31
	// taboolaItemCampaigns is the most campaigns whose items one sync
	// lists, the ones that spent most first.
	taboolaItemCampaigns = 50
)

// taboolaItem is an ad as a campaign's item list returns it.
type taboolaItem struct {
	ID            flexStr `json:"id"`
	CampaignID    flexStr `json:"campaign_id"`
	Type          string  `json:"type"`
	URL           string  `json:"url"`
	ThumbnailURL  string  `json:"thumbnail_url"`
	Title         string  `json:"title"`
	ApprovalState string  `json:"approval_state"`
	Status        string  `json:"status"`
}

// item maps a listed item. Its status is Taboola's word: RUNNING and
// PAUSED are what they say; an item being crawled, waiting for approval,
// rejected or stopped is none of the shared ones.
func (i taboolaItem) item() c.AdItem {
	st := c.AdOther
	switch strings.ToUpper(i.Status) {
	case "RUNNING":
		st = c.AdActive
	case "PAUSED":
		st = c.AdPaused
	}
	policy := map[string]string{"APPROVED": "approved", "REJECTED": "disapproved", "PENDING": "under_review"}[strings.ToUpper(i.ApprovalState)]
	return c.AdItem{ID: string(i.ID), ParentID: string(i.CampaignID), Name: i.Title, Status: st, ProviderStatus: i.Status,
		Details: adDetails(map[string]string{c.AdDetailType: strings.ToLower(i.Type), c.AdDetailHeadline: i.Title, c.AdDetailURL: i.URL,
			c.AdDetailThumbnail: i.ThumbnailURL, c.AdDetailPolicy: policy})}
}

// CampaignAds reads the ads (Taboola's items) that were shown in the
// window, with their numbers per day. The top campaign content report
// lists up to 1,000 items for a range with no day breakdown, so it is read
// a day at a time, for the window's last 31 days at most. What each ad is
// doing now comes from its campaign's item list, read for the 50 campaigns
// that spent most; an ad of another campaign keeps the status it had.
func (t TaboolaAds) CampaignAds(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdItem, []c.AdItemDay, error) {
	all := days(from, to)
	if len(all) > taboolaItemDays {
		all = all[len(all)-taboolaItemDays:]
	}
	reported := map[string]c.AdItem{}
	var order []string
	spent := map[string]int64{}
	var out []c.AdItemDay
	for _, date := range all {
		var resp struct {
			Results []struct {
				Item             flexStr  `json:"item"`
				ItemName         string   `json:"item_name"`
				ThumbnailURL     string   `json:"thumbnail_url"`
				URL              string   `json:"url"`
				Campaign         flexStr  `json:"campaign"`
				Spent            flexNum  `json:"spent"`
				Impressions      flexInt  `json:"impressions"`
				Clicks           flexInt  `json:"clicks"`
				Actions          *flexNum `json:"actions"`
				ConversionsValue *flexNum `json:"conversions_value"`
			} `json:"results"`
		}
		q := url.Values{"start_date": {date}, "end_date": {date}}
		if _, err := t.get(ctx, cl, creds, "/"+esc(acct.ID)+"/reports/top-campaign-content/dimensions/item_breakdown", q, &resp); err != nil {
			return nil, nil, err
		}
		for _, r := range resp.Results {
			id, camp := string(r.Item), string(r.Campaign)
			if id == "" || camp == "" || r.Spent == 0 && r.Impressions == 0 && r.Clicks == 0 {
				continue
			}
			day := c.AdItemDay{ID: id, ParentID: camp, Name: r.ItemName, Date: date, SpendMicros: micros(float64(r.Spent)),
				Impressions: int64(r.Impressions), Clicks: int64(r.Clicks)}
			if r.Actions != nil {
				day.Conversions = ptrF(float64(*r.Actions))
			}
			if r.ConversionsValue != nil && *r.ConversionsValue > 0 {
				day.ConversionValueMicros = ptrI(micros(float64(*r.ConversionsValue)))
			}
			out = append(out, day)
			spent[camp] += day.SpendMicros
			if _, ok := reported[id]; !ok {
				order = append(order, id)
			}
			// The newest day's name and link win.
			reported[id] = c.AdItem{ID: id, ParentID: camp, Name: r.ItemName,
				Details: adDetails(map[string]string{c.AdDetailHeadline: r.ItemName, c.AdDetailURL: r.URL, c.AdDetailThumbnail: r.ThumbnailURL})}
		}
	}
	camps := make([]string, 0, len(spent))
	for id := range spent {
		if taboolaCampaignID(id) == nil {
			camps = append(camps, id)
		}
	}
	slices.SortFunc(camps, func(a, b string) int {
		if spent[a] != spent[b] {
			if spent[a] > spent[b] {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	if len(camps) > taboolaItemCampaigns {
		camps = camps[:taboolaItemCampaigns]
	}
	var items []c.AdItem
	listed := map[string]bool{}
	for _, camp := range camps {
		var resp struct {
			Results []taboolaItem `json:"results"`
		}
		if _, err := t.get(ctx, cl, creds, t.campaignPath(acct, camp)+"/items/", nil, &resp); err != nil {
			return nil, nil, err
		}
		for _, i := range resp.Results {
			if i.ID == "" {
				continue
			}
			if i.CampaignID == "" {
				i.CampaignID = flexStr(camp)
			}
			items = append(items, i.item())
			listed[string(i.ID)] = true
		}
	}
	for _, id := range order {
		if !listed[id] {
			items = append(items, reported[id])
		}
	}
	return items, out, nil
}

// flexStr reads an id sent as a JSON string or number.
type flexStr string

func (f *flexStr) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" {
		s = ""
	}
	*f = flexStr(s)
	return nil
}

// ---- conversions ----

// taboolaCurrencies are the currencies the S2S endpoint takes for revenue.
var taboolaCurrencies = []string{"AUD", "BRL", "CAD", "CNY", "EUR", "GBP", "HKD", "ILS", "INR", "JPY", "KRW", "MXN", "NZD", "RUB", "SGD", "THB", "TRY", "USD", "ZAR"}

// ConversionTargets lists the account's active event-based conversions.
// Each one is its own target: its event name is what S2S sends, so the
// target id is the event name and a rule picks no event type.
func (t TaboolaAds) ConversionTargets(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.ConversionTarget, error) {
	var resp struct {
		Results []struct {
			DisplayName string `json:"display_name"`
			Status      string `json:"status"`
			Type        string `json:"type"`
			EventName   string `json:"event_name"`
		} `json:"results"`
	}
	if _, err := t.get(ctx, cl, creds, "/"+esc(acct.ID)+"/universal_pixel/conversion_rule", nil, &resp); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []c.ConversionTarget{}
	for _, r := range resp.Results {
		if r.Type != "EVENT_BASED" || r.EventName == "" || !strings.EqualFold(r.Status, "ACTIVE") || seen[r.EventName] {
			continue
		}
		seen[r.EventName] = true
		name := r.DisplayName
		if name == "" {
			name = r.EventName
		}
		out = append(out, c.ConversionTarget{ID: r.EventName, Name: name, EventTypes: []string{}})
	}
	return out, nil
}

// SendConversions posts S2S actions in bulk, matched on Taboola's click id
// (tblci on the landing page, click-id here); the target is the event
// name. Taboola takes the batch with 204 and processes it later, so a sent
// conversion is only known to have been received. The conversion's id goes
// as the order id.
func (t TaboolaAds) SendConversions(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, target c.ConversionTarget, convs []c.AdConversion) ([]c.ConversionResult, error) {
	numeric := acct.Meta["numeric_id"]
	if numeric == "" {
		return nil, c.Errf(c.ErrInvalid, "Taboola account %s has no numeric id yet; sync it, then try again", acct.ID)
	}
	results := make([]c.ConversionResult, len(convs))
	var actions []map[string]any
	var sentIdx []int
	for i, cv := range convs {
		results[i] = c.ConversionResult{Index: cv.Index}
		click := cv.ClickIDs["tblci"]
		if click == "" {
			results[i].Skipped, results[i].Reason = true, "no Taboola click id (tblci) to match on"
			continue
		}
		a := map[string]any{"click-id": click, "timestamp": cv.Time.UnixMilli(), "name": target.ID}
		if cv.EventID != "" {
			a["orderid"] = cv.EventID
		}
		cur := strings.ToUpper(cv.Currency)
		if cv.ValueMicros != nil && (cur == "" || slices.Contains(taboolaCurrencies, cur)) {
			a["revenue"] = float64(*cv.ValueMicros) / 1e6
			if cur != "" {
				a["currency"] = cur
			}
		}
		results[i].MatchKeys = []string{"tblci"}
		actions = append(actions, a)
		sentIdx = append(sentIdx, i)
	}
	u := cl.Base("taboola_ads.conversions", taboolaS2S) + "/" + esc(numeric) + "/log/3/bulk-s2s-action"
	for start := 0; start < len(actions); start += taboolaBatch {
		end := min(start+taboolaBatch, len(actions))
		// No token: the endpoint takes none.
		if err := newCall(cl, "Taboola Ads").post(ctx, u, map[string]any{"actions": actions[start:end]}, nil); err != nil {
			if start == 0 {
				return nil, err
			}
			for _, i := range sentIdx[start:] {
				results[i].Reason = err.Error()
			}
			return results, nil
		}
		for _, i := range sentIdx[start:end] {
			results[i].OK = true
		}
	}
	return results, nil
}

// ---- changes ----
//
// Outflare changes a Taboola campaign only when asked (Backstage API,
// "Update a Campaign" and "Patching targeted publishers"): it pauses or
// resumes it, sets its daily cap, and blocks or unblocks one publisher
// site on it. A campaign is updated with POST on its own address, where
// fields left out stay as they are.

var (
	_ c.AdsManager         = TaboolaAds{}
	_ c.AdExclusionManager = TaboolaAds{}
)

// taboolaCampaignID checks an id before it goes into a path: Taboola's
// campaign ids are numbers.
func taboolaCampaignID(id string) error {
	if id == "" || strings.Trim(id, "0123456789") != "" {
		return c.Errf(c.ErrInvalid, "not a Taboola campaign id: %q", id)
	}
	return nil
}

func (t TaboolaAds) campaignPath(acct c.AdAccount, campaignID string) string {
	return "/" + esc(acct.ID) + "/campaigns/" + esc(campaignID)
}

// AdCampaign reads one campaign.
func (t TaboolaAds) AdCampaign(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) (c.AdCampaign, error) {
	if err := taboolaCampaignID(campaignID); err != nil {
		return c.AdCampaign{}, err
	}
	var cp struct {
		ID                 flexStr `json:"id"`
		Name               string  `json:"name"`
		Status             string  `json:"status"`
		IsActive           *bool   `json:"is_active"`
		MarketingObjective string  `json:"marketing_objective"`
		DailyCap           flexNum `json:"daily_cap"`
	}
	status, err := t.get(ctx, cl, creds, t.campaignPath(acct, campaignID)+"/", nil, &cp)
	if status == http.StatusNotFound {
		return c.AdCampaign{}, c.ErrNotFound
	}
	if err != nil {
		return c.AdCampaign{}, err
	}
	camp := c.AdCampaign{ID: campaignID, Name: cp.Name, Status: taboolaStatus(cp.Status, cp.IsActive), ProviderStatus: cp.Status,
		Objective: strings.ToLower(cp.MarketingObjective)}
	if cp.DailyCap > 0 {
		camp.DailyBudgetMicros = ptrI(micros(float64(cp.DailyCap)))
	}
	return camp, nil
}

// SetCampaignStatus pauses or resumes a campaign (is_active).
func (t TaboolaAds) SetCampaignStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, active bool) error {
	if err := taboolaCampaignID(campaignID); err != nil {
		return err
	}
	_, err := t.send(ctx, cl, creds, http.MethodPost, t.campaignPath(acct, campaignID), nil, map[string]any{"is_active": active}, nil)
	return err
}

var _ c.AdStatusManager = TaboolaAds{}

// SetAdStatus pauses or resumes one ad (an item's is_active), leaving the
// campaign and its other items as they are. Taboola has no ad groups, so
// adGroupID is the campaign's id. An item still waiting for approval is
// paused once approved, which is Taboola's own rule.
func (t TaboolaAds) SetAdStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, adGroupID, adID string, active bool) error {
	if err := taboolaCampaignID(adGroupID); err != nil {
		return err
	}
	if adID == "" || strings.Trim(adID, "0123456789") != "" {
		return c.Errf(c.ErrInvalid, "not a Taboola item id: %q", adID)
	}
	_, err := t.send(ctx, cl, creds, http.MethodPost, t.campaignPath(acct, adGroupID)+"/items/"+esc(adID)+"/", nil, map[string]any{"is_active": active}, nil)
	return err
}

// SetCampaignDailyBudget sets a campaign's daily cap, in the account's
// currency, rounded to the cent.
func (t TaboolaAds) SetCampaignDailyBudget(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, budget int64) error {
	if err := taboolaCampaignID(campaignID); err != nil {
		return err
	}
	if budget <= 0 {
		return c.Errf(c.ErrInvalid, "the daily budget must be more than nothing")
	}
	cents := (budget + 5_000) / 10_000
	_, err := t.send(ctx, cl, creds, http.MethodPost, t.campaignPath(acct, campaignID), nil, map[string]any{"daily_cap": float64(cents) / 100}, nil)
	return err
}

// ExclusionKinds: a placement is a publisher site, by Taboola's alphabetic
// site id (the site column of its reports, what {site} carries on a
// click), which is what a campaign's publisher targeting takes.
func (TaboolaAds) ExclusionKinds() []string { return []string{c.ExcludePlacement} }

// taboolaSite is a publisher's alphabetic id, such as examplenews-home.
var taboolaSite = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)

// patchPublisher adds a publisher to the campaign's blocked list or takes
// it off. Taboola patches the list one publisher at a time, so the others
// stay as they are.
func (t TaboolaAds) patchPublisher(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, x c.AdExclusion, op string) error {
	if err := taboolaCampaignID(campaignID); err != nil {
		return err
	}
	site := strings.TrimSpace(x.Value)
	if x.Kind != c.ExcludePlacement || !taboolaSite.MatchString(site) {
		return c.Errf(c.ErrInvalid, "Taboola blocks publisher sites: the site's id from its reports, such as examplenews-home")
	}
	_, err := t.send(ctx, cl, creds, http.MethodPatch, t.campaignPath(acct, campaignID), nil,
		map[string]any{"patch_operation": op, "publisher_targeting": map[string]any{"publishers": []string{site}}}, nil)
	return err
}

// AddExclusion blocks one publisher site on a campaign.
func (t TaboolaAds) AddExclusion(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, x c.AdExclusion) error {
	return t.patchPublisher(ctx, cl, creds, acct, campaignID, x, "ADD")
}

// RemoveExclusion unblocks it.
func (t TaboolaAds) RemoveExclusion(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, x c.AdExclusion) error {
	return t.patchPublisher(ctx, cl, creds, acct, campaignID, x, "REMOVE")
}
