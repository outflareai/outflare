package ads

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// The click harvest reads one day of click_view; change events are read
// since a time with the user's email hashed; auto-applied recommendation
// types are listed.
func TestGoogleClicksChangesAndAutoApply(t *testing.T) {
	var queries []string
	cl, _, _ := fake(t, map[string]any{
		"POST /v23/customers/111/googleAds:search": func(r *http.Request) any {
			b, _ := io.ReadAll(r.Body)
			q := string(b)
			queries = append(queries, q)
			switch {
			case strings.Contains(q, "FROM click_view"):
				return map[string]any{"results": []any{
					map[string]any{"clickView": map[string]any{"gclid": "Cj0abc", "adGroupAd": "customers/111/adGroupAds/71~81", "keyword": "customers/111/adGroupCriteria/71~91",
						"keywordInfo": map[string]any{"text": "acme crm", "matchType": "EXACT"}},
						"segments": map[string]any{"device": "MOBILE", "date": "2026-11-08"}, "campaign": map[string]any{"id": "7"}, "adGroup": map[string]any{"id": "71"}},
					map[string]any{"clickView": map[string]any{"adGroupAd": ""}, "campaign": map[string]any{"id": "7"}},
				}}
			case strings.Contains(q, "FROM change_event"):
				return map[string]any{"results": []any{map[string]any{"changeEvent": map[string]any{
					"resourceName": "customers/111/changeEvents/1699999999~0~0", "changeDateTime": "2026-11-08 09:30:00", "userEmail": "ana@acme.example",
					"clientType": "GOOGLE_ADS_WEB_CLIENT", "changeResourceType": "CAMPAIGN", "resourceChangeOperation": "UPDATE",
					"changedFields": "campaign.status,campaign.name", "campaign": "customers/111/campaigns/7",
					"oldResource": map[string]any{"campaign": map[string]any{"status": "ENABLED"}}, "newResource": map[string]any{"campaign": map[string]any{"status": "PAUSED"}}}}}}
			case strings.Contains(q, "FROM recommendation_subscription"):
				return map[string]any{"results": []any{map[string]any{"recommendationSubscription": map[string]any{"type": "TARGET_CPA_OPT_IN", "status": "ENABLED"}}}}
			}
			return map[string]any{"results": []any{}}
		},
	})
	g := GoogleAds{}
	creds, acct := c.Creds{"access_token": "tok"}, c.AdAccount{ID: "111", Currency: "USD", TimeZone: "Europe/Berlin"}
	clicks, err := g.AdClicks(ctx, cl, creds, acct, "2026-11-08")
	if err != nil || len(clicks) != 1 {
		t.Fatalf("clicks %+v %v", clicks, err)
	}
	k := clicks[0]
	if k.ClickID != "Cj0abc" || k.Kind != "gclid" || k.CampaignID != "7" || k.AdGroupID != "71" || k.AdID != "81" || k.Keyword != "acme crm" || k.MatchType != "exact" || k.Device != "mobile" {
		t.Fatalf("click %+v", k)
	}
	if !strings.Contains(queries[0], "segments.date = '2026-11-08'") {
		t.Fatalf("click query: %s", queries[0])
	}
	if _, err := g.AdClicks(ctx, cl, creds, acct, "yesterday"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("bad day: %v", err)
	}

	since := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	evs, err := g.AdChangeEvents(ctx, cl, creds, acct, since)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events %+v %v", evs, err)
	}
	e := evs[0]
	berlin, _ := time.LoadLocation("Europe/Berlin")
	if e.ID != "customers/111/changeEvents/1699999999~0~0" || !e.At.Equal(time.Date(2026, 11, 8, 9, 30, 0, 0, berlin)) || e.ClientType != "GOOGLE_ADS_WEB_CLIENT" ||
		e.ResourceType != "CAMPAIGN" || e.Operation != "UPDATE" || e.CampaignID != "7" || len(e.ChangedFields) != 2 || e.ChangedFields[1] != "campaign.name" {
		t.Fatalf("event %+v", e)
	}
	if e.UserHash != c.HashEmail("ana@acme.example") || e.UserHash == "" || strings.Contains(e.UserHash, "@") {
		t.Fatalf("the user is hashed: %q", e.UserHash)
	}
	if e.New["campaign"].(map[string]any)["status"] != "PAUSED" {
		t.Fatalf("new resource %v", e.New)
	}
	q := queries[len(queries)-1]
	// The body is JSON, so > is escaped.
	q = strings.ReplaceAll(q, "\\u003e", ">")
	if !strings.Contains(q, "LIMIT 10000") || !strings.Contains(q, "change_event.change_date_time >= '2026-11-08 01:00:00'") || !strings.Contains(q, "user_email,") {
		t.Fatalf("change query: %s", q)
	}
	// A since older than Google keeps is moved inside the 30 days.
	if _, err := g.AdChangeEvents(ctx, cl, creds, acct, since.AddDate(0, -3, 0)); err != nil {
		t.Fatal(err)
	}
	q = strings.ReplaceAll(queries[len(queries)-1], "\\u003e", ">")
	from := strings.SplitN(strings.SplitN(q, ">= '", 2)[1], "'", 2)[0]
	at, err := time.ParseInLocation("2006-01-02 15:04:05", from, berlin)
	if err != nil || time.Since(at) > 31*24*time.Hour || time.Since(at) < 29*24*time.Hour {
		t.Fatalf("since %s (%v)", from, err)
	}

	types, err := g.AutoApplied(ctx, cl, creds, acct)
	if err != nil || len(types) != 1 || types[0] != "TARGET_CPA_OPT_IN" {
		t.Fatalf("auto-applied %v %v", types, err)
	}
}
