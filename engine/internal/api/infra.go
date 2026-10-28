package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/infra"
	"github.com/outflareai/outflare/engine/internal/leads"
)

func (s *Server) routesInfra(mux *http.ServeMux) {
	read, manage, approve := auth.ScopeInfraRead, auth.ScopeInfraManage, auth.ScopeInfraApprove
	mux.HandleFunc("GET /v1/workspaces/{ws}/infrastructure", s.inWorkspace(read, s.infraOverview))
	mux.HandleFunc("POST /v1/workspaces/{ws}/infrastructure/preview", s.inWorkspace(read, s.previewPlan))
	mux.HandleFunc("POST /v1/workspaces/{ws}/infrastructure/move", s.inWorkspace(manage, s.moveMailboxes))
	mux.HandleFunc("GET /v1/workspaces/{ws}/infra-plans", s.inWorkspace(read, s.listPlans))
	mux.HandleFunc("POST /v1/workspaces/{ws}/infra-plans", s.inWorkspace(manage, s.createPlan))
	mux.HandleFunc("GET /v1/workspaces/{ws}/infra-plans/{id}", s.inWorkspace(read, s.getPlan))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/infra-plans/{id}", s.inWorkspace(manage, s.updatePlan))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/infra-plans/{id}", s.inWorkspace(manage, s.deletePlan))
	mux.HandleFunc("POST /v1/workspaces/{ws}/infra-plans/{id}/approve", s.inWorkspace(approve, s.approvePlan))
	mux.HandleFunc("POST /v1/workspaces/{ws}/infra-plans/{id}/cancel", s.inWorkspace(manage, s.cancelPlan))
	mux.HandleFunc("POST /v1/workspaces/{ws}/infra-plans/{id}/bought", s.inWorkspace(manage, s.markBought))
	mux.HandleFunc("POST /v1/workspaces/{ws}/infra-plans/{id}/refresh", s.inWorkspace(manage, s.refreshPlan))
}

// planInput is what a person or agent sets on a plan. Unset fields keep
// their value (or default, on create).
type planInput struct {
	Name                  *string          `json:"name"`
	ProjectID             optional[string] `json:"project_id"`
	Supplier              *string          `json:"supplier"`
	SupplierIntegrationID optional[string] `json:"supplier_integration_id"`
	Provider              *string          `json:"provider"`
	DomainCount           *int             `json:"domain_count"`
	MailboxesPerDomain    *int             `json:"mailboxes_per_domain"`
	Brand                 *string          `json:"brand"`
	Personas              *[]infra.Persona `json:"personas"`
	Domains               *[]string        `json:"domains"`
	ReadyBy               optional[string] `json:"ready_by"`
	WarmupDays            *int             `json:"warmup_days"`
	MailboxDailyLimit     *int             `json:"mailbox_daily_limit"`
	RestBelow             *int             `json:"rest_below"`
	RetireAfterDays       optional[int]    `json:"retire_after_days"`
	Notes                 *string          `json:"notes"`
}

// planFields is a plan's settings, as stored.
type planFields struct {
	Name                  string          `json:"name"`
	ProjectID             *string         `json:"project_id"`
	Supplier              string          `json:"supplier"`
	SupplierIntegrationID *string         `json:"supplier_integration_id"`
	Provider              string          `json:"provider"`
	DomainCount           int             `json:"domain_count"`
	MailboxesPerDomain    int             `json:"mailboxes_per_domain"`
	Brand                 string          `json:"brand"`
	Personas              []infra.Persona `json:"personas"`
	ReadyBy               *string         `json:"ready_by"`
	WarmupDays            int             `json:"warmup_days"`
	MailboxDailyLimit     int             `json:"mailbox_daily_limit"`
	RestBelow             int             `json:"rest_below"`
	RetireAfterDays       *int            `json:"retire_after_days"`
	Notes                 string          `json:"notes"`
}

func defaultPlan() planFields {
	return planFields{Supplier: infra.SupplierManual, Provider: "google", MailboxesPerDomain: 3, Personas: []infra.Persona{},
		WarmupDays: infra.DefaultWarmupDays, MailboxDailyLimit: 30, RestBelow: 75}
}

