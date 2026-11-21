package ads

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// OutbrainAds reads Outbrain marketers (ad accounts) through the Amplify
// API (v0.1). Amplify has no sign-in app: the workspace gives its own
// Outbrain username and password, which are traded for an OB-TOKEN-V1
// token at GET /login. A token lasts 30 days and Outbrain allows two
// logins per hour per user, so the token is stored with the sealed
// credentials and renewed through CredsRefresher well before it expires.
// It only reads: the Amplify API has no endpoint that takes conversions.
type OutbrainAds struct{}

const (
	outbrainBase = "https://api.outbrain.com/amplify/v0.1"
	// outbrainTokenLife is how long a stored token is used before a new
	// one is fetched; Outbrain's last 30 days.
	outbrainTokenLife = 25 * 24 * time.Hour
	// outbrainReportDays is the longest range one daily report call asks
	// for, so one page holds every day of every campaign.
	outbrainReportDays = 31
	// outbrainCampaignPage is the most campaigns one list call returns.
	outbrainCampaignPage = 50
)

func (OutbrainAds) Definition() c.Definition {
	d := def("outbrain_ads", "Outbrain Ads", "https://www.outbrain.com/advertisers/",
		"https://developer.outbrain.com/docs/amplify/api/overview/", "https://my.outbrain.com/create-token",
		"Spend, impressions, clicks and conversions per campaign from your Outbrain marketers.",
		"Uses your own Outbrain login, once Outbrain has given it Amplify API access (apply on Outbrain's developer site). Outflare trades the username and password for an API token every few weeks and reads every marketer the login can see. It changes a campaign only when you ask: pause, resume, its own daily budget, blocking a publisher or section on it, and switching one of its ads off or on.")
	d.Auth = c.AuthAPIKey
	d.Capabilities = append(d.Capabilities, c.CapAdsManage)
	d.Fields = []c.Field{
		{Name: "username", Label: "Username", Help: "The Outbrain login with Amplify API access."},
		{Name: "password", Label: "Password", Secret: true, Help: "Outbrain allows two token requests an hour, so connecting again right away can be refused for an hour."},
	}
	return d
}

func (OutbrainAds) base(cl *c.Client) string { return cl.Base("outbrain_ads", outbrainBase) }

// login trades the username and password for a token.
func (o OutbrainAds) login(ctx context.Context, cl *c.Client, creds c.Creds) (string, error) {
	user, pass := strings.TrimSpace(creds["username"]), creds["password"]
	if user == "" || pass == "" {
		return "", c.Errf(c.ErrAuth, "add the Outbrain username and password")
	}
	req, err := http.NewRequest(http.MethodGet, o.base(cl)+"/login", nil)
	if err != nil {
		return "", c.Errf(c.ErrInvalid, "bad Outbrain Ads address: %v", err)
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	var out map[string]string
	status, err := c.DoJSON(ctx, cl, "Outbrain Ads", req, &out)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "", c.Errf(c.ErrAuth, "Outbrain refused the username or password (HTTP %d). Check them, and that the login has Amplify API access.", status)
	case status == http.StatusTooManyRequests:
		return "", c.Errf(c.ErrQuota, "Outbrain allows two token requests an hour per login. Try again in an hour.")
	case err != nil:
		return "", err
	}
	token := out["OB-TOKEN-V1"]
	if token == "" {
		return "", c.Errf(c.ErrUnavailable, "Outbrain did not return a token")
	}
	return token, nil
}

// RefreshDue reports whether there is no stored token or it is old enough
// to renew.
func (OutbrainAds) RefreshDue(creds c.Creds) bool {
	at, err := strconv.ParseInt(creds["token_at"], 10, 64)
	return creds["token"] == "" || err != nil || time.Since(time.Unix(at, 0)) > outbrainTokenLife
}

