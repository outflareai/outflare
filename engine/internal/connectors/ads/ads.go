// Package ads connects the workspace's own ad accounts: Google Ads, Meta,
// Microsoft Advertising, Reddit Ads, X Ads, LinkedIn Ads, TikTok Ads,
// Snapchat Ads, Taboola Ads, Outbrain Ads, Apple Ads, Amazon Ads and Pinterest Ads.
// Each one maps its own reports onto connectors.AdsReader (accounts,
// campaigns, daily numbers per campaign, money in micros of the account's
// currency), so a person or an agent reads every platform the same way. Google, Meta,
// Microsoft, Reddit, TikTok, Snapchat, Taboola and Pinterest also take conversions back (connectors.
// ConversionSender); nothing here changes campaigns, budgets or ads.
//
// Every connection uses the workspace's own account and keys: Outflare's
// sign-in apps only carry the person to the platform's consent screen, and
// the tokens they produce belong to the workspace. Request shapes follow
// each platform's public API reference; base URLs can be pointed at a fake
// in tests through connectors.Client.BaseURLs.
package ads

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// All returns every available ad connector.
func All() []c.Connector {
	return []c.Connector{GoogleAds{}, MetaAds{}, MicrosoftAds{}, RedditAds{}, XAds{}, LinkedInAds{}, TikTokAds{}, SnapchatAds{}, TaboolaAds{}, OutbrainAds{}, AppleAds{}, AmazonAds{}, PinterestAds{}}
}

func def(id, name, site, docs, keyURL, summary, description string) c.Definition {
	return c.Definition{
		ID: id, Name: name, Category: c.CategoryAds, Website: site, DocsURL: docs, KeyURL: keyURL,
		Summary: summary, Description: description, Auth: c.AuthOAuth2, Fields: []c.Field{},
		Capabilities: []string{c.CapAdsRead}, Pricing: "Free API; ad spend stays on your own account",
	}
}

// ---- HTTP ----

type call struct {
	cl     *c.Client
	name   string
	header http.Header
}

func newCall(cl *c.Client, name string) *call {
	return &call{cl: cl, name: name, header: http.Header{}}
}

func (k *call) bearer(token string) *call {
	k.header.Set("Authorization", "Bearer "+token)
	return k
}

func (k *call) set(key, value string) *call {
	if value != "" {
		k.header.Set(key, value)
	}
	return k
}

func (k *call) do(ctx context.Context, method, u string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return c.Errf(c.ErrInvalid, "bad %s address: %v", k.name, err)
	}
	for h, v := range k.header {
		req.Header[h] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	_, err = c.DoJSON(ctx, k.cl, k.name, req, out)
	return err
}

func (k *call) get(ctx context.Context, u string, out any) error {
	return k.do(ctx, http.MethodGet, u, nil, out)
}

func (k *call) post(ctx context.Context, u string, body, out any) error {
	return k.do(ctx, http.MethodPost, u, body, out)
}

// ---- values ----

// maxPages stops paging that never ends.
const maxPages = 200

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if n < 0 {
		return 0
	}
	return n
}

// flexInt reads a count sent as a JSON number or a string.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		*f = flexInt(n)
	}
	return nil
}

// flexNum reads a number sent as a JSON number or a string.
type flexNum float64

func (f *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		*f = flexNum(n)
	}
	return nil
}

func ptrF(v float64) *float64 { return &v }
func ptrI(v int64) *int64     { return &v }

// status maps a platform's word onto the shared statuses.
func status(word string) string {
	switch strings.ToUpper(strings.TrimSpace(word)) {
	case "ENABLED", "ACTIVE", "SERVING", "RUNNING", "IN_PROCESS":
		return c.AdActive
	case "PAUSED", "INACTIVE", "DRAFT", "ON_HOLD", "CAMPAIGN_PAUSED", "ADSET_PAUSED":
		return c.AdPaused
	case "REMOVED", "DELETED", "ARCHIVED", "CANCELED", "CANCELLED", "COMPLETED":
		return c.AdRemoved
	}
	return c.AdOther
}

// days returns the dates from from to to, inclusive.
func days(from, to string) []string {
	return c.AnalyticsQuery{From: from, To: to}.Days()
}

func mustDate(s string) time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return t
}

func esc(s string) string { return url.PathEscape(s) }
