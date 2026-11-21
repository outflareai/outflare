package ads

import (
	"cmp"
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ZeroparkAds reads a Zeropark advertiser account through the Stats API
// (panel.zeropark.com/api): its campaigns, and what each spent and got per
// day. The workspace pastes an API access token it makes in Zeropark (My
// account, Security), sent in the api-token header. Zeropark pays per
// visit (a redirect), so a day's clicks are its redirects; it reports no
// impressions. Days are in the account's own time zone (My account), which
// Zeropark also uses for the dates it is asked about. Reading changes
// nothing; the changes it can make are at the end of this file.
//
// Docs: the Zeropark Stats API user guide and the API reference in the
// panel (My account, API documentation; /v3/api-docs).
type ZeroparkAds struct{}

const (
	zeroparkBase = "https://panel.zeropark.com"
	// zeroparkPage is how many campaigns one page asks for (Zeropark
	// takes up to 10,000).
	zeroparkPage = 1000
)

func (ZeroparkAds) Definition() c.Definition {
	d := def("zeropark_ads", "Zeropark", "https://zeropark.com",
		"https://doc.zeropark.com/article/zeropark-api-usage-guide", "",
		"Spend, visits, conversions and payout per campaign and day from your Zeropark account.",
		"Uses your own API access token: in Zeropark, open My account, then Security, and add an access token. Zeropark shows it once, so paste it here straight away; it recommends making a new one every 30 days. Syncing only reads; a campaign is paused, resumed or given a new daily budget, and a target paused or resumed, only when a person asks or approves it.")
	d.Auth = c.AuthAPIKey
	d.Capabilities = append(d.Capabilities, c.CapAdsTargets, c.CapAdsManage, c.CapAdsBids)
	d.Fields = []c.Field{
		{Name: "api_token", Label: "API access token", Secret: true, Help: "My account, Security, Access tokens, Add new."},
	}
	return d
}

func (ZeroparkAds) base(cl *c.Client) string { return cl.Base("zeropark_ads", zeroparkBase) }

func (z ZeroparkAds) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) error {
	token := strings.TrimSpace(creds["api_token"])
	if token == "" {
		return c.Errf(c.ErrAuth, "Zeropark needs an API access token")
	}
	u := z.base(cl) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return newCall(cl, "Zeropark").set("api-token", token).get(ctx, u, out)
}

type zeroparkAccount struct {
	ClientID string   `json:"clientId"`
	Balance  *flexNum `json:"accountBalance"`
	Timezone string   `json:"timezone"`
}

func (z ZeroparkAds) account(ctx context.Context, cl *c.Client, creds c.Creds) (zeroparkAccount, error) {
	var a zeroparkAccount
	if err := z.get(ctx, cl, creds, "/api/account/details", nil, &a); err != nil {
		return a, err
	}
	if a.ClientID == "" {
		return a, c.Errf(c.ErrInvalid, "Zeropark returned no account for this token")
	}
	return a, nil
}

// Test reads the account the token belongs to. Its owner's name and email
// address aren't kept.
func (z ZeroparkAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	a, err := z.account(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	acct := c.Account{ID: a.ClientID, Name: "Zeropark " + a.ClientID, Details: map[string]string{}}
	if a.Balance != nil {
		acct.Details["balance_usd"] = strconv.FormatFloat(float64(*a.Balance), 'f', 2, 64)
	}
	if tz := zeroparkZone(a.Timezone); tz != "" {
		acct.Details["time_zone"] = tz
	}
	return acct, nil
}

// zeroparkZone is the account's time zone as an IANA name, or "" when
// Zeropark gives one Go doesn't know.
func zeroparkZone(tz string) string {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return ""
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return ""
	}
	return tz
}

// AdAccounts is the token's one account. Zeropark bills in US dollars.
func (z ZeroparkAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	a, err := z.account(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	return []c.AdAccount{{ID: a.ClientID, Name: "Zeropark " + a.ClientID, Currency: "USD", TimeZone: zeroparkZone(a.Timezone)}}, nil
}

// zeroparkCampaign is one element of /api/stats/campaign/all.
type zeroparkCampaign struct {
	Details struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		DailyBudget *struct {
			Type   string   `json:"type"`
			Amount *flexNum `json:"amount"`
		} `json:"dailyBudget"`
		State *struct {
			State string `json:"state"`
		} `json:"state"`
	} `json:"details"`
	Stats struct {
		Spent       flexNum `json:"spent"`
		Payout      flexNum `json:"payout"`
		Redirects   flexInt `json:"redirects"`
		Conversions flexInt `json:"conversions"`
	} `json:"stats"`
}

