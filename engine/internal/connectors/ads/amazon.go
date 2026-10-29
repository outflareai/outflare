package ads

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// AmazonAds reads Amazon Ads profiles (one advertiser in one marketplace,
// with its currency and time zone), their Sponsored Products campaigns and
// each campaign's daily numbers from the asynchronous v3 reporting API.
//
// The sign-in is Login with Amazon on the workspace's own Amazon Ads
// account: the refresh token is the workspace's. Every call also names the
// sign-in app in Amazon-Advertising-API-ClientId (kept beside the tokens,
// see connectors.OAuth.KeepClientID) and, past the profile list, the
// profile in Amazon-Advertising-API-Scope. Profiles live in one of three
// regions (North America, Europe, Far East), each with its own API host;
// the profile list is read from all three.
//
// Nothing is sent back: Amazon's conversion events (Ads API v1 Events,
// in beta) match on hashed identifiers per advertiser account rather than
// a click id, which doesn't fit connectors.ConversionSender yet.
type AmazonAds struct{}

// amazonRegions are Amazon Ads' API hosts by region.
var amazonRegions = []struct{ ID, Base string }{
	{"NA", "https://advertising-api.amazon.com"},
	{"EU", "https://advertising-api-eu.amazon.com"},
	{"FE", "https://advertising-api-fe.amazon.com"},
}

const (
	amazonName = "Amazon Ads"
	// amazonReportDays is the longest range one report covers.
	amazonReportDays = 31
	// amazonCampaignPage is how many campaigns one list call asks for.
	amazonCampaignPage = 100
)

// amazonPoll is how often and how long reports are waited for inside the
// sync job. Amazon builds reports in minutes; a report that isn't ready by
// then fails the sync, which runs again later and picks the same report up
// (Amazon answers a duplicate request with the report it already has).
// Tests shorten it.
var amazonPoll = struct{ every, limit time.Duration }{10 * time.Second, 5 * time.Minute}

func (AmazonAds) Definition() c.Definition {
	d := def("amazon_ads", "Amazon Ads", "https://advertising.amazon.com",
		"https://advertising.amazon.com/API/docs/en-us/", "",
		"Spend, impressions, clicks and sales per Sponsored Products campaign from your Amazon Ads profiles.",
		"Signs in with Amazon (Login with Amazon) and reads every advertising profile the sign-in can reach, in North America, Europe and the Far East, read-only: Sponsored Products campaigns and their daily numbers from Amazon's reports.")
	d.OAuth = &c.OAuth{
		AuthURL:      "https://www.amazon.com/ap/oa",
		TokenURL:     "https://api.amazon.com/auth/o2/token",
		Scopes:       []string{"advertising::campaign_management"},
		KeepClientID: true,
	}
	return d
}

func (AmazonAds) base(cl *c.Client, region string) string {
	for _, r := range amazonRegions {
		if r.ID == region {
			return cl.Base("amazon_ads."+strings.ToLower(r.ID), r.Base)
		}
	}
	return cl.Base("amazon_ads.na", amazonRegions[0].Base)
}

// amazonDuplicate finds the report id in Amazon's answer to a duplicate
// report request.
var amazonDuplicate = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// do sends one call with the workspace's token, the app's client id and,
// when profile is set, the profile's scope. Amazon's versioned media types
// go in contentType; JSON otherwise. It returns the status and body of
// any answer so callers can read Amazon's own refusals.
func (AmazonAds) do(ctx context.Context, cl *c.Client, creds c.Creds, profile, method, u, contentType string, body any) (int, []byte, error) {
	if creds["access_token"] == "" || creds["client_id"] == "" {
		return 0, nil, c.Errf(c.ErrAuth, "the Amazon Ads connection is missing its sign-in; connect it again")
	}
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, nil, c.Errf(c.ErrInvalid, "bad Amazon Ads address: %v", err)
	}
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Authorization", "Bearer "+creds["access_token"])
	req.Header.Set("Amazon-Advertising-API-ClientId", creds["client_id"])
	if profile != "" {
		req.Header.Set("Amazon-Advertising-API-Scope", profile)
	}
	req.Header.Set("Accept", contentType)
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, nil, c.Errf(c.ErrUnavailable, "could not reach Amazon Ads: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, nil, c.Errf(c.ErrUnavailable, "reading the Amazon Ads response: %v", err)
	}
	return resp.StatusCode, raw, nil
}

