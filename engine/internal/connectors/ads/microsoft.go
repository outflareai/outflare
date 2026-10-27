package ads

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// MicrosoftAds reads Microsoft Advertising (Bing) accounts through its
// REST API. Like Google Ads it needs a developer token as well as the
// sign-in; the workspace gets its own from the Microsoft Advertising
// developer portal.
type MicrosoftAds struct{}

const (
	msCustomerBase  = "https://clientcenter.api.bingads.microsoft.com/CustomerManagement/v13"
	msCampaignBase  = "https://campaign.api.bingads.microsoft.com/CampaignManagement/v13"
	msReportingBase = "https://reporting.api.bingads.microsoft.com/Reporting/v13"
)

// msPoll is how often and how long a report is waited for. Tests shorten it.
var msPoll = struct{ every, limit time.Duration }{3 * time.Second, 3 * time.Minute}

func (MicrosoftAds) Definition() c.Definition {
	d := def("microsoft_ads", "Microsoft Advertising", "https://ads.microsoft.com",
		"https://learn.microsoft.com/advertising/guides/get-started", "https://developers.ads.microsoft.com/Account",
		"Spend, clicks and conversions per campaign from your Microsoft Advertising (Bing) accounts.",
		"Signs in with Microsoft and reads every Microsoft Advertising account the sign-in can see. Microsoft also asks for a developer token: get your own from the Microsoft Advertising developer portal (Account, Request token) and add it after signing in. Read-only.")
	d.OAuth = &c.OAuth{
		AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		Scopes:   []string{"https://ads.microsoft.com/msads.manage", "offline_access"},
		Params:   map[string]string{"prompt": "select_account"},
	}
	return d
}

func (MicrosoftAds) SetupFields() []c.Field {
	return []c.Field{{Name: "developer_token", Label: "Developer token", Secret: true,
		Help: "From the Microsoft Advertising developer portal (developers.ads.microsoft.com): Account, Request token."}}
}

func (MicrosoftAds) call(cl *c.Client, creds c.Creds) *call {
	return newCall(cl, "Microsoft Advertising").bearer(creds["access_token"]).set("DeveloperToken", creds["developer_token"])
}

func (m MicrosoftAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	acct := c.Account{Details: map[string]string{}}
	if creds["developer_token"] == "" {
		acct.Details["setup"] = "add your developer token"
		return acct, nil
	}
	list, err := m.AdAccounts(ctx, cl, creds)
	if err != nil {
		return acct, err
	}
	acct.Details["accounts"] = fmt.Sprint(len(list))
	return acct, nil
}

func (m MicrosoftAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	if creds["developer_token"] == "" {
		return nil, c.Errf(c.ErrAuth, "Microsoft Advertising needs your developer token: add it on the connection")
	}
	base := cl.Base("microsoft_ads.customer", msCustomerBase)
	var user struct {
		User struct {
			ID flexInt `json:"Id"`
		} `json:"User"`
	}
	if err := m.call(cl, creds).post(ctx, base+"/User/Query", map[string]any{"UserId": nil}, &user); err != nil {
		return nil, err
	}
	var out []c.AdAccount
	for page := 0; page < 20; page++ {
		var resp struct {
			Accounts []struct {
				ID               flexInt `json:"Id"`
				Name             string  `json:"Name"`
				CurrencyCode     string  `json:"CurrencyCode"`
				TimeZone         string  `json:"TimeZone"`
				ParentCustomerID flexInt `json:"ParentCustomerId"`
				AccountLifeCycle string  `json:"AccountLifeCycleStatus"`
			} `json:"Accounts"`
		}
		body := map[string]any{
			"Predicates": []map[string]string{{"Field": "UserId", "Operator": "Equals", "Value": fmt.Sprint(int64(user.User.ID))}},
			"PageInfo":   map[string]int{"Index": page, "Size": 1000},
		}
		if err := m.call(cl, creds).post(ctx, base+"/Accounts/Search", body, &resp); err != nil {
			return nil, err
		}
		for _, a := range resp.Accounts {
			if a.AccountLifeCycle == "Inactive" {
				continue
			}
			out = append(out, c.AdAccount{ID: fmt.Sprint(int64(a.ID)), Name: a.Name, Currency: a.CurrencyCode, TimeZone: a.TimeZone,
				Meta: map[string]string{"customer_id": fmt.Sprint(int64(a.ParentCustomerID))}})
		}
		if len(resp.Accounts) < 1000 {
			break
		}
	}
	return out, nil
}

func (m MicrosoftAds) scoped(cl *c.Client, creds c.Creds, acct c.AdAccount) *call {
	return m.call(cl, creds).set("CustomerId", acct.Meta["customer_id"]).set("CustomerAccountId", acct.ID)
}

