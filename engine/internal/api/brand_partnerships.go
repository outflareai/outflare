package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/quota"
)

// Partnerships: a brand's partnership pipelines (SEO links, sponsorships,
// integrations, affiliates, comparison sites) are CRM record types that a
// setup kit adds (playbooks/kits/partnerships-*.yaml), each with its own
// stages and fields, a stage field and a company field pointing at the
// partner's company record. Presence shows each as a board. Adding a
// prospect takes only a website: it finds or adds the company, then adds
// the partnership at its first stage, or returns the one already there,
// so the same partner is never in one pipeline twice.

func (s *Server) routesPartnerships(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/brands/{brand}/partnerships", s.onBrand(auth.ScopeCustomersManage, s.addPartnership))
}

func (s *Server) addPartnership(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		Type       string                     `json:"type"`
		Website    string                     `json:"website"`
		Name       string                     `json:"name"`
		Stage      string                     `json:"stage"`
		Attributes map[string]json.RawMessage `json:"attributes"`
	}
	if !decode(w, r, &req) {
		return
	}
	domain := customers.CompanyDomain(strings.TrimSpace(req.Website))
	switch {
	case req.Type == "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "type is required: a partnership record type, such as link_partnership")
		return
	case domain == "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "website is required: the partner's site, such as acme.example")
		return
	case len(req.Name) > 200:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is at most 200 characters")
		return
	}
	for _, k := range []string{"company", "stage"} {
		if _, ok := req.Attributes[k]; ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", k+" is set from website and stage, not attributes")
			return
		}
	}
	brand, err := brandWrite(ctx, s.pool, a, &a.Brand)
	if s.allocError(w, r, err) {
		return
	}
	if !s.allowQuota(w, r, a.WorkspaceID, quota.Records, 2) {
		return
	}
	var view recordView
	created := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, deref(brand), false)
		if err != nil {
			return err
		}
		t := c.recordType(req.Type)
		if t == nil || t.Archived {
			return errInvalid("type " + req.Type + " is not an active record type: set up a partnership pipeline first (POST /v1/brands/{brand}/kits/partnerships-seo/apply and the other partnerships kits)")
		}
		company, stage := t.field("company"), t.field("stage")
		if company == nil || company.ValueType != "record_reference" || deref(company.TargetType) != "account" || stage == nil || stage.ValueType != "status" {
			return errInvalid(t.Name + " is not a partnership pipeline: it needs a company field pointing at companies and a stage status field")
		}
		set := c.optionSet(deref(stage.OptionSet))
		first := ""
		if set != nil {
			for _, o := range set.Options {
				if !o.Archived {
					first = o.Key
					break
				}
			}
		}
		if req.Stage == "" {
			req.Stage = first
		} else if set == nil || !slices.ContainsFunc(set.Options, func(o catalogOption) bool { return o.Key == req.Stage && !o.Archived }) {
			return errInvalid("stage " + req.Stage + " is not one of " + t.Name + "'s stages (GET .../catalog/option-sets lists them)")
		}
		ids, err := customers.EnsureCompanies(ctx, tx, a.WorkspaceID, brand, "partnerships", []customers.Company{{Domain: domain, Name: domain}})
		if err != nil {
			return err
		}
		companyID := ids[domain]
		if companyID == "" {
			return errInvalid("website " + req.Website + " can't be a company")
		}
		// The partner already in this pipeline comes back as it is.
		var id string
		err = tx.QueryRow(ctx, `SELECT id::text FROM records WHERE workspace_id = $1 AND brand_id = $2 AND record_type = $3
			AND attributes->>'company' = $4 AND archived_at IS NULL AND merged_into IS NULL ORDER BY created_at LIMIT 1`,
			a.WorkspaceID, deref(brand), t.Key, companyID).Scan(&id)
		if err == nil {
			view, err = loadRecord(ctx, tx, a, id)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = domain
		}
		if err := tx.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, brand_id, name, origin, acquired_via, created_by)
			VALUES ($1, $2, $3, $4, 'manual', 'partnerships', $5) RETURNING id::text`,
			a.WorkspaceID, t.Key, brand, name, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		attrs := map[string]json.RawMessage{}
		for k, v := range req.Attributes {
			attrs[k] = v
		}
		attrs["company"], _ = json.Marshal(companyID)
		if req.Stage != "" {
			attrs["stage"], _ = json.Marshal(req.Stage)
		}
		rec := recordView{ID: id, Attributes: map[string]any{}, AttributeSources: map[string]string{}}
		if _, err := applyAttributes(ctx, tx, c, t, a, &rec, attrs, "manual"); err != nil {
			return err
		}
		// The site's strength, when the pipeline keeps it and the
		// workspace's SEO tool has read it: from backlinks to the brand or
		// competitor scans. Written as enrichment, so a value someone
		// types wins.
		if f := t.field("domain_rating"); f != nil && f.ValueType == "number" {
			if _, given := req.Attributes["domain_rating"]; !given {
				dr, err := partnershipDomainRating(ctx, tx, a.WorkspaceID, deref(brand), domain)
				if err != nil {
					return err
				}
				if dr != nil {
					raw, _ := json.Marshal(*dr)
					if _, err := applyAttributes(ctx, tx, c, t, a, &rec, map[string]json.RawMessage{"domain_rating": raw}, "enrichment"); err != nil {
						return err
					}
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3 WHERE id = $1`, id, rec.Attributes, rec.AttributeSources); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "record.created", "record", id,
			map[string]any{"type": t.Key, "origin": "manual", "identities": 0, "via": "partnerships"}); err != nil {
			return err
		}
		if err := enqueuePartnershipSync(ctx, tx, a.WorkspaceID, id, "created"); err != nil {
			return err
		}
		created = true
		view, err = loadRecord(ctx, tx, a, id)
		view.Skipped = rec.Skipped
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"created": created, "record": view})
}

// partnershipDomainRating is a site's domain rating (0 to 100) as the
// workspace's SEO tool last read it for the brand: the highest of its
// backlinks' and its competitor placements' ratings, or nil when none.
func partnershipDomainRating(ctx context.Context, tx pgx.Tx, ws, brand, domain string) (*float64, error) {
	var dr *float64
	err := tx.QueryRow(ctx, `SELECT round(GREATEST(
			(SELECT max(source_rating) FROM backlinks WHERE workspace_id = $1 AND brand_id = $2 AND source_domain IN ($3, 'www.' || $3)),
			(SELECT max(domain_rating) FROM coverage_competitor_placements WHERE workspace_id = $1 AND brand_id = $2 AND host IN ($3, 'www.' || $3)))::numeric)::float8`,
		ws, brand, domain).Scan(&dr)
	return dr, err
}
