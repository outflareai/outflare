package seointel

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/fixes"
)

func init() {
	r := crawl.Rule{ID: "web_vitals_regressed", Severity: crawl.SevWarning, Category: "regressions", Title: "Real-user page experience regressed", Why: "The latest CrUX p75 worsened by at least 20% and is above the good threshold.", Fix: "Open Keywords → Search insights → Core Web Vitals. Compare the affected URL, device and metric with production deploys. Improve LCP resource loading, INP main-thread work or CLS layout stability, then check later field windows."}
	Rules = append(Rules, r)
	ruleByID[r.ID] = r
}
func RefreshVitals(ctx context.Context, tx pgx.Tx, ws, brand, site string) error {
	rows, e := tx.Query(ctx, `SELECT page,device,metric,array_agg(p75 ORDER BY day DESC),max(day) FROM (SELECT *,row_number() OVER(PARTITION BY page,device,metric ORDER BY day DESC) AS n FROM search_vitals WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3) v WHERE n<=2 GROUP BY page,device,metric`, ws, brand, site)
	if e != nil {
		return e
	}
	found := []fixes.Found{}
	keep := []string{}
	for rows.Next() {
		var page, device, metric string
		var values []float64
		var day time.Time
		if e = rows.Scan(&page, &device, &metric, &values, &day); e != nil {
			rows.Close()
			return e
		}
		key := page + "|" + device + "|" + metric
		if len(values) < 2 || day.Before(time.Now().UTC().AddDate(0, 0, -35)) {
			keep = append(keep, key)
			continue
		}
		threshold := 0.0
		switch metric {
		case "largest_contentful_paint":
			threshold = 2500
		case "interaction_to_next_paint":
			threshold = 200
		case "cumulative_layout_shift":
			threshold = .1
		}
		if threshold > 0 && values[1] > 0 && values[0] >= values[1]*1.2 && values[0] > threshold {
			found = append(found, fixes.Found{Rule: "web_vitals_regressed", Severity: crawl.SevWarning, Group: key, Count: 1, Unit: "metrics"})
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	var id string
	if e = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); e != nil {
		return e
	}
	_, e = fixes.Sync(ctx, tx, fixes.Run{Workspace: ws, BrandID: &brand, Source: FixSource, SubjectType: "seo_site", SubjectID: site, RunID: id, Rules: []string{"web_vitals_regressed"}, Keep: keep, Reopen: true}, found)
	return e
}
