package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/partners"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// Partner programs: customer referrals, affiliates and influencers on one
// platform. A program is a set of rules (credit, rewards, hold, payouts)
// started from a preset; partners join it with a ref code (partners_members.go);
// the workspace's own backend reports conversions (partners_conversions.go);
// rewards and payouts are a ledger (partners_ledger.go). Outflare never
// holds or sends money.

const (
	maxPartnerPrograms = 100
	maxPartnerRules    = 50
	maxPartnerGroups   = 50
)

func (s *Server) routesPartners(mux *http.ServeMux) {
	read, manage, conv := auth.ScopePartnersRead, auth.ScopePartnersManage, auth.ScopePartnersConversions
	mux.HandleFunc("GET /v1/workspaces/{ws}/partner-settings", s.inWorkspace(read, s.getPartnerSettings))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/partner-settings", s.inWorkspace(manage, s.updatePartnerSettings))
	mux.HandleFunc("GET /v1/workspaces/{ws}/partner-programs", s.inBrand(read, s.listPartnerPrograms))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-programs", s.inBrand(manage, s.createPartnerProgram))
	mux.HandleFunc("GET /v1/workspaces/{ws}/partner-programs/{id}", s.inBrand(read, s.getPartnerProgram))
	mux.HandleFunc("GET /v1/workspaces/{ws}/partner-programs/{id}/stats", s.inBrand(read, s.getPartnerProgramStats))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/partner-programs/{id}", s.inBrand(manage, s.updatePartnerProgram))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/partner-programs/{id}/rules", s.inBrand(manage, s.setPartnerRules))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-programs/{id}/groups", s.inBrand(manage, s.createPartnerGroup))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/partner-programs/{id}/groups/{group}", s.inBrand(manage, s.updatePartnerGroup))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/partner-programs/{id}/groups/{group}", s.inBrand(manage, s.deletePartnerGroup))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-programs/{id}/groups/{group}/partners", s.inBrand(manage, s.assignPartnerGroup))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-programs/{id}/postback", s.inBrand(manage, s.rotatePartnerPostback))

	mux.HandleFunc("GET /v1/workspaces/{ws}/partners", s.inBrand(read, s.listPartners))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partners", s.inBrand(manage, s.createPartner))
	mux.HandleFunc("GET /v1/workspaces/{ws}/partners/{id}", s.inBrand(read, s.getPartner))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/partners/{id}", s.inBrand(manage, s.updatePartner))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partners/{id}/promo-codes", s.inBrand(manage, s.addPartnerPromoCode))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/partners/{id}/promo-codes/{code}", s.inBrand(manage, s.removePartnerPromoCode))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partners/{id}/fees", s.inBrand(manage, s.addPartnerFee))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partners/{id}/adjustments", s.inBrand(manage, s.addPartnerAdjustment))

	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-conversions", s.inWorkspace(conv, s.createPartnerConversion))
	mux.HandleFunc("GET /v1/workspaces/{ws}/partner-conversions", s.inBrand(read, s.listPartnerConversions))
	mux.HandleFunc("GET /v1/workspaces/{ws}/partner-referrals", s.inBrand(read, s.listPartnerReferrals))

	mux.HandleFunc("GET /v1/workspaces/{ws}/partner-rewards", s.inBrand(read, s.listPartnerRewards))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-rewards/bulk", s.inBrand(manage, s.bulkPartnerRewards))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-rewards/{id}/approve", s.inBrand(manage, s.approvePartnerReward))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-rewards/{id}/reject", s.inBrand(manage, s.rejectPartnerReward))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-rewards/{id}/fulfil", s.inBrand(conv, s.fulfilPartnerReward))

	mux.HandleFunc("GET /v1/workspaces/{ws}/partner-payouts", s.inBrand(read, s.listPartnerPayouts))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-payouts", s.inBrand(manage, s.createPartnerPayouts))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-payouts/{id}/paid", s.inBrand(manage, s.markPartnerPayoutPaid))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-payouts/{id}/cancel", s.inBrand(manage, s.cancelPartnerPayout))

	// Public. A postback's secret names its program; a short link's code
	// (with the host or workspace) names its partner.
	mux.HandleFunc("GET /v1/partner-postbacks/{token}", s.partnerPostback)
	mux.HandleFunc("POST /v1/partner-postbacks/{token}", s.partnerPostback)
	mux.HandleFunc("GET /r/{code}", s.partnerLink)
	mux.HandleFunc("GET /r/{ws}/{code}", s.partnerLink)
	s.routesPartnerPortal(mux)
}

