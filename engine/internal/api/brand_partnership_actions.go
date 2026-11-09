package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/coverage"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/outcomes"
)

// Partnership stage actions: when a partnership reaches the stage where
// it shows up somewhere, Presence keeps it, so the appearance is checked
// over time like any other placement. A partnership change queues
// partnerships.sync for its record, and the daily coverage run queues the
// ones a change didn't (a bulk edit, an import). The job reconciles: it
// does what the record's stage calls for and hasn't been done, and each
// action happens once per partnership (a discovery receipt marks it), so
// moving a card back and forth adds nothing twice.
//
//   - link_partnership at live, with their_page: a placement of that page
//     (kind from link_type, paid when it had a price), checked now.
//   - integration_partnership at live, with listing_url: an integration
//     placement of the listing, checked now.
//   - comparison_listing at listed or ranked, with comparison_url: a
//     comparison placement, checked now.
//   - sponsorship at booked or later: a proposed agreement with the
//     publisher (finance approves it); at ran or reported: a placement
//     under that agreement, dated the run, with no page to check (a
//     newsletter or podcast can't be checked by reading a page).
//
// A partner whose page isn't filled in yet gets its placement once it is.

const jobPartnershipSync = "partnerships.sync"

// partnershipTypes are the record types the partnerships kits add.
var partnershipTypes = []string{"link_partnership", "sponsorship", "integration_partnership", "affiliate_partnership", "comparison_listing"}

func isPartnershipType(t string) bool { return slices.Contains(partnershipTypes, t) }

// enqueuePartnershipSync queues the stage actions of a partnership
// record, once per key.
func enqueuePartnershipSync(ctx context.Context, tx pgx.Tx, ws, record, key string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnershipSync, WorkspaceID: &ws, Payload: map[string]string{"record_id": record},
		IdempotencyKey: "partnerships.sync:" + record + ":" + key, MaxAttempts: 5})
	return err
}