// campaigns reads every page of /api/stats/campaign/all for an interval;
// CUSTOM takes a start and end day (YYYY-MM-DD here, dd/mm/yyyy to
// Zeropark).
func (z ZeroparkAds) campaigns(ctx context.Context, cl *c.Client, creds c.Creds, interval, from, to string) ([]zeroparkCampaign, error) {
	var out []zeroparkCampaign
	for page := 0; page < maxPages; page++ {
		q := url.Values{"interval": {interval}, "page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(zeroparkPage)},
			"sortColumn": {"SPENT"}, "sortOrder": {"DESC"}}
		if interval == "CUSTOM" {
			q.Set("startDate", zeroparkDate(from))
			q.Set("endDate", zeroparkDate(to))
		}
		var resp struct {
			Elements []zeroparkCampaign `json:"elements"`
			Total    int                `json:"total"`
		}
		if err := z.get(ctx, cl, creds, "/api/stats/campaign/all", q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Elements...)
		if len(resp.Elements) < zeroparkPage || (page+1)*zeroparkPage >= resp.Total {
			break
		}
	}
	return out, nil
}

// zeroparkDate turns 2026-11-14 into Zeropark's 14/11/2026.
func zeroparkDate(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.Format("02/01/2006")
}

// zeroparkStatus maps Zeropark's campaign state. A campaign that is on but
// held (out of budget, outside its day parting) is active: it runs again
// on its own.
func zeroparkStatus(state string) string {
	switch state {
	case "ACTIVE", "OUT_OF_BUDGET", "OUT_OF_DAILY_BUDGET", "OUT_OF_CREDIT", "TOP_UP_NEEDED", "DAY_PARTING", "CONVERSION_CAPPING":
		return c.AdActive
	case "PAUSED", "DEAL_PAUSED":
		return c.AdPaused
	case "DELETED", "ARCHIVED", "REJECTED", "DEAL_EXPIRED", "NONEXISTENT":
		return c.AdRemoved
	}
	return c.AdOther
}

// AdCampaigns lists the account's campaigns.
func (z ZeroparkAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	list, err := z.campaigns(ctx, cl, creds, "TODAY", "", "")
	if err != nil {
		return nil, err
	}
	out := make([]c.AdCampaign, 0, len(list))
	for _, e := range list {
		if e.Details.ID != "" {
			out = append(out, zeroparkCampaignOf(e))
		}
	}
	return out, nil
}