// partnerEventProps are the audit data keys that also go on the product
// event: enums, counts and flags, never notes, codes or amounts.
var partnerEventProps = []string{"kind", "program_kind", "side", "type", "source", "via", "status", "previous_status", "by", "rules", "has_note", "replaced",
	"default", "renamed", "partners"}

// partnerRecord is record for partner actions, with their enums as event
// properties.
func partnerRecord(ctx context.Context, tx pgx.Tx, p *principal, ws, action, targetType, id string, data map[string]any) error {
	props := map[string]any{}
	for _, k := range partnerEventProps {
		if v, ok := data[k]; ok {
			props[k] = v
		}
	}
	return recordWithProps(ctx, tx, p, ws, action, targetType, id, data, props)
}

type partnerGroupView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Partners is how many partners are in it.
	Partners int `json:"partners"`
	// Default is the group new partners join.
	Default bool `json:"default"`
}

type partnerProgramView struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Slug             string  `json:"slug"`
	Kind             string  `json:"kind"`
	Status           string  `json:"status"`
	BrandID          *string `json:"brand_id"`
	LandingURL       string  `json:"landing_url"`
	Currency         string  `json:"currency"`
	CreditWindowDays int     `json:"credit_window_days"`
	CreditModel      string  `json:"credit_model"`
	CodesWin         bool    `json:"codes_win"`
	HoldDays         int     `json:"hold_days"`
	AutoApprove      bool    `json:"auto_approve"`
	AutoAccept       bool    `json:"auto_accept"`
	// PayBrowserSignups pays sign-up rewards for sign-ups the website
	// reports with outflare.signup(), which anyone can call. Off: those make
	// the lead only.
	PayBrowserSignups bool   `json:"pay_browser_signups"`
	MinPayoutCents    int64  `json:"min_payout_cents"`
	TermsURL          string `json:"terms_url"`
	// SalesSource is a payment connector (stripe) whose synced payments
	// and refunds are also partner sales; "" for the API and postbacks only.
	SalesSource     string         `json:"sales_source"`
	PostbackHint    string         `json:"postback_hint"`
	PortalEnabled   bool           `json:"portal_enabled"`
	PortalSubdomain *string        `json:"portal_subdomain"`
	Portal          portalSettings `json:"portal"`
	// PortalURL is where partners sign up and sign in: the program's own
	// domain, its subdomain of the partners domain, or /p/<id>/ here.
	PortalURL string             `json:"portal_url"`
	Rules     []partners.Rule    `json:"rules"`
	Groups    []partnerGroupView `json:"groups"`
	// DefaultGroupID is the group new partners join; null is none.
	DefaultGroupID *string   `json:"default_group_id"`
	Partners       int       `json:"partners"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

const partnerProgramSelect = `
	SELECT p.id::text, p.name, p.slug, p.kind, p.status, p.brand_id::text, p.landing_url, p.currency, p.credit_window_days, p.credit_model,
	       p.codes_win, p.hold_days, p.auto_approve, p.auto_accept, p.pay_browser_signups, p.min_payout_cents, p.terms_url, p.sales_source, p.postback_hint,
	       p.portal_enabled, p.portal_subdomain, p.portal, p.default_group_id::text,
	       (SELECT count(*) FROM partner_memberships m WHERE m.program_id = p.id)::int, p.created_at, p.updated_at
	FROM partner_programs p WHERE p.workspace_id = $1`

func scanPartnerProgram(row pgx.Row) (partnerProgramView, error) {
	var v partnerProgramView
	var portal []byte
	err := row.Scan(&v.ID, &v.Name, &v.Slug, &v.Kind, &v.Status, &v.BrandID, &v.LandingURL, &v.Currency, &v.CreditWindowDays, &v.CreditModel,
		&v.CodesWin, &v.HoldDays, &v.AutoApprove, &v.AutoAccept, &v.PayBrowserSignups, &v.MinPayoutCents, &v.TermsURL, &v.SalesSource, &v.PostbackHint,
		&v.PortalEnabled, &v.PortalSubdomain, &portal, &v.DefaultGroupID, &v.Partners, &v.CreatedAt, &v.UpdatedAt)
	if err == nil {
		v.Portal = portalFor(v.Kind, portal)
	}
	return v, err
}

// partnerQuerier is a pool or a transaction.
type partnerQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// partnerRules loads a program's rules in order.
func partnerRules(ctx context.Context, q partnerQuerier, ws, program string) ([]partners.Rule, error) {
	rows, err := q.Query(ctx, `SELECT id::text, group_id::text, membership_id::text, trigger, side, kind, percent_bp, amount_cents, action, action_data,
		recurrence, months, min_sales, product_ids FROM partner_reward_rules WHERE workspace_id = $1 AND program_id = $2 ORDER BY created_at, id`, ws, program)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (partners.Rule, error) {
		var r partners.Rule
		err := row.Scan(&r.ID, &r.GroupID, &r.PartnerID, &r.Trigger, &r.Side, &r.Kind, &r.PercentBP, &r.AmountCents, &r.Action, &r.ActionData,
			&r.Recurrence, &r.Months, &r.MinSales, &r.ProductIDs)
		return r, err
	})
}

// findPartnerProgram finds a program by id or slug, with its rules and groups.
func (s *Server) findPartnerProgram(w http.ResponseWriter, r *http.Request, ws, ref string) (*partnerProgramView, bool) {
	ctx := r.Context()
	ref = strings.ToLower(strings.TrimSpace(ref))
	v, err := scanPartnerProgram(s.pool.QueryRow(ctx, partnerProgramSelect+` AND (p.id::text = $2 OR p.slug = $2)`, ws, ref))
	if err == nil {
		err = partnerBrandAccess(r, accessFrom(ctx), v.BrandID)
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "partner program not found")
		return nil, false
	}
	if errors.As(err, new(errDenied)) {
		writeError(w, r, http.StatusForbidden, "forbidden", err.Error())
		return nil, false
	}
	if err == nil {
		v.Rules, err = partnerRules(ctx, s.pool, ws, v.ID)
	}
	if err == nil {
		var rows pgx.Rows
		rows, err = s.pool.Query(ctx, `SELECT g.id::text, g.name, (SELECT count(*) FROM partner_memberships m WHERE m.group_id = g.id)::int,
			COALESCE(p.default_group_id = g.id, false)
			FROM partner_groups g JOIN partner_programs p ON p.id = g.program_id
			WHERE g.workspace_id = $1 AND g.program_id = $2 ORDER BY g.name`, ws, v.ID)
		if err == nil {
			v.Groups, err = pgx.CollectRows(rows, pgx.RowToStructByPos[partnerGroupView])
		}
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	if v.Groups == nil {
		v.Groups = []partnerGroupView{}
	}
	if v.PortalURL, err = s.portalBase(ctx, v.ID, v.PortalSubdomain); err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return &v, true
}

func (s *Server) listPartnerPrograms(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	if status != "" && !slices.Contains([]string{"active", "paused", "archived"}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active, paused or archived")
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, status}
	rows, err := s.pool.Query(r.Context(), partnerProgramSelect+` AND ($2 = '' OR p.status = $2)`+sel.sql("p.brand_id", &args)+` ORDER BY p.created_at`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (partnerProgramView, error) { return scanPartnerProgram(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for i := range list {
		if list[i].Rules, err = partnerRules(r.Context(), s.pool, a.WorkspaceID, list[i].ID); err != nil {
			s.internal(w, r, err)
			return
		}
		list[i].Groups = []partnerGroupView{}
		if list[i].PortalURL, err = s.portalBase(r.Context(), list[i].ID, list[i].PortalSubdomain); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if list == nil {
		list = []partnerProgramView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getPartnerProgram(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

type partnerProgramInput struct {
	Name              *string          `json:"name"`
	Slug              *string          `json:"slug"`
	Kind              *string          `json:"kind"`
	Status            *string          `json:"status"`
	Brand             *string          `json:"brand"`
	LandingURL        *string          `json:"landing_url"`
	Currency          *string          `json:"currency"`
	CreditWindowDays  *int             `json:"credit_window_days"`
	CreditModel       *string          `json:"credit_model"`
	CodesWin          *bool            `json:"codes_win"`
	HoldDays          *int             `json:"hold_days"`
	AutoApprove       *bool            `json:"auto_approve"`
	AutoAccept        *bool            `json:"auto_accept"`
	PayBrowserSignups *bool            `json:"pay_browser_signups"`
	MinPayoutCents    *int64           `json:"min_payout_cents"`
	TermsURL          *string          `json:"terms_url"`
	SalesSource       *string          `json:"sales_source"`
	Rules             *[]partnerRuleIn `json:"rules"`
}

var (
	programSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,59}$`)
	currencyRE    = regexp.MustCompile(`^[A-Z]{3}$`)
)