// json sends a call and reads a JSON answer, turning a refusal into an error.
func (a AmazonAds) json(ctx context.Context, cl *c.Client, creds c.Creds, profile, method, u, contentType string, body, out any) error {
	status, raw, err := a.do(ctx, cl, creds, profile, method, u, contentType, body)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return c.HTTPError(amazonName, status, string(raw))
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return c.Errf(c.ErrUnavailable, "Amazon Ads returned something that is not JSON")
	}
	return nil
}

type amazonProfile struct {
	ProfileID    json.Number `json:"profileId"`
	CountryCode  string      `json:"countryCode"`
	CurrencyCode string      `json:"currencyCode"`
	Timezone     string      `json:"timezone"`
	AccountInfo  struct {
		MarketplaceStringID string `json:"marketplaceStringId"`
		ID                  string `json:"id"`
		Type                string `json:"type"`
		Name                string `json:"name"`
	} `json:"accountInfo"`
}

// profiles lists the sign-in's profiles in every region. A region the
// sign-in has nothing in, or isn't allowed in, is skipped; only when every
// region refuses is that an error.
func (a AmazonAds) profiles(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	var out []c.AdAccount
	var firstErr error
	answered := 0
	seen := map[string]bool{}
	for _, r := range amazonRegions {
		var list []amazonProfile
		if err := a.json(ctx, cl, creds, "", http.MethodGet, a.base(cl, r.ID)+"/v2/profiles", "", nil, &list); err != nil {
			if k := c.KindOf(err); k == c.ErrUnavailable || k == c.ErrQuota {
				// A region that is down must not look like profiles that went away.
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		answered++
		for _, p := range list {
			id := p.ProfileID.String()
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			name := strings.TrimSpace(p.AccountInfo.Name)
			if p.CountryCode != "" {
				name = strings.TrimSpace(name + " (" + p.CountryCode + ")")
			}
			out = append(out, c.AdAccount{ID: id, Name: name, Currency: strings.ToUpper(p.CurrencyCode), TimeZone: p.Timezone,
				Meta: map[string]string{"region": r.ID, "country": p.CountryCode, "marketplace": p.AccountInfo.MarketplaceStringID,
					"account_id": p.AccountInfo.ID, "account_type": p.AccountInfo.Type}})
		}
	}
	if answered == 0 && firstErr != nil {
		return nil, firstErr
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (a AmazonAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	accts, err := a.profiles(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	if len(accts) == 0 {
		return c.Account{}, c.Errf(c.ErrInvalid, "this Amazon sign-in has no Amazon Ads profiles; sign in with the account that runs your ads")
	}
	first := accts[0]
	id := first.Meta["account_id"]
	if id == "" {
		id = first.ID
	}
	name := strings.TrimSpace(strings.TrimSuffix(first.Name, " ("+first.Meta["country"]+")"))
	return c.Account{ID: id, Name: name, Details: map[string]string{"accounts": fmt.Sprint(len(accts))}}, nil
}

func (a AmazonAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	return a.profiles(ctx, cl, creds)
}

// AdCampaigns lists the profile's Sponsored Products campaigns, archived
// ones included, through the v3 list call (a POST with a nextToken).
func (a AmazonAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	const media = "application/vnd.spCampaign.v3+json"
	u := a.base(cl, acct.Meta["region"]) + "/sp/campaigns/list"
	var out []c.AdCampaign
	next := ""
	for i := 0; i < maxPages; i++ {
		body := map[string]any{"maxResults": amazonCampaignPage,
			"stateFilter": map[string]any{"include": []string{"ENABLED", "PAUSED", "ARCHIVED"}}}
		if next != "" {
			body["nextToken"] = next
		}
		var page struct {
			Campaigns []struct {
				CampaignID    json.Number `json:"campaignId"`
				Name          string      `json:"name"`
				State         string      `json:"state"`
				TargetingType string      `json:"targetingType"`
				Budget        struct {
					Budget     float64 `json:"budget"`
					BudgetType string  `json:"budgetType"`
				} `json:"budget"`
			} `json:"campaigns"`
			NextToken string `json:"nextToken"`
		}
		if err := a.json(ctx, cl, creds, acct.ID, http.MethodPost, u, media, body, &page); err != nil {
			return nil, err
		}
		for _, cp := range page.Campaigns {
			camp := c.AdCampaign{ID: cp.CampaignID.String(), Name: cp.Name, Status: status(cp.State), ProviderStatus: cp.State,
				Objective: "sponsored_products"}
			if strings.EqualFold(cp.Budget.BudgetType, "DAILY") && cp.Budget.Budget > 0 {
				camp.DailyBudgetMicros = ptrI(c.Micros(cp.Budget.Budget))
			}
			out = append(out, camp)
		}
		if page.NextToken == "" || page.NextToken == next {
			break
		}
		next = page.NextToken
	}
	return out, nil
}

// amazonReport is a v3 report's status.
type amazonReport struct {
	ReportID      string  `json:"reportId"`
	Status        string  `json:"status"`
	URL           *string `json:"url"`
	FailureReason *string `json:"failureReason"`
}

// amazonRow is one row of the spCampaigns report, daily by campaign.
type amazonRow struct {
	Date         string      `json:"date"`
	CampaignID   json.Number `json:"campaignId"`
	CampaignName string      `json:"campaignName"`
	Impressions  flexInt     `json:"impressions"`
	Clicks       flexInt     `json:"clicks"`
	Cost         flexNum     `json:"cost"`
	Purchases7d  *flexNum    `json:"purchases7d"`
	Sales7d      *flexNum    `json:"sales7d"`
}

// amazonColumns are the spCampaigns report's columns Outflare reads.
// Purchases and sales are Amazon's default seven-day attribution for
// Sponsored Products.
var amazonColumns = []string{"date", "campaignId", "campaignName", "impressions", "clicks", "cost", "purchases7d", "sales7d"}

// AdDays asks for one Sponsored Products campaign report per 31 days,
// waits for them all (bounded, inside the sync job), and reads each
// gzipped JSON file. Days are in the profile's time zone; cost and sales
// are in its currency.
func (a AmazonAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	all := days(from, to)
	var ids []string
	for start := 0; start < len(all); start += amazonReportDays {
		window := all[start:min(start+amazonReportDays, len(all))]
		id, err := a.createReport(ctx, cl, creds, acct, window[0], window[len(window)-1])
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	var out []c.AdDay
	for _, id := range ids {
		u, err := a.waitReport(ctx, cl, creds, acct, id)
		if err != nil {
			return nil, err
		}
		rows, err := a.download(ctx, cl, u)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// createReport asks for a report and returns its id. Asking again for a
// report Amazon is still building answers 425 with the earlier id, which
// is then waited for instead.
func (a AmazonAds) createReport(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) (string, error) {
	body := map[string]any{
		"name":      "Outflare " + from + " to " + to,
		"startDate": from,
		"endDate":   to,
		"configuration": map[string]any{
			"adProduct":    "SPONSORED_PRODUCTS",
			"groupBy":      []string{"campaign"},
			"columns":      amazonColumns,
			"reportTypeId": "spCampaigns",
			"timeUnit":     "DAILY",
			"format":       "GZIP_JSON",
		},
	}
	status, raw, err := a.do(ctx, cl, creds, acct.ID, http.MethodPost, a.base(cl, acct.Meta["region"])+"/reporting/reports",
		"application/vnd.createasyncreportrequest.v3+json", body)
	if err != nil {
		return "", err
	}
	if status == 425 {
		if id := amazonDuplicate.FindString(string(raw)); id != "" {
			return id, nil
		}
	}
	if status < 200 || status > 299 {
		return "", c.HTTPError(amazonName, status, string(raw))
	}
	var rep amazonReport
	if json.Unmarshal(raw, &rep) != nil || rep.ReportID == "" {
		return "", c.Errf(c.ErrUnavailable, "Amazon Ads did not return a report id")
	}
	return rep.ReportID, nil
}

// waitReport polls a report until it is ready and returns its download
// address.
func (a AmazonAds) waitReport(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, id string) (string, error) {
	u := a.base(cl, acct.Meta["region"]) + "/reporting/reports/" + esc(id)
	deadline := time.Now().Add(amazonPoll.limit)
	for {
		var rep amazonReport
		if err := a.json(ctx, cl, creds, acct.ID, http.MethodGet, u, "application/vnd.createasyncreportrequest.v3+json", nil, &rep); err != nil {
			return "", err
		}
		switch strings.ToUpper(rep.Status) {
		case "COMPLETED":
			if rep.URL == nil || *rep.URL == "" {
				return "", c.Errf(c.ErrUnavailable, "Amazon Ads finished the report without a download address; it is tried again on the next sync")
			}
			return *rep.URL, nil
		case "FAILED":
			reason := ""
			if rep.FailureReason != nil {
				reason = ": " + *rep.FailureReason
			}
			return "", c.Errf(c.ErrUnavailable, "Amazon Ads could not build the report%s; it is tried again on the next sync", reason)
		}
		if time.Now().After(deadline) {
			return "", c.Errf(c.ErrUnavailable, "Amazon Ads is still building the report; it is picked up on the next sync")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(amazonPoll.every):
		}
	}
}

// download reads a finished report: a gzipped JSON array of rows, from a
// signed storage address that takes no Amazon Ads headers.
func (AmazonAds) download(ctx context.Context, cl *c.Client, u string) ([]c.AdDay, error) {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://127.0.0.1") {
		return nil, c.Errf(c.ErrUnavailable, "Amazon Ads sent a report address that is not https")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := cl.Do(req)
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "could not download the Amazon Ads report: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.Errf(c.ErrUnavailable, "could not download the Amazon Ads report (HTTP %d); it is tried again on the next sync", resp.StatusCode)
	}
	zr, err := gzip.NewReader(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "the Amazon Ads report is not gzipped JSON")
	}
	defer zr.Close()
	var rows []amazonRow
	if err := json.NewDecoder(io.LimitReader(zr, 256<<20)).Decode(&rows); err != nil {
		return nil, c.Errf(c.ErrUnavailable, "the Amazon Ads report could not be read: %v", err)
	}
	var out []c.AdDay
	for _, r := range rows {
		id := r.CampaignID.String()
		if id == "" || len(r.Date) < 10 || r.Cost == 0 && r.Impressions == 0 && r.Clicks == 0 {
			continue
		}
		day := c.AdDay{CampaignID: id, CampaignName: r.CampaignName, Date: r.Date[:10],
			SpendMicros: c.Micros(float64(r.Cost)), Impressions: int64(r.Impressions), Clicks: int64(r.Clicks)}
		if r.Purchases7d != nil {
			day.Conversions = ptrF(float64(*r.Purchases7d))
		}
		if r.Sales7d != nil {
			day.ConversionValueMicros = ptrI(c.Micros(float64(*r.Sales7d)))
		}
		out = append(out, day)
	}
	return out, nil
}