// apply validates in and copies it onto f, returning the problem in words.
func (in *planInput) apply(f *planFields) string {
	if in.Name != nil {
		f.Name = strings.TrimSpace(*in.Name)
	}
	if in.ProjectID.Set {
		f.ProjectID = in.ProjectID.Value
	}
	if in.Supplier != nil {
		f.Supplier = *in.Supplier
	}
	if in.SupplierIntegrationID.Set {
		f.SupplierIntegrationID = in.SupplierIntegrationID.Value
	}
	if in.Provider != nil {
		f.Provider = *in.Provider
	}
	if in.DomainCount != nil {
		f.DomainCount = *in.DomainCount
	}
	if in.MailboxesPerDomain != nil {
		f.MailboxesPerDomain = *in.MailboxesPerDomain
	}
	if in.Brand != nil {
		f.Brand = strings.TrimSpace(*in.Brand)
	}
	if in.Personas != nil {
		f.Personas = []infra.Persona{}
		for _, p := range *in.Personas {
			p.FirstName, p.LastName = strings.TrimSpace(p.FirstName), strings.TrimSpace(p.LastName)
			if p.FirstName != "" {
				f.Personas = append(f.Personas, p)
			}
		}
	}
	if in.ReadyBy.Set {
		f.ReadyBy = in.ReadyBy.Value
	}
	if in.WarmupDays != nil {
		f.WarmupDays = *in.WarmupDays
	}
	if in.MailboxDailyLimit != nil {
		f.MailboxDailyLimit = *in.MailboxDailyLimit
	}
	if in.RestBelow != nil {
		f.RestBelow = *in.RestBelow
	}
	if in.RetireAfterDays.Set {
		f.RetireAfterDays = in.RetireAfterDays.Value
	}
	if in.Notes != nil {
		f.Notes = *in.Notes
	}
	if in.Domains != nil && in.DomainCount == nil && len(*in.Domains) > f.DomainCount {
		f.DomainCount = len(*in.Domains)
	}
	switch {
	case f.Name == "" || len(f.Name) > 200:
		return "name is required, at most 200 characters"
	case f.ProjectID != nil && !validUUID(*f.ProjectID):
		return "project not found"
	case !slices.Contains(infra.Suppliers(), f.Supplier):
		return "supplier must be one of " + strings.Join(infra.Suppliers(), ", ")
	case f.SupplierIntegrationID != nil && !validUUID(*f.SupplierIntegrationID):
		return "supplier_integration_id must be a UUID"
	case f.Provider != "google" && f.Provider != "microsoft":
		return "provider must be google or microsoft"
	case f.DomainCount < 1 || f.DomainCount > infra.MaxDomains:
		return "domain_count must be 1 to 200"
	case f.MailboxesPerDomain < 1 || f.MailboxesPerDomain > infra.MaxMailboxesPerDomain:
		return "mailboxes_per_domain must be 1 to 10"
	case len(f.Brand) > 60:
		return "brand must be at most 60 characters"
	case len(f.Personas) > 20:
		return "at most 20 personas"
	case f.WarmupDays < infra.MinWarmupDays || f.WarmupDays > infra.MaxWarmupDays:
		return "warmup_days must be 7 to 60"
	case f.MailboxDailyLimit < 1 || f.MailboxDailyLimit > 500:
		return "mailbox_daily_limit must be 1 to 500"
	case f.RestBelow < 0 || f.RestBelow > 100:
		return "rest_below must be 0 to 100 (percent)"
	case f.RetireAfterDays != nil && (*f.RetireAfterDays < 3 || *f.RetireAfterDays > 120):
		return "retire_after_days must be 3 to 120, or null to never retire on its own"
	case len(f.Notes) > 4000:
		return "notes must be at most 4000 characters"
	}
	if f.ReadyBy != nil {
		if _, err := time.Parse(time.DateOnly, *f.ReadyBy); err != nil {
			return "ready_by must be a date such as 2026-11-14"
		}
	}
	for _, p := range f.Personas {
		if len(p.FirstName) > 40 || len(p.LastName) > 40 {
			return "persona names must be at most 40 characters"
		}
	}
	if in.Domains != nil {
		if len(*in.Domains) > f.DomainCount {
			return "more domains than domain_count"
		}
		for i, d := range *in.Domains {
			kind, v, err := leads.ParseSuppression(d)
			if err != nil || kind != "domain" {
				return "domains must be names such as acme-com.example: " + d
			}
			(*in.Domains)[i] = v
		}
	}
	return ""
}

func (f *planFields) goal(now time.Time, chosen, exclude []string) infra.Goal {
	g := infra.Goal{DomainCount: f.DomainCount, MailboxesPerDomain: f.MailboxesPerDomain, Brand: f.Brand, Personas: f.Personas,
		Domains: chosen, Exclude: exclude, WarmupDays: f.WarmupDays, DailyLimit: f.MailboxDailyLimit, Now: now}
	if f.ReadyBy != nil {
		t, _ := time.Parse(time.DateOnly, *f.ReadyBy)
		g.ReadyBy = &t
	}
	return g
}