// check validates the settings a program input sets.
func (in partnerProgramInput) check() error {
	if in.Name != nil {
		if n := strings.TrimSpace(*in.Name); n == "" || len(n) > 120 {
			return errInvalid("name must be 1 to 120 characters")
		}
	}
	if in.Slug != nil && !programSlugRE.MatchString(*in.Slug) {
		return errInvalid("slug must be 1 to 60 lowercase letters, digits and hyphens, such as geonode-affiliates")
	}
	if in.Kind != nil && !slices.Contains(partners.Kinds, *in.Kind) {
		return errInvalid("kind is referral, affiliate or influencer")
	}
	if in.Status != nil && !slices.Contains([]string{"active", "paused", "archived"}, *in.Status) {
		return errInvalid("status is active, paused or archived")
	}
	if in.Currency != nil && !currencyRE.MatchString(*in.Currency) {
		return errInvalid("currency is a three-letter code such as USD")
	}
	if in.CreditWindowDays != nil && (*in.CreditWindowDays < 1 || *in.CreditWindowDays > 365) {
		return errInvalid("credit_window_days is 1 to 365")
	}
	if in.CreditModel != nil && *in.CreditModel != "first" && *in.CreditModel != "last" {
		return errInvalid("credit_model is first or last")
	}
	if in.HoldDays != nil && (*in.HoldDays < 0 || *in.HoldDays > 365) {
		return errInvalid("hold_days is 0 to 365")
	}
	if in.MinPayoutCents != nil && *in.MinPayoutCents < 0 {
		return errInvalid("min_payout_cents can't be negative")
	}
	return nil
}

