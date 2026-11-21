package api

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/analyticsync"
	"github.com/outflareai/outflare/engine/internal/auth"
)

// AI crawler requests a brand's own server reports, for a site that isn't
// behind a CDN whose logs Outflare can read. AI crawlers run no scripts,
// so the website script never sees them: the site's server tells Outflare
// the user agent of each request (the libraries' crawler tracker counts
// them in memory and sends the counts), and Outflare keeps only how often
// each AI crawler it knows came, per host and day. The user agents
// themselves, paths and addresses are not kept. Where a CDN connection
// has the same host and day, its logs win, so nothing counts twice.

// aiBotReportMax is how many entries one report may carry.
const aiBotReportMax = 1000

var reportHost = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

func (s *Server) routesAIBotReports(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/brands/{brand}/ai-bot-hits", s.onBrand(auth.ScopeCustomersManage, s.reportAIBotHits))
}

type aiBotReport struct {
	Hits []struct {
		UserAgent string `json:"user_agent"`
		Host      string `json:"host"`
		Day       string `json:"day"`
		Requests  *int64 `json:"requests"`
	} `json:"hits"`
}

func (s *Server) reportAIBotHits(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in aiBotReport
	if !decode(w, r, &in) {
		return
	}
	if len(in.Hits) == 0 || len(in.Hits) > aiBotReportMax {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `send {"hits": [{"user_agent": "...", "host": "www.example.com"}]} with 1 to 1000 entries`)
		return
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	type key struct{ host, day, token string }
	sums := map[key]int64{}
	ignored := 0
	for i, h := range in.Hits {
		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h.Host)), ".")
		if j := strings.LastIndex(host, ":"); j > 0 && !strings.Contains(host[j:], "]") {
			host = host[:j] // a port
		}
		if !reportHost.MatchString(host) || !strings.Contains(host, ".") {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "hits["+itoa(i)+"].host is the site's host name, such as www.example.com")
			return
		}
		day := today
		if h.Day != "" {
			d, err := time.Parse(time.DateOnly, h.Day)
			if err != nil || d.After(today.AddDate(0, 0, 1)) || d.Before(today.AddDate(0, 0, -7)) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "hits["+itoa(i)+"].day is a date (YYYY-MM-DD) in the last 7 days; today when left out")
				return
			}
			day = d
		}
		n := int64(1)
		if h.Requests != nil {
			n = *h.Requests
		}
		if n < 1 || n > 10_000_000 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "hits["+itoa(i)+"].requests is how many requests, 1 when left out")
			return
		}
		token := analyticsync.BotToken(h.UserAgent)
		if token == "" {
			ignored++
			continue
		}
		sums[key{host, day.Format(time.DateOnly), token}] += n
	}
	ctx := r.Context()
	var total int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for k, n := range sums {
			total += n
			if _, err := tx.Exec(ctx, `INSERT INTO ai_bot_reports (workspace_id, brand_id, host, day, token, requests) VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (brand_id, host, day, token) DO UPDATE SET requests = ai_bot_reports.requests + EXCLUDED.requests, updated_at = now()`,
				a.WorkspaceID, a.Brand, k.host, k.day, k.token, n); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"counted": total, "ignored": ignored})
}