func (m MicrosoftAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	var resp struct {
		Campaigns []struct {
			ID           flexInt `json:"Id"`
			Name         string  `json:"Name"`
			Status       string  `json:"Status"`
			CampaignType string  `json:"CampaignType"`
			DailyBudget  flexNum `json:"DailyBudget"`
		} `json:"Campaigns"`
	}
	body := map[string]any{"AccountId": atoi64(acct.ID), "CampaignType": "Search Shopping DynamicSearchAds Audience PerformanceMax"}
	if err := m.scoped(cl, creds, acct).post(ctx, cl.Base("microsoft_ads.campaign", msCampaignBase)+"/Campaigns/QueryByAccountId", body, &resp); err != nil {
		return nil, err
	}
	out := make([]c.AdCampaign, 0, len(resp.Campaigns))
	for _, cp := range resp.Campaigns {
		x := c.AdCampaign{ID: fmt.Sprint(int64(cp.ID)), Name: cp.Name, Status: status(cp.Status), ProviderStatus: cp.Status,
			Objective: strings.ToLower(cp.CampaignType)}
		if cp.DailyBudget > 0 {
			x.DailyBudgetMicros = ptrI(c.Micros(float64(cp.DailyBudget)))
		}
		out = append(out, x)
	}
	return out, nil
}

func msDate(s string) map[string]int {
	t := mustDate(s)
	return map[string]int{"Day": t.Day(), "Month": int(t.Month()), "Year": t.Year()}
}

func (m MicrosoftAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	base := cl.Base("microsoft_ads.reporting", msReportingBase)
	req := map[string]any{"ReportRequest": map[string]any{
		"Type": "CampaignPerformanceReportRequest", "Format": "Csv", "FormatVersion": "2.0", "ReportName": "Outflare",
		"ExcludeReportHeader": true, "ExcludeReportFooter": true, "ExcludeColumnHeaders": false, "ReturnOnlyCompleteData": false,
		"Aggregation": "Daily",
		"Columns":     []string{"TimePeriod", "CampaignId", "CampaignName", "Spend", "Impressions", "Clicks", "Conversions", "Revenue"},
		"Scope":       map[string]any{"AccountIds": []int64{atoi64(acct.ID)}},
		"Time":        map[string]any{"CustomDateRangeStart": msDate(from), "CustomDateRangeEnd": msDate(to)},
	}}
	var sub struct {
		ReportRequestID string `json:"ReportRequestId"`
	}
	if err := m.scoped(cl, creds, acct).post(ctx, base+"/GenerateReport/Submit", req, &sub); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(msPoll.limit)
	for {
		var poll struct {
			ReportRequestStatus struct {
				Status            string `json:"Status"`
				ReportDownloadURL string `json:"ReportDownloadUrl"`
			} `json:"ReportRequestStatus"`
		}
		if err := m.scoped(cl, creds, acct).post(ctx, base+"/GenerateReport/Poll", map[string]string{"ReportRequestId": sub.ReportRequestID}, &poll); err != nil {
			return nil, err
		}
		switch poll.ReportRequestStatus.Status {
		case "Success":
			if poll.ReportRequestStatus.ReportDownloadURL == "" {
				return nil, nil // no data in the range
			}
			return m.download(ctx, cl, poll.ReportRequestStatus.ReportDownloadURL)
		case "Error":
			return nil, c.Errf(c.ErrUnavailable, "Microsoft Advertising could not build the report; it is tried again on the next sync")
		}
		if time.Now().After(deadline) {
			return nil, c.Errf(c.ErrUnavailable, "Microsoft Advertising took too long to build the report; it is tried again on the next sync")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(msPoll.every):
		}
	}
}

// download reads the report: a zip holding one CSV.
func (MicrosoftAds) download(ctx context.Context, cl *c.Client, u string) ([]c.AdDay, error) {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://127.0.0.1") {
		return nil, c.Errf(c.ErrUnavailable, "Microsoft Advertising sent a report address that is not https")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := cl.Do(req)
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "could not download the Microsoft Advertising report: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.HTTPError("Microsoft Advertising", resp.StatusCode, "")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "reading the Microsoft Advertising report: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(zr.File) == 0 {
		return nil, c.Errf(c.ErrUnavailable, "the Microsoft Advertising report is not a zip file")
	}
	f, err := zr.File[0].Open()
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "opening the Microsoft Advertising report: %v", err)
	}
	defer f.Close()
	return parseMSReport(io.LimitReader(f, 256<<20))
}

func parseMSReport(r io.Reader) ([]c.AdDay, error) {
	br := bufio.NewReader(r)
	if b, err := br.Peek(3); err == nil && string(b) == "\xef\xbb\xbf" {
		_, _ = br.Discard(3) // the byte order mark Excel wants
	}
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	head, err := cr.Read()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "the Microsoft Advertising report could not be read: %v", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	get := func(row []string, name string) string {
		if i, ok := col[name]; ok && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
	num := func(s string) float64 {
		v, _ := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSuffix(s, "%"), ",", ""), 64)
		return v
	}
	var out []c.AdDay
	for {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, c.Errf(c.ErrUnavailable, "the Microsoft Advertising report could not be read: %v", err)
		}
		date := get(row, "TimePeriod")
		if t, err := time.Parse("1/2/2006", date); err == nil {
			date = t.Format(time.DateOnly)
		}
		if _, err := time.Parse(time.DateOnly, date); err != nil || get(row, "CampaignId") == "" {
			continue // a footer or a blank line
		}
		out = append(out, c.AdDay{CampaignID: get(row, "CampaignId"), CampaignName: get(row, "CampaignName"), Date: date,
			SpendMicros: c.Micros(num(get(row, "Spend"))), Impressions: int64(num(get(row, "Impressions"))), Clicks: int64(num(get(row, "Clicks"))),
			Conversions: ptrF(num(get(row, "Conversions"))), ConversionValueMicros: ptrI(c.Micros(num(get(row, "Revenue"))))})
	}
	return out, nil
}