// checkSalesSource reports whether src is "" or a payment connector.
func (s *Server) checkSalesSource(src *string) error {
	if src == nil || *src == "" {
		return nil
	}
	d, ok := s.integrations.Registry.Definition(*src)
	if !ok || !slices.Contains(d.Capabilities, connectors.CapPayments) {
		return errInvalid("sales_source is a billing connector's id, such as stripe, or \"\" for none")
	}
	return nil
}

// cleanURL checks an optional page address; "" clears it.
func cleanURL(field string, p *string) (*string, error) {
	if p == nil {
		return nil, nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return &v, nil
	}
	dest, err := utm.Destination(v)
	if err != nil {
		return nil, errInvalid(field + ": " + err.Error())
	}
	return &dest, nil
}

func (s *Server) createPartnerProgram(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in partnerProgramInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == nil || in.Kind == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name and kind (referral, affiliate or influencer) are required")
		return
	}
	if in.Status != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a new program is active; pause or archive it with PATCH")
		return
	}
	if s.allocError(w, r, in.check()) {
		return
	}
	if s.allocError(w, r, s.checkSalesSource(in.SalesSource)) {
		return
	}
	name := strings.TrimSpace(*in.Name)
	slug := utm.Slug(name)
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	if in.Slug != nil {
		slug = *in.Slug
	}
	if !programSlugRE.MatchString(slug) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name needs letters or digits for the slug, or pass slug")
		return
	}
	landing, err := cleanURL("landing_url", in.LandingURL)
	if s.allocError(w, r, err) {
		return
	}
	terms, err := cleanURL("terms_url", in.TermsURL)
	if s.allocError(w, r, err) {
		return
	}
	brand, err := s.campaignBrand(r, a.WorkspaceID, in.Brand)
	if err == nil {
		err = checkPartnerBrandWrite(a, brand)
	}
	if s.allocError(w, r, err) {
		return
	}
	p := partners.PresetFor(*in.Kind)
	set := func(dst *int, src *int) {
		if src != nil {
			*dst = *src
		}
	}
	set(&p.CreditWindowDays, in.CreditWindowDays)
	set(&p.HoldDays, in.HoldDays)
	if in.CreditModel != nil {
		p.CreditModel = *in.CreditModel
	}
	if in.AutoApprove != nil {
		p.AutoApprove = *in.AutoApprove
	}
	if in.AutoAccept != nil {
		p.AutoAccept = *in.AutoAccept
	}
	if in.MinPayoutCents != nil {
		p.MinPayoutCents = *in.MinPayoutCents
	}
	codesWin := in.CodesWin == nil || *in.CodesWin
	currency := "USD"
	if in.Currency != nil {
		currency = *in.Currency
	}
	ctx := r.Context()
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM partner_programs WHERE workspace_id = $1`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= maxPartnerPrograms {
			return errInvalid("a workspace can have at most 100 partner programs; archive one and reuse it")
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO partner_programs (workspace_id, brand_id, name, slug, kind, landing_url, currency, credit_window_days, credit_model,
				codes_win, hold_days, auto_approve, auto_accept, min_payout_cents, terms_url, created_by, sales_source)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			ON CONFLICT (workspace_id, slug) DO NOTHING RETURNING id::text`,
			a.WorkspaceID, brand, name, slug, *in.Kind, deref(landing), currency, p.CreditWindowDays, p.CreditModel,
			codesWin, p.HoldDays, p.AutoApprove, p.AutoAccept, p.MinPayoutCents, deref(terms), nullIfEmpty(a.ActorID), deref(in.SalesSource)).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConflict
		}
		if err != nil {
			return err
		}
		rules := p.Rules
		if in.Rules != nil {
			if rules, err = checkPartnerRules(*in.Rules, nil, nil); err != nil {
				return err
			}
		}
		if err := insertPartnerRules(ctx, tx, a.WorkspaceID, id, rules); err != nil {
			return err
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_program.created", "partner_program", id,
			map[string]any{"kind": *in.Kind, "brand_id": brand, "rules": len(rules)})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "another program already uses the slug "+slug+"; pass another slug")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	if v, ok := s.findPartnerProgram(w, r, a.WorkspaceID, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

// updatePartnerProgram changes a program's settings. Its kind and slug
// stay: links and imports name the program by them.
func (s *Server) updatePartnerProgram(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in partnerProgramInput
	if !decode(w, r, &in) {
		return
	}
	switch {
	case in.Kind != nil && *in.Kind != cur.Kind, in.Slug != nil && *in.Slug != cur.Slug:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a program's kind and slug can't change; every setting a kind presets can")
		return
	case in.Rules != nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "replace rules with PUT .../partner-programs/{id}/rules")
		return
	case in.Currency != nil && *in.Currency != cur.Currency:
		var n int
		if err := s.pool.QueryRow(r.Context(), `SELECT count(*) FROM partner_rewards WHERE program_id = $1`, cur.ID).Scan(&n); err != nil {
			s.internal(w, r, err)
			return
		}
		if n > 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "currency can't change once the program has rewards")
			return
		}
	}
	if s.allocError(w, r, in.check()) || s.allocError(w, r, s.checkSalesSource(in.SalesSource)) {
		return
	}
	var name *string
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		name = &n
	}
	landing, err := cleanURL("landing_url", in.LandingURL)
	if s.allocError(w, r, err) {
		return
	}
	terms, err := cleanURL("terms_url", in.TermsURL)
	if s.allocError(w, r, err) {
		return
	}
	brand := cur.BrandID
	if in.Brand != nil {
		brand, err = s.campaignBrand(r, a.WorkspaceID, in.Brand)
		if err == nil {
			err = checkPartnerBrandWrite(a, brand)
		}
		if s.allocError(w, r, err) {
			return
		}
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE partner_programs SET name = COALESCE($3, name), status = COALESCE($4, status), brand_id = $5,
				landing_url = COALESCE($6, landing_url), currency = COALESCE($7, currency),
				credit_window_days = COALESCE($8, credit_window_days), credit_model = COALESCE($9, credit_model),
				codes_win = COALESCE($10, codes_win), hold_days = COALESCE($11, hold_days), auto_approve = COALESCE($12, auto_approve),
				auto_accept = COALESCE($13, auto_accept), min_payout_cents = COALESCE($14, min_payout_cents),
				terms_url = COALESCE($15, terms_url), sales_source = COALESCE($16, sales_source),
				pay_browser_signups = COALESCE($17, pay_browser_signups), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, cur.ID, name, in.Status, brand, landing, in.Currency, in.CreditWindowDays, in.CreditModel,
			in.CodesWin, in.HoldDays, in.AutoApprove, in.AutoAccept, in.MinPayoutCents, terms, in.SalesSource, in.PayBrowserSignups); err != nil {
			return err
		}
		changed := []string{}
		for k, set := range map[string]bool{"name": in.Name != nil, "status": in.Status != nil, "brand": in.Brand != nil,
			"landing_url": in.LandingURL != nil, "currency": in.Currency != nil, "credit_window_days": in.CreditWindowDays != nil,
			"credit_model": in.CreditModel != nil, "codes_win": in.CodesWin != nil, "hold_days": in.HoldDays != nil,
			"auto_approve": in.AutoApprove != nil, "auto_accept": in.AutoAccept != nil, "min_payout_cents": in.MinPayoutCents != nil,
			"terms_url": in.TermsURL != nil, "sales_source": in.SalesSource != nil, "pay_browser_signups": in.PayBrowserSignups != nil} {
			if set {
				changed = append(changed, k)
			}
		}
		slices.Sort(changed)
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_program.updated", "partner_program", cur.ID,
			map[string]any{"kind": cur.Kind, "status": coalesce(in.Status, cur.Status), "changed": changed})
	})
	if s.allocError(w, r, err) {
		return
	}
	if v, ok := s.findPartnerProgram(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func coalesce(p *string, def string) string {
	if p != nil {
		return *p
	}
	return def
}

// partnerRuleIn is a reward rule as callers send it.
type partnerRuleIn struct {
	Group       *string        `json:"group_id"`
	Partner     *string        `json:"partner_id"`
	Trigger     string         `json:"trigger"`
	Side        string         `json:"side"`
	Kind        string         `json:"kind"`
	PercentBP   *int           `json:"percent_bp"`
	AmountCents *int64         `json:"amount_cents"`
	Action      string         `json:"action"`
	ActionData  map[string]any `json:"action_data"`
	Recurrence  string         `json:"recurrence"`
	Months      *int           `json:"months"`
	MinSales    int            `json:"min_sales"`
	ProductIDs  []string       `json:"product_ids"`
}

var (
	ruleActionRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,59}$`)
	// A custom conversion's event name, as the customer events catalog
	// spells names.
	conversionEventRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_.]{0,79}$`)
)

