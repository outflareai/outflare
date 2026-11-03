// Package coverage is the shared record of where a brand appears, as the
// brand agent plan's Coverage rework (section 4) asks: the plays that
// produce an appearance (a link won, press coverage confirmed, a review
// profile found) write a placement here instead of each module tracking
// appearances its own way. Opportunities (candidates), placements,
// publishers and observations keep their own lifecycles; a submission or
// a pitch accepted is not publication, so a play records a placement only
// once it has observed the appearance, and the observation goes with it.
// Uniqueness is the publisher's page and position (URL and inventory),
// never the domain alone.
package coverage

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
)

// Placement is an appearance a play observed.
type Placement struct {
	// URL of the page it is on; the position on it, when the page holds
	// several (a newsletter issue's slots), in Inventory.
	URL       string
	Inventory string
	Title     string
	// Kind from the placement kinds (news, resource, blog, review, partner,
	// community, ...), Arrangement earned, paid, exchange, affiliate or
	// unknown.
	Kind        string
	Arrangement string
	// TargetURL is the brand's page it links to, when a link was expected.
	TargetURL    string
	ExpectedLink bool
	// Source names the play's module and SourceID its record (a link
	// prospect, a press coverage row, a review page), so the appearance is
	// recorded once and discovery never adds it again as a candidate.
	Source, SourceID string
	// What the play saw: link_present, mention_present, and anything else
	// worth keeping as evidence.
	Evidence    map[string]any
	PublishedOn *time.Time
}

// ErrNoURL means the placement's URL is not an http(s) address.
var ErrNoURL = errors.New("coverage: the placement needs an http(s) URL")

// Canonical is a placement's URL as coverage keys it: lower-case host, no
// fragment, no tracking parameters, a path.
func Canonical(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "", ErrNoURL
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	values := u.Query()
	for k := range values {
		if strings.HasPrefix(strings.ToLower(k), "utm_") || k == "gclid" || k == "fbclid" {
			values.Del(k)
		}
	}
	u.RawQuery = values.Encode()
	return u.String(), nil
}

// Record keeps an observed appearance as a live placement of the brand,
// with its publisher (a company record for the page's domain), an
// observation and its history, in the play's own transaction. A placement
// the page already has is verified again rather than added twice. It
// returns the placement and whether it was new.
func Record(ctx context.Context, tx pgx.Tx, ws, brand string, in Placement) (string, bool, error) {
	u, err := Canonical(in.URL)
	if err != nil {
		return "", false, err
	}
	host := strings.ToLower((&url.URL{}).Hostname())
	if p, err := url.Parse(u); err == nil {
		host = p.Hostname()
	}
	domain := customers.CompanyDomain(host)
	if domain == "" {
		return "", false, ErrNoURL
	}
	companies, err := customers.EnsureCompanies(ctx, tx, ws, &brand, "coverage", []customers.Company{{Domain: domain}})
	if err != nil {
		return "", false, err
	}
	record := companies[domain]
	if record == "" {
		return "", false, errors.New("coverage: no company record for " + domain)
	}
	var publisher string
	if err := tx.QueryRow(ctx, `INSERT INTO coverage_publishers (workspace_id, brand_id, record_id, domain)
		VALUES ($1, $2, $3, $4) ON CONFLICT (workspace_id, brand_id, domain) DO UPDATE SET updated_at = now() RETURNING id::text`,
		ws, brand, record, domain).Scan(&publisher); err != nil {
		return "", false, err
	}
	kind, arrangement := in.Kind, in.Arrangement
	if kind == "" {
		kind = "custom"
	}
	if arrangement == "" {
		arrangement = "unknown"
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = u
	}
	if len(title) > 500 {
		title = title[:500]
	}
	evidence := map[string]any{}
	for k, v := range in.Evidence {
		evidence[k] = v
	}
	evidence["source"], evidence["source_id"] = in.Source, in.SourceID
	ev, _ := json.Marshal(evidence)
	var id string
	var created bool
	// A placement on the page already (any stage short of archived) is
	// verified again; otherwise a live one is added.
	err = tx.QueryRow(ctx, `SELECT id::text FROM coverage_placements WHERE workspace_id = $1 AND brand_id = $2 AND url = $3 AND inventory = $4`,
		ws, brand, u, in.Inventory).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		created = true
		if err := tx.QueryRow(ctx, `INSERT INTO coverage_placements (workspace_id, brand_id, publisher_id, title, url, inventory, target_url, kind, arrangement, stage,
				expected_link, published_on, health, first_verified_at, last_verified_at, last_check_at, next_check_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'live', $10, $11, 'up', now(), now(), now(), now() + interval '7 days') RETURNING id::text`,
			ws, brand, publisher, title, u, in.Inventory, in.TargetURL, kind, arrangement, in.ExpectedLink, in.PublishedOn).Scan(&id); err != nil {
			return "", false, err
		}
	case err != nil:
		return "", false, err
	default:
		if _, err := tx.Exec(ctx, `UPDATE coverage_placements SET
				stage = CASE WHEN stage IN ('identified', 'contacting', 'discussing', 'agreed', 'awaiting_publication', 'declined', 'cancelled') THEN 'live' ELSE stage END,
				published_on = COALESCE(published_on, $2), health = 'up', first_verified_at = COALESCE(first_verified_at, now()), last_verified_at = now(),
				last_check_at = now(), failure_count = 0, version = version + 1, updated_at = now()
			WHERE id = $1`, id, in.PublishedOn); err != nil {
			return "", false, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO coverage_observations (workspace_id, brand_id, placement_id, health, source, evidence) VALUES ($1, $2, $3, 'up', $4, $5)`,
		ws, brand, id, in.Source, ev); err != nil {
		return "", false, err
	}
	action := "verified"
	if created {
		action = "recorded"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO coverage_history (workspace_id, brand_id, resource, resource_id, action, detail) VALUES ($1, $2, 'placements', $3, $4, $5)`,
		ws, brand, id, action, ev); err != nil {
		return "", false, err
	}
	// Discovery never offers it as a candidate again: it is a placement.
	if in.Source != "" && in.SourceID != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO coverage_discovery_receipts (workspace_id, brand_id, source, source_id) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
			ws, brand, in.Source, in.SourceID); err != nil {
			return "", false, err
		}
	}
	return id, created, nil
}

// Observe adds what a play saw on a page the brand has a placement on
// (a link gone, a page gone), without deciding the placement's stage: the
// coverage monitor's rules do that from the observations. It does nothing
// when the page has no placement.
func Observe(ctx context.Context, tx pgx.Tx, ws, brand, pageURL, health, source string, evidence map[string]any) error {
	u, err := Canonical(pageURL)
	if err != nil {
		return nil
	}
	ev, _ := json.Marshal(evidence)
	_, err = tx.Exec(ctx, `INSERT INTO coverage_observations (workspace_id, brand_id, placement_id, health, source, evidence)
		SELECT workspace_id, brand_id, id, $4, $5, $6 FROM coverage_placements WHERE workspace_id = $1 AND brand_id = $2 AND url = $3`,
		ws, brand, u, health, source, ev)
	return err
}
