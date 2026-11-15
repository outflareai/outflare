package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/inbox"
)

// Renewal deals (docs/revenue.md, "Renewal deals"): a journey's
// open_renewal_deal step opens one deal for each renewal a company has
// coming up, so the renewal is worked in the pipeline like any deal.

// renewalDealsPerRun is the most deals one step opens for one company.
const renewalDealsPerRun = 10

// OpenRenewalDeals is the engine's journeys.DealMover for
// open_renewal_deal: for the company, and for each record that names it
// as its company (an agreement), whose "Renews on" date is within the
// next days, it opens a deal at stage (the first stage when empty) with
// the renewal's value, closing on the renewal date. Where deals have an
// owner field it is the company's owner; the forecast counts the deal
// for the company's owner either way. A renewal that has had a deal for that date gets no
// second one, whatever became of the first.
func (s *Server) OpenRenewalDeals(ctx context.Context, tx pgx.Tx, ws, recordID, stage string, within int, ref string) (string, string, error) {
	a := &access{principal: principal{ActorType: "system"}, WorkspaceID: ws}
	company, err := lockRecord(ctx, tx, a, recordID)
	var conflict recordConflict
	if errors.Is(err, errNotFound) || errors.As(err, &conflict) {
		return "skipped", "the company is gone", nil
	}
	if err != nil {
		return "", "", err
	}
	if company.Type != "account" || company.BrandID == nil {
		return "skipped", "renewal deals open for companies", nil
	}
	brand := *company.BrandID
	c, err := loadCatalog(ctx, tx, ws, brand, false)
	if err != nil {
		return "", "", err
	}
	t := c.recordType("deal")
	if t == nil || t.Archived {
		return "failed", "the brand has no deal type to open a renewal deal in", nil
	}
	if stage == "" {
		if f := t.field("stage"); f != nil && f.OptionSet != nil {
			if set := c.optionSet(*f.OptionSet); set != nil {
				for _, o := range set.Options {
					if !o.Archived {
						stage = o.Key
						break
					}
				}
			}
		}
	}
	shape, err := checkDealRule(c, inbox.DealRule{Type: "deal", Field: "stage", Stage: stage})
	if err != nil {
		// The catalog changed since the journey was published.
		return "failed", err.Error(), nil
	}
	if shape.acct == "" {
		return "failed", "deals have no field that points at a company", nil
	}
	var currency string
	if err := tx.QueryRow(ctx, `SELECT currency FROM brands WHERE id = $1 AND workspace_id = $2`, brand, ws).Scan(&currency); err != nil {
		return "", "", err
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	rows, err := tx.Query(ctx, `SELECT r.id::text, r.record_type, r.name, r.attributes->>'renews_on',
			CASE WHEN jsonb_typeof(r.attributes->'value'->'amount_minor') = 'number' THEN (r.attributes->'value'->>'amount_minor')::numeric::bigint END,
			NULLIF(r.attributes->'value'->>'currency', '')
		FROM records r
		WHERE r.workspace_id = $1 AND r.brand_id = $2 AND r.deleted_at IS NULL AND r.merged_into IS NULL AND r.archived_at IS NULL
			AND (r.id = $3 OR (r.record_type NOT IN ('person', 'account', 'deal') AND r.attributes->>'company' = $3::text))
			AND r.attributes->>'renews_on' ~ '^\d{4}-\d{2}-\d{2}$'
			AND r.attributes->>'renews_on' >= $4 AND r.attributes->>'renews_on' <= $5
			AND COALESCE(r.attributes->>'stage', '') NOT IN ('ended', 'cancelled', 'canceled', 'lost')
			AND NOT EXISTS (SELECT 1 FROM deals d WHERE d.workspace_id = r.workspace_id AND d.renewal_of = r.id
				AND d.renewal_on = (r.attributes->>'renews_on')::date)
		ORDER BY r.attributes->>'renews_on', r.id LIMIT $6`,
		ws, brand, company.ID, today.Format(time.DateOnly), today.AddDate(0, 0, within).Format(time.DateOnly), renewalDealsPerRun)
	if err != nil {
		return "", "", err
	}
	type renewal struct {
		id, kind, name, on string
		value              *int64
		code               *string
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (renewal, error) {
		var v renewal
		return v, row.Scan(&v.id, &v.kind, &v.name, &v.on, &v.value, &v.code)
	})
	if err != nil {
		return "", "", err
	}
	if len(list) == 0 {
		return "skipped", fmt.Sprintf("nothing of theirs renews in the next %d days without a deal already", within), nil
	}
	owner, _ := company.Attributes["owner"].(string)
	var opened []string
	for _, v := range list {
		name := "Renewal: " + company.Name
		if v.id != company.ID && v.name != "" {
			name += ", " + v.name
		}
		if len(name) > 300 {
			name = name[:300]
		}
		attrs := map[string]json.RawMessage{}
		attrs["stage"], _ = json.Marshal(stage)
		if f := t.field(shape.acct); f != nil && f.Multiple {
			attrs[shape.acct], _ = json.Marshal([]string{company.ID})
		} else {
			attrs[shape.acct], _ = json.Marshal(company.ID)
		}
		if f := t.field("close_date"); f != nil && !f.Archived && f.ValueType == "date" {
			attrs["close_date"], _ = json.Marshal(v.on)
		}
		if f := t.field("owner"); f != nil && !f.Archived && owner != "" {
			attrs["owner"], _ = json.Marshal(owner)
		}
		// What it is worth: the record's own value, or for a company
		// with none a year of what it pays a month.
		amount, code := v.value, currency
		if amount != nil && *amount > 0 {
			if v.code != nil {
				code = strings.ToUpper(*v.code)
			}
		} else {
			amount = nil
			if v.id == company.ID {
				var mrr int64
				err := tx.QueryRow(ctx, `SELECT mrr_minor FROM revenue_customers WHERE record_id = $1 AND workspace_id = $2 AND brand_id = $3 AND mrr_minor > 0`,
					company.ID, ws, brand).Scan(&mrr)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return "", "", err
				}
				if mrr > 0 {
					year := mrr * 12
					amount = &year
				}
			}
		}
		if f := t.field("value"); f != nil && !f.Archived && f.ValueType == "currency" && amount != nil {
			attrs["value"], _ = json.Marshal(map[string]any{"amount_minor": *amount, "currency": code})
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, brand_id, name, origin, acquired_via)
			VALUES ($1, 'deal', $2, $3, 'first_party', 'renewal') RETURNING id::text`, ws, brand, name).Scan(&id); err != nil {
			return "", "", err
		}
		rec := recordView{ID: id, Attributes: map[string]any{}, AttributeSources: map[string]string{}}
		if _, err := applyAttributes(ctx, tx, c, t, a, &rec, attrs, replyDealSource); err != nil {
			var bad errInvalid
			if errors.As(err, &bad) {
				return "failed", err.Error(), nil
			}
			return "", "", err
		}
		if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3 WHERE id = $1`, id, rec.Attributes, rec.AttributeSources); err != nil {
			return "", "", err
		}
		// The deal's row is made with its record; it says what renews.
		tag, err := tx.Exec(ctx, `UPDATE deals SET renewal_of = $2, renewal_on = $3::date, updated_at = now() WHERE id = $1 AND workspace_id = $4`, id, v.id, v.on, ws)
		if err != nil {
			return "", "", err
		}
		if tag.RowsAffected() == 0 {
			return "failed", "the deal could not be opened: the brand's deal stages don't include " + stage, nil
		}
		if err := record(ctx, tx, &a.principal, ws, "record.created", "record", id,
			map[string]any{"type": "deal", "origin": "first_party", "via": "journey", "identities": 0, "renewal_of": v.id, "run_id": ref}); err != nil {
			return "", "", err
		}
		opened = append(opened, v.on)
	}
	if len(opened) == 1 {
		return "done", "opened a renewal deal for " + opened[0], nil
	}
	return "done", fmt.Sprintf("opened %d renewal deals, the first for %s", len(opened), opened[0]), nil
}