// RefreshCreds fetches a new token; older tokens stay valid until they
// expire.
func (o OutbrainAds) RefreshCreds(ctx context.Context, cl *c.Client, creds c.Creds) (c.Creds, error) {
	token, err := o.login(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	out := c.Creds{}
	for k, v := range creds {
		out[k] = v
	}
	out["token"] = token
	out["token_at"] = strconv.FormatInt(time.Now().Unix(), 10)
	return out, nil
}

// token is the stored token, or a new one when there is none yet (a
// connection being tested before it is saved).
func (o OutbrainAds) token(ctx context.Context, cl *c.Client, creds c.Creds) (string, error) {
	if t := creds["token"]; t != "" {
		return t, nil
	}
	return o.login(ctx, cl, creds)
}

func (o OutbrainAds) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) error {
	token, err := o.token(ctx, cl, creds)
	if err != nil {
		return err
	}
	u := o.base(cl) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return newCall(cl, "Outbrain Ads").set("OB-TOKEN-V1", token).get(ctx, u, out)
}

type outbrainMarketer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func (o OutbrainAds) marketers(ctx context.Context, cl *c.Client, creds c.Creds) ([]outbrainMarketer, error) {
	var resp struct {
		Marketers []outbrainMarketer `json:"marketers"`
	}
	if err := o.get(ctx, cl, creds, "/marketers", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Marketers, nil
}

func (o OutbrainAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if creds["token"] == "" {
		// Log in once and use the token for the calls below.
		token, err := o.login(ctx, cl, creds)
		if err != nil {
			return c.Account{}, err
		}
		creds = c.Creds{"token": token}
	}
	ms, err := o.marketers(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	acct := c.Account{Details: map[string]string{"accounts": fmt.Sprint(len(ms))}}
	if len(ms) == 1 {
		acct.ID, acct.Name = ms[0].ID, ms[0].Name
	}
	return acct, nil
}

// AdAccounts lists the marketers the login can see. Spend is in the
// marketer's currency, which Amplify gives on its budgets; a marketer
// without a budget has spent nothing yet and is listed in USD.
func (o OutbrainAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	ms, err := o.marketers(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdAccount, 0, len(ms))
	for _, m := range ms {
		if m.ID == "" {
			continue
		}
		acct := c.AdAccount{ID: m.ID, Name: m.Name, Currency: "USD"}
		var b struct {
			Budgets []struct {
				Currency string `json:"currency"`
			} `json:"budgets"`
		}
		if err := o.get(ctx, cl, creds, "/marketers/"+esc(m.ID)+"/budgets", nil, &b); err != nil {
			return nil, err
		}
		for _, x := range b.Budgets {
			if x.Currency != "" {
				acct.Currency = strings.ToUpper(x.Currency)
				break
			}
		}
		out = append(out, acct)
	}
	return out, nil
}

// outbrainCampaign is a campaign as Amplify returns it. BlockedSites is
// there only when asked for (extraFields=BlockedSites).
type outbrainCampaign struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Enabled      bool            `json:"enabled"`
	AutoArchived bool            `json:"autoArchived"`
	Objective    json.RawMessage `json:"objective"`
	Budget       *struct {
		ID          string   `json:"id"`
		Shared      bool     `json:"shared"`
		Type        string   `json:"type"`
		Pacing      string   `json:"pacing"`
		Amount      *flexNum `json:"amount"`
		DailyTarget *flexNum `json:"dailyTarget"`
	} `json:"budget"`
	BlockedSites *struct {
		BlockedPublishers []outbrainBlocked `json:"blockedPublishers"`
		BlockedSections   []outbrainBlocked `json:"blockedSections"`
	} `json:"blockedSites"`
}

// outbrainBlocked is a blocked publisher or section; only its id is sent
// back.
type outbrainBlocked struct {
	ID string `json:"id"`
}

func (cp outbrainCampaign) adCampaign() c.AdCampaign {
	camp := c.AdCampaign{ID: cp.ID, Name: cp.Name}
	switch {
	case cp.AutoArchived:
		camp.Status, camp.ProviderStatus = c.AdRemoved, "ARCHIVED"
	case cp.Enabled:
		camp.Status, camp.ProviderStatus = c.AdActive, "ENABLED"
	default:
		camp.Status, camp.ProviderStatus = c.AdPaused, "DISABLED"
	}
	var objective string
	if json.Unmarshal(cp.Objective, &objective) == nil {
		camp.Objective = strings.ToLower(objective)
	}
	if b := cp.Budget; b != nil {
		switch {
		case strings.EqualFold(b.Type, "DAILY") && b.Amount != nil && *b.Amount > 0:
			camp.DailyBudgetMicros = ptrI(c.Micros(float64(*b.Amount)))
		case strings.EqualFold(b.Pacing, "DAILY_TARGET") && b.DailyTarget != nil && *b.DailyTarget > 0:
			camp.DailyBudgetMicros = ptrI(c.Micros(float64(*b.DailyTarget)))
		}
	}
	return camp
}

// AdCampaigns lists the marketer's campaigns, archived ones included.
func (o OutbrainAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	var out []c.AdCampaign
	for page := 0; page < maxPages; page++ {
		q := url.Values{"includeArchived": {"true"}, "fetch": {"all"},
			"limit": {strconv.Itoa(outbrainCampaignPage)}, "offset": {strconv.Itoa(page * outbrainCampaignPage)}}
		var resp struct {
			Campaigns  []outbrainCampaign `json:"campaigns"`
			TotalCount int                `json:"totalCount"`
		}
		if err := o.get(ctx, cl, creds, "/marketers/"+esc(acct.ID)+"/campaigns", q, &resp); err != nil {
			return nil, err
		}
		for _, cp := range resp.Campaigns {
			out = append(out, cp.adCampaign())
		}
		if len(resp.Campaigns) < outbrainCampaignPage || (page+1)*outbrainCampaignPage >= resp.TotalCount {
			break
		}
	}
	return out, nil
}

// AdDays reads the daily report by campaign. Each call covers at most 31
// days and asks for that many results per campaign, so nothing is left on
// a second page. Conversions are Outbrain's conversions from clicks, by
// conversion date, with their value.
func (o OutbrainAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	all := days(from, to)
	var out []c.AdDay
	for start := 0; start < len(all); start += outbrainReportDays {
		window := all[start:min(start+outbrainReportDays, len(all))]
		q := url.Values{"from": {window[0]}, "to": {window[len(window)-1]}, "breakdown": {"daily"},
			"limit": {strconv.Itoa(len(window))}, "includeArchivedCampaigns": {"true"}}
		var resp struct {
			CampaignResults []struct {
				CampaignID string `json:"campaignId"`
				Results    []struct {
					Metadata struct {
						ID       string `json:"id"`
						FromDate string `json:"fromDate"`
					} `json:"metadata"`
					Metrics struct {
						Impressions flexInt  `json:"impressions"`
						Clicks      flexInt  `json:"clicks"`
						Spend       flexNum  `json:"spend"`
						Conversions *flexNum `json:"conversions"`
						SumValue    *flexNum `json:"sumValue"`
					} `json:"metrics"`
				} `json:"results"`
			} `json:"campaignResults"`
		}
		if err := o.get(ctx, cl, creds, "/reports/marketers/"+esc(acct.ID)+"/campaigns/periodic", q, &resp); err != nil {
			return nil, err
		}
		for _, cr := range resp.CampaignResults {
			for _, r := range cr.Results {
				date := r.Metadata.FromDate
				if date == "" {
					date = r.Metadata.ID
				}
				m := r.Metrics
				if len(date) < 10 || m.Spend == 0 && m.Impressions == 0 && m.Clicks == 0 {
					continue
				}
				day := c.AdDay{CampaignID: cr.CampaignID, Date: date[:10], SpendMicros: c.Micros(float64(m.Spend)),
					Impressions: int64(m.Impressions), Clicks: int64(m.Clicks)}
				if m.Conversions != nil {
					day.Conversions = ptrF(float64(*m.Conversions))
				}
				if m.SumValue != nil && *m.SumValue > 0 {
					day.ConversionValueMicros = ptrI(c.Micros(float64(*m.SumValue)))
				}
				out = append(out, day)
			}
		}
	}
	return out, nil
}

// ---- ads (promoted links) ----

var _ c.AdCampaignAdsReader = OutbrainAds{}

const (
	// outbrainItemDays is the most days of ads one sync reads.
	outbrainItemDays = 31
	// outbrainItemCampaigns is how many campaigns' ads get their days, the
	// ones that spent most first: each is a report call, and a marketer has
	// 30 report calls a minute.
	outbrainItemCampaigns = 10
	// outbrainContentPage is how many promoted links one report page asks
	// for, and outbrainContentPages how many pages are read.
	outbrainContentPage  = 500
	outbrainContentPages = 4
)

// outbrainMetrics are a report row's numbers.
type outbrainMetrics struct {
	Impressions flexInt  `json:"impressions"`
	Clicks      flexInt  `json:"clicks"`
	Spend       flexNum  `json:"spend"`
	Conversions *flexNum `json:"conversions"`
	SumValue    *flexNum `json:"sumValue"`
}

// outbrainApproval reads a promoted link's approval, sent as a word or as
// an object with a status, onto the shared policy words.
func outbrainApproval(raw json.RawMessage) string {
	var word string
	if json.Unmarshal(raw, &word) != nil {
		var obj struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(raw, &obj)
		word = obj.Status
	}
	return map[string]string{"APPROVED": "approved", "REJECTED": "disapproved", "PENDING": "under_review"}[strings.ToUpper(word)]
}

// CampaignAds reads the ads (Outbrain's promoted links) that were shown in
// the window's last 31 days: which they are, with their title, landing
// page, whether they are switched on and whether Outbrain approved them,
// from the promoted content report; and their numbers per day from the
// periodic content report of the 10 campaigns that spent most, one call
// each. Ads of the other campaigns are listed without days.
func (o OutbrainAds) CampaignAds(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdItem, []c.AdItemDay, error) {
	all := days(from, to)
	if len(all) > outbrainItemDays {
		all = all[len(all)-outbrainItemDays:]
	}
	if len(all) == 0 {
		return nil, nil, nil
	}
	from, to = all[0], all[len(all)-1]
	var items []c.AdItem
	parent := map[string]string{}
	spent := map[string]int64{}
	for page := 0; page < outbrainContentPages; page++ {
		q := url.Values{"from": {from}, "to": {to}, "includeArchivedCampaigns": {"true"},
			"limit": {strconv.Itoa(outbrainContentPage)}, "offset": {strconv.Itoa(page * outbrainContentPage)}, "sort": {"-spend"}}
		var resp struct {
			Results []struct {
				Metadata struct {
					ID             string          `json:"id"`
					Title          string          `json:"title"`
					URL            string          `json:"url"`
					Enabled        *bool           `json:"enabled"`
					ApprovalStatus json.RawMessage `json:"approvalStatus"`
					CampaignID     string          `json:"campaignId"`
				} `json:"metadata"`
				Metrics outbrainMetrics `json:"metrics"`
			} `json:"results"`
		}
		if err := o.get(ctx, cl, creds, "/reports/marketers/"+esc(acct.ID)+"/promotedContent", q, &resp); err != nil {
			return nil, nil, err
		}
		for _, r := range resp.Results {
			m := r.Metadata
			if m.ID == "" || m.CampaignID == "" || parent[m.ID] != "" {
				continue
			}
			it := c.AdItem{ID: m.ID, ParentID: m.CampaignID, Name: m.Title,
				Details: adDetails(map[string]string{c.AdDetailHeadline: m.Title, c.AdDetailURL: m.URL, c.AdDetailPolicy: outbrainApproval(m.ApprovalStatus)})}
			// A report that doesn't say whether the link is on leaves the
			// status as it was.
			if m.Enabled != nil {
				it.Status, it.ProviderStatus = c.AdPaused, "DISABLED"
				if *m.Enabled {
					it.Status, it.ProviderStatus = c.AdActive, "ENABLED"
				}
			}
			items = append(items, it)
			parent[m.ID] = m.CampaignID
			spent[m.CampaignID] += c.Micros(float64(r.Metrics.Spend))
		}
		if len(resp.Results) < outbrainContentPage {
			break
		}
	}
	camps := make([]string, 0, len(spent))
	for id := range spent {
		if outbrainID.MatchString(id) {
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
	if len(camps) > outbrainItemCampaigns {
		camps = camps[:outbrainItemCampaigns]
	}
	var out []c.AdItemDay
	for _, camp := range camps {
		q := url.Values{"from": {from}, "to": {to}, "breakdown": {"daily"}, "limit": {strconv.Itoa(len(all))}, "includeArchivedCampaigns": {"true"}}
		var resp struct {
			PromotedLinkResults []struct {
				PromotedLinkID string `json:"promotedLinkId"`
				Results        []struct {
					Metadata struct {
						ID       string `json:"id"`
						FromDate string `json:"fromDate"`
					} `json:"metadata"`
					Metrics outbrainMetrics `json:"metrics"`
				} `json:"results"`
			} `json:"promotedLinkResults"`
		}
		if err := o.get(ctx, cl, creds, "/reports/marketers/"+esc(acct.ID)+"/campaigns/"+esc(camp)+"/periodicContent", q, &resp); err != nil {
			return nil, nil, err
		}
		for _, pl := range resp.PromotedLinkResults {
			for _, r := range pl.Results {
				date := r.Metadata.FromDate
				if date == "" {
					date = r.Metadata.ID
				}
				m := r.Metrics
				if pl.PromotedLinkID == "" || len(date) < 10 || m.Spend == 0 && m.Impressions == 0 && m.Clicks == 0 {
					continue
				}
				day := c.AdItemDay{ID: pl.PromotedLinkID, ParentID: camp, Date: date[:10], SpendMicros: c.Micros(float64(m.Spend)),
					Impressions: int64(m.Impressions), Clicks: int64(m.Clicks)}
				if m.Conversions != nil {
					day.Conversions = ptrF(float64(*m.Conversions))
				}
				if m.SumValue != nil && *m.SumValue > 0 {
					day.ConversionValueMicros = ptrI(c.Micros(float64(*m.SumValue)))
				}
				out = append(out, day)
			}
		}
	}
	return items, out, nil
}

// ---- changes ----
//
// Outflare changes an Outbrain campaign only when asked (Amplify API,
// "Update an Existing Campaign" and "Update an existing Budget", where
// fields left out of a PUT stay as they are): it pauses or resumes it,
// sets its own daily budget, and blocks or unblocks a publisher or a
// section on it.

var (
	_ c.AdsManager         = OutbrainAds{}
	_ c.AdExclusionManager = OutbrainAds{}
)

// Outbrain's own limits on a campaign's blocked list.
const (
	outbrainMaxPublishers = 30
	outbrainMaxSections   = 100
)

// outbrainID is an Amplify id (a campaign's, a budget's, a publisher's or
// a section's), checked before it goes into a path or a list.
var outbrainID = regexp.MustCompile(`^[0-9A-Za-z_-]{1,80}$`)

// send sends one call to the Amplify API and returns its HTTP status.
func (o OutbrainAds) send(ctx context.Context, cl *c.Client, creds c.Creds, method, path string, q url.Values, body, out any) (int, error) {
	token, err := o.token(ctx, cl, creds)
	if err != nil {
		return 0, err
	}
	u := o.base(cl) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, c.Errf(c.ErrInvalid, "Outbrain Ads can't take that request")
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return 0, c.Errf(c.ErrInvalid, "bad Outbrain Ads address: %v", err)
	}
	req.Header.Set("OB-TOKEN-V1", token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.DoJSON(ctx, cl, "Outbrain Ads", req, out)
}

// campaign reads one campaign with its blocked publishers and sections.
func (o OutbrainAds) campaign(ctx context.Context, cl *c.Client, creds c.Creds, campaignID string) (outbrainCampaign, error) {
	var cp outbrainCampaign
	if !outbrainID.MatchString(campaignID) {
		return cp, c.Errf(c.ErrInvalid, "not an Outbrain campaign id: %q", campaignID)
	}
	status, err := o.send(ctx, cl, creds, http.MethodGet, "/campaigns/"+esc(campaignID), url.Values{"extraFields": {"BlockedSites"}}, nil, &cp)
	if status == http.StatusNotFound {
		return cp, c.ErrNotFound
	}
	return cp, err
}

// AdCampaign reads one campaign.
func (o OutbrainAds) AdCampaign(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) (c.AdCampaign, error) {
	cp, err := o.campaign(ctx, cl, creds, campaignID)
	if err != nil {
		return c.AdCampaign{}, err
	}
	camp := cp.adCampaign()
	camp.ID = campaignID
	return camp, nil
}

// SetCampaignStatus pauses or resumes a campaign (enabled).
func (o OutbrainAds) SetCampaignStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, active bool) error {
	if !outbrainID.MatchString(campaignID) {
		return c.Errf(c.ErrInvalid, "not an Outbrain campaign id: %q", campaignID)
	}
	_, err := o.send(ctx, cl, creds, http.MethodPut, "/campaigns/"+esc(campaignID), nil, map[string]any{"enabled": active}, nil)
	return err
}

var _ c.AdStatusManager = OutbrainAds{}

// SetAdStatus switches one ad (a promoted link) off or on, leaving the
// campaign and its other promoted links as they are. Outbrain has no ad
// groups, so adGroupID is the campaign's id; the call takes the promoted
// link's id alone.
func (o OutbrainAds) SetAdStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, adGroupID, adID string, active bool) error {
	if !outbrainID.MatchString(adID) {
		return c.Errf(c.ErrInvalid, "not an Outbrain promoted link id: %q", adID)
	}
	_, err := o.send(ctx, cl, creds, http.MethodPut, "/promotedLinks/"+esc(adID), nil, map[string]any{"enabled": active}, nil)
	return err
}

// SetCampaignDailyBudget sets the campaign's daily budget, rounded to the
// cent: the amount of a DAILY budget, or the daily target of one paced by
// a daily target, which is what AdCampaigns reads as the daily budget. A
// budget other campaigns share, or one that isn't daily, is refused: a
// change there would not mean what was asked.
func (o OutbrainAds) SetCampaignDailyBudget(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, micros int64) error {
	if micros <= 0 {
		return c.Errf(c.ErrInvalid, "the daily budget must be more than nothing")
	}
	cp, err := o.campaign(ctx, cl, creds, campaignID)
	if err != nil {
		return err
	}
	b := cp.Budget
	if b == nil || !outbrainID.MatchString(b.ID) {
		return c.Errf(c.ErrInvalid, "Outbrain shows no budget on this campaign")
	}
	if b.Shared {
		return c.Errf(c.ErrInvalid, "This campaign's budget is shared with other campaigns. Change it in Outbrain, where you can see them all.")
	}
	amount := float64((micros+5_000)/10_000) / 100
	var body map[string]any
	switch {
	case strings.EqualFold(b.Type, "DAILY"):
		body = map[string]any{"amount": amount}
	case strings.EqualFold(b.Pacing, "DAILY_TARGET"):
		body = map[string]any{"dailyTarget": amount}
	default:
		return c.Errf(c.ErrInvalid, "This campaign's budget is %s, not a daily one. Change it in Outbrain.", strings.ToLower(cmp.Or(b.Type, "of another kind")))
	}
	_, err = o.send(ctx, cl, creds, http.MethodPut, "/budgets/"+esc(b.ID), nil, body, nil)
	return err
}

// ExclusionKinds: a placement is a publisher, or with Detail "section" one
// of a publisher's sections, by the id Outbrain's publisher and section
// reports give it.
func (OutbrainAds) ExclusionKinds() []string { return []string{c.ExcludePlacement} }

// block adds a publisher or section to a campaign's blocked list or takes
// it off. Outbrain takes the whole list on every update ("a full list
// including publishers / sections which are already blocked"), so the
// current list is read first and sent back with the one change. If the
// current list can't be read, nothing is sent: a list with only the new
// entry would unblock everything else.
func (o OutbrainAds) block(ctx context.Context, cl *c.Client, creds c.Creds, campaignID string, x c.AdExclusion, add bool) error {
	id := strings.TrimSpace(x.Value)
	section := strings.EqualFold(strings.TrimSpace(x.Detail), "section")
	if x.Kind != c.ExcludePlacement || !outbrainID.MatchString(id) {
		return c.Errf(c.ErrInvalid, "Outbrain blocks publishers and sections: the id from its publisher or section report")
	}
	cp, err := o.campaign(ctx, cl, creds, campaignID)
	if err != nil {
		return err
	}
	if cp.BlockedSites == nil {
		return c.Errf(c.ErrUnavailable, "Outbrain didn't say what this campaign blocks now, so nothing was changed")
	}
	publishers, sections := cp.BlockedSites.BlockedPublishers, cp.BlockedSites.BlockedSections
	list := &publishers
	limit, what := outbrainMaxPublishers, "publishers"
	if section {
		list, limit, what = &sections, outbrainMaxSections, "sections"
	}
	at := slices.IndexFunc(*list, func(b outbrainBlocked) bool { return b.ID == id })
	switch {
	case add && at >= 0, !add && at < 0:
		return nil // already as asked
	case add && len(*list) >= limit:
		return c.Errf(c.ErrInvalid, "Outbrain lets a campaign block %d %s, and this one already does. Unblock one first.", limit, what)
	case add:
		*list = append(*list, outbrainBlocked{ID: id})
	default:
		*list = slices.Delete(*list, at, at+1)
	}
	// Ids only, and empty lists as [], never null. Every entry Outbrain
	// listed goes back as it came: one without an id means the list wasn't
	// understood, and leaving it out would unblock it.
	for _, in := range [][]outbrainBlocked{publishers, sections} {
		for _, b := range in {
			if b.ID == "" {
				return c.Errf(c.ErrUnavailable, "Outbrain's list of what this campaign blocks couldn't be read in full, so nothing was changed")
			}
		}
	}
	clean := func(in []outbrainBlocked) []outbrainBlocked {
		return append([]outbrainBlocked{}, in...)
	}
	body := map[string]any{"blockedSites": map[string]any{"blockedPublishers": clean(publishers), "blockedSections": clean(sections)}}
	_, err = o.send(ctx, cl, creds, http.MethodPut, "/campaigns/"+esc(campaignID), url.Values{"extraFields": {"BlockedSites"}}, body, nil)
	return err
}

// AddExclusion blocks a publisher or a section on a campaign.
func (o OutbrainAds) AddExclusion(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, x c.AdExclusion) error {
	return o.block(ctx, cl, creds, campaignID, x, true)
}

// RemoveExclusion unblocks it.
func (o OutbrainAds) RemoveExclusion(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, x c.AdExclusion) error {
	return o.block(ctx, cl, creds, campaignID, x, false)
}