func (s *Server) partnershipSyncJob(ctx context.Context, j jobs.Job) error {
	var req struct {
		Record string `json:"record_id"`
	}
	if err := json.Unmarshal(j.Payload, &req); err != nil || j.WorkspaceID == nil || !validUUID(req.Record) {
		return fmt.Errorf("%w: invalid partnership sync job", jobs.ErrPermanent)
	}
	ws := *j.WorkspaceID
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var brand, typ, name string
		var attrs map[string]any
		err := tx.QueryRow(ctx, `SELECT brand_id::text, record_type, name, attributes FROM records
			WHERE workspace_id = $1 AND id = $2 AND brand_id IS NOT NULL AND archived_at IS NULL AND merged_into IS NULL FOR UPDATE`, ws, req.Record).Scan(&brand, &typ, &name, &attrs)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !isPartnershipType(typ) {
			return nil
		}
		str := func(k string) string { v, _ := attrs[k].(string); return strings.TrimSpace(v) }
		stage := str("stage")
		a := &access{WorkspaceID: ws, Brand: brand}
		done := func(action string) (bool, error) {
			var ok bool
			err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM coverage_discovery_receipts WHERE workspace_id = $1 AND brand_id = $2 AND source = 'partnerships' AND source_id = $3)`,
				ws, brand, req.Record+":"+action).Scan(&ok)
			return ok, err
		}
		mark := func(action string) error {
			_, err := tx.Exec(ctx, `INSERT INTO coverage_discovery_receipts (workspace_id, brand_id, source, source_id) VALUES ($1, $2, 'partnerships', $3) ON CONFLICT DO NOTHING`,
				ws, brand, req.Record+":"+action)
			return err
		}
		// A page placement, recorded and then checked for real.
		page := func(pageURL, kind, arrangement, target string) error {
			if pageURL == "" {
				return nil
			}
			if ok, err := done("placement"); ok || err != nil {
				return err
			}
			id, _, err := coverage.Record(ctx, tx, ws, brand, coverage.Placement{URL: pageURL, Title: name, Kind: kind, Arrangement: arrangement,
				TargetURL: target, ExpectedLink: target != "", Source: "partnerships", SourceID: req.Record + ":placement",
				Evidence: map[string]any{"partnership_id": req.Record, "partnership_type": typ, "stage": stage, "reported_by": "partnership_stage"}})
			if errors.Is(err, coverage.ErrNoURL) {
				return nil
			}
			if err != nil {
				return err
			}
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobCoverageCheck, WorkspaceID: &ws, Payload: map[string]string{"placement_id": id, "brand_id": brand},
				IdempotencyKey: "coverage.check:" + id + ":partnership", MaxAttempts: 3}); err != nil {
				return err
			}
			return recordWithProps(ctx, tx, nil, ws, "partnership.placement_added", "record", req.Record,
				map[string]any{"brand_id": brand, "type": typ, "stage": stage, "placement_id": id, "kind": kind},
				map[string]any{"brand_id": brand, "type": typ, "stage": stage, "kind": kind})
		}
		switch typ {
		case "link_partnership":
			if stage == "live" {
				kind := map[string]string{"resource_page": "resource"}[str("link_type")]
				if kind == "" {
					kind = "blog"
				}
				arrangement := "earned"
				if p, ok := attrs["price"].(map[string]any); ok {
					if n, _ := p["amount_minor"].(float64); n > 0 {
						arrangement = "paid"
					}
				}
				return page(str("their_page"), kind, arrangement, str("target_page"))
			}
		case "integration_partnership":
			if stage == "live" {
				return page(str("listing_url"), "integration", "unknown", "")
			}
		case "comparison_listing":
			if stage == "listed" || stage == "ranked" {
				arrangement := map[string]string{"free": "earned", "paid": "paid", "affiliate": "affiliate"}[str("listing_type")]
				if arrangement == "" {
					arrangement = "unknown"
				}
				return page(str("comparison_url"), "comparison", arrangement, "")
			}
		case "sponsorship":
			return s.sponsorshipActions(ctx, tx, a, req.Record, name, stage, attrs, done, mark)
		}
		return nil
	})
}

// sponsorshipActions records a booked sponsorship's agreement and, once
// it ran, its placement.
func (s *Server) sponsorshipActions(ctx context.Context, tx pgx.Tx, a *access, record, name, stage string, attrs map[string]any,
	done func(string) (bool, error), mark func(string) error) error {
	booked := slices.Contains([]string{"booked", "creative_sent", "ran", "reported"}, stage)
	ran := stage == "ran" || stage == "reported"
	if !booked {
		return nil
	}
	ws, brand := a.WorkspaceID, a.Brand
	company, _ := attrs["company"].(string)
	var domain string
	if company != "" {
		if err := tx.QueryRow(ctx, `SELECT value FROM record_identities WHERE workspace_id = $1 AND record_id = $2 AND namespace = 'domain' AND valid_to IS NULL ORDER BY verified DESC, created_at LIMIT 1`,
			ws, company).Scan(&domain); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	if domain == "" {
		return nil
	}
	publisher, err := coveragePublisherFor(ctx, tx, a, domain)
	if err != nil {
		var invalid errInvalid
		if errors.As(err, &invalid) {
			return nil
		}
		return err
	}
	date := func(k string) *string {
		if v, _ := attrs[k].(string); v != "" {
			return &v
		}
		return nil
	}
	var amount float64
	currency := "USD"
	if p, ok := attrs["price"].(map[string]any); ok {
		amount, _ = p["amount_minor"].(float64)
		if c, _ := p["currency"].(string); c != "" {
			currency = c
		}
	}
	// The agreement this partnership booked before, if any: its history
	// names the partnership.
	var agreement string
	if err := tx.QueryRow(ctx, `SELECT h.resource_id::text FROM coverage_history h JOIN coverage_agreements g ON g.id = h.resource_id::uuid
		WHERE h.workspace_id = $1 AND h.brand_id = $2 AND h.resource = 'agreements' AND h.action = 'created' AND h.detail->>'partnership_id' = $3
		ORDER BY h.created_at LIMIT 1`, ws, brand, record).Scan(&agreement); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if ok, err := done("agreement"); err != nil {
		return err
	} else if !ok {
		deliverables, _ := attrs["deliverables"].(string)
		terms := strings.TrimSpace(deliverables)
		if err := tx.QueryRow(ctx, `INSERT INTO coverage_agreements (workspace_id, brand_id, publisher_id, title, currency, amount_minor, status, starts_on, ends_on, terms)
			VALUES ($1, $2, $3, $4, $5, $6, 'proposed', $7::date, $8::date, $9) RETURNING id::text`,
			ws, brand, publisher, "Sponsorship: "+name, currency, int64(amount), date("run_on"), date("run_until"), terms).Scan(&agreement); err != nil {
			return err
		}
		if err := coverageHistory(ctx, tx, a, "agreements", agreement, "created", map[string]any{"partnership_id": record, "stage": stage}); err != nil {
			return err
		}
		if err := mark("agreement"); err != nil {
			return err
		}
		if err := recordWithProps(ctx, tx, nil, ws, "partnership.agreement_added", "record", record,
			map[string]any{"brand_id": brand, "type": "sponsorship", "stage": stage, "agreement_id": agreement},
			map[string]any{"brand_id": brand, "type": "sponsorship", "stage": stage}); err != nil {
			return err
		}
	}
	if !ran {
		return nil
	}
	if ok, err := done("placement"); ok || err != nil {
		return err
	}
	channel, _ := attrs["channel"].(string)
	kind := map[string]string{"newsletter": "newsletter", "podcast": "podcast", "creator": "influencer", "event": "sponsorship", "display": "display_ad"}[channel]
	if kind == "" {
		kind = "sponsorship"
	}
	published := date("run_on")
	if published == nil {
		today := time.Now().UTC().Format(time.DateOnly)
		published = &today
	}
	var placement string
	if err := tx.QueryRow(ctx, `INSERT INTO coverage_placements (workspace_id, brand_id, publisher_id, title, kind, arrangement, stage, agreement_id, published_on, monitor)
		VALUES ($1, $2, $3, $4, $5, 'paid', 'live', NULLIF($6, '')::uuid, $7::date, false) RETURNING id::text`,
		ws, brand, publisher, name, kind, agreement, published).Scan(&placement); err != nil {
		return err
	}
	if err := coverageHistory(ctx, tx, a, "placements", placement, "recorded", map[string]any{"partnership_id": record, "stage": stage, "source": "partnerships"}); err != nil {
		return err
	}
	if err := mark("placement"); err != nil {
		return err
	}
	return recordWithProps(ctx, tx, nil, ws, "partnership.placement_added", "record", record,
		map[string]any{"brand_id": brand, "type": "sponsorship", "stage": stage, "placement_id": placement, "kind": kind},
		map[string]any{"brand_id": brand, "type": "sponsorship", "stage": stage, "kind": kind})
}

// queuePartnershipSyncs queues the partnerships at a stage with an action
// whose action hasn't happened, for those a change didn't queue (a bulk
// edit, an import). Part of the daily coverage run.
func (s *Server) queuePartnershipSyncs(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.workspace_id::text, r.id::text FROM records r
			WHERE r.record_type = ANY($1) AND r.brand_id IS NOT NULL AND r.archived_at IS NULL AND r.merged_into IS NULL
			AND r.attributes->>'stage' IN ('live', 'listed', 'ranked', 'booked', 'creative_sent', 'ran', 'reported')
			AND NOT EXISTS (SELECT 1 FROM coverage_discovery_receipts d WHERE d.workspace_id = r.workspace_id AND d.brand_id = r.brand_id
				AND d.source = 'partnerships' AND d.source_id = r.id::text || ':placement')
			ORDER BY r.updated_at DESC LIMIT 500`, partnershipTypes)
		if err != nil {
			return err
		}
		type due struct{ ws, id string }
		var list []due
		for rows.Next() {
			var d due
			if err = rows.Scan(&d.ws, &d.id); err != nil {
				rows.Close()
				return err
			}
			list = append(list, d)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		day := time.Now().UTC().Format(time.DateOnly)
		for _, d := range list {
			if err := enqueuePartnershipSync(ctx, tx, d.ws, d.id, "day:"+day); err != nil {
				return err
			}
		}
		return nil
	})
}

