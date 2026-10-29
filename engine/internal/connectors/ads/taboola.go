package ads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/url"
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
		"Uses your own Backstage API credentials: ask your Taboola account manager for API access, and paste the client id and secret they issue. A network's credentials read every advertiser account under it. Read-only; conversions go back to your event-based conversions as server-to-server actions, matched on Taboola's click id.")
	d.Auth = c.AuthAPIKey
	d.Capabilities = append(d.Capabilities, c.CapAdsConversions)
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

// get sends one GET to the Backstage API, trading the credentials for a
// token first; a refused token is traded again once.
func (t TaboolaAds) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) (int, error) {
	u := t.base(cl) + taboolaAPI + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var status int
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := t.token(ctx, cl, creds, attempt > 0)
		if err != nil {
			return 0, err
		}
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return 0, c.Errf(c.ErrInvalid, "bad Taboola Ads address: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
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

// taboolaStatus maps a campaign's status onto the shared statuses.
func taboolaStatus(word string) string {
	switch strings.ToUpper(strings.TrimSpace(word)) {
	case "RUNNING":
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
			camp := c.AdCampaign{ID: cp.ID, Name: cp.Name, Status: taboolaStatus(cp.Status), ProviderStatus: cp.Status,
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
