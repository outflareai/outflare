package seo

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const seRankingAPI = "https://api.seranking.com/v1"

// SERanking reads SE Ranking's Data API: a domain's organic overview, the
// keywords it ranks for and its competitors in search, from SE Ranking's
// keyword database for one country. Every request costs 100 API credits
// from the account's Data API plan whatever the row count; SE Ranking
// does not say per response what it charged, so the recorded units are its
// price list. Checking the key reads the subscription, which is free.
type SERanking struct{}

// seRankingCall is what one Data API request costs, in credits.
const seRankingCall = 100

func (SERanking) Definition() c.Definition {
	d := def("seranking", "SE Ranking", "https://seranking.com", "https://seranking.com/api/data/getting-started/",
		"https://online.seranking.com/admin.api.dashboard.html",
		"Keyword positions, estimated search traffic and competitors for any domain, from SE Ranking's keyword database.",
		"Needs an SE Ranking Data API key. Reads the keywords a domain ranks for in one country, with position, search volume and the ranking page, plus its overview and competitors. "+
			"Each request spends 100 API credits from your SE Ranking plan; Outflare records each call in Outcomes and stops at the connection's monthly budget. "+
			"Keywords you track in an SE Ranking project for one of your sites come into Rankings each day, with their positions and ranking pages, without Outflare checking them again.",
		"API credits from your SE Ranking Data API plan", []c.Field{
			{Name: "api_key", Label: "API key", Secret: true, Help: "SE Ranking > API dashboard > API key. The same key reads your projects' tracked keywords."},
		})
	d.Capabilities = append(d.Capabilities, c.CapTrackedRanks)
	return d
}

func (SERanking) SEOReports() []string {
	return []string{c.SEOOverview, c.SEOKeywords, c.SEOCompetitors}
}

func (SERanking) Estimate(q c.SEOQuery) c.SEOCost {
	return c.SEOCost{Units: seRankingCall, Estimated: true}
}

// seRankingSource is SE Ranking's database code for a country: the
// lower-case two-letter code, except the United Kingdom, which it files
// under uk.
func seRankingSource(country string) string {
	if country = strings.ToLower(country); country == "gb" {
		return "uk"
	}
	return country
}

func (SERanking) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) error {
	return get(ctx, cl, "SE Ranking", cl.Base("seranking", seRankingAPI)+path+"?"+q.Encode(),
		http.Header{"Authorization": {"Token " + c.Token(creds)}}, out)
}

func (s SERanking) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var resp struct {
		Info struct {
			Status     string   `json:"status"`
			Expiration string   `json:"expiration_date"`
			Limit      *float64 `json:"units_limit"`
			Left       *float64 `json:"units_left"`
		} `json:"subscription_info"`
	}
	if err := s.get(ctx, cl, creds, "/account/subscription", url.Values{}, &resp); err != nil {
		return c.Account{}, err
	}
	if st := strings.ToLower(resp.Info.Status); st != "" && st != "active" {
		return c.Account{}, c.Errf(c.ErrAuth, "SE Ranking says the Data API subscription is %s; renew it in SE Ranking first", st)
	}
	acct := c.Account{Name: "SE Ranking Data API", Details: map[string]string{}, Credits: n(resp.Info.Left)}
	if resp.Info.Limit != nil {
		acct.Details["units_limit"] = itoa(int(*resp.Info.Limit))
	}
	if resp.Info.Expiration != "" {
		acct.Details["expires"] = resp.Info.Expiration
	}
	return acct, nil
}

func (s SERanking) SEOReport(ctx context.Context, cl *c.Client, creds c.Creds, q c.SEOQuery) (c.SEOReport, error) {
	rep := c.SEOReport{Report: q.Report, Target: q.Target, Country: q.Country, Notes: []string{}}
	rep.Cost = s.Estimate(q)
	v := url.Values{"source": {seRankingSource(q.Country)}, "domain": {q.Target}}
	switch q.Report {
	case c.SEOOverview:
		v.Set("with_subdomains", "1")
		var resp struct {
			Organic struct {
				Keywords *float64 `json:"keywords_count"`
				Traffic  *float64 `json:"traffic_sum"`
				Price    *float64 `json:"price_sum"`
				Top1to5  *float64 `json:"top1_5"`
			} `json:"organic"`
		}
		if err := s.get(ctx, cl, creds, "/domain/overview/db", v, &resp); err != nil {
			return rep, err
		}
		o := resp.Organic
		rep.Totals = c.SEOMetrics{OrganicKeywords: n(o.Keywords), OrganicTraffic: n(o.Traffic), TrafficValue: n(o.Price)}
		rep.Notes = append(rep.Notes, "SE Ranking counts positions 1 to 5 together, so the top 3 is not reported. It has no domain rating or links in this report.")
	case c.SEOKeywords:
		v.Set("with_subdomains", "true")
		v.Set("limit", itoa(q.Limit))
		v.Set("order_field", "traffic")
		v.Set("order_type", "desc")
		var rows []struct {
			Keyword  string   `json:"keyword"`
			Position *float64 `json:"position"`
			Volume   any      `json:"volume"`
			Traffic  any      `json:"traffic"`
			URL      string   `json:"url"`
		}
		if err := s.get(ctx, cl, creds, "/domain/keywords", v, &rows); err != nil {
			return rep, err
		}
		for _, r := range rows {
			rep.Rows = append(rep.Rows, c.SEORow{Key: r.Keyword, URL: r.URL, SEOMetrics: c.SEOMetrics{
				Position: n(r.Position), Volume: num(r.Volume), Traffic: num(r.Traffic),
			}})
		}
		rep.Notes = append(rep.Notes, "Traffic is SE Ranking's estimate of monthly visits from each keyword.")
	case c.SEOCompetitors:
		v.Set("type", "organic")
		var rows []struct {
			Domain   string   `json:"domain"`
			Common   *float64 `json:"common_keywords"`
			Keywords *float64 `json:"total_keywords"`
			Traffic  *float64 `json:"traffic_sum"`
			Price    *float64 `json:"price_sum"`
		}
		if err := s.get(ctx, cl, creds, "/domain/competitors", v, &rows); err != nil {
			return rep, err
		}
		self := strings.TrimPrefix(strings.ToLower(q.Target), "www.")
		for _, r := range rows {
			if d := strings.TrimPrefix(strings.ToLower(r.Domain), "www."); d == "" || d == self {
				continue
			}
			if len(rep.Rows) == q.Limit {
				break
			}
			rep.Rows = append(rep.Rows, c.SEORow{Key: r.Domain, SEOMetrics: c.SEOMetrics{
				CommonKeywords: n(r.Common), OrganicKeywords: n(r.Keywords), OrganicTraffic: n(r.Traffic), TrafficValue: n(r.Price),
			}})
		}
	default:
		return rep, c.Errf(c.ErrInvalid, "SE Ranking here answers %s", strings.Join(s.SEOReports(), ", "))
	}
	return rep, nil
}
