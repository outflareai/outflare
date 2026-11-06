package seo

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestSERanking(t *testing.T) {
	cl, calls := fake(t, map[string]any{
		"GET /account/subscription": map[string]any{"subscription_info": map[string]any{
			"status": "active", "start_date": "2026-10-01", "expiration_date": "2026-12-01", "units_limit": 100000, "units_left": 61200,
		}},
		"GET /domain/overview/db": map[string]any{
			"organic": map[string]any{"keywords_count": 1200, "traffic_sum": 3400, "price_sum": 5100, "top1_5": 40},
			"adv":     map[string]any{"keywords_count": 3},
		},
		"GET /domain/keywords": []any{
			map[string]any{"keyword": "cold email tool", "position": 3, "prev_pos": 5, "volume": 2400, "traffic": 310, "url": "https://example.com/"},
			map[string]any{"keyword": "email warmup", "position": 12, "volume": "880", "traffic": "14", "url": "https://example.com/warmup"},
		},
		"GET /domain/competitors": []any{
			map[string]any{"domain": "example.com", "common_keywords": 1200},
			map[string]any{"domain": "rival-com.example", "common_keywords": 300, "total_keywords": 900, "traffic_sum": 2000, "price_sum": 800},
		},
	})
	creds := c.Creds{"api_key": "k"}
	s := SERanking{}
	acct, err := s.Test(ctx, cl, creds)
	if err != nil || val(acct.Credits) != 61200 || acct.Details["units_limit"] != "100000" || acct.Details["expires"] != "2026-12-01" {
		t.Fatalf("test: %+v %v", acct, err)
	}

	rep, err := s.SEOReport(ctx, cl, creds, q(c.SEOOverview))
	if err != nil || val(rep.Totals.OrganicKeywords) != 1200 || val(rep.Totals.OrganicTraffic) != 3400 || val(rep.Totals.TrafficValue) != 5100 ||
		rep.Totals.Top3 != nil || rep.Totals.DomainRating != nil || rep.Cost.Units != 100 || !rep.Cost.Estimated {
		t.Fatalf("overview: %+v %v", rep, err)
	}
	if !strings.Contains((*calls)[1], "source=us") || !strings.Contains((*calls)[1], "domain=example.com") {
		t.Fatalf("overview call: %s", (*calls)[1])
	}

	rep, err = s.SEOReport(ctx, cl, creds, q(c.SEOKeywords))
	if err != nil || len(rep.Rows) != 2 || rep.Rows[0].Key != "cold email tool" || val(rep.Rows[0].Position) != 3 ||
		val(rep.Rows[0].Volume) != 2400 || rep.Rows[0].URL != "https://example.com/" || val(rep.Rows[1].Volume) != 880 || val(rep.Rows[1].Traffic) != 14 {
		t.Fatalf("keywords: %+v %v", rep, err)
	}
	if !strings.Contains((*calls)[2], "limit=10") || !strings.Contains((*calls)[2], "order_field=traffic") {
		t.Fatalf("keywords call: %s", (*calls)[2])
	}

	rep, err = s.SEOReport(ctx, cl, creds, q(c.SEOCompetitors))
	if err != nil || len(rep.Rows) != 1 || rep.Rows[0].Key != "rival-com.example" || val(rep.Rows[0].CommonKeywords) != 300 || val(rep.Rows[0].OrganicTraffic) != 2000 {
		t.Fatalf("competitors: %+v %v", rep, err)
	}

	uk := q(c.SEOOverview)
	uk.Country = "GB"
	if _, err := s.SEOReport(ctx, cl, creds, uk); err != nil || !strings.Contains((*calls)[len(*calls)-1], "source=uk") {
		t.Fatalf("uk: %v %s", err, (*calls)[len(*calls)-1])
	}
	if _, err := s.SEOReport(ctx, cl, creds, q(c.SEOAudit)); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("audit: %v", err)
	}
}

func TestSERankingKeyAndSubscription(t *testing.T) {
	var auth string
	status := "expired"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if auth == "Token bad" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"subscription_info":{"status":"` + status + `"}}`))
	}))
	t.Cleanup(srv.Close)
	cl := &c.Client{BaseURLs: map[string]string{"seranking": srv.URL}}
	if _, err := (SERanking{}).Test(ctx, cl, c.Creds{"api_key": "bad"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("bad key: %v", err)
	}
	if _, err := (SERanking{}).Test(ctx, cl, c.Creds{"api_key": "k"}); c.KindOf(err) != c.ErrAuth || auth != "Token k" {
		t.Fatalf("expired: %v %q", err, auth)
	}
}
