package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// Partners: a partner is a customer record that joined a program. Each
// membership has a ref code, a tracking campaign (channel affiliates,
// named after the code so a partner's name never appears in
// utm_campaign), promo codes and a short link, /r/<code> on the
// workspace's tracking domain.

const maxPartnerPromoCodes = 20

var partnerCodeRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// partnerPromoCode is a promo code tied to a partner: status local when it
// lives only in Outflare (you made it in your billing system yourself), or
// pending, created or failed when Outflare makes it in the billing account.
type partnerPromoCode struct {
	Code   string `json:"code"`
	Coupon string `json:"coupon"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

type partnerStats struct {
	Clicks         int   `json:"clicks"`
	Referrals      int   `json:"referrals"`
	Customers      int   `json:"customers"`
	SalesCents     int64 `json:"sales_cents"`
	PendingCents   int64 `json:"pending_cents"`
	ApprovedCents  int64 `json:"approved_cents"`
	PaidCents      int64 `json:"paid_cents"`
	BalanceCents   int64 `json:"balance_cents"`
	PendingActions int   `json:"pending_actions"`
}

type partnerView struct {
	ID           string             `json:"id"`
	ProgramID    string             `json:"program_id"`
	Program      string             `json:"program"`
	RecordID     string             `json:"record_id"`
	Name         string             `json:"name"`
	Email        string             `json:"email"`
	Code         string             `json:"code"`
	Status       string             `json:"status"`
	GroupID      *string            `json:"group_id"`
	CampaignID   *string            `json:"campaign_id"`
	Link         string             `json:"link"`
	CampaignLink string             `json:"campaign_link"`
	PromoCodes   []partnerPromoCode `json:"promo_codes"`
	PayoutMethod string             `json:"payout_method"`
	// PostbackURL is the partner's own tracker, called on each conversion
	// credited to them ({placeholders} filled in); "" for none.
	PostbackURL    string       `json:"postback_url"`
	Note           string       `json:"note"`
	Source         string       `json:"source"`
	Currency       string       `json:"currency"`
	Stats          partnerStats `json:"stats"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	landing        string
	payoutSealed   []byte
	brandID        *string
	campaignSlug   string
	campaignCode   string
	campaignSrc    string
	campaignMedium string
}

// partnerSelect reads memberships with their record, program, campaign
// and totals. Cash totals count cash, fees and adjustments.
const partnerSelect = `
	SELECT m.id::text, m.program_id::text, p.slug, p.brand_id::text, m.record_id::text, COALESCE(r.name, ''),
	       COALESCE((SELECT i.value FROM record_identities i WHERE i.record_id = m.record_id AND i.namespace = 'email' AND i.valid_to IS NULL
	                 ORDER BY i.verified DESC, i.valid_from LIMIT 1), ''),
	       m.code, m.status, m.group_id::text, m.campaign_id::text, m.payout_method, m.postback_url, m.note, m.source, p.currency, p.landing_url,
	       COALESCE(c.slug, ''), COALESCE(c.code, ''), COALESCE(c.source, ''), COALESCE(c.medium, ''),
	       COALESCE((SELECT json_agg(json_build_object('code', pc.code, 'coupon', pc.coupon, 'status', pc.sync_status, 'error', pc.sync_error)
	                 ORDER BY pc.created_at) FROM partner_promo_codes pc WHERE pc.membership_id = m.id), '[]'),
	       (SELECT count(*) FROM partner_clicks k WHERE k.membership_id = m.id)::int,
	       (SELECT count(*) FROM partner_referrals f WHERE f.membership_id = m.id)::int,
	       (SELECT count(*) FROM partner_referrals f WHERE f.membership_id = m.id AND f.first_sale_at IS NOT NULL)::int,
	       COALESCE((SELECT sum(CASE WHEN v.type = 'sale' THEN v.amount_cents ELSE -v.amount_cents END) FROM partner_conversions v
	                 JOIN partner_referrals f ON f.id = v.referral_id WHERE f.membership_id = m.id AND v.type IN ('sale', 'refund')), 0)::bigint,
	       COALESCE((SELECT sum(w.amount_cents) FILTER (WHERE w.status = 'pending')
	                 FROM partner_rewards w WHERE w.membership_id = m.id AND w.side = 'partner' AND w.kind <> 'action'), 0)::bigint,
	       COALESCE((SELECT sum(w.amount_cents) FILTER (WHERE w.status = 'approved')
	                 FROM partner_rewards w WHERE w.membership_id = m.id AND w.side = 'partner' AND w.kind <> 'action'), 0)::bigint,
	       COALESCE((SELECT sum(w.amount_cents) FILTER (WHERE w.status = 'paid')
	                 FROM partner_rewards w WHERE w.membership_id = m.id AND w.side = 'partner' AND w.kind <> 'action'), 0)::bigint,
	       COALESCE((SELECT sum(w.amount_cents) FILTER (WHERE w.status = 'approved' AND w.payout_id IS NULL)
	                 FROM partner_rewards w WHERE w.membership_id = m.id AND w.side = 'partner' AND w.kind <> 'action'), 0)::bigint,
	       (SELECT count(*) FROM partner_rewards w WHERE w.membership_id = m.id AND w.kind = 'action' AND w.status = 'approved')::int,
	       m.created_at, m.updated_at
	FROM partner_memberships m
	JOIN partner_programs p ON p.id = m.program_id
	LEFT JOIN records r ON r.id = m.record_id
	LEFT JOIN tracking_campaigns c ON c.id = m.campaign_id
	WHERE m.workspace_id = $1`

