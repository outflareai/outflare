package ads

import (
	"context"
	"fmt"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// GoogleAds reads Google Ads accounts through the Google Ads API. Every
// call needs a developer token as well as the sign-in; the workspace adds
// its own, from its own Google Ads manager account.
type GoogleAds struct{}

const (
	googleAdsBase    = "https://googleads.googleapis.com"
	googleAdsVersion = "v23"
	scopeAdWords     = "https://www.googleapis.com/auth/adwords"
)

func (GoogleAds) Definition() c.Definition {
	d := def("google_ads", "Google Ads", "https://ads.google.com",
		"https://developers.google.com/google-ads/api/docs/start", "https://developers.google.com/google-ads/api/docs/get-started/dev-token",
		"Spend, clicks and conversions per campaign from your Google Ads accounts.",
		"Signs in with Google and reads every Google Ads account the sign-in can see, including accounts under a manager account. Google also asks for a developer token: create one in your own Google Ads manager account (Admin, API Center) and add it after signing in. It never changes your campaigns; it sends conversions only to the conversion actions you choose under Paid ads, and adds people only to the Customer Match lists your journeys name.")
	d.Capabilities = append(d.Capabilities, c.CapAdsConversions, c.CapAdsAudiences)
	d.OAuth = &c.OAuth{
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		Scopes:   []string{scopeAdWords, "openid", "email"},
		Params:   map[string]string{"access_type": "offline", "prompt": "consent"},
	}
	return d
}

func (GoogleAds) SetupFields() []c.Field {
	return []c.Field{{Name: "developer_token", Label: "Developer token", Secret: true,
		Help: "From your own Google Ads manager account: Admin, API Center. Basic access is enough."}}
}

func (g GoogleAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	acct := c.Account{Details: map[string]string{}}
	if creds["developer_token"] == "" {
		acct.Details["setup"] = "add your developer token"
		return acct, nil
	}
	ids, err := g.accessible(ctx, cl, creds)
	if err != nil {
		return acct, err
	}
	acct.Details["accounts"] = fmt.Sprint(len(ids))
	return acct, nil
}

func (GoogleAds) base(cl *c.Client) string {
	return cl.Base("google_ads", googleAdsBase) + "/" + googleAdsVersion
}

func (GoogleAds) call(cl *c.Client, creds c.Creds, loginID string) *call {
	return newCall(cl, "Google Ads").bearer(creds["access_token"]).
		set("developer-token", creds["developer_token"]).set("login-customer-id", loginID)
}

func (g GoogleAds) accessible(ctx context.Context, cl *c.Client, creds c.Creds) ([]string, error) {
	var resp struct {
		ResourceNames []string `json:"resourceNames"`
	}
	if err := g.call(cl, creds, "").get(ctx, g.base(cl)+"/customers:listAccessibleCustomers", &resp); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.ResourceNames))
	for _, n := range resp.ResourceNames {
		out = append(out, strings.TrimPrefix(n, "customers/"))
	}
	return out, nil
}

type gaqlRow struct {
	Customer struct {
		ID              flexInt `json:"id"`
		DescriptiveName string  `json:"descriptiveName"`
		CurrencyCode    string  `json:"currencyCode"`
		TimeZone        string  `json:"timeZone"`
		Manager         bool    `json:"manager"`
	} `json:"customer"`
	CustomerClient struct {
		ID              flexInt `json:"id"`
		DescriptiveName string  `json:"descriptiveName"`
		CurrencyCode    string  `json:"currencyCode"`
		TimeZone        string  `json:"timeZone"`
		Manager         bool    `json:"manager"`
		Status          string  `json:"status"`
	} `json:"customerClient"`
	Campaign struct {
		ID                     flexInt `json:"id"`
		Name                   string  `json:"name"`
		Status                 string  `json:"status"`
		AdvertisingChannelType string  `json:"advertisingChannelType"`
	} `json:"campaign"`
	ConversionAction struct {
		ID   flexInt `json:"id"`
		Name string  `json:"name"`
	} `json:"conversionAction"`
	UserList struct {
		ID   flexInt `json:"id"`
		Name string  `json:"name"`
	} `json:"userList"`
	CampaignBudget struct {
		AmountMicros flexInt `json:"amountMicros"`
	} `json:"campaignBudget"`
	Segments struct {
		Date string `json:"date"`
	} `json:"segments"`
	Metrics struct {
		CostMicros       flexInt `json:"costMicros"`
		Impressions      flexInt `json:"impressions"`
		Clicks           flexInt `json:"clicks"`
		Conversions      flexNum `json:"conversions"`
		ConversionsValue flexNum `json:"conversionsValue"`
	} `json:"metrics"`
}

