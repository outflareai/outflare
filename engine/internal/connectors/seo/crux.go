package seo

import (
	"context"
	"encoding/json"
	"fmt"
	c "github.com/outflareai/outflare/engine/internal/connectors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type CrUX struct{}

func (CrUX) Definition() c.Definition {
	return c.Definition{ID: "google_crux", Name: "Chrome UX Report", Category: c.CategorySEO, Website: "https://developer.chrome.com/docs/crux", DocsURL: "https://developer.chrome.com/docs/crux/history-api", KeyURL: "https://console.cloud.google.com/apis/credentials", Summary: "Real-user Core Web Vitals and weekly field history.", Description: "Enable the Chrome UX Report and History APIs in your Google Cloud project and connect your own API key. Low-traffic pages may have no field data.", Auth: c.AuthAPIKey, Fields: []c.Field{{Name: "api_key", Label: "API key", Secret: true}}, Capabilities: []string{c.CapWebVitals}, Pricing: "Your Google Cloud API quota"}
}
func (CrUX) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if creds["api_key"] == "" {
		return c.Account{}, c.Errf(c.ErrAuth, "Add your own Chrome UX Report API key")
	}
	var result any
	u := cl.Base("google_crux.current", "https://chromeuxreport.googleapis.com") + "/v1/records:queryRecord?key=" + url.QueryEscape(creds["api_key"])
	if e := post(ctx, cl, "Chrome UX Report", u, http.Header{}, map[string]any{"origin": "https://www.google.com"}, &result); e != nil {
		return c.Account{}, e
	}
	return c.Account{Name: "Chrome UX Report"}, nil
}
func (CrUX) WebVitals(ctx context.Context, cl *c.Client, creds c.Creds, page, device string) ([]c.WebVital, error) {
	body := map[string]any{"formFactor": device, "collectionPeriodCount": 40}
	u, e := url.Parse(page)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, c.Errf(c.ErrInvalid, "Use an https page or origin")
	}
	if u.Path == "" || u.Path == "/" {
		body["origin"] = u.Scheme + "://" + u.Host
	} else {
		body["url"] = page
	}
	type date struct{ Year, Month, Day int }
	type period struct {
		LastDate date `json:"lastDate"`
	}
	var resp struct {
		Record struct {
			CollectionPeriods []period `json:"collectionPeriods"`
			Metrics           map[string]struct {
				Percentiles struct {
					P75s []json.RawMessage `json:"p75s"`
				} `json:"percentilesTimeseries"`
				Histogram []struct {
					Densities []*float64 `json:"densities"`
				} `json:"histogramTimeseries"`
			} `json:"metrics"`
		} `json:"record"`
	}
	endpoint := cl.Base("google_crux", "https://chromeuxreport.googleapis.com") + "/v1/records:queryHistoryRecord?key=" + url.QueryEscape(creds["api_key"])
	if e = post(ctx, cl, "Chrome UX Report", endpoint, http.Header{}, body, &resp); e != nil {
		return nil, e
	}
	out := []c.WebVital{}
	for metric, m := range resp.Record.Metrics {
		for i, raw := range m.Percentiles.P75s {
			if i >= len(resp.Record.CollectionPeriods) || string(raw) == "null" {
				continue
			}
			text := string(raw)
			if len(text) > 1 && text[0] == '"' {
				if e = json.Unmarshal(raw, &text); e != nil {
					return nil, e
				}
			}
			value, e := strconv.ParseFloat(text, 64)
			if e != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return nil, c.Errf(c.ErrUnavailable, "Invalid CrUX percentile")
			}
			d := resp.Record.CollectionPeriods[i].LastDate
			when := time.Date(d.Year, time.Month(d.Month), d.Day, 0, 0, 0, 0, time.UTC)
			if d.Year < 2000 || int(when.Month()) != d.Month || when.Day() != d.Day {
				return nil, fmt.Errorf("invalid CrUX period")
			}
			good := 0.0
			if len(m.Histogram) > 0 && i < len(m.Histogram[0].Densities) && m.Histogram[0].Densities[i] != nil {
				good = *m.Histogram[0].Densities[i]
			}
			out = append(out, c.WebVital{Page: page, Device: device, Day: when.Format(time.DateOnly), Metric: metric, P75: value, GoodShare: good})
		}
	}
	return out, nil
}