type planItemView struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	Domain     string    `json:"domain"`
	Owned      bool      `json:"owned"`
	FirstName  string    `json:"first_name"`
	LastName   string    `json:"last_name"`
	Stage      string    `json:"stage"`
	Detail     string    `json:"detail"`
	Available  *bool     `json:"available"`
	PriceCents *int64    `json:"price_cents"`
	MailboxID  *string   `json:"mailbox_id"`
	Lifecycle  *string   `json:"lifecycle_stage"`
	ChangedAt  time.Time `json:"stage_changed_at"`
}

type planCounts struct {
	Domains   map[string]int `json:"domains"`
	Mailboxes map[string]int `json:"mailboxes"`
}

type planView struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	planFields
	Quote       json.RawMessage `json:"quote"`
	OrderRef    *string         `json:"order_ref"`
	Timeline    infra.Timeline  `json:"timeline"`
	Capacity    int             `json:"daily_capacity"`
	Warnings    []infra.Warning `json:"warnings"`
	Counts      planCounts      `json:"counts"`
	Items       []planItemView  `json:"items,omitempty"`
	CreatedBy   *string         `json:"created_by"`
	ApprovedBy  *string         `json:"approved_by"`
	ApprovedAt  *time.Time      `json:"approved_at"`
	CompletedAt *time.Time      `json:"completed_at"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

const planSelect = `
	SELECT id::text, status, name, project_id::text, supplier, supplier_integration_id::text, provider, domain_count,
	       mailboxes_per_domain, brand, personas, to_char(ready_by, 'YYYY-MM-DD'), warmup_days, mailbox_daily_limit,
	       rest_below, retire_after_days, notes, quote, order_ref, created_by::text, approved_by::text, approved_at,
	       completed_at, created_at, updated_at
	FROM infra_plans WHERE workspace_id = $1 AND ($2::uuid IS NULL OR id = $2)
	ORDER BY created_at DESC`

func (s *Server) queryPlans(ctx context.Context, q pgx.Tx, ws string, id *string, withItems bool) ([]planView, error) {
	rows, err := q.Query(ctx, planSelect, ws, id)
	if err != nil {
		return nil, err
	}
	plans, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (planView, error) {
		var v planView
		var personas []byte
		err := r.Scan(&v.ID, &v.Status, &v.Name, &v.ProjectID, &v.Supplier, &v.SupplierIntegrationID, &v.Provider, &v.DomainCount,
			&v.MailboxesPerDomain, &v.Brand, &personas, &v.ReadyBy, &v.WarmupDays, &v.MailboxDailyLimit,
			&v.RestBelow, &v.RetireAfterDays, &v.Notes, &v.Quote, &v.OrderRef, &v.CreatedBy, &v.ApprovedBy, &v.ApprovedAt,
			&v.CompletedAt, &v.CreatedAt, &v.UpdatedAt)
		if err == nil {
			err = json.Unmarshal(personas, &v.Personas)
		}
		return v, err
	})
	if err != nil {
		return nil, err
	}
	for i := range plans {
		p := &plans[i]
		rows, err := q.Query(ctx, `
			SELECT i.id::text, i.kind, i.name, i.domain, i.owned, i.first_name, i.last_name, i.stage, i.detail, i.available,
			       i.price_cents, i.mailbox_id::text, m.lifecycle_stage, i.stage_changed_at
			FROM infra_plan_items i LEFT JOIN mailboxes m ON m.id = i.mailbox_id
			WHERE i.plan_id = $1 ORDER BY i.domain, i.kind DESC, length(split_part(i.name, '@', 1)), i.name`, p.ID)
		if err != nil {
			return nil, err
		}
		items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[planItemView])
		if err != nil {
			return nil, err
		}
		p.Counts = planCounts{Domains: map[string]int{}, Mailboxes: map[string]int{}}
		var chosen []string
		for _, it := range items {
			if it.Kind == "domain" {
				p.Counts.Domains[it.Stage]++
				chosen = append(chosen, it.Domain)
				continue
			}
			stage := it.Stage
			if it.Lifecycle != nil {
				stage = *it.Lifecycle // warming, ready, resting, retired
			}
			p.Counts.Mailboxes[stage]++
		}
		// The timeline counts from approval once approved, else from now.
		now := time.Now()
		if p.ApprovedAt != nil {
			now = *p.ApprovedAt
		}
		d := infra.Plan(p.goal(now, chosen, nil))
		p.Timeline, p.Capacity, p.Warnings = d.Timeline, d.DailyCapacity, d.Warnings
		if p.Status != infra.PlanDraft {
			// Warnings are for deciding; after approval they are history.
			p.Warnings = []infra.Warning{}
		} else {
			p.Warnings = append(p.Warnings, quoteWarnings(p.Supplier, p.Quote, items)...)
		}
		if withItems {
			p.Items = items
		}
	}
	return plans, nil
}

// quoteWarnings block approving a supplier plan until it has a price and
// every domain to buy is available.
func quoteWarnings(supplier string, quote json.RawMessage, items []planItemView) []infra.Warning {
	if supplier == infra.SupplierManual {
		return nil
	}
	var q struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(quote, &q)
	switch q.Status {
	case "ready":
	case "failed":
		return []infra.Warning{{Code: "quote_failed", Blocking: true, Message: "No price yet: " + q.Error + " Refresh the quote to try again."}}
	default:
		return []infra.Warning{{Code: "quote_pending", Blocking: true, Message: "Getting prices and availability from the supplier."}}
	}
	var taken []string
	for _, it := range items {
		if it.Kind == "domain" && !it.Owned && it.Available != nil && !*it.Available {
			taken = append(taken, it.Name)
		}
	}
	if len(taken) > 0 {
		return []infra.Warning{{Code: "domains_unavailable", Blocking: true,
			Message: "Not available to buy: " + strings.Join(taken, ", ") + ". Replace them, or let the plan suggest others."}}
	}
	return nil
}

func (s *Server) findPlan(w http.ResponseWriter, r *http.Request, ws, id string, withItems bool) (*planView, bool) {
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "plan not found")
		return nil, false
	}
	var list []planView
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		list, err = s.queryPlans(r.Context(), tx, ws, &id, withItems)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	if len(list) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "plan not found")
		return nil, false
	}
	return &list[0], true
}

func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	var list []planView
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if list, err = s.queryPlans(r.Context(), tx, a.WorkspaceID, nil, false); err != nil {
			return err
		}
		list, err = plansFor(r.Context(), tx, a.WorkspaceID, list, sel)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []planView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// plansFor keeps the plans that pass a brand filter: a plan belongs to its
// project's brand, and one with no project (or a project with no brand)
// to no brand.
func plansFor(ctx context.Context, q pgx.Tx, ws string, plans []planView, sel brandSel) ([]planView, error) {
	if sel.all() {
		return plans, nil
	}
	brands := map[string]*string{}
	rows, err := q.Query(ctx, `SELECT id::text, brand_id::text FROM projects WHERE workspace_id = $1`, ws)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var b *string
		if err := rows.Scan(&id, &b); err != nil {
			return nil, err
		}
		brands[id] = b
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return slices.DeleteFunc(plans, func(p planView) bool {
		var b *string
		if p.ProjectID != nil {
			b = brands[*p.ProjectID]
		}
		return !sel.matches(b)
	}), nil
}

func (s *Server) getPlan(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if p, ok := s.findPlan(w, r, a.WorkspaceID, r.PathValue("id"), true); ok {
		writeJSON(w, http.StatusOK, p)
	}
}

// workspaceDomains lists the workspace's sending domains.
func workspaceDomains(ctx context.Context, q pgx.Tx, ws string) (map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT domain FROM sending_domains WHERE workspace_id = $1`, ws)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowTo[string])
	out := map[string]bool{}
	for _, d := range list {
		out[d] = true
	}
	return out, err
}