// search runs a GAQL query against one customer, every page.
func (g GoogleAds) search(ctx context.Context, cl *c.Client, creds c.Creds, customer, loginID, query string) ([]gaqlRow, error) {
	var out []gaqlRow
	token := ""
	for i := 0; i < maxPages; i++ {
		body := map[string]any{"query": query}
		if token != "" {
			body["pageToken"] = token
		}
		var resp struct {
			Results       []gaqlRow `json:"results"`
			NextPageToken string    `json:"nextPageToken"`
		}
		if err := g.call(cl, creds, loginID).post(ctx, g.base(cl)+"/customers/"+esc(customer)+"/googleAds:search", body, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Results...)
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	return out, nil
}

func (g GoogleAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	if creds["developer_token"] == "" {
		return nil, c.Errf(c.ErrAuth, "Google Ads needs your developer token: add it on the connection")
	}
	ids, err := g.accessible(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []c.AdAccount
	for _, id := range ids {
		rows, err := g.search(ctx, cl, creds, id, "", "SELECT customer.id, customer.descriptive_name, customer.currency_code, customer.time_zone, customer.manager FROM customer")
		if err != nil {
			// A suspended or cancelled account refuses queries; the others
			// still sync.
			if c.KindOf(err) == c.ErrInvalid || c.KindOf(err) == c.ErrAuth {
				continue
			}
			return nil, err
		}
		if len(rows) == 0 {
			continue
		}
		cu := rows[0].Customer
		if !cu.Manager {
			if !seen[id] {
				seen[id] = true
				out = append(out, c.AdAccount{ID: id, Name: cu.DescriptiveName, Currency: cu.CurrencyCode, TimeZone: cu.TimeZone})
			}
			continue
		}
		// A manager account: read the accounts under it through it.
		clients, err := g.search(ctx, cl, creds, id, id, "SELECT customer_client.id, customer_client.descriptive_name, customer_client.currency_code, customer_client.time_zone, customer_client.manager, customer_client.status FROM customer_client WHERE customer_client.manager = FALSE")
		if err != nil {
			return nil, err
		}
		for _, r := range clients {
			cc := r.CustomerClient
			cid := fmt.Sprint(int64(cc.ID))
			if seen[cid] || cc.Status == "CANCELED" || cc.Status == "CLOSED" {
				continue
			}
			seen[cid] = true
			out = append(out, c.AdAccount{ID: cid, Name: cc.DescriptiveName, Currency: cc.CurrencyCode, TimeZone: cc.TimeZone,
				Meta: map[string]string{"login_customer_id": id}})
		}
	}
	return out, nil
}

func (g GoogleAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT campaign.id, campaign.name, campaign.status, campaign.advertising_channel_type, campaign_budget.amount_micros FROM campaign")
	if err != nil {
		return nil, err
	}
	out := make([]c.AdCampaign, 0, len(rows))
	for _, r := range rows {
		cp := c.AdCampaign{ID: fmt.Sprint(int64(r.Campaign.ID)), Name: r.Campaign.Name, Status: status(r.Campaign.Status),
			ProviderStatus: r.Campaign.Status, Objective: strings.ToLower(r.Campaign.AdvertisingChannelType)}
		if b := int64(r.CampaignBudget.AmountMicros); b > 0 {
			cp.DailyBudgetMicros = ptrI(b)
		}
		out = append(out, cp)
	}
	return out, nil
}

func (g GoogleAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	q := fmt.Sprintf("SELECT campaign.id, campaign.name, segments.date, metrics.cost_micros, metrics.impressions, metrics.clicks, metrics.conversions, metrics.conversions_value FROM campaign WHERE segments.date BETWEEN '%s' AND '%s'",
		mustDate(from).Format("2006-01-02"), mustDate(to).Format("2006-01-02"))
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], q)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdDay, 0, len(rows))
	for _, r := range rows {
		m := r.Metrics
		out = append(out, c.AdDay{CampaignID: fmt.Sprint(int64(r.Campaign.ID)), CampaignName: r.Campaign.Name, Date: r.Segments.Date,
			SpendMicros: int64(m.CostMicros), Impressions: int64(m.Impressions), Clicks: int64(m.Clicks),
			Conversions: ptrF(float64(m.Conversions)), ConversionValueMicros: ptrI(c.Micros(float64(m.ConversionsValue)))})
	}
	return out, nil
}