// checkPartnerRules validates rules; groups are the program's group ids,
// and members the ids of its partners that rules name.
func checkPartnerRules(in []partnerRuleIn, groups, members []string) ([]partners.Rule, error) {
	if len(in) > maxPartnerRules {
		return nil, errInvalid("a program can have at most 50 reward rules")
	}
	out := []partners.Rule{}
	for i, r := range in {
		bad := func(msg string) error { return errInvalid(fmt.Sprintf("rules[%d]: %s", i, msg)) }
		if r.Side == "" {
			r.Side = "partner"
		}
		if r.Recurrence == "" {
			r.Recurrence = "every"
		}
		if r.Trigger == "lead" {
			r.Trigger = "signup"
		}
		switch {
		case !conversionEventRE.MatchString(r.Trigger) || r.Trigger == "refund":
			return nil, bad("trigger is signup, sale or a custom conversion's event name, such as trial_started")
		case r.Side != "partner" && r.Side != "referred":
			return nil, bad("side is partner or referred")
		case !slices.Contains([]string{"percent", "fixed", "action"}, r.Kind):
			return nil, bad("kind is percent, fixed or action")
		case r.Side == "referred" && r.Kind != "action":
			return nil, bad("a reward for the referred customer is an action, such as granting credit in your product")
		case r.Kind == "percent" && (r.PercentBP == nil || *r.PercentBP < 1 || *r.PercentBP > 10000):
			return nil, bad("percent_bp is 1 to 10000 (2500 is 25%)")
		case r.Kind == "percent" && r.Trigger == "signup":
			return nil, bad("a percentage needs a value; use fixed for sign-ups")
		case r.Kind == "fixed" && (r.AmountCents == nil || *r.AmountCents < 1):
			return nil, bad("amount_cents must be above 0")
		case r.Kind == "action" && !ruleActionRE.MatchString(r.Action):
			return nil, bad("action is a name your system knows, such as grant_credit: lowercase letters, digits, dots, hyphens and underscores")
		case !slices.Contains([]string{"first", "every", "months"}, r.Recurrence):
			return nil, bad("recurrence is first, every or months")
		case (r.Recurrence == "months") != (r.Months != nil):
			return nil, bad("months is set exactly when recurrence is months")
		case r.Months != nil && (*r.Months < 1 || *r.Months > 120):
			return nil, bad("months is 1 to 120")
		case r.MinSales < 0:
			return nil, bad("min_sales can't be negative")
		case len(r.ProductIDs) > 50:
			return nil, bad("at most 50 product_ids")
		case r.Group != nil && !slices.Contains(groups, *r.Group):
			return nil, bad("group_id is not a group of this program")
		case r.Partner != nil && !slices.Contains(members, *r.Partner):
			return nil, bad("partner_id is not a partner in this program")
		case r.Partner != nil && r.Group != nil:
			return nil, bad("a rule is for one partner (partner_id) or a group (group_id), not both")
		}
		if r.Trigger != "sale" {
			r.Recurrence, r.Months = "every", nil
		}
		if r.Kind != "percent" {
			r.PercentBP = nil
		}
		if r.Kind != "fixed" {
			r.AmountCents = nil
		}
		if r.Kind != "action" {
			r.Action, r.ActionData = "", nil
		}
		if r.ActionData == nil {
			r.ActionData = map[string]any{}
		}
		if b, _ := json.Marshal(r.ActionData); len(b) > 4000 {
			return nil, bad("action_data is at most 4,000 bytes of JSON")
		}
		if r.ProductIDs == nil {
			r.ProductIDs = []string{}
		}
		out = append(out, partners.Rule{GroupID: r.Group, PartnerID: r.Partner, Trigger: r.Trigger, Side: r.Side, Kind: r.Kind, PercentBP: r.PercentBP,
			AmountCents: r.AmountCents, Action: r.Action, ActionData: r.ActionData, Recurrence: r.Recurrence, Months: r.Months,
			MinSales: r.MinSales, ProductIDs: r.ProductIDs})
	}
	return out, nil
}

