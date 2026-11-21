package ads

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestMicrosoftAdsKeywords(t *testing.T) {
	old := msPoll
	msPoll.every, msPoll.limit = time.Millisecond, time.Second
	t.Cleanup(func() { msPoll = old })
	zipOf := func(csv string) []byte {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		f, _ := zw.Create("report.csv")
		f.Write([]byte("\ufeff" + csv))
		zw.Close()
		return b.Bytes()
	}
	var asked string
	var srvURL string
	cl, reqs, u := fake(t, map[string]any{
		"POST /GenerateReport/Submit": func(r *http.Request) any {
			var body struct{ ReportRequest struct{ Type string } }
			json.NewDecoder(r.Body).Decode(&body)
			asked = body.ReportRequest.Type
			return map[string]any{"ReportRequestId": "r1"}
		},
		"POST /GenerateReport/Poll": func(*http.Request) any {
			return map[string]any{"ReportRequestStatus": map[string]any{"Status": "Success", "ReportDownloadUrl": srvURL + "/download/" + asked}}
		},
		"GET /download/KeywordPerformanceReportRequest": zipOf("TimePeriod,AdGroupId,KeywordId,Keyword,BidMatchType,KeywordStatus,Spend,Impressions,Clicks,Conversions,Revenue\r\n" +
			"2026-10-01,41,900,residential proxies,Phrase,Active,10.00,100,5,1,40.00\r\n" +
			"2026-10-01,41,900,residential proxies,Phrase,Active,2.50,20,1,0,0\r\n" +
			"2026-10-02,41,901,cheap proxies,Exact,Paused,1.00,10,1,0,0\r\n"),
		"GET /download/SearchQueryPerformanceReportRequest": zipOf("TimePeriod,AdGroupId,SearchQuery,Keyword,BidMatchType,DeliveredMatchType,Spend,Impressions,Clicks,Conversions,Revenue\r\n" +
			"2026-10-01,41,free proxy list,residential proxies,Phrase,Broad,3.00,30,3,0,0\r\n"),
		"POST /Keywords":                          map[string]any{"PartialErrors": []any{}},
		"PUT /Keywords":                           map[string]any{"PartialErrors": []any{}},
		"POST /EntityNegativeKeywords":            map[string]any{"NestedPartialErrors": []any{}},
		"DELETE /EntityNegativeKeywords":          map[string]any{"NestedPartialErrors": []any{}},
		"POST /NegativeKeywords/QueryByEntityIds": map[string]any{"EntityNegativeKeywords": []any{map[string]any{"EntityId": 31, "NegativeKeywords": []any{map[string]any{"Id": 7, "Text": "Free Proxy List", "MatchType": "Exact"}, map[string]any{"Id": 8, "Text": "other", "MatchType": "Exact"}}}}},
		"POST /Campaigns/QueryByAccountId":        map[string]any{"Campaigns": []any{map[string]any{"Id": 31, "Name": "Bing brand", "Status": "Active", "CampaignType": "Search"}}},
		"POST /AdGroups/QueryByCampaignId":        map[string]any{"AdGroups": []any{map[string]any{"Id": 41, "Name": "Proxies", "Status": "Active"}}},
		"PUT /AdGroups":                           map[string]any{"PartialErrors": []any{map[string]any{"Message": "the ad group is deleted"}}},
	})
	srvURL = u
	ms, creds, acct := MicrosoftAds{}, c.Creds{"access_token": "tok", "developer_token": "dev"}, c.AdAccount{ID: "77", Meta: map[string]string{"customer_id": "8"}}

	kws, err := ms.AdKeywordDays(ctx, cl, creds, acct, "2026-10-01", "2026-10-02")
	if err != nil || len(kws) != 2 {
		t.Fatalf("keywords %+v %v", kws, err)
	}
	if k := kws[0]; k.ID != "900" || k.AdGroupID != "41" || k.MatchType != "phrase" || k.Status != "active" || k.SpendMicros != 12_500_000 || k.Clicks != 6 || *k.Conversions != 1 || *k.ConversionValueMicros != 40_000_000 {
		t.Fatalf("a keyword's day is the sum of its rows: %+v", k)
	}
	if kws[1].Status != "paused" || kws[1].MatchType != "exact" {
		t.Fatalf("second keyword %+v", kws[1])
	}
	terms, err := ms.AdSearchTermDays(ctx, cl, creds, acct, "2026-10-01", "2026-10-02")
	if err != nil || len(terms) != 1 || terms[0].Term != "free proxy list" || terms[0].KeywordText != "residential proxies" || terms[0].KeywordMatchType != "phrase" || terms[0].MatchType != "broad" || terms[0].Clicks != 3 {
		t.Fatalf("search terms %+v %v", terms, err)
	}

	last := func() seen { return (*reqs)[len(*reqs)-1] }
	if err := ms.AddKeyword(ctx, cl, creds, acct, "41", " rotating proxies ", "exact"); err != nil {
		t.Fatal(err)
	}
	if r := last(); r.method != "POST" || r.path != "/Keywords" || !strings.Contains(r.body, `"AdGroupId":41`) || !strings.Contains(r.body, `"Text":"rotating proxies"`) || !strings.Contains(r.body, `"MatchType":"Exact"`) {
		t.Fatalf("add keyword %+v", r)
	}
	if err := ms.AddKeyword(ctx, cl, creds, acct, "41", "x", "fuzzy"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("bad match type: %v", err)
	}
	// A campaign negative; broad becomes phrase, which Microsoft has.
	if err := ms.AddNegativeKeyword(ctx, cl, creds, acct, "31", "", "free proxy list", "broad"); err != nil {
		t.Fatal(err)
	}
	if r := last(); r.path != "/EntityNegativeKeywords" || !strings.Contains(r.body, `"EntityId":31`) || !strings.Contains(r.body, `"EntityType":"Campaign"`) || !strings.Contains(r.body, `"MatchType":"Phrase"`) {
		t.Fatalf("campaign negative %+v", r)
	}
	if err := ms.AddNegativeKeyword(ctx, cl, creds, acct, "31", "41", "free proxy list", "exact"); err != nil {
		t.Fatal(err)
	}
	if r := last(); !strings.Contains(r.body, `"EntityId":41`) || !strings.Contains(r.body, `"EntityType":"AdGroup"`) {
		t.Fatalf("ad group negative %+v", r)
	}
	// Removing finds the negative by its text and match type, whatever its case.
	if err := ms.RemoveNegativeKeyword(ctx, cl, creds, acct, "31", "", "free proxy list", "exact"); err != nil {
		t.Fatal(err)
	}
	if r := last(); r.method != "DELETE" || r.path != "/EntityNegativeKeywords" || !strings.Contains(r.body, `"Id":7`) || strings.Contains(r.body, `"Id":8`) {
		t.Fatalf("remove negative %+v", r)
	}
	n := len(*reqs)
	if err := ms.RemoveNegativeKeyword(ctx, cl, creds, acct, "31", "", "not there", "exact"); err != nil || len(*reqs) != n+1 {
		t.Fatalf("a negative that isn't there succeeds without a delete: %v", err)
	}
	if err := ms.SetKeywordStatus(ctx, cl, creds, acct, "41", "900", false); err != nil {
		t.Fatal(err)
	}
	if r := last(); r.method != "PUT" || r.path != "/Keywords" || !strings.Contains(r.body, `"Id":900`) || !strings.Contains(r.body, `"Status":"Paused"`) {
		t.Fatalf("pause keyword %+v", r)
	}
	// An ad group's change names its campaign, and a refusal is an error.
	err = ms.SetAdGroupStatus(ctx, cl, creds, acct, "41", true)
	if r := last(); c.KindOf(err) != c.ErrInvalid || !strings.Contains(err.Error(), "the ad group is deleted") || r.method != "PUT" || r.path != "/AdGroups" || !strings.Contains(r.body, `"CampaignId":31`) || !strings.Contains(r.body, `"Status":"Active"`) {
		t.Fatalf("ad group %v %+v", err, r)
	}
	if err := ms.SetAdGroupStatus(ctx, cl, creds, acct, "99", true); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("an unknown ad group: %v", err)
	}
	if _, err := ms.SplitKeyword(ctx, cl, creds, acct, "31", "41", "n", "x"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("split: %v", err)
	}
}