// ---- conversions ----

// ConversionTargets lists the account's enabled conversion actions that
// take uploaded clicks (Google's "Import, from clicks" actions), which is
// what offline and enhanced conversions for leads are sent to.
func (g GoogleAds) ConversionTargets(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.ConversionTarget, error) {
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT conversion_action.id, conversion_action.name FROM conversion_action WHERE conversion_action.type = 'UPLOAD_CLICKS' AND conversion_action.status = 'ENABLED'")
	if err != nil {
		return nil, err
	}
	out := make([]c.ConversionTarget, 0, len(rows))
	for _, r := range rows {
		out = append(out, c.ConversionTarget{ID: fmt.Sprint(int64(r.ConversionAction.ID)), Name: r.ConversionAction.Name, EventTypes: []string{}})
	}
	return out, nil
}

// SendConversions uploads click conversions with partial failure on, so
// one bad row doesn't sink the batch. A conversion carries its click id
// when there is one, and the hashed email and phone for enhanced
// conversions; the event id is the order id Google dedupes on.
func (g GoogleAds) SendConversions(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, target c.ConversionTarget, convs []c.AdConversion) ([]c.ConversionResult, error) {
	results := make([]c.ConversionResult, len(convs))
	var body []map[string]any
	var sentIdx []int
	for i, cv := range convs {
		results[i] = c.ConversionResult{Index: cv.Index}
		row := map[string]any{
			"conversionAction":   "customers/" + acct.ID + "/conversionActions/" + target.ID,
			"conversionDateTime": cv.Time.UTC().Format("2006-01-02 15:04:05-07:00"),
			"orderId":            cv.EventID,
		}
		var keys []string
		for _, k := range []string{"gclid", "gbraid", "wbraid"} {
			if v := cv.ClickIDs[k]; v != "" {
				row[k] = v
				keys = append(keys, k)
				break
			}
		}
		var ids []map[string]string
		for _, e := range cv.Emails {
			ids = append(ids, map[string]string{"hashedEmail": e})
		}
		if len(cv.Emails) > 0 {
			keys = append(keys, "email")
		}
		for _, p := range cv.Phones {
			ids = append(ids, map[string]string{"hashedPhoneNumber": p})
		}
		if len(cv.Phones) > 0 {
			keys = append(keys, "phone")
		}
		if len(keys) == 0 {
			results[i].Skipped, results[i].Reason = true, "no click id, email or phone to match on"
			continue
		}
		if len(ids) > 0 {
			row["userIdentifiers"] = ids
		}
		if cv.ValueMicros != nil {
			row["conversionValue"] = float64(*cv.ValueMicros) / 1e6
			row["currencyCode"] = cv.Currency
		}
		if cv.ConsentGranted {
			row["consent"] = map[string]string{"adUserData": "GRANTED"}
		}
		results[i].MatchKeys = keys
		body = append(body, row)
		sentIdx = append(sentIdx, i)
	}
	if len(body) == 0 {
		return results, nil
	}
	var resp struct {
		PartialFailureError *struct {
			Message string `json:"message"`
			Details []struct {
				Errors []struct {
					Message  string `json:"message"`
					Location struct {
						FieldPathElements []struct {
							FieldName string `json:"fieldName"`
							Index     *int   `json:"index"`
						} `json:"fieldPathElements"`
					} `json:"location"`
				} `json:"errors"`
			} `json:"details"`
		} `json:"partialFailureError"`
	}
	err := g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+":uploadClickConversions",
		map[string]any{"conversions": body, "partialFailure": true}, &resp)
	if err != nil {
		return nil, err
	}
	failed := map[int]string{}
	if pf := resp.PartialFailureError; pf != nil {
		for _, d := range pf.Details {
			for _, e := range d.Errors {
				for _, f := range e.Location.FieldPathElements {
					if f.FieldName == "conversions" && f.Index != nil {
						failed[*f.Index] = e.Message
					}
				}
			}
		}
		if len(failed) == 0 && pf.Message != "" {
			// Google said something failed but not which: count it on all.
			for j := range sentIdx {
				failed[j] = pf.Message
			}
		}
	}
	for j, i := range sentIdx {
		if msg, bad := failed[j]; bad {
			results[i].Reason = msg
			continue
		}
		results[i].OK = true
	}
	return results, nil
}