// writeItems replaces a draft plan's items with a fresh draft.
func writeItems(ctx context.Context, tx pgx.Tx, ws, planID string, f *planFields, chosen []string) error {
	owned, err := workspaceDomains(ctx, tx, ws)
	if err != nil {
		return err
	}
	// Names the supplier said are taken are not suggested again.
	rows, err := tx.Query(ctx, `SELECT name FROM infra_plan_items WHERE plan_id = $1 AND kind = 'domain' AND available = false`, planID)
	if err != nil {
		return err
	}
	taken, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	exclude := taken
	for d := range owned {
		exclude = append(exclude, d)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM infra_plan_items WHERE plan_id = $1`, planID); err != nil {
		return err
	}
	d := infra.Plan(f.goal(time.Now(), chosen, exclude))
	for _, dom := range d.Domains {
		if _, err := tx.Exec(ctx, `
			INSERT INTO infra_plan_items (workspace_id, plan_id, kind, name, domain, owned) VALUES ($1, $2, 'domain', $3, $3, $4)`,
			ws, planID, dom.Name, owned[dom.Name]); err != nil {
			return err
		}
		for _, m := range dom.Mailboxes {
			if _, err := tx.Exec(ctx, `
				INSERT INTO infra_plan_items (workspace_id, plan_id, kind, name, domain, owned, first_name, last_name)
				VALUES ($1, $2, 'mailbox', $3, $4, $5, $6, $7)`,
				ws, planID, m.Email, dom.Name, owned[dom.Name], m.FirstName, m.LastName); err != nil {
				return err
			}
		}
	}
	return nil
}

// previewPlan drafts a plan without saving it, so a person or agent can
// try goals out.
func (s *Server) previewPlan(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in planInput
	if !decode(w, r, &in) {
		return
	}
	f := defaultPlan()
	if in.Name == nil {
		f.Name = "Preview"
	}
	if msg := in.apply(&f); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	var owned map[string]bool
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		owned, err = workspaceDomains(r.Context(), tx, a.WorkspaceID)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var exclude, chosen []string
	for d := range owned {
		exclude = append(exclude, d)
	}
	if in.Domains != nil {
		chosen = *in.Domains
	}
	writeJSON(w, http.StatusOK, infra.Plan(f.goal(time.Now(), chosen, exclude)))
}

func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in planInput
	if !decode(w, r, &in) {
		return
	}
	f := defaultPlan()
	if msg := in.apply(&f); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	var chosen []string
	if in.Domains != nil {
		chosen = *in.Domains
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := checkProject(ctx, tx, a.WorkspaceID, f.ProjectID); err != nil {
			return err
		}
		personas, _ := json.Marshal(f.Personas)
		quote := `{"status": "none"}`
		if f.Supplier != infra.SupplierManual {
			quote = `{"status": "pending"}`
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO infra_plans (workspace_id, name, project_id, supplier, supplier_integration_id, provider, domain_count,
				mailboxes_per_domain, brand, personas, ready_by, warmup_days, mailbox_daily_limit, rest_below, retire_after_days,
				notes, quote, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::date, $12, $13, $14, $15, $16, $17, $18)
			RETURNING id::text`,
			a.WorkspaceID, f.Name, f.ProjectID, f.Supplier, f.SupplierIntegrationID, f.Provider, f.DomainCount,
			f.MailboxesPerDomain, f.Brand, personas, f.ReadyBy, f.WarmupDays, f.MailboxDailyLimit, f.RestBelow, f.RetireAfterDays,
			f.Notes, quote, a.ActorID).Scan(&id)
		if err != nil {
			return err
		}
		if err := writeItems(ctx, tx, a.WorkspaceID, id, &f, chosen); err != nil {
			return err
		}
		if f.Supplier != infra.SupplierManual {
			if err := infra.QueueQuote(ctx, tx, a.WorkspaceID, id); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "infra.plan_created", "infra_plan", id,
			map[string]any{"name": f.Name, "domains": f.DomainCount, "mailboxes_per_domain": f.MailboxesPerDomain, "supplier": f.Supplier})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "project not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if p, ok := s.findPlan(w, r, a.WorkspaceID, id, true); ok {
		writeJSON(w, http.StatusCreated, p)
	}
}

