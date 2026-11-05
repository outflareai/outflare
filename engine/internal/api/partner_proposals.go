package api

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/utm"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

type proposalDeliverable struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Channel       string `json:"channel"`
	Format        string `json:"format"`
	PlacementURL  string `json:"placement_url"`
	Requirements  string `json:"requirements"`
	Deadline      string `json:"deadline"`
	LiveDays      int    `json:"live_days"`
	LinkURL       string `json:"link_url"`
	LinkText      string `json:"link_text"`
	LinkAttribute string `json:"link_attribute"`
}
type proposalMilestone struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	DeliverableID string `json:"deliverable_id"`
	Amount        int64  `json:"amount_cents"`
	Benefit       string `json:"benefit"`
	WaitDays      int    `json:"wait_days"`
}
type proposalConfig struct {
	PartnerSiteName    string                `json:"partner_site_name"`
	PartnerSiteURL     string                `json:"partner_site_url"`
	PartnerSiteIconURL string                `json:"partner_site_icon_url"`
	Title              string                `json:"title"`
	Brief              string                `json:"brief"`
	RequestQuote       bool                  `json:"request_quote"`
	ReplyTo            string                `json:"reply_to"`
	BrandKitURL        string                `json:"brand_kit_url"`
	ProductURL         string                `json:"product_url"`
	Terms              string                `json:"terms"`
	CommissionTerms    string                `json:"commission_terms"`
	Deliverables       []proposalDeliverable `json:"deliverables"`
	Milestones         []proposalMilestone   `json:"milestones"`
}
type proposalDeliveryProgress struct {
	URL         string     `json:"url"`
	Note        string     `json:"note"`
	SubmittedAt *time.Time `json:"submitted_at"`
	VerifiedAt  *time.Time `json:"verified_at"`
}
type proposalMilestoneProgress struct {
	ApprovedAt *time.Time `json:"approved_at"`
	Note       string     `json:"note"`
	RewardIDs  []string   `json:"reward_ids"`
}
type proposalProgress struct {
	Deliverables map[string]proposalDeliveryProgress  `json:"deliverables"`
	Milestones   map[string]proposalMilestoneProgress `json:"milestones"`
	InvoiceNote  string                               `json:"invoice_note"`
	QuotedCents  int64                                `json:"quoted_cents"`
	QuoteNote    string                               `json:"quote_note"`
}
type proposalView struct {
	ID            string           `json:"id"`
	Member        string           `json:"membership_id"`
	Record        string           `json:"record_id"`
	Recipient     string           `json:"recipient"`
	Status        string           `json:"status"`
	Version       int              `json:"version"`
	Config        proposalConfig   `json:"config"`
	Progress      proposalProgress `json:"progress"`
	AcceptedAt    *time.Time       `json:"accepted_at"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
	InvitationURL string           `json:"invitation_url,omitempty"`
}

func (c *proposalConfig) check() error {
	if len(c.PartnerSiteName) > 160 {
		return errInvalid("partner_site_name is at most 160 characters")
	}
	for i, raw := range []string{c.PartnerSiteURL, c.PartnerSiteIconURL} {
		if raw != "" {
			u, err := utm.Destination(raw)
			if err != nil || len(raw) > 2000 {
				return errInvalid("invitation website and icon must be full web addresses")
			}
			if i == 1 && !strings.HasPrefix(u, "https://") {
				return errInvalid("the invitation icon must use https")
			}
		}
	}

	if strings.TrimSpace(c.Title) == "" || len(c.Title) > 160 || strings.TrimSpace(c.Brief) == "" || len(c.Brief) > 10000 || len(c.Terms) > 10000 || len(c.CommissionTerms) > 2000 || len(c.Deliverables) < 1 || len(c.Deliverables) > 30 || len(c.Milestones) > 30 {
		return errInvalid("a proposal needs a title, brief and 1 to 30 deliverables; text or milestone limits exceeded")
	}
	a, err := mail.ParseAddress(c.ReplyTo)
	if err != nil || a.Address != c.ReplyTo {
		return errInvalid("reply_to must be the brand email address for discussing changes")
	}
	ids := map[string]bool{}
	for _, d := range c.Deliverables {
		if !fieldKeyRE.MatchString(d.ID) || ids[d.ID] || strings.TrimSpace(d.Title) == "" || len(d.Title) > 200 || len(d.Channel) > 100 || len(d.Format) > 100 || len(d.Requirements) > 10000 || len(d.Deadline) > 200 || len(d.LinkText) > 500 || len(d.LinkAttribute) > 100 || d.LiveDays < 0 || d.LiveDays > 3650 {
			return errInvalid("deliverables need unique lowercase ids, a title and valid timing")
		}
		ids[d.ID] = true
		for _, u := range []string{d.PlacementURL, d.LinkURL} {
			if u != "" {
				if _, err := utm.Destination(u); err != nil {
					return errInvalid("deliverable links must be full http or https addresses")
				}
			}
		}
	}
	seen := map[string]bool{}
	for _, m := range c.Milestones {
		if !fieldKeyRE.MatchString(m.ID) || seen[m.ID] || !ids[m.DeliverableID] || strings.TrimSpace(m.Title) == "" || len(m.Title) > 200 || m.Amount < 0 || m.Amount > 1e8 || len(m.Benefit) > 1000 || m.WaitDays < 0 || m.WaitDays > 3650 {
			return errInvalid("milestones need unique ids, a linked deliverable, title and valid amounts/timing")
		}
		seen[m.ID] = true
	}
	for _, u := range []string{c.BrandKitURL, c.ProductURL} {
		if u != "" {
			if _, err := utm.Destination(u); err != nil {
				return errInvalid("brand kit and product links must be full addresses")
			}
		}
	}
	if c.Milestones == nil {
		c.Milestones = []proposalMilestone{}
	}
	return nil
}

const proposalSelect = `SELECT x.id::text,x.membership_id::text,x.record_id::text,COALESCE(NULLIF(r.name,''),m.code),x.status,x.version,x.config,x.progress,x.accepted_at,x.created_at,x.updated_at FROM partner_proposals x JOIN partner_memberships m ON m.id=x.membership_id LEFT JOIN records r ON r.id=x.record_id WHERE x.workspace_id=$1 AND x.program_id=$2 AND x.brand_id IS NOT DISTINCT FROM $3::uuid`

func scanProposal(row pgx.Row) (proposalView, error) {
	var v proposalView
	err := row.Scan(&v.ID, &v.Member, &v.Record, &v.Recipient, &v.Status, &v.Version, &v.Config, &v.Progress, &v.AcceptedAt, &v.CreatedAt, &v.UpdatedAt)
	if v.Progress.Deliverables == nil {
		v.Progress.Deliverables = map[string]proposalDeliveryProgress{}
	}
	if v.Progress.Milestones == nil {
		v.Progress.Milestones = map[string]proposalMilestoneProgress{}
	}
	return v, err
}
func (s *Server) routesPartnerProposals(mux *http.ServeMux) {
	b := "partner-programs/{id}/proposals"
	s.twinRoute(mux, "GET", b, s.inBrand, auth.ScopePartnersRead, s.listPartnerProposals)
	s.twinRoute(mux, "POST", b, s.inBrand, auth.ScopePartnersManage, s.createPartnerProposal)
	s.twinRoute(mux, "POST", b+"/{proposal}", s.inBrand, auth.ScopePartnersManage, s.updatePartnerProposal)
	mux.HandleFunc("GET /p/{program}/api/proposals", s.inPortal(s.portalProposals))
	mux.HandleFunc("POST /p/{program}/api/proposals/{proposal}", s.inPortal(s.portalProposalAction))
	mux.HandleFunc("POST /p/{program}/api/proposal-preview", s.proposalPreview)
	mux.HandleFunc("POST /p/{program}/api/proposal-sign-in", s.proposalInviteSignIn)
}
func (s *Server) listPartnerProposals(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	p, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	s.proposalList(w, r, p.ID, a.WorkspaceID, p.BrandID, "")
}
func (s *Server) portalProposals(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	s.proposalList(w, r, c.program.id, c.program.ws, c.program.brandID, c.partner.ID)
}
func (s *Server) proposalList(w http.ResponseWriter, r *http.Request, program, ws string, brand *string, member string) {
	rows, err := s.pool.Query(r.Context(), proposalSelect+` AND ($4='' OR (x.membership_id::text=$4 AND x.status<>'draft')) ORDER BY x.updated_at DESC LIMIT 300`, ws, program, brand, member)
	if s.allocError(w, r, err) {
		return
	}
	defer rows.Close()
	data := []proposalView{}
	for rows.Next() {
		v, err := scanProposal(rows)
		if s.allocError(w, r, err) {
			return
		}
		data = append(data, v)
	}
	if s.allocError(w, r, rows.Err()) {
		return
	}
	writeJSON(w, 200, map[string]any{"data": data})
}
func (s *Server) createPartnerProposal(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	p, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in struct {
		PartnerID string         `json:"partner_id"`
		Email     string         `json:"email"`
		Name      string         `json:"name"`
		Config    proposalConfig `json:"config"`
	}
	if !decode(w, r, &in) || s.allocError(w, r, in.Config.check()) {
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var rec, member string
		if in.PartnerID != "" {
			if err := tx.QueryRow(ctx, `SELECT id::text,record_id::text FROM partner_memberships WHERE workspace_id=$1 AND program_id=$2 AND id::text=$3`, a.WorkspaceID, p.ID, in.PartnerID).Scan(&member, &rec); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return errNotFound
				}
				return err
			}
		} else {
			email, err := normalizeEmail(in.Email)
			if err != nil || email == "" {
				return errInvalid("choose one partner or supply the recipient email")
			}
			if len(in.Name) > 200 {
				return errInvalid("recipient name is at most 200 characters")
			}
			rec, err = customers.EnsurePerson(ctx, tx, a.WorkspaceID, "email", email, false, RequestID(ctx))
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE records SET name=$3 WHERE workspace_id=$1 AND id=$2 AND name=''`, a.WorkspaceID, rec, strings.TrimSpace(in.Name)); err != nil {
				return err
			}
			err = tx.QueryRow(ctx, `SELECT id::text FROM partner_memberships WHERE workspace_id=$1 AND program_id=$2 AND record_id=$3`, a.WorkspaceID, p.ID, rec).Scan(&member)
			if errors.Is(err, pgx.ErrNoRows) {
				member, err = s.joinPartner(ctx, tx, a.WorkspaceID, &a.principal, p, partnerJoin{rec: rec, status: "active", source: "manual"})
			}
			if err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(in.Config)
		if err := tx.QueryRow(ctx, `INSERT INTO partner_proposals(workspace_id,brand_id,program_id,membership_id,record_id,config) VALUES($1,$2,$3,$4,$5,$6) RETURNING id::text`, a.WorkspaceID, p.BrandID, p.ID, member, rec, raw).Scan(&id); err != nil {
			return err
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_proposal.created", "partner_proposal", id, map[string]any{"program_id": p.ID, "partner_id": member})
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := scanProposal(s.pool.QueryRow(ctx, proposalSelect+` AND x.id=$4`, a.WorkspaceID, p.ID, p.BrandID, id))
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, v)
}