// ---- audiences ----

// Audiences lists the account's open Customer Match lists.
func (g GoogleAds) Audiences(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdAudience, error) {
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT user_list.id, user_list.name FROM user_list WHERE user_list.type = 'CRM_BASED' AND user_list.membership_status = 'OPEN'")
	if err != nil {
		return nil, err
	}
	out := make([]c.AdAudience, 0, len(rows))
	for _, r := range rows {
		out = append(out, c.AdAudience{ID: fmt.Sprint(int64(r.UserList.ID)), Name: r.UserList.Name})
	}
	return out, nil
}

// SyncAudience adds the person to a Customer Match list, or removes them,
// through an offline user data job: create it, add the one operation,
// run it. Google processes the job on its own time, and matches the
// hashed identifiers on its side.
func (g GoogleAds) SyncAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, audienceID, change string, p c.AudienceMember) error {
	var ids []map[string]string
	for _, e := range p.Emails {
		ids = append(ids, map[string]string{"hashedEmail": e})
	}
	for _, ph := range p.Phones {
		ids = append(ids, map[string]string{"hashedPhoneNumber": ph})
	}
	if len(ids) == 0 {
		return nil
	}
	meta := map[string]any{"userList": "customers/" + acct.ID + "/userLists/" + audienceID}
	if p.ConsentGranted {
		meta["consent"] = map[string]string{"adUserData": "GRANTED", "adPersonalization": "GRANTED"}
	}
	k := g.call(cl, creds, acct.Meta["login_customer_id"])
	var job struct {
		ResourceName string `json:"resourceName"`
	}
	err := k.post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/offlineUserDataJobs:create",
		map[string]any{"job": map[string]any{"type": "CUSTOMER_MATCH_USER_LIST", "customerMatchUserListMetadata": meta}}, &job)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(job.ResourceName, "customers/"+acct.ID+"/offlineUserDataJobs/") {
		return c.Errf(c.ErrUnavailable, "Google Ads made no user data job")
	}
	op := "create"
	if change == c.AudienceRemove {
		op = "remove"
	}
	if err := k.post(ctx, g.base(cl)+"/"+job.ResourceName+":addOperations",
		map[string]any{"operations": []any{map[string]any{op: map[string]any{"userIdentifiers": ids}}}}, nil); err != nil {
		return err
	}
	return k.post(ctx, g.base(cl)+"/"+job.ResourceName+":run", map[string]any{}, nil)
}