func checkProject(ctx context.Context, tx pgx.Tx, ws string, id *string) error {
	if id == nil {
		return nil
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE workspace_id = $1 AND id = $2)`, ws, *id).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errNotFound
	}
	return nil
}

var errNotDraft = errors.New("plan is not a draft")

func (s *Server) updatePlan(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	cur, ok := s.findPlan(w, r, a.WorkspaceID, id, true)
	if !ok {
		return
	}
	var in planInput
	if !decode(w, r, &in) {
		return
	}
	f := cur.planFields
	if msg := in.apply(&f); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	// Keep the domains already in the plan unless new ones are given, so
	// changing the mailboxes or dates does not reshuffle names.
	var chosen []string
	if in.Domains != nil {
		chosen = *in.Domains
	} else {
		for _, it := range cur.Items {
			if it.Kind == "domain" && (it.Available == nil || *it.Available || it.Owned) {
				chosen = append(chosen, it.Name)
			}
		}
		if len(chosen) > f.DomainCount {
			chosen = chosen[:f.DomainCount]
		}
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM infra_plans WHERE id = $1 FOR UPDATE`, id).Scan(&status); err != nil {
			return err
		}
		if status != infra.PlanDraft {
			return errNotDraft
		}
		if err := checkProject(ctx, tx, a.WorkspaceID, f.ProjectID); err != nil {
			return err
		}
		personas, _ := json.Marshal(f.Personas)
		quote := `{"status": "none"}`
		if f.Supplier != infra.SupplierManual {
			quote = `{"status": "pending"}`
		}
		if _, err := tx.Exec(ctx, `
			UPDATE infra_plans SET name = $2, project_id = $3, supplier = $4, supplier_integration_id = $5, provider = $6,
				domain_count = $7, mailboxes_per_domain = $8, brand = $9, personas = $10, ready_by = $11::date, warmup_days = $12,
				mailbox_daily_limit = $13, rest_below = $14, retire_after_days = $15, notes = $16, quote = $17, updated_at = now()
			WHERE id = $1`,
			id, f.Name, f.ProjectID, f.Supplier, f.SupplierIntegrationID, f.Provider, f.DomainCount, f.MailboxesPerDomain,
			f.Brand, personas, f.ReadyBy, f.WarmupDays, f.MailboxDailyLimit, f.RestBelow, f.RetireAfterDays, f.Notes, quote); err != nil {
			return err
		}
		if err := writeItems(ctx, tx, a.WorkspaceID, id, &f, chosen); err != nil {
			return err
		}
		if f.Supplier != infra.SupplierManual {
			if err := infra.QueueQuote(ctx, tx, a.WorkspaceID, id); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "infra.plan_updated", "infra_plan", id, map[string]any{})
	})
	switch {
	case errors.Is(err, errNotDraft):
		writeError(w, r, http.StatusConflict, "conflict", "only a draft plan can be changed; this one is "+cur.Status)
		return
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "project not found")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if p, ok := s.findPlan(w, r, a.WorkspaceID, id, true); ok {
		writeJSON(w, http.StatusOK, p)
	}
}