// AdDays asks for each day on its own (Zeropark's statistics add a whole
// interval up). Conversions and payout are what Zeropark counted from its
// own postbacks.
func (z ZeroparkAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	var out []c.AdDay
	for _, day := range days(from, to) {
		list, err := z.campaigns(ctx, cl, creds, "CUSTOM", day, day)
		if err != nil {
			return nil, err
		}
		for _, e := range list {
			s := e.Stats
			if e.Details.ID == "" || s.Spent == 0 && s.Redirects == 0 && s.Conversions == 0 {
				continue
			}
			d := c.AdDay{CampaignID: e.Details.ID, CampaignName: e.Details.Name, Date: day,
				SpendMicros: c.Micros(float64(s.Spent)), Clicks: int64(s.Redirects), Conversions: ptrF(float64(s.Conversions))}
			if s.Payout > 0 {
				d.ConversionValueMicros = ptrI(c.Micros(float64(s.Payout)))
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// AdTargetDays reads one campaign's targets for one day, every page, and
// leaves out those that neither spent nor sent a visit.
func (z ZeroparkAds) AdTargetDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, day string) ([]c.AdTargetDay, error) {
	if campaignID == "" || strings.ContainsAny(campaignID, "/?#") {
		return nil, c.Errf(c.ErrInvalid, "not a Zeropark campaign id: %q", campaignID)
	}
	var out []c.AdTargetDay
	for page := 0; page < maxPages; page++ {
		q := url.Values{"interval": {"CUSTOM"}, "startDate": {zeroparkDate(day)}, "endDate": {zeroparkDate(day)},
			"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(zeroparkPage)}, "sortColumn": {"SPENT"}, "sortOrder": {"DESC"}}
		var resp struct {
			Elements []struct {
				ID      string `json:"id"`
				Target  string `json:"target"`
				Address string `json:"address"`
				Source  string `json:"source"`
				Stats   struct {
					Spent     flexNum `json:"spent"`
					Redirects flexInt `json:"redirects"`
				} `json:"stats"`
			} `json:"elements"`
			Total int `json:"total"`
		}
		if err := z.get(ctx, cl, creds, "/api/stats/campaign/"+esc(campaignID)+"/targets", q, &resp); err != nil {
			return nil, err
		}
		for _, e := range resp.Elements {
			if e.Stats.Spent == 0 && e.Stats.Redirects == 0 {
				continue
			}
			var keys []string
			for _, k := range []string{e.Target, e.Address, e.ID} {
				if k = strings.TrimSpace(k); k != "" && !slices.Contains(keys, k) {
					keys = append(keys, k)
				}
			}
			out = append(out, c.AdTargetDay{CampaignID: campaignID, Date: day, Keys: keys, Source: e.Source,
				SpendMicros: c.Micros(float64(e.Stats.Spent)), Clicks: int64(e.Stats.Redirects)})
		}
		if len(resp.Elements) < zeroparkPage || (page+1)*zeroparkPage >= resp.Total {
			break
		}
	}
	return out, nil
}

// zeroparkTargetWindow is how far back AdTargetStates looks for targets with
// traffic.
const zeroparkTargetWindow = "LAST_30_DAYS"

// zeroparkTargetPages bounds AdTargetStates: the 50,000 targets that spent
// most.
const zeroparkTargetPages = 5

// zeroparkState reads a state Zeropark sends as a word or as an object
// holding one.
type zeroparkState string

func (s *zeroparkState) UnmarshalJSON(b []byte) error {
	var word string
	if json.Unmarshal(b, &word) == nil {
		*s = zeroparkState(word)
		return nil
	}
	var obj struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(b, &obj) // anything else reads as unknown
	*s = zeroparkState(obj.State)
	return nil
}

// zeroparkTargetState maps Zeropark's target states: paused is the
// advertiser's own pause, stopped everything Zeropark holds back.
func zeroparkTargetState(state string) (string, string) {
	detail := strings.ToLower(strings.TrimSpace(state))
	switch detail {
	case "", "active":
		return c.AdTargetActive, ""
	case "paused":
		return c.AdTargetPaused, ""
	}
	return c.AdTargetStopped, detail
}

// AdTargetStates lists the campaign's targets that had traffic in the last 30
// days, with the state and bid Zeropark holds for each.
func (z ZeroparkAds) AdTargetStates(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) ([]c.AdTargetState, error) {
	if err := zeroparkCampaignID(campaignID); err != nil {
		return nil, err
	}
	var out []c.AdTargetState
	for page := 0; page < zeroparkTargetPages; page++ {
		q := url.Values{"interval": {zeroparkTargetWindow}, "page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(zeroparkPage)},
			"sortColumn": {"SPENT"}, "sortOrder": {"DESC"}}
		var resp struct {
			Elements []struct {
				ID      string        `json:"id"`
				Target  string        `json:"target"`
				Address string        `json:"address"`
				Source  string        `json:"source"`
				State   zeroparkState `json:"state"`
				Bid     zeroparkBid   `json:"bid"`
				MinBid  flexNum       `json:"minBid"`
			} `json:"elements"`
			Total int `json:"total"`
		}
		if err := z.get(ctx, cl, creds, "/api/stats/campaign/"+esc(campaignID)+"/targets", q, &resp); err != nil {
			return nil, err
		}
		for _, e := range resp.Elements {
			var keys []string
			for _, k := range []string{e.Target, e.Address, e.ID} {
				if k = strings.TrimSpace(k); k != "" && !slices.Contains(keys, k) {
					keys = append(keys, k)
				}
			}
			if len(keys) == 0 {
				continue
			}
			t := c.AdTargetState{CampaignID: campaignID, ID: cmp.Or(strings.TrimSpace(e.ID), keys[0]), Keys: keys, Source: e.Source,
				Bid: e.Bid.bid(), MinBidMicros: c.Micros(float64(e.MinBid))}
			t.State, t.StateDetail = zeroparkTargetState(string(e.State))
			out = append(out, t)
		}
		if len(resp.Elements) < zeroparkPage || (page+1)*zeroparkPage >= resp.Total {
			break
		}
	}
	return out, nil
}

// ---- changes ----
//
// Zeropark's campaign API takes its arguments in the query string and
// answers the campaign's (or the targets') new state. Every change here is
// asked for by a person, or by an agent within its grants, through the
// action layer; nothing is changed on a sync.

var (
	_ c.AdsManager         = ZeroparkAds{}
	_ c.AdExclusionManager = ZeroparkAds{}
	_ c.AdBidManager       = ZeroparkAds{}
)

func (z ZeroparkAds) post(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values) error {
	token := strings.TrimSpace(creds["api_token"])
	if token == "" {
		return c.Errf(c.ErrAuth, "Zeropark needs an API access token")
	}
	u := z.base(cl) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return newCall(cl, "Zeropark").set("api-token", token).post(ctx, u, nil, nil)
}

// zeroparkCampaignID checks an id before it goes into a path.
func zeroparkCampaignID(id string) error {
	if id == "" || strings.ContainsAny(id, "/?#") {
		return c.Errf(c.ErrInvalid, "not a Zeropark campaign id: %q", id)
	}
	return nil
}

func zeroparkCampaignOf(e zeroparkCampaign) c.AdCampaign {
	d := e.Details
	state := ""
	if d.State != nil {
		state = d.State.State
	}
	camp := c.AdCampaign{ID: d.ID, Name: d.Name, Status: zeroparkStatus(state), ProviderStatus: state}
	if b := d.DailyBudget; b != nil && strings.EqualFold(b.Type, "LIMITED") && b.Amount != nil && *b.Amount > 0 {
		camp.DailyBudgetMicros = ptrI(c.Micros(float64(*b.Amount)))
	}
	return camp
}

// AdCampaign reads one campaign, by its id, from the campaign statistics
// (Zeropark has no call for a single campaign).
func (z ZeroparkAds) AdCampaign(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) (c.AdCampaign, error) {
	if err := zeroparkCampaignID(campaignID); err != nil {
		return c.AdCampaign{}, err
	}
	q := url.Values{"interval": {"TODAY"}, "page": {"0"}, "limit": {"100"}, "campaignNameOrId": {campaignID}}
	var resp struct {
		Elements []zeroparkCampaign `json:"elements"`
	}
	if err := z.get(ctx, cl, creds, "/api/stats/campaign/all", q, &resp); err != nil {
		return c.AdCampaign{}, err
	}
	for _, e := range resp.Elements {
		if strings.EqualFold(e.Details.ID, campaignID) {
			return zeroparkCampaignOf(e), nil
		}
	}
	return c.AdCampaign{}, c.ErrNotFound
}

// SetCampaignStatus pauses or resumes a campaign.
func (z ZeroparkAds) SetCampaignStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, active bool) error {
	if err := zeroparkCampaignID(campaignID); err != nil {
		return err
	}
	verb := "pause"
	if active {
		verb = "resume"
	}
	return z.post(ctx, cl, creds, "/api/campaign/"+esc(campaignID)+"/"+verb, nil)
}

// SetCampaignDailyBudget sets a campaign's daily budget, in US dollars,
// rounded to the cent.
func (z ZeroparkAds) SetCampaignDailyBudget(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, micros int64) error {
	if err := zeroparkCampaignID(campaignID); err != nil {
		return err
	}
	if micros <= 0 {
		return c.Errf(c.ErrInvalid, "the daily budget must be more than nothing")
	}
	cents := (micros + 5_000) / 10_000
	budget := strconv.FormatFloat(float64(cents)/100, 'f', 2, 64)
	return z.post(ctx, cl, creds, "/api/campaign/"+esc(campaignID)+"/set/budget/daily", url.Values{"budget": {budget}})
}

// ExclusionKinds: a placement is one of a campaign's targets, by its
// address or hash (what a click's {target} carries, and what the spend
// per target names).
func (ZeroparkAds) ExclusionKinds() []string { return []string{c.ExcludePlacement} }

func (z ZeroparkAds) setTarget(ctx context.Context, cl *c.Client, creds c.Creds, campaignID string, x c.AdExclusion, verb string) error {
	if err := zeroparkCampaignID(campaignID); err != nil {
		return err
	}
	target := strings.TrimSpace(x.Value)
	if x.Kind != c.ExcludePlacement || target == "" {
		return c.Errf(c.ErrInvalid, "Zeropark excludes placements: a target's address or hash")
	}
	return z.post(ctx, cl, creds, "/api/campaign/"+esc(campaignID)+"/targets/"+verb, url.Values{"hashOrAddress": {target}})
}

// AddExclusion pauses one target of a campaign; the campaign keeps
// running on its other targets.
func (z ZeroparkAds) AddExclusion(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, x c.AdExclusion) error {
	return z.setTarget(ctx, cl, creds, campaignID, x, "pause")
}

// RemoveExclusion resumes a paused target.
func (z ZeroparkAds) RemoveExclusion(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, x c.AdExclusion) error {
	return z.setTarget(ctx, cl, creds, campaignID, x, "resume")
}

// BidRange: Zeropark takes bids from 0.0001 to 5 dollars a visit.
func (ZeroparkAds) BidRange() (int64, int64) { return 100, 5_000_000 }

type zeroparkBid struct {
	Value   *flexNum `json:"value"`
	AutoBid bool     `json:"autoBid"`
}

func (b zeroparkBid) bid() c.AdBid {
	out := c.AdBid{Auto: b.AutoBid}
	if b.Value != nil {
		out.Micros = c.Micros(float64(*b.Value))
	}
	return out
}

// Bid reads the campaign's bid, or one target's from the campaign's
// targets (asked for by its address or hash).
func (z ZeroparkAds) Bid(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, placement string) (c.AdBid, error) {
	if err := zeroparkCampaignID(campaignID); err != nil {
		return c.AdBid{}, err
	}
	if placement = strings.TrimSpace(placement); placement == "" {
		var b zeroparkBid
		if err := z.get(ctx, cl, creds, "/api/stats/campaign/"+esc(campaignID)+"/bid", nil, &b); err != nil {
			return c.AdBid{}, err
		}
		return b.bid(), nil
	}
	q := url.Values{"interval": {"TODAY"}, "page": {"0"}, "limit": {"100"}, "targetAddresses": {placement}}
	var resp struct {
		Elements []struct {
			ID      string      `json:"id"`
			Target  string      `json:"target"`
			Address string      `json:"address"`
			Bid     zeroparkBid `json:"bid"`
		} `json:"elements"`
	}
	if err := z.get(ctx, cl, creds, "/api/stats/campaign/"+esc(campaignID)+"/targets", q, &resp); err != nil {
		return c.AdBid{}, err
	}
	for _, e := range resp.Elements {
		if strings.EqualFold(e.Target, placement) || strings.EqualFold(e.Address, placement) || strings.EqualFold(e.ID, placement) {
			return e.Bid.bid(), nil
		}
	}
	return c.AdBid{}, c.ErrNotFound
}

// SetBid sets the campaign's bid, or one target's. Zeropark rounds it to
// its own scale (four decimals under a cent, three under ten cents, two
// above).
func (z ZeroparkAds) SetBid(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, placement string, micros int64) error {
	if err := zeroparkCampaignID(campaignID); err != nil {
		return err
	}
	lo, hi := z.BidRange()
	if micros < lo || micros > hi {
		return c.Errf(c.ErrInvalid, "Zeropark takes bids from 0.0001 to 5 dollars")
	}
	q := url.Values{"bid": {strconv.FormatFloat(float64(micros)/1e6, 'f', 4, 64)}}
	path := "/api/campaign/" + esc(campaignID) + "/bid"
	if placement = strings.TrimSpace(placement); placement != "" {
		path = "/api/campaign/" + esc(campaignID) + "/target/bid"
		q.Set("hash", placement)
	}
	return z.post(ctx, cl, creds, path, q)
}
