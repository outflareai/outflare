package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
)

// Leads partners submit on the portal: the email of someone they say they
// referred. The portal setting lead_submissions decides what happens:
// review (the team approves each), auto (taken at once when nobody else
// has the person), or off. Taking one binds a referral via manual through
// the conversion path, so it follows the program's rules, flows and
// first-partner-keeps-them like any lead; a submitted lead is never a
// customer's own sign-up, so it records no signed_up event.

var leadSubmissionModes = []string{"", "review", "auto"}

func (s *Server) routesPartnerLeads(mux *http.ServeMux) {
	read, manage := auth.ScopePartnersRead, auth.ScopePartnersManage
	mux.HandleFunc("POST /p/{program}/api/leads", s.inPortal(s.portalSubmitLead))
	mux.HandleFunc("GET /p/{program}/api/leads", s.inPortal(s.portalLeads))
	s.twinRoute(mux, "GET", "partner-leads", s.inBrand, read, s.listPartnerLeads)
	s.twinRoute(mux, "POST", "partner-leads/{id}/approve", s.inBrand, manage, s.approvePartnerLead)
	s.twinRoute(mux, "POST", "partner-leads/{id}/reject", s.inBrand, manage, s.rejectPartnerLead)
}

// portalLeadView is a submitted lead as its partner sees it: no name or
// email, like their referred customers.
type portalLeadView struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) portalSubmitLead(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	p := c.program
	switch {
	case c.readOnly:
		writeError(w, r, http.StatusForbidden, "forbidden", "you are seeing the portal as this partner, read-only")
		return
	case p.settings.LeadSubmissions == "":
		writeError(w, r, http.StatusForbidden, "forbidden", "this program doesn't take leads from partners")
		return
	case c.partner.Status != "active":
		writeError(w, r, http.StatusForbidden, "forbidden", "you can submit leads once the program has accepted you")
		return
	}
	var in struct {
		Email string `json:"email"`
		Note  string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	email, err := normalizeEmail(in.Email)
	in.Note = strings.TrimSpace(in.Note)
	switch {
	case err != nil || email == "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "email is not a valid address")
		return
	case len(in.Note) > 1000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is at most 1,000 characters")
		return
	}
	if !s.portalLimit.allow(w, r, "lead:"+c.partner.ID) {
		return
	}
	ctx := r.Context()
	var id, rec, refused string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if rec, err = customers.EnsurePerson(ctx, tx, p.ws, "email", email, false, RequestID(ctx)); err != nil {
			return err
		}
		switch {
		case rec == c.partner.RecordID:
			refused = "self_referral"
		default:
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM partner_referrals WHERE program_id = $1 AND record_id = $2)`, p.id, rec).Scan(&taken); err != nil {
				return err
			}
			if taken {
				refused = "already_referred"
			}
		}
		if refused != "" {
			return nil
		}
		err = tx.QueryRow(ctx, `INSERT INTO partner_lead_submissions (workspace_id, brand_id, program_id, membership_id, record_id, note)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (program_id, record_id) WHERE status <> 'rejected' DO NOTHING RETURNING id::text`,
			p.ws, p.brandID, p.id, c.partner.ID, rec, in.Note).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			refused = "already_submitted"
			return nil
		}
		if err != nil {
			return err
		}
		return partnerRecord(ctx, tx, nil, p.ws, "partner_lead.submitted", "partner_lead", id,
			map[string]any{"program_id": p.id, "partner_id": c.partner.ID, "record_id": rec, "has_note": in.Note != "", "mode": p.settings.LeadSubmissions})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if refused != "" {
		msg := map[string]string{
			"self_referral":     "you can't submit yourself as a lead",
			"already_referred":  "this person is already a referred customer of this program",
			"already_submitted": "this person was already submitted as a lead",
		}[refused]
		writeError(w, r, http.StatusConflict, refused, msg)
		return
	}
	if p.settings.LeadSubmissions == "auto" {
		if _, err := s.takePartnerLead(ctx, p.ws, nil, id); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	v, err := s.portalLead(ctx, c.partner.ID, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

const portalLeadCols = `id::text, status, reason, note, created_at FROM partner_lead_submissions`

func scanPortalLead(row pgx.Row) (portalLeadView, error) {
	var v portalLeadView
	err := row.Scan(&v.ID, &v.Status, &v.Reason, &v.Note, &v.CreatedAt)
	if err == nil {
		v.Label = "Lead " + strings.ToUpper(v.ID[:6])
	}
	return v, err
}

func (s *Server) portalLead(ctx context.Context, membership, id string) (portalLeadView, error) {
	return scanPortalLead(s.pool.QueryRow(ctx, `SELECT `+portalLeadCols+` WHERE membership_id = $1 AND id = $2`, membership, id))
}

func (s *Server) portalLeads(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	rows, err := s.pool.Query(r.Context(), `SELECT `+portalLeadCols+` WHERE membership_id = $1 ORDER BY created_at DESC LIMIT 500`, c.partner.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (portalLeadView, error) { return scanPortalLead(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []portalLeadView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// partnerLeadView is a submitted lead as the team sees it.
type partnerLeadView struct {
	ID          string     `json:"id"`
	ProgramID   string     `json:"program_id"`
	PartnerID   string     `json:"partner_id"`
	PartnerName string     `json:"partner_name"`
	RecordID    string     `json:"record_id"`
	Name        string     `json:"name"`
	Email       string     `json:"email"`
	Note        string     `json:"note"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason"`
	ReferralID  *string    `json:"referral_id"`
	DecidedAt   *time.Time `json:"decided_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

const partnerLeadSelect = `SELECT l.id::text, l.program_id::text, l.membership_id::text, COALESCE(pr.name, ''), l.record_id::text, COALESCE(lr.name, ''),
		COALESCE((SELECT i.value FROM record_identities i WHERE i.record_id = l.record_id AND i.namespace = 'email' AND i.valid_to IS NULL ORDER BY i.verified DESC, i.valid_from LIMIT 1), ''),
		l.note, l.status, l.reason, l.referral_id::text, l.decided_at, l.created_at
	FROM partner_lead_submissions l
	JOIN partner_memberships m ON m.id = l.membership_id
	LEFT JOIN records pr ON pr.id = m.record_id
	LEFT JOIN records lr ON lr.id = l.record_id`

func scanPartnerLead(row pgx.Row) (partnerLeadView, error) {
	var v partnerLeadView
	err := row.Scan(&v.ID, &v.ProgramID, &v.PartnerID, &v.PartnerName, &v.RecordID, &v.Name, &v.Email, &v.Note, &v.Status, &v.Reason,
		&v.ReferralID, &v.DecidedAt, &v.CreatedAt)
	return v, err
}

func (s *Server) listPartnerLeads(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	program, status := strings.ToLower(q.Get("program")), q.Get("status")
	if status != "" && !slices.Contains([]string{"pending", "approved", "rejected"}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is pending, approved or rejected")
		return
	}
	limit, ok := pageLimit(w, r, 100, 500)
	if !ok {
		return
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, program, status, before, limit + 1}
	rows, err := s.pool.Query(r.Context(), partnerLeadSelect+` JOIN partner_programs p ON p.id = l.program_id
		WHERE l.workspace_id = $1 AND ($2 = '' OR p.id::text = $2 OR p.slug = $2) AND ($3 = '' OR l.status = $3)
		  AND ($4::timestamptz IS NULL OR l.created_at < $4)`+sel.sql("l.brand_id", &args)+`
		ORDER BY l.created_at DESC LIMIT $5`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (partnerLeadView, error) { return scanPartnerLead(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *time.Time
	if len(list) > limit {
		list = list[:limit]
		next = &list[limit-1].CreatedAt
	}
	if list == nil {
		list = []partnerLeadView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}

// onePartnerLead finds a pending or decided lead the caller may see.
func (s *Server) onePartnerLead(w http.ResponseWriter, r *http.Request) (partnerLeadView, bool) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return partnerLeadView{}, false
	}
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "lead not found")
		return partnerLeadView{}, false
	}
	args := []any{a.WorkspaceID, id}
	v, err := scanPartnerLead(s.pool.QueryRow(r.Context(), partnerLeadSelect+` WHERE l.workspace_id = $1 AND l.id = $2`+sel.sql("l.brand_id", &args), args...))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "lead not found")
		return v, false
	}
	if err != nil {
		s.internal(w, r, err)
		return v, false
	}
	return v, true
}

func (s *Server) approvePartnerLead(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.onePartnerLead(w, r)
	if !ok {
		return
	}
	if cur.Status != "pending" {
		writeError(w, r, http.StatusConflict, "conflict", "this lead was already "+cur.Status)
		return
	}
	if _, err := s.takePartnerLead(r.Context(), a.WorkspaceID, &a.principal, cur.ID); err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.onePartnerLead(w, r); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) rejectPartnerLead(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.onePartnerLead(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	switch {
	case cur.Status != "pending":
		writeError(w, r, http.StatusConflict, "conflict", "this lead was already "+cur.Status)
		return
	case len(in.Reason) > 500:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "reason is at most 500 characters")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE partner_lead_submissions SET status = 'rejected', reason = $3, decided_at = now()
			WHERE workspace_id = $1 AND id = $2 AND status = 'pending'`, a.WorkspaceID, cur.ID, in.Reason)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_lead.rejected", "partner_lead", cur.ID,
			map[string]any{"program_id": cur.ProgramID, "partner_id": cur.PartnerID, "has_note": in.Reason != ""})
	})
	if s.allocError(w, r, err) {
		return
	}
	if v, ok := s.onePartnerLead(w, r); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// takePartnerLead binds a pending lead to its partner as a referral, via