func (s *Server) deletePlan(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "plan not found")
		return
	}
	ctx := r.Context()
	var status string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT status FROM infra_plans WHERE workspace_id = $1 AND id = $2 FOR UPDATE`, a.WorkspaceID, id).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if status != infra.PlanDraft && status != infra.PlanCancelled {
			return errConflict
		}
		if _, err := tx.Exec(ctx, `DELETE FROM infra_plans WHERE id = $1`, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "infra.plan_deleted", "infra_plan", id, map[string]any{})
	})
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "plan not found")
	case errors.Is(err, errConflict):
		writeError(w, r, http.StatusConflict, "conflict", "only a draft or cancelled plan can be deleted; cancel it first")
	case err != nil:
		s.internal(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) approvePlan(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if a.Test {
		writeError(w, r, http.StatusConflict, "test_workspace", "a test workspace cannot buy domains or mailboxes; approve the plan in a live workspace")
		return
	}
	id := r.PathValue("id")
	cur, ok := s.findPlan(w, r, a.WorkspaceID, id, true)
	if !ok {
		return
	}
	var req struct {
		// The price the approver saw, so a quote that changed in between
		// is never approved by accident. Needed when the plan has a price.
		OnceCents    *int64 `json:"once_cents"`
		MonthlyCents *int64 `json:"monthly_cents"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if cur.Status != infra.PlanDraft {
		writeError(w, r, http.StatusConflict, "conflict", "this plan is already "+cur.Status)
		return
	}
	for _, wn := range cur.Warnings {
		if wn.Blocking {
			writeError(w, r, http.StatusConflict, "plan_not_ready", wn.Message)
			return
		}
	}
	var q struct {
		OnceCents    *int64 `json:"once_cents"`
		MonthlyCents *int64 `json:"monthly_cents"`
		Currency     string `json:"currency"`
	}
	_ = json.Unmarshal(cur.Quote, &q)
	if !sameCents(q.OnceCents, req.OnceCents) || !sameCents(q.MonthlyCents, req.MonthlyCents) {
		writeError(w, r, http.StatusConflict, "price_mismatch",
			"send once_cents and monthly_cents exactly as the plan's quote shows them, to confirm the price you are approving")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE infra_plans SET status = 'building', approved_by = $2, approved_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'draft' AND quote = $3`, id, a.ActorID, []byte(cur.Quote))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		// Domains the workspace already has go straight to the DNS check.
		if _, err := tx.Exec(ctx, `UPDATE infra_plan_items SET stage = 'dns', stage_changed_at = now() WHERE plan_id = $1 AND kind = 'domain' AND owned`, id); err != nil {
			return err
		}
		if err := infra.QueueAdvance(ctx, tx, a.WorkspaceID, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "infra.plan_approved", "infra_plan", id, map[string]any{
			"supplier": cur.Supplier, "domains": cur.DomainCount, "mailboxes": cur.DomainCount * cur.MailboxesPerDomain,
			"once_cents": q.OnceCents, "monthly_cents": q.MonthlyCents, "currency": q.Currency})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the plan changed while you were approving it; review it again")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if p, ok := s.findPlan(w, r, a.WorkspaceID, id, true); ok {
		writeJSON(w, http.StatusOK, p)
	}
}

func sameCents(a, b *int64) bool {
	if a == nil {
		return true // no price to confirm
	}
	return b != nil && *a == *b
}

func (s *Server) cancelPlan(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	cur, ok := s.findPlan(w, r, a.WorkspaceID, id, false)
	if !ok {
		return
	}
	if cur.Status == infra.PlanCancelled || cur.Status == infra.PlanReady {
		writeError(w, r, http.StatusConflict, "conflict", "this plan is already "+cur.Status)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE infra_plans SET status = 'cancelled', cancelled_at = now(), updated_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE infra_plan_items SET stage = 'cancelled', detail = 'The plan was cancelled.', stage_changed_at = now()
			WHERE plan_id = $1 AND stage NOT IN ('done', 'failed')`, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "infra.plan_cancelled", "infra_plan", id, map[string]any{"from": cur.Status})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if p, ok := s.findPlan(w, r, a.WorkspaceID, id, true); ok {
		writeJSON(w, http.StatusOK, p)
	}
}

