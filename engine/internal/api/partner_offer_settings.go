package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
)

// Partnerships (docs/partnerships.md): a brand's saved offer templates, and
// its rule for a paid link that is removed after payment. The rule is copied
// into each offer when it is sent, so an agreement keeps the rule it was
// made under.

type offerTemplate struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Config    proposalConfig `json:"config"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// linkRule is what happens when a paid link is removed after payment.
type linkRule struct {
	Removal         string `json:"removal"`
	RestoreDays     int    `json:"restore_days"`
	GuaranteeMonths int    `json:"guarantee_months"`
}

var linkRemovals = map[string]bool{"restore_or_refund": true, "restore_or_refund_forever": true, "shrinking_refund": true, "warn_only": true}

func defaultLinkRule() linkRule {
	return linkRule{Removal: "restore_or_refund", RestoreDays: 14, GuaranteeMonths: 12}
}

func (s *Server) routesPartnerOfferSettings(mux *http.ServeMux) {
	read, manage := auth.ScopePartnersRead, auth.ScopePartnersManage
	mux.HandleFunc("GET /v1/brands/{brand}/partner-link-rule", s.onBrand(read, s.getPartnerLinkRule))
	mux.HandleFunc("PUT /v1/brands/{brand}/partner-link-rule", s.onBrand(manage, s.putPartnerLinkRule))
	mux.HandleFunc("GET /v1/brands/{brand}/partner-offer-templates", s.onBrand(read, s.listOfferTemplates))
	mux.HandleFunc("POST /v1/brands/{brand}/partner-offer-templates", s.onBrand(manage, s.saveOfferTemplate))
	mux.HandleFunc("PUT /v1/brands/{brand}/partner-offer-templates/{template}", s.onBrand(manage, s.saveOfferTemplate))
	mux.HandleFunc("DELETE /v1/brands/{brand}/partner-offer-templates/{template}", s.onBrand(manage, s.deleteOfferTemplate))
}

// brandLinkRule is the brand's rule, or the default when it has none.
func brandLinkRule(ctx context.Context, q pgx.Tx, ws, brand string) (linkRule, error) {
	r := defaultLinkRule()
	err := q.QueryRow(ctx, `SELECT removal,restore_days,guarantee_months FROM partner_link_rules WHERE workspace_id=$1 AND brand_id=$2`, ws, brand).Scan(&r.Removal, &r.RestoreDays, &r.GuaranteeMonths)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultLinkRule(), nil
	}
	return r, err
}

func (s *Server) getPartnerLinkRule(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var out linkRule
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) (err error) {
		out, err = brandLinkRule(r.Context(), tx, a.WorkspaceID, a.Brand)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) putPartnerLinkRule(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in linkRule
	if !decode(w, r, &in) {
		return
	}
	if !linkRemovals[in.Removal] || in.RestoreDays < 1 || in.RestoreDays > 90 || in.GuaranteeMonths < 1 || in.GuaranteeMonths > 120 {
		s.allocError(w, r, errInvalid("removal is restore_or_refund, restore_or_refund_forever, shrinking_refund or warn_only; restore_days 1 to 90; guarantee_months 1 to 120"))
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO partner_link_rules(brand_id,workspace_id,removal,restore_days,guarantee_months) VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (brand_id) DO UPDATE SET removal=EXCLUDED.removal,restore_days=EXCLUDED.restore_days,guarantee_months=EXCLUDED.guarantee_months,updated_at=now()
			WHERE partner_link_rules.workspace_id=EXCLUDED.workspace_id`, a.Brand, a.WorkspaceID, in.Removal, in.RestoreDays, in.GuaranteeMonths); err != nil {
			return err
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_link_rule.updated", "brand", a.Brand, map[string]any{"removal": in.Removal, "restore_days": in.RestoreDays, "guarantee_months": in.GuaranteeMonths})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, in)
}

const offerTemplateSelect = `SELECT id::text,name,config,created_at,updated_at FROM partner_offer_templates WHERE workspace_id=$1 AND brand_id=$2`

func scanOfferTemplate(row pgx.Row) (offerTemplate, error) {
	var t offerTemplate
	err := row.Scan(&t.ID, &t.Name, &t.Config, &t.CreatedAt, &t.UpdatedAt)
	if t.Config.Milestones == nil {
		t.Config.Milestones = []proposalMilestone{}
	}
	if t.Config.Deliverables == nil {
		t.Config.Deliverables = []proposalDeliverable{}
	}
	return t, err
}

func (s *Server) listOfferTemplates(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), offerTemplateSelect+` ORDER BY lower(name) LIMIT 200`, a.WorkspaceID, a.Brand)
	if s.allocError(w, r, err) {
		return
	}
	defer rows.Close()
	out := []offerTemplate{}
	for rows.Next() {
		t, err := scanOfferTemplate(rows)
		if s.allocError(w, r, err) {
			return
		}
		out = append(out, t)
	}
	if s.allocError(w, r, rows.Err()) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// saveOfferTemplate creates a template (POST) or replaces one (PUT). A
// template holds the same terms as an offer, without its recipient.
func (s *Server) saveOfferTemplate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Name   string         `json:"name"`
		Config proposalConfig `json:"config"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 120 {
		s.allocError(w, r, errInvalid("a template needs a name of at most 120 characters"))
		return
	}
	if err := in.Config.check(); err != nil {
		s.allocError(w, r, err)
		return
	}
	in.Config.Removal = nil
	raw, _ := json.Marshal(in.Config)
	ctx := r.Context()
	id := r.PathValue("template")
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		action := "partner_offer_template.updated"
		if id == "" {
			action = "partner_offer_template.created"
			if err := tx.QueryRow(ctx, `INSERT INTO partner_offer_templates(workspace_id,brand_id,name,config) VALUES($1,$2,$3,$4) RETURNING id::text`, a.WorkspaceID, a.Brand, in.Name, raw).Scan(&id); err != nil {
				return err
			}
		} else {
			if !validUUID(id) {
				return errNotFound
			}
			tag, err := tx.Exec(ctx, `UPDATE partner_offer_templates SET name=$4,config=$5,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, id, in.Name, raw)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errNotFound
			}
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, action, "partner_offer_template", id, nil)
	})
	if s.allocError(w, r, err) {
		return
	}
	t, err := scanOfferTemplate(s.pool.QueryRow(ctx, offerTemplateSelect+` AND id=$3`, a.WorkspaceID, a.Brand, id))
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) deleteOfferTemplate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("template")
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if !validUUID(id) {
			return errNotFound
		}
		tag, err := tx.Exec(ctx, `DELETE FROM partner_offer_templates WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_offer_template.deleted", "partner_offer_template", id, nil)
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}