type proposalAction struct {
	Action        string          `json:"action"`
	Version       int             `json:"version"`
	Config        *proposalConfig `json:"config"`
	DeliverableID string          `json:"deliverable_id"`
	MilestoneID   string          `json:"milestone_id"`
	URL           string          `json:"url"`
	Note          string          `json:"note"`
	Amount        int64           `json:"amount_cents"`
}

func (s *Server) updatePartnerProposal(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	p, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	s.changeProposal(w, r, p.ID, a.WorkspaceID, p.BrandID, "", &a.principal, false)
}
func (s *Server) portalProposalAction(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	if c.readOnly || c.partner.Status != "active" {
		writeError(w, r, 403, "forbidden", "only the assigned active partner can respond")
		return
	}
	s.changeProposal(w, r, c.program.id, c.program.ws, c.program.brandID, c.partner.ID, nil, true)
}
func (s *Server) changeProposal(w http.ResponseWriter, r *http.Request, program, ws string, brand *string, member string, actor *principal, partner bool) {
	var in proposalAction
	if !decode(w, r, &in) {
		return
	}
	if len(in.Note) > 4000 {
		s.allocError(w, r, errInvalid("note is at most 4,000 characters"))
		return
	}
	ctx := r.Context()
	var out proposalView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, err := scanProposal(tx.QueryRow(ctx, proposalSelect+` AND x.id::text=$4 AND ($5='' OR (x.membership_id::text=$5 AND x.status<>'draft')) FOR UPDATE OF x`, ws, program, brand, r.PathValue("proposal"), member))
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if in.Version != v.Version {
			return errInvalid("the proposal changed; reload and review the current version")
		}
		now := time.Now().UTC()
		token := ""
		invitationHash := []byte(nil)
		clearInvite := false
		if partner {
			switch in.Action {
			case "accept", "decline":
				if v.Status != "offered" && !(in.Action == "decline" && v.Status == "quote_requested") {
					return errInvalid("only a current offer can be accepted or declined")
				}
				if in.Action == "accept" {
					v.Status = "accepted"
					v.AcceptedAt = &now
				} else {
					v.Status = "declined"
				}
			case "quote":
				if v.Status != "quote_requested" || in.Amount <= 0 || in.Amount > 1e8 {
					return errInvalid("submit a positive quote for a quote request")
				}
				v.Progress.QuotedCents = in.Amount
				v.Progress.QuoteNote = in.Note
				v.Status = "quote_received"
			case "billing":
				if v.Status != "accepted" {
					return errInvalid("accept the proposal before adding invoice instructions")
				}
				v.Progress.InvoiceNote = in.Note
			case "submit":
				if v.Status != "accepted" {
					return errInvalid("accept the proposal before submitting work")
				}
				found := false
				for _, d := range v.Config.Deliverables {
					if d.ID == in.DeliverableID {
						found = true
					}
				}
				if !found {
					return errInvalid("choose a deliverable from this agreement")
				}
				if _, err := utm.Destination(in.URL); err != nil {
					return errInvalid("submit a full published work URL")
				}
				existing := v.Progress.Deliverables[in.DeliverableID]
				if existing.VerifiedAt != nil {
					return errInvalid("the brand already verified this deliverable; email them to discuss changes")
				}
				v.Progress.Deliverables[in.DeliverableID] = proposalDeliveryProgress{URL: in.URL, Note: in.Note, SubmittedAt: &now}
			default:
				return errInvalid("choose accept, decline, quote, submit or billing")
			}
		} else {
			switch in.Action {
			case "revise":
				if v.Status == "accepted" || v.Status == "completed" {
					return errInvalid("accepted agreements are immutable; create a new proposal for different terms")
				}
				if in.Config == nil {
					return errInvalid("config is required")
				}
				if err := in.Config.check(); err != nil {
					return err
				}
				v.Config = *in.Config
				v.Version++
				v.Status = "draft"
				v.Progress.Deliverables = map[string]proposalDeliveryProgress{}
				v.Progress.Milestones = map[string]proposalMilestoneProgress{}
				clearInvite = true
			case "invite":
				if v.Status != "draft" && v.Status != "offered" && v.Status != "quote_requested" {
					return errInvalid("revise the proposal before sending a new offer")
				}
				var enabled bool
				if err := tx.QueryRow(ctx, `SELECT portal_enabled FROM partner_programs WHERE workspace_id=$1 AND id=$2`, ws, program).Scan(&enabled); err != nil {
					return err
				}
				if !enabled {
					return errInvalid("open the partner portal before inviting this recipient")
				}
				v.Status = "offered"
				if v.Config.RequestQuote {
					v.Status = "quote_requested"
				}
				token, invitationHash = auth.NewToken("ofpi_")
			case "verify":
				if v.Status != "accepted" {
					return errInvalid("only accepted work can be verified")
				}
				d := v.Progress.Deliverables[in.DeliverableID]
				if d.SubmittedAt == nil {
					return errInvalid("the partner must submit this deliverable first")
				}
				if d.VerifiedAt != nil {
					return errInvalid("this deliverable is already verified")
				}
				d.VerifiedAt = &now
				v.Progress.Deliverables[in.DeliverableID] = d
			case "approve":
				if v.Status != "accepted" {
					return errInvalid("only accepted agreements can release milestones")
				}
				var m *proposalMilestone
				for i := range v.Config.Milestones {
					if v.Config.Milestones[i].ID == in.MilestoneID {
						m = &v.Config.Milestones[i]
					}
				}
				if m == nil {
					return errInvalid("choose a milestone from this agreement")
				}
				if v.Progress.Milestones[m.ID].ApprovedAt != nil {
					return errInvalid("this milestone was already approved")
				}
				d := v.Progress.Deliverables[m.DeliverableID]
				if d.VerifiedAt == nil || now.Before(d.VerifiedAt.Add(time.Duration(m.WaitDays)*24*time.Hour)) {
					return errInvalid("verify the deliverable and wait until this milestone is eligible; manual approval is still required")
				}
				var currency string
				if err := tx.QueryRow(ctx, `SELECT currency FROM partner_programs WHERE workspace_id=$1 AND id=$2`, ws, program).Scan(&currency); err != nil {
					return err
				}
				rewards := []string{}
				for _, kind := range []string{"fee", "action"} {
					if kind == "fee" && m.Amount == 0 || kind == "action" && m.Benefit == "" {
						continue
					}
					entry := rewardEntry{program: program, membership: v.Member, side: "partner", recipient: v.Record, kind: kind, currency: currency, note: v.Config.Title + " · " + m.Title, hold: now}
					if kind == "fee" {
						entry.amount = m.Amount
					} else {
						entry.action = "partner_benefit"
						entry.actionData = map[string]any{"label": m.Benefit, "proposal_id": v.ID, "milestone_id": m.ID}
					}
					rid, err := insertPartnerReward(ctx, tx, ws, entry)
					if err != nil {
						return err
					}
					if err := approvePartnerReward(ctx, tx, actor, ws, rid, "manual"); err != nil {
						return err
					}
					rewards = append(rewards, rid)
				}
				v.Progress.Milestones[m.ID] = proposalMilestoneProgress{ApprovedAt: &now, Note: in.Note, RewardIDs: rewards}
			case "complete":
				if v.Status != "accepted" {
					return errInvalid("only an accepted agreement can be completed")
				}
				for _, m := range v.Config.Milestones {
					if v.Progress.Milestones[m.ID].ApprovedAt == nil {
						return errInvalid("approve all milestones before completing")
					}
				}
				for _, d := range v.Config.Deliverables {
					p := v.Progress.Deliverables[d.ID]
					if p.VerifiedAt == nil || now.Before(p.VerifiedAt.Add(time.Duration(d.LiveDays)*24*time.Hour)) {
						return errInvalid("verify all deliverables and finish their required live periods before completing")
					}
				}
				v.Status = "completed"
			default:
				return errInvalid("choose revise, invite, verify, approve or complete")
			}
		}
		config, _ := json.Marshal(v.Config)
		progress, _ := json.Marshal(v.Progress)
		if _, err := tx.Exec(ctx, `UPDATE partner_proposals SET status=$5,version=$6,config=$7,progress=$8,accepted_at=$9,invitation_hash=CASE WHEN $10 THEN NULL WHEN $11::bytea IS NULL THEN invitation_hash ELSE $11 END,updated_at=now() WHERE workspace_id=$1 AND program_id=$2 AND brand_id IS NOT DISTINCT FROM $3::uuid AND id=$4`, ws, program, brand, v.ID, v.Status, v.Version, config, progress, v.AcceptedAt, clearInvite, invitationHash); err != nil {
			return err
		}
		if token != "" {
			p, err := s.portalProgramByID(ctx, program)
			if err != nil {
				return err
			}
			base, err := s.portalBase(ctx, program, p.subdomain)
			if err != nil {
				return err
			}
			v.InvitationURL = base + "?proposal=" + v.ID + "#invite=" + token
		}
		data := map[string]any{"program_id": program, "partner_id": v.Member, "status": v.Status, "action": in.Action, "version": v.Version}
		if in.Action == "accept" {
			data["agreement"] = v.Config
		}
		if in.MilestoneID != "" {
			data["milestone_id"] = in.MilestoneID
		}
		if err := partnerRecord(ctx, tx, actor, ws, "partner_proposal.updated", "partner_proposal", v.ID, data); err != nil {
			return err
		}
		out = v
		return nil
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) readProposalInvitation(w http.ResponseWriter, r *http.Request) (*portalProgram, *proposalView, bool) {
	p, ok := s.loadPortalProgram(w, r, false)
	if !ok || !s.portalLimit.allow(w, r, "ip:"+clientIP(r)) {
		return nil, nil, false
	}
	var in struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &in) {
		return nil, nil, false
	}
	if !strings.HasPrefix(in.Token, "ofpi_") {
		writeError(w, r, 404, "not_found", "this invitation is unavailable; contact the brand")
		return nil, nil, false
	}
	v, err := scanProposal(s.pool.QueryRow(r.Context(), proposalSelect+` AND x.invitation_hash=$4 AND x.status IN ('offered','quote_requested')`, p.ws, p.id, p.brandID, auth.HashToken(in.Token)))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, 404, "not_found", "this invitation is unavailable; contact the brand")
		return nil, nil, false
	}
	if s.allocError(w, r, err) {
		return nil, nil, false
	}
	w.Header().Set("Cache-Control", "no-store")
	return p, &v, true
}
func (s *Server) proposalPreview(w http.ResponseWriter, r *http.Request) {
	_, v, ok := s.readProposalInvitation(w, r)
	if !ok {
		return
	}
	// Deliberately return an allowlist: the offer is visible only after recipient authentication.
	writeJSON(w, 200, map[string]any{"id": v.ID, "site_name": v.Config.PartnerSiteName, "site_url": v.Config.PartnerSiteURL, "site_icon_url": v.Config.PartnerSiteIconURL})
}
func (s *Server) proposalInviteSignIn(w http.ResponseWriter, r *http.Request) {
	p, v, ok := s.readProposalInvitation(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var email string
	err := s.pool.QueryRow(ctx, `SELECT value FROM record_identities WHERE workspace_id=$1 AND record_id=$2 AND namespace='email' AND valid_to IS NULL ORDER BY verified DESC,valid_from LIMIT 1`, p.ws, v.Record).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, 400, "invalid_request", "ask the brand to add an email address to your invitation")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	if !s.portalLimit.allow(w, r, "email:"+email) {
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		token, err := newSignInLink(ctx, tx, p.ws, p.id, v.Record, nil)
		if err != nil {
			return err
		}
		base, err := s.portalBase(ctx, p.id, p.subdomain)
		if err != nil {
			return err
		}
		subject := "Your partnership proposal"
		text := "Open your proposal and sign in securely:\n\n{{sign_in_url}}\n\nThis link works once, for 30 minutes. Reply to the brand to discuss changes."
		return s.portalEmail(ctx, tx, p, email, v.ID, emails.Draft{Subject: &subject, Text: &text, Data: map[string]any{"sign_in_url": base + "?proposal=" + v.ID + "#signin=" + token}})
	})
	if s.portalEmailError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"sent": true})
}