// markBought records that the person bought domains of a manual plan.
func (s *Server) markBought(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	cur, ok := s.findPlan(w, r, a.WorkspaceID, id, false)
	if !ok {
		return
	}
	var req struct {
		// The domains bought; all of the plan's domains waiting to be bought
		// when empty.
		Domains []string `json:"domains"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if cur.Status != infra.PlanBuilding || cur.Supplier != infra.SupplierManual {
		writeError(w, r, http.StatusConflict, "conflict", "only a plan you buy for yourself, while it is building, takes this")
		return
	}
	for i, d := range req.Domains {
		req.Domains[i] = strings.ToLower(strings.TrimSpace(d))
	}
	ctx := r.Context()
	var n int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE infra_plan_items SET stage = 'dns', detail = '', stage_changed_at = now()
			WHERE plan_id = $1 AND kind = 'domain' AND stage = 'buying' AND (cardinality($2::text[]) = 0 OR lower(name) = ANY($2))`,
			id, req.Domains)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		if err := infra.QueueAdvance(ctx, tx, a.WorkspaceID, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "infra.domains_bought", "infra_plan", id, map[string]any{"domains": n})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": n})
}

// refreshPlan gets a new quote for a draft, or checks an active plan now.
func (s *Server) refreshPlan(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	cur, ok := s.findPlan(w, r, a.WorkspaceID, id, false)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		switch cur.Status {
		case infra.PlanDraft:
			if cur.Supplier == infra.SupplierManual {
				return nil
			}
			if _, err := tx.Exec(ctx, `UPDATE infra_plans SET quote = '{"status": "pending"}', updated_at = now() WHERE id = $1`, id); err != nil {
				return err
			}
			return infra.QueueQuote(ctx, tx, a.WorkspaceID, id)
		case infra.PlanBuilding, infra.PlanWarming:
			return infra.QueueAdvance(ctx, tx, a.WorkspaceID, id)
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}

// moveMailboxes moves mailboxes between lifecycle stages by hand.
func (s *Server) moveMailboxes(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		MailboxIDs []string `json:"mailbox_ids"`
		Stage      string   `json:"stage"`
		Reason     string   `json:"reason"`
		// For resting: when the rest ends (default in 7 days). For warming:
		// the earliest it can be ready (default in 14 days).
		Until    *time.Time `json:"until"`
		AutoRest *bool      `json:"auto_rest"`
		// For retired: also cancel the mailbox at the supplier that built it.
		CancelAtSupplier bool `json:"cancel_at_supplier"`
	}
	if !decode(w, r, &req) {
		return
	}
	switch {
	case len(req.MailboxIDs) == 0 || len(req.MailboxIDs) > 1000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "give 1 to 1000 mailbox_ids")
		return
	case !slices.Contains([]string{infra.StageWarming, infra.StageReady, infra.StageResting, infra.StageRetired}, req.Stage):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "stage must be warming, ready, resting or retired")
		return
	case len(req.Reason) > 500:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "reason must be at most 500 characters")
		return
	case req.Until != nil && req.Until.Before(time.Now()):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "until must be in the future")
		return
	}
	for _, id := range req.MailboxIDs {
		if !validUUID(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "mailbox_ids must be UUIDs")
			return
		}
	}
	move := infra.Move{To: req.Stage, Reason: strings.TrimSpace(req.Reason), Until: req.Until, AutoRest: req.AutoRest}
	switch req.Stage {
	case infra.StageResting:
		if move.Until == nil {
			t := time.Now().Add(infra.RestDays * 24 * time.Hour)
			move.Until = &t
		}
	case infra.StageWarming:
		if move.Until == nil {
			t := time.Now().Add(infra.DefaultWarmupDays * 24 * time.Hour)
			move.Until = &t
		}
	default:
		move.Until = nil
	}
	if move.Reason == "" {
		move.Reason = map[string]string{
			infra.StageWarming: "Warming up, set by hand.", infra.StageReady: "Marked ready by hand.",
			infra.StageResting: "Resting, set by hand.", infra.StageRetired: "Retired by hand.",
		}[req.Stage]
	}
	ctx := r.Context()
	var results []infra.MoveResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if results, err = infra.MoveMailboxes(ctx, tx, a.WorkspaceID, req.MailboxIDs, move); err != nil {
			return err
		}
		for _, x := range results {
			if x.Skipped != "" {
				continue
			}
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox.stage_changed", "mailbox", x.ID,
				map[string]any{"from": x.From, "to": x.To, "reason": move.Reason}); err != nil {
				return err
			}
			if req.CancelAtSupplier && x.To == infra.StageRetired && x.PlanID != nil {
				if err := infra.QueueCancel(ctx, tx, a.WorkspaceID, *x.PlanID, x.Email); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if results == nil {
		results = []infra.MoveResult{}
	}
	moved := 0
	for _, x := range results {
		if x.Skipped == "" {
			moved++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"moved": moved, "results": results})
}