// ruleMembers is which of the partners rules name are in the program.
func (s *Server) ruleMembers(ctx context.Context, program string, in []partnerRuleIn) ([]string, error) {
	ids := []string{}
	for _, r := range in {
		if r.Partner != nil && validUUID(*r.Partner) {
			ids = append(ids, *r.Partner)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM partner_memberships WHERE program_id = $1 AND id = ANY($2::uuid[])`, program, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func insertPartnerRules(ctx context.Context, tx pgx.Tx, ws, program string, rules []partners.Rule) error {
	for i, r := range rules {
		data, _ := json.Marshal(r.ActionData)
		// created_at orders them, so each is a microsecond after the last.
		if _, err := tx.Exec(ctx, `INSERT INTO partner_reward_rules (workspace_id, program_id, group_id, trigger, side, kind, percent_bp,
			amount_cents, action, action_data, recurrence, months, min_sales, product_ids, created_at, membership_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, now() + make_interval(secs => $15::float8 / 1000000), $16)`,
			ws, program, r.GroupID, r.Trigger, r.Side, r.Kind, r.PercentBP, r.AmountCents, r.Action, string(data), r.Recurrence,
			r.Months, r.MinSales, r.ProductIDs, i, r.PartnerID); err != nil {
			return err
		}
	}
	return nil
}

// setPartnerRules replaces a program's reward rules. Rewards already in
// the ledger keep what they earned.
func (s *Server) setPartnerRules(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in struct {
		Rules *[]partnerRuleIn `json:"rules"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Rules == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "rules is required; [] makes the program track only")
		return
	}
	groups := []string{}
	for _, g := range cur.Groups {
		groups = append(groups, g.ID)
	}
	members, err := s.ruleMembers(r.Context(), cur.ID, *in.Rules)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rules, err := checkPartnerRules(*in.Rules, groups, members)
	if s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM partner_reward_rules WHERE workspace_id = $1 AND program_id = $2`, a.WorkspaceID, cur.ID); err != nil {
			return err
		}
		if err := insertPartnerRules(ctx, tx, a.WorkspaceID, cur.ID, rules); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE partner_programs SET updated_at = now() WHERE id = $1`, cur.ID); err != nil {
			return err
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_program.rules_updated", "partner_program", cur.ID,
			map[string]any{"kind": cur.Kind, "rules": len(rules)})
	})
	if s.allocError(w, r, err) {
		return
	}
	if v, ok := s.findPartnerProgram(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) createPartnerGroup(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in struct {
		Name    string `json:"name"`
		Default bool   `json:"default"`
	}
	if !decode(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 80 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name must be 1 to 80 characters, such as Gold")
		return
	}
	if len(cur.Groups) >= maxPartnerGroups {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a program can have at most 50 groups")
		return
	}
	ctx := r.Context()
	var g partnerGroupView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO partner_groups (workspace_id, program_id, name) VALUES ($1, $2, $3)
			ON CONFLICT (program_id, name) DO NOTHING RETURNING id::text, name`, a.WorkspaceID, cur.ID, name).Scan(&g.ID, &g.Name)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConflict
		}
		if err != nil {
			return err
		}
		if in.Default {
			g.Default = true
			if _, err := tx.Exec(ctx, `UPDATE partner_programs SET default_group_id = $2, updated_at = now() WHERE id = $1`, cur.ID, g.ID); err != nil {
				return err
			}
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_program.group_created", "partner_program", cur.ID,
			map[string]any{"group_id": g.ID, "default": in.Default})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the program already has a group called "+name)
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

// deletePartnerGroup removes a group with its own rules; its partners
// move to no group.
func (s *Server) deletePartnerGroup(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	g := r.PathValue("group")
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM partner_groups WHERE workspace_id = $1 AND program_id = $2 AND id::text = $3`, a.WorkspaceID, cur.ID, g)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_program.group_deleted", "partner_program", cur.ID,
			map[string]any{"group_id": g})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rotatePartnerPostback makes a new incoming postback URL for a program
// and shows it once; the old one stops working.
func (s *Server) rotatePartnerPostback(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	secret, hash := auth.NewToken(auth.PrefixPostback)
	hint := secret[:len(auth.PrefixPostback)+4] + "…"
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE partner_programs SET postback_hash = $3, postback_hint = $4, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID, hash, hint); err != nil {
			return err
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_program.postback_rotated", "partner_program", cur.ID,
			map[string]any{"replaced": cur.PostbackHint != ""})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"url":  strings.TrimRight(s.publicURL, "/") + "/v1/partner-postbacks/" + secret,
		"hint": hint,
	})
}
