package api

import (
	"context"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Presence watches each placement over time: after every page check, what
// the checks agree on is kept on the placement (link and mention present,
// missing or removed, offline since when), so lists can show it and work
// can be raised. The observations remain the evidence and the history.

// coverageBrandHosts is the sites a link to the brand points at: the target
// page's site when the placement has one, else every domain of the brand.
func coverageBrandHosts(target, website string, domains []string) []string {
	if u, err := url.Parse(target); err == nil && u.Hostname() != "" {
		return []string{strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")}
	}
	hosts := []string{}
	for _, d := range append([]string{website}, domains...) {
		d = strings.TrimSpace(d)
		if u, err := url.Parse(d); err == nil && u.Hostname() != "" {
			d = u.Hostname()
		}
		if d = strings.TrimPrefix(strings.ToLower(d), "www."); d != "" && !strings.ContainsAny(d, "/ ") {
			hosts = append(hosts, d)
		}
	}
	return hosts
}

// coverageHostIn reports whether host is one of hosts or a subdomain of one.
func coverageHostIn(host string, hosts []string) bool {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	for _, h := range hosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// coverageNextStatus is a link's or mention's status after a check. Only a
// page that loaded says anything. One check without it never removes what
// was there; two in a row do.
func coverageNextStatus(status string, now, before any, up, beforeUp bool) string {
	switch {
	case !up || now == nil:
		return status
	case now == true:
		return "present"
	case (status == "present" || status == "removed") && beforeUp && before == false:
		return "removed"
	case status == "present":
		return "present"
	case status == "removed":
		return "removed"
	default:
		return "missing"
	}
}

// coverageWatch keeps what the checks agree on, on the placement, and
// returns the link and mention status. Rotating inventory (ads,
// newsletters, sponsorships) is never marked removed from one sampled page.
func coverageWatch(ctx context.Context, tx pgx.Tx, ws, brand, id, observed, health string, rotating bool, evidence, previous map[string]any, previousHealth string) (string, string, error) {
	var link, mention string
	if err := tx.QueryRow(ctx, "SELECT link_status,mention_status FROM coverage_placements WHERE workspace_id=$1 AND brand_id=$2 AND id=$3", ws, brand, id).Scan(&link, &mention); err != nil {
		return "", "", err
	}
	up, beforeUp := health == "up", previousHealth == "up"
	link = coverageNextStatus(link, evidence["link_present"], previous["link_present"], up, beforeUp)
	mention = coverageNextStatus(mention, evidence["mention_present"], previous["mention_present"], up, beforeUp)
	if rotating {
		if link == "removed" {
			link = "missing"
		}
		if mention == "removed" {
			mention = "missing"
		}
	}
	_, err := tx.Exec(ctx, `UPDATE coverage_placements SET link_status=$4,mention_status=$5,
 link_removed_at=CASE WHEN $4='removed' THEN coalesce(link_removed_at,now()) ELSE NULL END,
 mention_removed_at=CASE WHEN $5='removed' THEN coalesce(mention_removed_at,now()) ELSE NULL END,
 offline_since=CASE WHEN $6='gone' THEN coalesce(offline_since,now()) WHEN $7 THEN NULL ELSE offline_since END
 WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, ws, brand, id, link, mention, observed, up)
	return link, mention, err
}

// coverageRefreshTraction gives each of the brand's placements its
// verdict on whether it has taken off: too_early in its first two weeks
// live, then taking_off when AI answers cited the page at least three
// times in 90 days or its site sent at least 50 visits in 28 days, steady
// with any of either, quiet with none. Pages not confirmed live are
// unknown. It returns how many placements changed verdict.
func coverageRefreshTraction(ctx context.Context, tx pgx.Tx, ws, brand string) (int, error) {
	tag, err := tx.Exec(ctx, `WITH sig AS (
 SELECT p.id,
  CASE WHEN p.health<>'up' OR coalesce(p.published_on,p.first_verified_at::date) IS NULL THEN NULL
   ELSE coalesce(p.published_on,p.first_verified_at::date) END AS live_on,
  (SELECT count(*) FROM ai_citations ci JOIN ai_cohorts co ON co.id=ci.cohort_id AND co.workspace_id=ci.workspace_id
   WHERE ci.workspace_id=$1 AND co.brand_id=$2 AND rtrim(ci.url,'/')=rtrim(p.url,'/') AND ci.sampled_at>now()-interval '90 days')::int AS cited,
  (SELECT count(*) FROM site_sessions ss WHERE ss.workspace_id=$1 AND ss.brand_id=$2 AND ss.started_at>now()-interval '28 days'
   AND ss.referrer_host<>'' AND regexp_replace(lower(ss.referrer_host),'^www\.','')=`+coverageHostSQL("p.url")+`)::int AS visits
 FROM coverage_placements p WHERE p.workspace_id=$1 AND p.brand_id=$2 AND p.url<>''),
 v AS (SELECT id, cited, CASE WHEN live_on IS NULL THEN 'unknown' WHEN live_on>current_date-14 THEN 'too_early'
  WHEN cited>=3 OR visits>=50 THEN 'taking_off' WHEN cited>0 OR visits>0 THEN 'steady' ELSE 'quiet' END AS verdict FROM sig)
 UPDATE coverage_placements p SET traction=v.verdict, ai_citations_90d=v.cited, traction_at=CASE WHEN p.traction<>v.verdict THEN now() ELSE p.traction_at END
 FROM v WHERE p.id=v.id AND p.workspace_id=$1 AND p.brand_id=$2 AND (p.traction<>v.verdict OR p.ai_citations_90d<>v.cited)`, ws, brand)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