type stageMailbox struct {
	ID        string     `json:"id"`
	Email     string     `json:"email"`
	Domain    string     `json:"domain"`
	Stage     string     `json:"stage"`
	Reason    string     `json:"reason"`
	Until     *time.Time `json:"until"`
	ChangedAt time.Time  `json:"changed_at"`
	AutoRest  bool       `json:"auto_rest"`
	PlanID    *string    `json:"plan_id"`
	Placed7d  int        `json:"placed_7d"`
	InboxRate *float64   `json:"inbox_rate"`
}

// infraOverview is every mailbox by lifecycle stage, the ones not ready
// with why and until when, and the plans in progress.
func (s *Server) infraOverview(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	countArgs := []any{a.WorkspaceID}
	countWhere := sel.sql("brand_id", &countArgs)
	listArgs := []any{a.WorkspaceID}
	listWhere := sel.sql("m.brand_id", &listArgs)
	counts := map[string]int{"warming": 0, "ready": 0, "resting": 0, "retired": 0}
	var list []stageMailbox
	var plans []planView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT lifecycle_stage, count(*)::int FROM mailboxes WHERE workspace_id = $1`+countWhere+` GROUP BY 1`, countArgs...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var st string
			var n int
			if err := rows.Scan(&st, &n); err != nil {
				return err
			}
			counts[st] = n
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `
			SELECT m.id::text, m.email, m.domain, m.lifecycle_stage, m.stage_reason, m.stage_until, m.stage_changed_at,
			       m.auto_rest, m.infra_plan_id::text, COALESCE(w.placed, 0)::int,
			       CASE WHEN w.placed > 0 THEN w.inbox::float / w.placed END
			FROM mailboxes m
			LEFT JOIN LATERAL (
				SELECT count(*) FILTER (WHERE g.landed IS NOT NULL) AS placed, count(*) FILTER (WHERE g.landed = 'inbox') AS inbox
				FROM warmup_messages g WHERE g.from_mailbox_id = m.id AND g.created_at > now() - interval '7 days'
			) w ON true
			WHERE m.workspace_id = $1 AND m.lifecycle_stage <> 'ready'`+listWhere+`
			ORDER BY CASE m.lifecycle_stage WHEN 'resting' THEN 0 WHEN 'warming' THEN 1 ELSE 2 END, m.stage_until NULLS LAST, lower(m.email)
			LIMIT 1000`, listArgs...)
		if err != nil {
			return err
		}
		if list, err = pgx.CollectRows(rows, pgx.RowToStructByPos[stageMailbox]); err != nil {
			return err
		}
		if plans, err = s.queryPlans(ctx, tx, a.WorkspaceID, nil, false); err != nil {
			return err
		}
		plans, err = plansFor(ctx, tx, a.WorkspaceID, plans, sel)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []stageMailbox{}
	}
	active := []planView{}
	for _, p := range plans {
		if p.Status != infra.PlanReady && p.Status != infra.PlanCancelled {
			active = append(active, p)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mailboxes": counts, "not_ready": list, "plans": active, "suppliers": infra.Suppliers(),
	})
}
