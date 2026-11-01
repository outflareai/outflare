package analytics

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// LandingOutcomes uses GA4's session landing page and session source. Key
// events are GA4's configured key events; they may occur more than once/session.
func (g GA4) LandingOutcomes(ctx context.Context, cl *c.Client, creds c.Creds, q c.PageConversionQuery) ([]c.PageConversionRow, error) {
	if err := checkSite("Google Analytics", q.Site, digits); err != nil {
		return nil, err
	}
	out := []c.PageConversionRow{}
	index := map[string]int{}
	for offset := 0; offset < 50000; {
		body := map[string]any{"dateRanges": []map[string]string{{"startDate": q.From, "endDate": q.To}}, "dimensions": []map[string]string{{"name": "date"}, {"name": "hostName"}, {"name": "landingPagePlusQueryString"}, {"name": "sessionSourceMedium"}}, "metrics": []map[string]string{{"name": "sessions"}, {"name": "totalUsers"}, {"name": "keyEvents"}, {"name": "purchaseRevenue"}}, "limit": 10000, "offset": offset, "orderBys": []map[string]any{{"dimension": map[string]string{"dimensionName": "date"}}, {"dimension": map[string]string{"dimensionName": "hostName"}}, {"dimension": map[string]string{"dimensionName": "landingPagePlusQueryString"}}, {"dimension": map[string]string{"dimensionName": "sessionSourceMedium"}}}}
		var resp struct {
			Rows []struct {
				DimensionValues []struct {
					Value string `json:"value"`
				} `json:"dimensionValues"`
				MetricValues []struct {
					Value string `json:"value"`
				} `json:"metricValues"`
			} `json:"rows"`
			RowCount int `json:"rowCount"`
			Metadata struct {
				Currency string `json:"currencyCode"`
			} `json:"metadata"`
		}
		u := cl.Base("google_analytics", gaData) + "/v1beta/properties/" + q.Site + ":runReport"
		if err := newCall(cl, "Google Analytics").bearer(creds["access_token"]).post(ctx, u, body, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Rows {
			if len(r.DimensionValues) != 4 || len(r.MetricValues) != 4 {
				continue
			}
			day, host, path, source := expand(r.DimensionValues[0].Value), strings.ToLower(r.DimensionValues[1].Value), r.DimensionValues[2].Value, r.DimensionValues[3].Value
			// Keep GA4's unknown landing-page bucket in referral totals.
			if path != "(not set)" {
				path = strings.SplitN(path, "?", 2)[0]
				if !strings.HasPrefix(path, "/") {
					path = "/" + path
				}
				parsed, parseErr := url.Parse(path)
				if parseErr != nil || parsed.IsAbs() {
					continue
				}
				path = parsed.EscapedPath()
			}
			n := func(i int) float64 { v, _ := strconv.ParseFloat(r.MetricValues[i].Value, 64); return v }
			sessions, visitors, convs := int(n(0)), int(n(1)), int(n(2))
			key := day + "\x00" + host + "\x00" + path
			idx, ok := index[key]
			if !ok {
				idx = len(out)
				index[key] = idx
				out = append(out, c.PageConversionRow{Day: day, Host: host, Path: path})
			}
			x := &out[idx]
			x.Sessions += sessions
			x.Visitors += visitors
			x.Conversions += convs
			if strings.HasSuffix(source, " / organic") {
				x.SearchSessions += sessions
				x.SearchConversions += convs
			}
			merged := false
			for j := range x.Outcomes {
				if x.Outcomes[j].Source == source {
					x.Outcomes[j].Sessions += sessions
					x.Outcomes[j].Conversions += convs
					x.Outcomes[j].RevenueMicros += int64(n(3) * 1e6)
					merged = true
					break
				}
			}
			if !merged {
				x.Outcomes = append(x.Outcomes, c.SearchOutcome{Source: source, Sessions: sessions, Conversions: convs, RevenueMicros: int64(n(3) * 1e6), Currency: resp.Metadata.Currency})
			}
		}
		offset += len(resp.Rows)
		if offset >= resp.RowCount || len(resp.Rows) == 0 {
			break
		}
		if offset >= 50000 {
			return nil, c.Errf(c.ErrQuota, "GA4 landing-page attribution exceeds 50,000 rows; narrow the reporting property")
		}
	}
	return out, nil
}