func scanPartner(row pgx.Row) (partnerView, error) {
	var v partnerView
	st := &v.Stats
	err := row.Scan(&v.ID, &v.ProgramID, &v.Program, &v.brandID, &v.RecordID, &v.Name, &v.Email, &v.Code, &v.Status, &v.GroupID, &v.CampaignID,
		&v.payoutSealed, &v.PostbackURL, &v.Note, &v.Source, &v.Currency, &v.landing, &v.campaignSlug, &v.campaignCode, &v.campaignSrc, &v.campaignMedium,
		&v.PromoCodes, &st.Clicks, &st.Referrals, &st.Customers, &st.SalesCents, &st.PendingCents, &st.ApprovedCents, &st.PaidCents,
		&st.BalanceCents, &st.PendingActions, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// partnerLinkBase is where a program's short links point: its portal's
// own domain or subdomain when its portal is on, else the workspace's
// verified tracking domain, else this server with the workspace's id.
func (s *Server) partnerLinkBase(ctx context.Context, ws, program string) (string, error) {
	var domain string
	var sub *string
	var portal bool
	err := s.pool.QueryRow(ctx, `SELECT p.portal_enabled, p.portal_subdomain, COALESCE((SELECT d.domain FROM tracking_domains d
		WHERE d.id = p.portal_domain_id AND d.status = 'verified' AND (d.https_status IS NULL OR d.https_status = 'active')), '')
		FROM partner_programs p WHERE p.id = $1`, program).Scan(&portal, &sub, &domain)
	if err != nil {
		return "", err
	}
	switch {
	case portal && domain != "":
		return "https://" + domain + "/r/", nil
	case portal && sub != nil && s.partnersDomain != "":
		return "https://" + *sub + "." + s.partnersDomain + "/r/", nil
	}
	err = s.pool.QueryRow(ctx, `SELECT domain FROM tracking_domains d WHERE workspace_id = $1 AND status = 'verified'
		AND NOT EXISTS (SELECT 1 FROM partner_programs p WHERE p.portal_domain_id = d.id) ORDER BY created_at LIMIT 1`, ws).Scan(&domain)
	if err == nil {
		return "https://" + domain + "/r/", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	return strings.TrimRight(s.publicURL, "/") + "/r/" + ws + "/", nil
}

// fill adds the partner's links.
func (v *partnerView) fill(base string, box *secrets.Box) {
	if len(v.payoutSealed) > 0 {
		if b, err := box.Open(v.payoutSealed, []byte(v.ID)); err == nil {
			v.PayoutMethod = string(b)
		}
	}
	v.Link = base + url.PathEscape(v.Code)
	if v.landing != "" && v.campaignSlug != "" {
		v.CampaignLink, _ = utm.Tag(v.landing, utm.Tags{Source: v.campaignSrc, Medium: v.campaignMedium, Campaign: v.campaignSlug, Code: v.campaignCode, Ref: v.Code})
	}
}

func (s *Server) loadPartner(ctx context.Context, ws, id string) (partnerView, error) {
	if !validUUID(id) {
		return partnerView{}, errNotFound
	}
	v, err := scanPartner(s.pool.QueryRow(ctx, partnerSelect+` AND m.id = $2`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	if err != nil {
		return v, err
	}
	base, err := s.partnerLinkBase(ctx, ws, v.ProgramID)
	v.fill(base, s.box)
	return v, err
}

func (s *Server) findPartner(w http.ResponseWriter, r *http.Request, ws, id string) (*partnerView, bool) {
	v, err := s.loadPartner(r.Context(), ws, id)
	if err == nil {
		err = partnerBrandAccess(r, accessFrom(r.Context()), v.brandID)
	}
	if errors.As(err, new(errDenied)) {
		writeError(w, r, http.StatusForbidden, "forbidden", err.Error())
		return nil, false
	}
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "partner not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return &v, true
}

func (s *Server) listPartners(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	status, program, record, details := q.Get("status"), q.Get("program"), q.Get("record_id"), q.Get("payout_details")
	if status != "" && !slices.Contains([]string{"pending", "active", "suspended", "rejected"}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is pending, active, suspended or rejected")
		return
	}
	if details != "" && details != "missing" && details != "set" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "payout_details is missing or set")
		return
	}
	if record != "" && !validUUID(record) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "record_id is a record's id")
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
	args := []any{a.WorkspaceID, status, strings.ToLower(program), record, before, limit + 1, details}
	rows, err := s.pool.Query(r.Context(), partnerSelect+`
		AND ($2 = '' OR m.status = $2) AND ($3 = '' OR p.id::text = $3 OR p.slug = $3) AND ($4 = '' OR m.record_id::text = $4)
		AND ($5::timestamptz IS NULL OR m.created_at < $5) AND ($7 = '' OR (m.payout_method IS NULL) = ($7 = 'missing'))`+sel.sql("p.brand_id", &args)+`
		ORDER BY m.created_at DESC LIMIT $6`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (partnerView, error) { return scanPartner(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	bases := map[string]string{}
	var next *time.Time
	if len(list) > limit {
		list = list[:limit]
		next = &list[limit-1].CreatedAt
	}
	for i := range list {
		base, ok := bases[list[i].ProgramID]
		if !ok {
			if base, err = s.partnerLinkBase(r.Context(), a.WorkspaceID, list[i].ProgramID); err != nil {
				s.internal(w, r, err)
				return
			}
			bases[list[i].ProgramID] = base
		}
		list[i].fill(base, s.box)
	}
	if list == nil {
		list = []partnerView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

func (s *Server) getPartner(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findPartner(w, r, a.WorkspaceID, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

type partnerInput struct {
	Program      *string `json:"program"`
	RecordID     *string `json:"record_id"`
	Email        *string `json:"email"`
	Name         *string `json:"name"`
	Code         *string `json:"code"`
	Status       *string `json:"status"`
	GroupID      *string `json:"group_id"`
	PayoutMethod *string `json:"payout_method"`
	PostbackURL  *string `json:"postback_url"`
	Note         *string `json:"note"`
}

func (in partnerInput) check() error {
	switch {
	case in.Status != nil && !slices.Contains([]string{"pending", "active", "suspended", "rejected"}, *in.Status):
		return errInvalid("status is pending, active, suspended or rejected")
	case in.Code != nil && !partnerCodeRE.MatchString(*in.Code):
		return errInvalid("code is 1 to 64 letters, digits, hyphens or underscores, such as ana or ana-20")
	case in.PayoutMethod != nil && len(*in.PayoutMethod) > 500:
		return errInvalid("payout_method is at most 500 characters; never store bank details here")
	case in.Note != nil && len(*in.Note) > 2000:
		return errInvalid("note is at most 2,000 characters")
	case in.PostbackURL != nil && checkPostbackURL(*in.PostbackURL) != nil:
		return checkPostbackURL(*in.PostbackURL)
	case in.Name != nil && len(*in.Name) > 200:
		return errInvalid("name is at most 200 characters")
	}
	return nil
}

// newPartnerCode makes a friendly ref code from a name or address: the
// first word, then digits when that is taken.
func newPartnerCode(ctx context.Context, tx pgx.Tx, ws, hint string) (string, error) {
	hint = strings.ToLower(hint)
	if at := strings.IndexByte(hint, '@'); at > 0 {
		hint = hint[:at]
	}
	base := strings.Trim(utm.Slug(strings.Fields(hint + " partner")[0]), "-")
	if len(base) > 20 {
		base = base[:20]
	}
	if base == "" {
		base = "partner"
	}
	for i := 0; i < 20; i++ {
		code := base
		if i > 0 {
			n, _ := rand.Int(rand.Reader, big.NewInt(9000))
			code = base + n.Add(n, big.NewInt(1000)).String()
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM partner_memberships WHERE workspace_id = $1 AND lower(code) = $2)
			OR EXISTS (SELECT 1 FROM partner_promo_codes WHERE workspace_id = $1 AND lower(code) = $2)`, ws, code).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return code, nil
		}
	}
	return "", errors.New("partners: no free code for " + base)
}

// codeFree reports whether no partner or promo code in the workspace uses code.
func codeFree(ctx context.Context, tx pgx.Tx, ws, code string) (bool, error) {
	var taken bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM partner_memberships WHERE workspace_id = $1 AND lower(code) = lower($2))
		OR EXISTS (SELECT 1 FROM partner_promo_codes WHERE workspace_id = $1 AND lower(code) = lower($2))`, ws, code).Scan(&taken)
	return !taken, err
}

// partnerRecord finds the partner's record by id, or the person with an
// email, making one when nobody has it.
func (s *Server) partnerRecord(ctx context.Context, tx pgx.Tx, ws string, recordID, email, name *string) (string, error) {
	var rec string
	switch {
	case recordID != nil && *recordID != "":
		if !validUUID(*recordID) {
			return "", errInvalid("record_id is a record's id")
		}
		err := tx.QueryRow(ctx, `SELECT id::text FROM records WHERE workspace_id = $1 AND id = $2 AND merged_into IS NULL`, ws, *recordID).Scan(&rec)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errInvalid("record_id: no such record in this workspace")
		}
		if err != nil {
			return "", err
		}
	case email != nil && *email != "":
		e, err := normalizeEmail(*email)
		if err != nil || e == "" {
			return "", errInvalid("email is not a valid address")
		}
		if rec, err = customers.EnsurePerson(ctx, tx, ws, "email", e, false, RequestID(ctx)); err != nil {
			return "", err
		}
	default:
		return "", errInvalid("record_id or email is required: a partner is a customer record")
	}
	if name != nil && strings.TrimSpace(*name) != "" {
		if _, err := tx.Exec(ctx, `UPDATE records SET name = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2 AND name = ''`,
			rec, ws, cutUTF8(strings.TrimSpace(*name), 200)); err != nil {
			return "", err
		}
	}
	return rec, nil
}

// partnerJoin is a record joining a program.
type partnerJoin struct {
	rec, code, status, source string
	group                     *string
	payout, note              string
	// From the portal's sign-up form.
	application map[string]any
	termsAt     *time.Time
}

// joinPartner adds a record to a program with its code and campaign, in
// tx. p is who did it: nil for the partner signing up on the portal.
func (s *Server) joinPartner(ctx context.Context, tx pgx.Tx, ws string, p *principal, prog *partnerProgramView, j partnerJoin) (string, error) {
	rec, code, status, source := j.rec, j.code, j.status, j.source
	actor := ""
	if p != nil {
		actor = p.ActorID
	}
	if j.application == nil {
		j.application = map[string]any{}
	}
	if code == "" {
		var hint string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(r.name, ''), (SELECT i.value FROM record_identities i WHERE i.record_id = r.id
			AND i.namespace = 'email' AND i.valid_to IS NULL LIMIT 1), '') FROM records r WHERE r.id = $1`, rec).Scan(&hint); err != nil {
			return "", err
		}
		var err error
		if code, err = newPartnerCode(ctx, tx, ws, hint); err != nil {
			return "", err
		}
	} else if free, err := codeFree(ctx, tx, ws, code); err != nil {
		return "", err
	} else if !free {
		return "", errCodeTaken
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO partner_memberships (workspace_id, program_id, record_id, code, status, group_id, note, source, created_by,
		application, terms_accepted_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT (program_id, record_id) DO NOTHING RETURNING id::text`,
		ws, prog.ID, rec, code, status, j.group, j.note, source, nullIfEmpty(actor), j.application, j.termsAt).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errPartnerExists
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE partner_memberships SET payout_method = $2 WHERE id = $1`, id, s.sealPayout(id, j.payout)); err != nil {
		return "", err
	}
	c, created, err := utm.Ensure(ctx, tx, ws, utm.Spec{Channel: "affiliates", Source: "partner", Name: code,
		BrandID: prog.BrandID, ObjectKind: "partner_program", ObjectID: id})
	if err != nil {
		return "", err
	}
	if created {
		if err := record(ctx, tx, p, ws, "tracking_campaign.created", "tracking_campaign", c.ID,
			map[string]any{"channel": "affiliates", "brand_id": prog.BrandID, "via": "partner_program"}); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE partner_memberships SET campaign_id = $2 WHERE id = $1`, id, c.ID); err != nil {
		return "", err
	}
	if _, err := customers.Record(ctx, tx, ws, customers.Event{RecordID: rec, Name: "partner.joined", Source: "partners",
		Properties: map[string]any{"program_id": prog.ID, "program_kind": prog.Kind, "partner_id": id, "status": status},
		MessageID:  "partner.joined:" + id, RequestID: RequestID(ctx)}); err != nil {
		return "", err
	}
	return id, partnerRecord(ctx, tx, p, ws, "partner.created", "partner", id,
		map[string]any{"program_id": prog.ID, "program_kind": prog.Kind, "record_id": rec, "status": status, "source": source})
}

// sealPayout seals a partner's free-text payout method, bound to their
// membership; "" stores nothing.
func (s *Server) sealPayout(id, method string) []byte {
	method = strings.TrimSpace(method)
	if method == "" {
		return nil
	}
	return s.box.Seal([]byte(method), []byte(id))
}

var (
	errPartnerExists = errors.New("partner exists")
	errCodeTaken     = errors.New("code taken")
)

func (s *Server) createPartner(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in partnerInput
	if !decode(w, r, &in) {
		return
	}
	if in.Program == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "program (its id or slug) is required")
		return
	}
	if s.allocError(w, r, in.check()) {
		return
	}
	prog, ok := s.findPartnerProgram(w, r, a.WorkspaceID, *in.Program)
	if !ok {
		return
	}
	if prog.Status == "archived" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the program is archived; restore it first")
		return
	}
	if in.GroupID != nil && !slices.ContainsFunc(prog.Groups, func(g partnerGroupView) bool { return g.ID == *in.GroupID }) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "group_id is not a group of this program")
		return
	}
	status := coalesce(in.Status, "active")
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := s.partnerRecord(ctx, tx, a.WorkspaceID, in.RecordID, in.Email, in.Name)
		if err != nil {
			return err
		}
		id, err = s.joinPartner(ctx, tx, a.WorkspaceID, &a.principal, prog, partnerJoin{rec: rec, code: deref(in.Code), status: status, source: "api",
			group: in.GroupID, payout: deref(in.PayoutMethod), note: deref(in.Note)})
		return err
	})
	switch {
	case errors.Is(err, errPartnerExists):
		writeError(w, r, http.StatusConflict, "conflict", "this person is already a partner in the program; PATCH their membership instead")
		return
	case errors.Is(err, errCodeTaken):
		writeError(w, r, http.StatusConflict, "conflict", "another partner or promo code already uses the code "+deref(in.Code))
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	if v, ok := s.findPartner(w, r, a.WorkspaceID, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

// updatePartner approves, suspends or rejects a partner, moves them to
// another group, or changes their payout method or note. The code stays:
// links already shared carry it.
func (s *Server) updatePartner(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartner(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in partnerInput
	if !decode(w, r, &in) {
		return
	}
	switch {
	case in.Program != nil && *in.Program != cur.ProgramID && *in.Program != cur.Program,
		in.RecordID != nil && *in.RecordID != cur.RecordID, in.Email != nil, in.Name != nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a membership's program and person can't change; add the person to the other program instead")
		return
	case in.Code != nil && *in.Code != cur.Code:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a partner's code can't change, because links already shared carry it; add a promo code instead")
		return
	}
	if s.allocError(w, r, in.check()) {
		return
	}
	group := cur.GroupID
	if in.GroupID != nil {
		group = in.GroupID
		if *in.GroupID == "" {
			group = nil
		} else {
			var ok bool
			if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM partner_groups WHERE program_id = $1 AND id::text = $2)`,
				cur.ProgramID, *in.GroupID).Scan(&ok); err != nil {
				s.internal(w, r, err)
				return
			}
			if !ok {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "group_id is not a group of this program; \"\" is no group")
				return
			}
		}
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if in.PayoutMethod != nil {
			if _, err := tx.Exec(ctx, `UPDATE partner_memberships SET payout_method = $2 WHERE id = $1`, cur.ID, s.sealPayout(cur.ID, *in.PayoutMethod)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE partner_memberships SET status = COALESCE($3, status), group_id = $4,
			note = COALESCE($5, note), postback_url = COALESCE($6, postback_url), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID, in.Status, group, in.Note, trimPtr(in.PostbackURL)); err != nil {
			return err
		}
		data := map[string]any{"program_id": cur.ProgramID, "record_id": cur.RecordID, "status": coalesce(in.Status, cur.Status),
			"previous_status": cur.Status, "group_id": group}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner.updated", "partner", cur.ID, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	if v, ok := s.findPartner(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// addPartnerPromoCode ties a promo code (as customers type it at checkout)
// to a partner, so a conversion that reports it credits them.
func (s *Server) addPartnerPromoCode(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartner(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in struct {
		Code string `json:"code"`
		// Coupon makes the code in the workspace's billing account too,
		// for this coupon (its id there).
		Coupon string `json:"coupon"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !partnerCodeRE.MatchString(in.Code) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "code is 1 to 64 letters, digits, hyphens or underscores, such as ANA20")
		return
	}
	if in.Coupon = strings.TrimSpace(in.Coupon); len(in.Coupon) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "coupon is the id of a coupon in your billing account")
		return
	}
	var integration *string
	if in.Coupon != "" {
		var err error
		if integration, err = s.promoCodeConnection(r.Context(), a.WorkspaceID, cur.ProgramID); err != nil {
			s.internal(w, r, err)
			return
		}
		if integration == nil {
			writeError(w, r, http.StatusConflict, "connector_needed", "connect a billing account that can make promo codes (Stripe, with Write access to Promotion codes), or leave coupon out and make the code there yourself")
			return
		}
	}
	if len(cur.PromoCodes) >= maxPartnerPromoCodes {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a partner can have at most 20 promo codes")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// A partner's own ref code may double as their promo code.
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM partner_memberships WHERE workspace_id = $1 AND lower(code) = lower($2) AND id <> $3)
			OR EXISTS (SELECT 1 FROM partner_promo_codes WHERE workspace_id = $1 AND lower(code) = lower($2))`, a.WorkspaceID, in.Code, cur.ID).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return errConflict
		}
		var id string
		status := "local"
		if integration != nil {
			status = "pending"
		}
		if err := tx.QueryRow(ctx, `INSERT INTO partner_promo_codes (workspace_id, membership_id, code, coupon, integration_id, sync_status)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, a.WorkspaceID, cur.ID, in.Code, in.Coupon, integration, status).Scan(&id); err != nil {
			return err
		}
		if integration != nil {
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnerPromoCode, WorkspaceID: &a.WorkspaceID, Payload: partnerPromoJob{ID: id},
				MaxAttempts: 5, IdempotencyKey: "partner-promo:" + id}); err != nil {
				return err
			}
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner.promo_code_added", "partner", cur.ID,
			map[string]any{"program_id": cur.ProgramID, "promo_code_id": id})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "another partner or promo code already uses "+in.Code)
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	if v, ok := s.findPartner(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

// promoCodeConnection is the billing connection that makes a program's
// promo codes: its sales source's, else any connected one that can.
func (s *Server) promoCodeConnection(ctx context.Context, ws, program string) (*string, error) {
	var ids []string
	for _, d := range s.integrations.Registry.All() {
		if slices.Contains(d.Capabilities, connectors.CapPromoCodes) {
			ids = append(ids, d.ID)
		}
	}
	var id string
	err := s.pool.QueryRow(ctx, `SELECT i.id::text FROM integrations i, partner_programs p
		WHERE i.workspace_id = $1 AND p.id = $2 AND i.connector = ANY($3) AND i.status = 'connected'
		ORDER BY (i.connector = p.sales_source) DESC, i.created_at LIMIT 1`, ws, program, ids).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &id, err
}

func (s *Server) removePartnerPromoCode(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartner(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `DELETE FROM partner_promo_codes WHERE workspace_id = $1 AND membership_id = $2 AND lower(code) = lower($3) RETURNING id::text`,
			a.WorkspaceID, cur.ID, r.PathValue("code")).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner.promo_code_removed", "partner", cur.ID,
			map[string]any{"program_id": cur.ProgramID, "promo_code_id": id})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// partnerLink is a partner's short link: /r/<code> on the workspace's
// tracking domain, or /r/<workspace>/<code> on this server. It counts the
// click and sends the visitor to the program's landing page (or the page
// in ?to on the same site) tagged with the partner's campaign, ref and
// click id. ?sub (or sub_id) is the partner's own sub-id, kept as
// utm_content. A paused or inactive partner's link still lands, untagged.
func (s *Server) partnerLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code, wsRef := r.PathValue("code"), r.PathValue("ws")
	var ws string
	var err error
	if wsRef == "" {
		// A tracking domain, or a portal's subdomain of the partners domain.
		host := requestHost(r)
		err = s.pool.QueryRow(ctx, `SELECT workspace_id::text FROM tracking_domains WHERE domain = $1 AND status = 'verified'`, host).Scan(&ws)
		if sub, ok := strings.CutSuffix(host, "."+s.partnersDomain); errors.Is(err, pgx.ErrNoRows) && ok && s.partnersDomain != "" {
			err = s.pool.QueryRow(ctx, `SELECT workspace_id::text FROM partner_programs WHERE portal_subdomain = $1 AND portal_enabled`, sub).Scan(&ws)
		}
	} else {
		ws, err = s.workspaceIDFor(ctx, wsRef)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		metrics.PartnerClick("error")
		s.internal(w, r, err)
		return
	}
	notFound := func() {
		metrics.PartnerClick("not_found")
		http.Error(w, "This partner link is not valid.", http.StatusNotFound)
	}
	if ws == "" || !partnerCodeRE.MatchString(code) {
		notFound()
		return
	}
	var (
		id, status, progStatus, landing, slug, ccode, src, medium string
	)
	err = s.pool.QueryRow(ctx, `SELECT m.id::text, m.status, p.status, p.landing_url, COALESCE(c.slug, ''), COALESCE(c.code, ''), COALESCE(c.source, ''), COALESCE(c.medium, '')
		FROM partner_memberships m JOIN partner_programs p ON p.id = m.program_id LEFT JOIN tracking_campaigns c ON c.id = m.campaign_id
		WHERE m.workspace_id = $1 AND lower(m.code) = lower($2)`, ws, code).Scan(&id, &status, &progStatus, &landing, &slug, &ccode, &src, &medium)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && landing == "" {
		notFound()
		return
	}
	if err != nil {
		metrics.PartnerClick("error")
		s.internal(w, r, err)
		return
	}
	dest := landingPage(landing, r.URL.Query().Get("to"))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer-when-downgrade")
	if status != "active" || progStatus != "active" {
		metrics.PartnerClick("inactive")
		http.Redirect(w, r, dest, http.StatusFound)
		return
	}
	q := r.URL.Query()
	sub := q.Get("sub")
	if sub == "" {
		sub = q.Get("sub_id")
	}
	sub = cutUTF8(sub, 100)
	refHost := ""
	if u, err := url.Parse(r.Referer()); err == nil {
		refHost = cutUTF8(strings.ToLower(u.Hostname()), 255)
	}
	ip := sha256.Sum256([]byte(ws + "|" + clientIP(r)))
	var click string
	if err := s.pool.QueryRow(ctx, `INSERT INTO partner_clicks (workspace_id, membership_id, sub_id, referrer_host, ip_hash) VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
		ws, id, sub, refHost, ip[:]).Scan(&click); err != nil {
		// The visitor still lands; the click is lost.
		metrics.PartnerClick("error")
		s.log.ErrorContext(ctx, "partners: store click", "err", err, "workspace_id", ws)
		http.Redirect(w, r, dest, http.StatusFound)
		return
	}
	metrics.PartnerClick("tracked")
	content := utm.Value(sub)
	if utm.CheckContent(content) != nil {
		content = ""
	}
	tagged, err := utm.Tag(dest, utm.Tags{Source: src, Medium: medium, Campaign: slug, Content: content, Code: ccode, Ref: code})
	if err != nil {
		tagged = dest
	}
	http.Redirect(w, r, addQuery(tagged, "ref_click", click), http.StatusFound)
}

// landingPage is the program's landing page, or to when it is a path or
// an address on the same site (the same host or a subdomain of it).
func landingPage(landing, to string) string {
	to = strings.TrimSpace(to)
	if to == "" {
		return landing
	}
	base, err := url.Parse(landing)
	if err != nil {
		return landing
	}
	if strings.HasPrefix(to, "/") && !strings.HasPrefix(to, "//") && !strings.Contains(to, `\`) {
		u, err := base.Parse(to)
		if err != nil {
			return landing
		}
		return u.String()
	}
	dest, err := utm.Destination(to)
	if err != nil {
		return landing
	}
	u, _ := url.Parse(dest)
	h, own := strings.TrimPrefix(u.Hostname(), "www."), strings.TrimPrefix(base.Hostname(), "www.")
	if h != own && !strings.HasSuffix(h, "."+own) {
		return landing
	}
	return dest
}

// addQuery adds k=v to a link's query, before any fragment.
func addQuery(link, k, v string) string {
	u, err := url.Parse(link)
	if err != nil {
		return link
	}
	if u.RawQuery != "" {
		u.RawQuery += "&"
	}
	u.RawQuery += url.QueryEscape(k) + "=" + url.QueryEscape(v)
	return u.String()
}

// getPartnerProgramStats totals a program across all its partners, so the
// program's figures don't depend on how many partners a page holds.
func (s *Server) getPartnerProgramStats(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	prog, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var v struct {
		Partners map[string]int `json:"partners"`
		partnerStats
	}
	var active, pending, suspended, rejected, missing int
	st := &v.partnerStats
	err := s.pool.QueryRow(r.Context(), `
		WITH m AS (SELECT id, status, payout_method FROM partner_memberships WHERE workspace_id = $1 AND program_id = $2),
		     w AS (SELECT w.* FROM partner_rewards w JOIN m ON m.id = w.membership_id WHERE w.workspace_id = $1)
		SELECT (SELECT count(*) FILTER (WHERE status = 'active') FROM m)::int,
		       (SELECT count(*) FILTER (WHERE status = 'pending') FROM m)::int,
		       (SELECT count(*) FILTER (WHERE status = 'suspended') FROM m)::int,
		       (SELECT count(*) FILTER (WHERE status = 'rejected') FROM m)::int,
		       (SELECT count(*) FILTER (WHERE status = 'active' AND payout_method IS NULL) FROM m)::int,
		       (SELECT count(*) FROM partner_clicks k JOIN m ON m.id = k.membership_id WHERE k.workspace_id = $1)::int,
		       (SELECT count(*) FROM partner_referrals f JOIN m ON m.id = f.membership_id WHERE f.workspace_id = $1)::int,
		       (SELECT count(*) FROM partner_referrals f JOIN m ON m.id = f.membership_id WHERE f.workspace_id = $1 AND f.first_sale_at IS NOT NULL)::int,
		       COALESCE((SELECT sum(CASE WHEN v.type = 'sale' THEN v.amount_cents ELSE -v.amount_cents END) FROM partner_conversions v
		                 JOIN partner_referrals f ON f.id = v.referral_id JOIN m ON m.id = f.membership_id
		                 WHERE v.workspace_id = $1 AND v.type IN ('sale', 'refund')), 0)::bigint,
		       COALESCE((SELECT sum(amount_cents) FILTER (WHERE status = 'pending') FROM w WHERE side = 'partner' AND kind <> 'action'), 0)::bigint,
		       COALESCE((SELECT sum(amount_cents) FILTER (WHERE status = 'approved') FROM w WHERE side = 'partner' AND kind <> 'action'), 0)::bigint,
		       COALESCE((SELECT sum(amount_cents) FILTER (WHERE status = 'paid') FROM w WHERE side = 'partner' AND kind <> 'action'), 0)::bigint,
		       COALESCE((SELECT sum(amount_cents) FILTER (WHERE status = 'approved' AND payout_id IS NULL) FROM w WHERE side = 'partner' AND kind <> 'action'), 0)::bigint,
		       (SELECT count(*) FROM w WHERE kind = 'action' AND status = 'approved')::int`, a.WorkspaceID, prog.ID).
		Scan(&active, &pending, &suspended, &rejected, &missing, &st.Clicks, &st.Referrals, &st.Customers, &st.SalesCents, &st.PendingCents,
			&st.ApprovedCents, &st.PaidCents, &st.BalanceCents, &st.PendingActions)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v.Partners = map[string]int{"total": active + pending + suspended + rejected, "active": active, "pending": pending, "suspended": suspended, "rejected": rejected,
		"missing_payout_details": missing}
	writeJSON(w, http.StatusOK, v)
}