func TestMicrosoftAdsAdjustments(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"POST /OfflineConversionAdjustments/Apply": map[string]any{"PartialErrors": []any{map[string]any{"Index": 1, "Message": "no such conversion"}}},
	})
	ms, creds, acct := MicrosoftAds{}, c.Creds{"access_token": "tok", "developer_token": "dev"}, c.AdAccount{ID: "77", Meta: map[string]string{"customer_id": "8"}}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	res, err := ms.AdjustConversions(ctx, cl, creds, acct, c.ConversionTarget{ID: "Offline sale"}, []c.AdConversionAdjustment{
		{Index: 0, Retract: true, Time: at.Add(time.Hour), ConversionTime: at, ClickIDs: map[string]string{"msclkid": "m1"}},
		{Index: 1, ValueMicros: 12_500_000, Currency: "USD", Time: at.Add(time.Hour), ConversionTime: at, Emails: []string{"hashed"}},
		{Index: 2, Retract: true, Time: at.Add(time.Hour), ConversionTime: at},
	})
	if err != nil || !res[0].OK || res[1].OK || res[1].Reason != "no such conversion" || !res[2].Skipped {
		t.Fatalf("adjust %+v %v", res, err)
	}
	body := (*reqs)[len(*reqs)-1].body
	for _, want := range []string{`"AdjustmentType":"Retract"`, `"MicrosoftClickId":"m1"`, `"ConversionTime":"2026-10-01T12:00:00Z"`, `"AdjustmentTime":"2026-10-01T13:00:00Z"`,
		`"AdjustmentType":"Restate"`, `"AdjustmentValue":12.5`, `"AdjustmentCurrencyCode":"USD"`, `"HashedEmailAddress":"hashed"`, `"ConversionName":"Offline sale"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	if strings.Count(body, "ConversionName") != 2 {
		t.Fatalf("the one with nothing to find it by is not sent: %s", body)
	}
}
