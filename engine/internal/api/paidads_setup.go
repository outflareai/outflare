package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/paidads"
)

// The guided path to measuring ads in cost per new paying customer: each
// step with whether it is done, a line on where it stands, and the page
// and MCP tool that fix it.

type adSetupStep struct {
	Step   string    `json:"step"`
	Title  string    `json:"title"`
	Done   bool      `json:"done"`
	Detail string    `json:"detail"`
	Fix    statusFix `json:"fix"`
}

type adSetupView struct {
	BrandID   string        `json:"brand_id"`
	Steps     []adSetupStep `json:"steps"`
	Done      int           `json:"done"`
	Total     int           `json:"total"`
	Ready     bool          `json:"ready"`
	CheckedAt time.Time     `json:"checked_at"`
}

func (s *Server) adsSetup(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.adSetupSteps(r.Context(), a.WorkspaceID, a.Brand)
	if errors.Is(err, paidads.ErrNoBrand) {
		writeError(w, r, http.StatusNotFound, "not_found", "brand not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// knowledgeReviewColumn is the brand_context column another workstream
// adds (migration 0501) when a person marks the knowledge sheet
// reviewed; until it exists the step reports as coming.
const knowledgeReviewColumn = "knowledge_reviewed_at"

func (s *Server) adSetupSteps(ctx context.Context, ws, brand string) (adSetupView, error) {
	v := adSetupView{BrandID: brand, Steps: []adSetupStep{}, CheckedAt: time.Now().UTC()}
	brandPage := s.brandAppPath(ctx, ws, brand, "")
	page := func(p string) string { return brandPage + p }
	var c struct {
		siteKeys, usedKeys, signups, payments, paymentSources, accounts, googleAccounts, untemplated, rules int
		hasTarget, hasConsent, knowledgeRead, reviewColumn                                                  bool
		knowledgeError                                                                                      string
		reviewedAt                                                                                          *time.Time
	}
	payers := s.paymentConnectors()
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM site_keys k WHERE k.workspace_id = $1 AND k.revoked_at IS NULL AND (k.brand_id IS NULL OR k.brand_id = $2)),
			(SELECT count(*) FROM site_keys k WHERE k.workspace_id = $1 AND k.revoked_at IS NULL AND (k.brand_id IS NULL OR k.brand_id = $2) AND k.last_used_at IS NOT NULL),
			(SELECT count(*) FROM customer_events e WHERE e.workspace_id = $1 AND e.kind = 'track' AND e.name = 'signed_up' AND e.occurred_at > now() - interval '90 days'
				AND COALESCE(e.brand_id, outflare_first_brand($1)) = $2),
			(SELECT count(*) FROM customer_events e WHERE e.workspace_id = $1 AND e.kind = 'track' AND e.name = 'payment.succeeded' AND e.occurred_at > now() - interval '90 days'
				AND COALESCE(e.brand_id, outflare_first_brand($1)) = $2),
			(SELECT count(*) FROM integrations i WHERE i.workspace_id = $1 AND i.connector = ANY($3) AND i.status = 'connected' AND (i.brand_id IS NULL OR i.brand_id = $2)),
			(SELECT count(*) FROM ad_accounts x WHERE x.workspace_id = $1 AND x.status = 'active' AND x.brand_id = $2),
			(SELECT count(*) FROM ad_accounts x WHERE x.workspace_id = $1 AND x.status = 'active' AND x.brand_id = $2 AND x.connector = 'google_ads'),
			(SELECT count(*) FROM ad_entities e JOIN ad_accounts x ON x.id = e.ad_account_id
				WHERE e.workspace_id = $1 AND x.status = 'active' AND x.connector = 'google_ads' AND COALESCE(e.brand_id, x.brand_id) = $2
				  AND e.level = 'ad' AND e.status = 'active' AND e.details->>'templated' = 'false'),
			(SELECT count(*) FROM ad_conversion_rules u JOIN ad_accounts x ON x.id = u.ad_account_id WHERE u.workspace_id = $1 AND u.enabled AND x.brand_id = $2),
			EXISTS (SELECT 1 FROM ad_brand_economics y WHERE y.brand_id = $2 AND y.target_cost_per_payer_micros IS NOT NULL),
			EXISTS (SELECT 1 FROM ad_settings t WHERE t.workspace_id = $1 AND t.brand_id = $2),
			COALESCE((SELECT c.knowledge_read_at IS NOT NULL FROM brand_context c WHERE c.brand_id = $2), false),
			COALESCE((SELECT c.knowledge_error FROM brand_context c WHERE c.brand_id = $2), ''),
			EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'brand_context' AND column_name = $4),
			EXISTS (SELECT 1 FROM brands b WHERE b.workspace_id = $1 AND b.id = $2)`, ws, brand, payers, knowledgeReviewColumn).
		Scan(&c.siteKeys, &c.usedKeys, &c.signups, &c.payments, &c.paymentSources, &c.accounts, &c.googleAccounts, &c.untemplated, &c.rules,
			&c.hasTarget, &c.hasConsent, &c.knowledgeRead, &c.knowledgeError, &c.reviewColumn, new(bool))
	if err != nil {
		return v, err
	}
	var isBrand bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM brands WHERE workspace_id = $1 AND id = $2)`, ws, brand).Scan(&isBrand); err != nil {
		return v, err
	}
	if !isBrand {
		return v, paidads.ErrNoBrand
	}
	if c.reviewColumn {
		// Coded against the column another workstream adds; read only when
		// it exists, so this compiles and runs either way.
		err := s.pool.QueryRow(ctx, `SELECT `+knowledgeReviewColumn+` FROM brand_context WHERE brand_id = $1`, brand).Scan(&c.reviewedAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return v, err
		}
	}
	add := func(step, title string, done bool, detail string, tool, p, who string) {
		v.Steps = append(v.Steps, adSetupStep{Step: step, Title: title, Done: done, Detail: detail, Fix: statusFix{Tool: tool, Page: page(p), Who: who}})
		if done {
			v.Done++
		}
		v.Total++
	}
	switch {
	case c.usedKeys > 0:
		add("site_script", "Put the website script on the site", true, "The website script sends visits, so paid clicks land with their ids.", "get_brand_setup", "setup", "person")
	case c.siteKeys > 0:
		add("site_script", "Put the website script on the site", false, "A site key exists but no visit has arrived yet: add the script to the site, or check it loads.", "get_brand_setup", "setup", "person")
	default:
		add("site_script", "Put the website script on the site", false, "Without the script no click is seen, so nothing can be tied to an ad.", "get_brand_setup", "setup", "person")
	}
	add("events", "Send sign-ups and payments as events", c.signups > 0 || c.payments > 0,
		countOf(c.signups, "sign-up", "sign-ups")+" and "+countOf(c.payments, "payment", "payments")+" in the last 90 days. Send signed_up and payment.succeeded from your product or connect a billing tool.",
		"get_catalog", "ai", "person")
	add("payments", "Connect where payments happen", c.paymentSources > 0 || c.payments > 0,
		map[bool]string{true: countOf(c.paymentSources, "billing connection", "billing connections") + " connected.", false: "Connect Stripe or another billing tool, or send payment.succeeded yourself, so first payments can be counted."}[c.paymentSources > 0 || c.payments > 0],
		"request_connection", "connectors", "person")
	add("ad_account", "Connect an ad account to the brand", c.accounts > 0,
		map[bool]string{true: countOf(c.accounts, "ad account belongs", "ad accounts belong") + " to the brand.", false: "No ad account belongs to this brand yet: connect one, or set an account's brand."}[c.accounts > 0],
		"update_ad_account", "ads", "person")
	add("conversion_feed", "Send conversions back to the network", c.rules > 0,
		map[bool]string{true: countOf(c.rules, "conversion rule sends", "conversion rules send") + " events back.", false: "No conversion rule yet: the network's bidding learns nothing from real sign-ups and payments."}[c.rules > 0],
		"create_ad_conversion_rule", "ads", "agent")
	switch {
	case c.googleAccounts == 0:
		add("tracking_template", "Add the tracking template to Google ads", true, "No Google Ads account on the brand; nothing to add.", "get_ad_tracking_health", "ads", "agent")
	case c.untemplated > 0:
		add("tracking_template", "Add the tracking template to Google ads", false, countOf(c.untemplated, "live ad links", "live ads link")+" without the template, so their clicks can't be tied to the ad. Fix tracking links adds it.", "get_ad_tracking_health", "ads", "agent")
	default:
		add("tracking_template", "Add the tracking template to Google ads", true, "Every live Google ad carries the template.", "get_ad_tracking_health", "ads", "agent")
	}
	add("economics", "Set what a new customer may cost", c.hasTarget,
		map[bool]string{true: "The target cost per new paying customer is set.", false: "Set the target cost per new paying customer (and per sign-up), the window, payback and margin."}[c.hasTarget],
		"set_ad_economics", "ads", "person")
	switch {
	case !c.reviewColumn:
		add("knowledge_sheet", "Review what Outflare thinks you sell", false, "coming", "get_brand_context", "company", "person")
	case c.knowledgeError != "":
		add("knowledge_sheet", "Review what Outflare thinks you sell", false, "Reading the website failed ("+c.knowledgeError+"); fix it and read again, then review the sheet.", "get_brand_context", "company", "person")
	case !c.knowledgeRead:
		add("knowledge_sheet", "Review what Outflare thinks you sell", false, "The website hasn't been read yet.", "get_brand_context", "company", "person")
	case c.reviewedAt == nil:
		add("knowledge_sheet", "Review what Outflare thinks you sell", false, "The sheet is read but nobody has said it looks right.", "get_brand_context", "company", "person")
	default:
		add("knowledge_sheet", "Review what Outflare thinks you sell", true, "Reviewed on "+c.reviewedAt.UTC().Format("2 Jan 2006")+".", "get_brand_context", "company", "person")
	}
	add("consent", "Choose who conversions are sent for", c.hasConsent,
		map[bool]string{true: "The consent setting is chosen.", false: "Choose opt_out (everyone unless they said no) or opt_in (only people who said yes)."}[c.hasConsent],
		"set_ad_settings", "ads", "person")
	v.Ready = v.Done == v.Total
	return v, nil
}