// Affiliates join the partner program outside the pipeline: Geonode-style
// products add a partner through the API when someone makes their
// affiliate link in the product. So the pipeline follows the program, and
// sends nothing: a new partner whose customer record is an affiliate
// partnership's contact, or whose work email's domain is its company's,
// moves that partnership to signed_up, their first referred sale moves it
// to active, and each sale or refund updates referred_customers and
// referred_revenue. A partnership already further on (active) stays; one
// marked inactive or declined comes back, since they joined.

const jobPartnershipAffiliate = "partnerships.affiliate"

// affiliateStages are the affiliate pipeline's stages in order.
var affiliateStages = []string{"prospect", "contacted", "interested", "terms_agreed", "signed_up", "active"}

// enqueueAffiliateSync queues bringing a partner's affiliate partnerships
// up to date, once per key.
func enqueueAffiliateSync(ctx context.Context, tx pgx.Tx, ws, partner, key string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnershipAffiliate, WorkspaceID: &ws, Payload: map[string]string{"partner_id": partner},
		IdempotencyKey: "partnerships.affiliate:" + partner + ":" + key, MaxAttempts: 5})
	return err
}

func (s *Server) partnershipAffiliateJob(ctx context.Context, j jobs.Job) error {
	var req struct {
		Partner string `json:"partner_id"`
	}
	if err := json.Unmarshal(j.Payload, &req); err != nil || j.WorkspaceID == nil || !validUUID(req.Partner) {
		return fmt.Errorf("%w: invalid affiliate sync job", jobs.ErrPermanent)
	}
	ws := *j.WorkspaceID
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var person, currency string
		var brand *string
		err := tx.QueryRow(ctx, `SELECT m.record_id::text, p.brand_id::text, p.currency FROM partner_memberships m JOIN partner_programs p ON p.id = m.program_id
			WHERE m.workspace_id = $1 AND m.id = $2 AND m.status IN ('pending', 'active')`, ws, req.Partner).Scan(&person, &brand, &currency)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && brand == nil) {
			return nil
		}
		if err != nil {
			return err
		}
		// The partner's work email domains (never a personal mailbox).
		rows, err := tx.Query(ctx, `SELECT value FROM record_identities WHERE workspace_id = $1 AND record_id = $2 AND namespace = 'email' AND valid_to IS NULL`, ws, person)
		if err != nil {
			return err
		}
		var domains []string
		for rows.Next() {
			var email string
			if err = rows.Scan(&email); err != nil {
				rows.Close()
				return err
			}
			if _, d, ok := strings.Cut(email, "@"); ok && !outcomes.IsFreeMail(d) {
				if d = customers.CompanyDomain(d); d != "" && !slices.Contains(domains, d) {
					domains = append(domains, d)
				}
			}
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		var customersReferred int
		var revenue int64
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT c.referral_id) FILTER (WHERE c.type = 'sale'),
				COALESCE(sum(c.amount_cents) FILTER (WHERE c.type = 'sale'), 0) - COALESCE(sum(c.amount_cents) FILTER (WHERE c.type = 'refund'), 0)
			FROM partner_conversions c JOIN partner_referrals f ON f.id = c.referral_id WHERE c.workspace_id = $1 AND f.membership_id = $2`,
			ws, req.Partner).Scan(&customersReferred, &revenue); err != nil {
			return err
		}
		target := "signed_up"
		if customersReferred > 0 {
			target = "active"
		}
		list, err := tx.Query(ctx, `SELECT r.id::text FROM records r WHERE r.workspace_id = $1 AND r.brand_id = $2 AND r.record_type = 'affiliate_partnership'
			AND r.archived_at IS NULL AND r.merged_into IS NULL AND (r.attributes->>'contact' = $3 OR r.attributes->>'company' IN (
				SELECT i.record_id::text FROM record_identities i WHERE i.workspace_id = $1 AND i.namespace = 'domain' AND i.value = ANY($4) AND i.valid_to IS NULL))
			ORDER BY r.created_at FOR UPDATE`, ws, *brand, person, domains)
		if err != nil {
			return err
		}
		var ids []string
		for list.Next() {
			var id string
			if err = list.Scan(&id); err != nil {
				list.Close()
				return err
			}
			ids = append(ids, id)
		}
		list.Close()
		if err = list.Err(); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		c, err := loadCatalog(ctx, tx, ws, *brand, false)
		if err != nil {
			return err
		}
		t := c.recordType("affiliate_partnership")
		if t == nil {
			return nil
		}
		a := &access{WorkspaceID: ws, Brand: *brand}
		for _, id := range ids {
			var rec recordView
			if err := tx.QueryRow(ctx, `SELECT attributes, attribute_sources FROM records WHERE id = $1`, id).Scan(&rec.Attributes, &rec.AttributeSources); err != nil {
				return err
			}
			rec.ID = id
			if rec.Attributes == nil {
				rec.Attributes = map[string]any{}
			}
			if rec.AttributeSources == nil {
				rec.AttributeSources = map[string]string{}
			}
			was, _ := rec.Attributes["stage"].(string)
			in := map[string]json.RawMessage{}
			set := func(k string, v any) {
				if t.field(k) != nil {
					in[k], _ = json.Marshal(v)
				}
			}
			if i := slices.Index(affiliateStages, was); i < slices.Index(affiliateStages, target) {
				set("stage", target)
			}
			if c, _ := rec.Attributes["contact"].(string); c == "" {
				set("contact", person)
			}
			set("referred_customers", customersReferred)
			set("referred_revenue", map[string]any{"amount_minor": revenue, "currency": currency})
			changed, err := applyAttributes(ctx, tx, c, t, a, &rec, in, "manual")
			if err != nil {
				return err
			}
			if !changed {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now() WHERE id = $1`,
				id, rec.Attributes, rec.AttributeSources); err != nil {
				return err
			}
			stage, _ := rec.Attributes["stage"].(string)
			if err := recordWithProps(ctx, tx, nil, ws, "partnership.affiliate_synced", "record", id,
				map[string]any{"brand_id": *brand, "partner_id": req.Partner, "stage": stage, "was": was, "referred_customers": customersReferred},
				map[string]any{"brand_id": *brand, "stage": stage, "was": was, "referred_customers": customersReferred}); err != nil {
				return err
			}
		}
		return nil
	})
}
