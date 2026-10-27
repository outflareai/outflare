package emails

import (
	"context"
	"html"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/tracking"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// tagOwnLinks adds the campaign tags of a broadcast or journey to the
// links in its email that go to the brand's own sites (any brand's, for an
// email with none). Links to other sites, and links tagged by hand, stay as
// written. The campaign is made with the first email that has such a link.
func (s *Service) tagOwnLinks(ctx context.Context, tx pgx.Tx, ws string, brandID *string, trigger, ref, htmlBody, text string) (string, string, error) {
	rows, err := tx.Query(ctx, `SELECT website, domains FROM brands WHERE workspace_id = $1 AND ($2::uuid IS NULL OR id = $2::uuid)`, ws, brandID)
	if err != nil {
		return "", "", err
	}
	sites := []string{}
	for rows.Next() {
		var website string
		var domains []string
		if err := rows.Scan(&website, &domains); err != nil {
			rows.Close()
			return "", "", err
		}
		sites = append(append(sites, website), domains...)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	hosts := utm.Hosts(sites...)
	if len(hosts) == 0 || !mentionsAny(htmlBody+" "+text, hosts) {
		return htmlBody, text, nil
	}
	kind, table := "email_broadcast", "email_broadcasts"
	if trigger == "journey" {
		kind, table = "journey", "journeys"
	}
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM `+table+` WHERE workspace_id = $1 AND id::text = $2`, ws, ref).Scan(&name); err != nil {
		return "", "", err
	}
	c, created, err := utm.Ensure(ctx, tx, ws, utm.Spec{Channel: "lifecycle_email", Source: "outflare", Name: name, BrandID: brandID,
		ObjectKind: kind, ObjectID: ref})
	if err != nil {
		return "", "", err
	}
	if created {
		if err := s.record(ctx, tx, System, ws, "tracking_campaign.created", "tracking_campaign", c.ID,
			map[string]any{"channel": c.Channel, "source": c.Source, "object_kind": kind}); err != nil {
			return "", "", err
		}
	}
	if htmlBody != "" {
		htmlBody = hrefs.ReplaceAllStringFunc(htmlBody, func(m string) string {
			p := hrefs.FindStringSubmatch(m)
			return p[1] + p[2] + html.EscapeString(c.OwnLink(html.UnescapeString(p[3]), hosts)) + p[4]
		})
	}
	text = tracking.ReplaceLinks(text, func(link string) string { return c.OwnLink(link, hosts) })
	return htmlBody, text, nil
}

// mentionsAny is a quick check that body names one of hosts at all.
func mentionsAny(body string, hosts []string) bool {
	body = strings.ToLower(body)
	for _, h := range hosts {
		if strings.Contains(body, h) {
			return true
		}
	}
	return false
}