// the conversion path. A lead the program can't credit (someone else
// referred them first, the partner isn't active) is rejected with the
// reason. It returns the lead's new status.
func (s *Server) takePartnerLead(ctx context.Context, ws string, p *principal, id string) (string, error) {
	var program, rec, code, membership string
	var taken bool
	err := s.pool.QueryRow(ctx, `SELECT l.program_id::text, l.record_id::text, m.code, m.id::text,
			EXISTS (SELECT 1 FROM partner_referrals f WHERE f.program_id = l.program_id AND f.record_id = l.record_id)
		FROM partner_lead_submissions l JOIN partner_memberships m ON m.id = l.membership_id
		WHERE l.workspace_id = $1 AND l.id = $2 AND l.status = 'pending'`, ws, id).Scan(&program, &rec, &code, &membership, &taken)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	status, reason := "approved", ""
	var referral *string
	if taken {
		// Someone was credited with them while the lead waited: the first
		// partner keeps a customer.
		status, reason = "rejected", "already_referred"
	} else {
		in := conversionInput{Type: "signup", Program: program, RecordID: rec, Ref: code}
		if err := in.check(time.Now()); err != nil {
			return "", err
		}
		res, err := s.recordPartnerConversion(ctx, ws, p, in, "manual")
		if errors.Is(err, errConflict) {
			res, err = s.recordPartnerConversion(ctx, ws, p, in, "manual")
		}
		if err != nil {
			return "", err
		}
		switch {
		case res.PartnerID != nil && *res.PartnerID != membership:
			status, reason = "rejected", "already_referred"
		case !res.Credited || res.ReferralID == nil:
			status, reason = "rejected", res.Reason
			if reason == "" {
				reason = "not_credited"
			}
		default:
			referral = res.ReferralID
		}
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE partner_lead_submissions SET status = $3, reason = $4, referral_id = $5, decided_at = now()
			WHERE workspace_id = $1 AND id = $2 AND status = 'pending'`, ws, id, status, reason, referral)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return partnerRecord(ctx, tx, p, ws, "partner_lead."+status, "partner_lead", id,
			map[string]any{"program_id": program, "partner_id": membership, "reason": reason, "by": map[bool]string{true: "team", false: "auto"}[p != nil]})
	})
	return status, err
}
