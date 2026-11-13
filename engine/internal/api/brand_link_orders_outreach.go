package api

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
	"github.com/outflareai/outflare/engine/internal/leads"
)

// The catalog's cell menu (Link orders → Catalog, right-click a cell):
// "Find email" reads the addresses already stored for the donor's site, and
// "Send to a sequence" adds one of them, or a typed one, to one of the
// brand's Outreach sequences with the same rules as adding leads there.
func (s *Server) routesLinkOrderOutreach(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/link-orders/catalog/items/{id}/contacts", s.onBrand(auth.ScopeCoverageRead, s.getLinkOrderDonorContacts))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/catalog/items/{id}/outreach", s.onBrand(auth.ScopeCoverageManage, s.sendLinkOrderDonorToSequence))
}

func (s *Server) getLinkOrderDonorContacts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	id, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	out, err := linkorders.FindDonorContacts(r.Context(), s.pool, ws, brand, id)
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// donorOutreachResult is what sending a donor to a sequence did.
type donorOutreachResult struct {
	DonorID        uuid.UUID `json:"donor_id"`
	Domain         string    `json:"domain"`
	Email          string    `json:"email"`
	LeadID         string    `json:"lead_id"`
	SequenceID     string    `json:"sequence_id"`
	SequenceName   string    `json:"sequence_name"`
	SequenceStatus string    `json:"sequence_status"`
	// Result is enrolled, already_enrolled, suppressed, excluded or
	// active_elsewhere: the same outcomes as adding leads to a sequence.
	Result string `json:"result"`
}

func (s *Server) sendLinkOrderDonorToSequence(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	// Adding to a sequence is an Outreach write as well as a catalog one.
	if !a.has(auth.ScopeSequencesManage) {
		writeError(w, r, http.StatusForbidden, "forbidden", s.missingScope(a, auth.ScopeSequencesManage))
		return
	}
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	id, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		SequenceID string `json:"sequence_id"`
		Email      string `json:"email"`
		Name       string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !validUUID(req.SequenceID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sequence_id is required")
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || len(email) > 320 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "email must be one email address, such as editor@example.com")
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name must be at most 200 characters")
		return
	}
	donor, err := linkorders.GetDonor(r.Context(), s.pool, ws, brand, id)
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	ctx := r.Context()
	out := donorOutreachResult{DonorID: donor.ID, Domain: donor.Domain, Email: email, SequenceID: req.SequenceID}
	var counts enrollCounts
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// One of this brand's own sequences, not archived and not a
		// platform campaign's mirror.
		err := tx.QueryRow(ctx, `SELECT name, status FROM sequences WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 AND status <> 'archived'`,
			req.SequenceID, ws, brand).Scan(&out.SequenceName, &out.SequenceStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if p, err := linkedPlatform(ctx, tx, req.SequenceID); err != nil {
			return err
		} else if p != "" {
			return errLinked(p)
		}
		first, last, _ := strings.Cut(name, " ")
		imp, err := leads.ImportIn(ctx, tx, ws.String(), brand.String(), "link_orders", nil, []leads.Lead{{
			Email: email, FirstName: first, LastName: strings.TrimSpace(last), Company: donor.Domain,
			Website: "https://" + donor.Domain + "/", Custom: map[string]string{"link_order_domain": donor.Domain},
		}})
		if err != nil {
			return err
		}
		if imp.OtherBrand > 0 {
			return errInvalid("this address is another brand's lead; add it from that brand")
		}
		var recordID *string
		if err := tx.QueryRow(ctx, `SELECT id::text, record_id::text FROM leads WHERE workspace_id = $1 AND brand_id = $2 AND lower(email) = $3`,
			ws, brand, email).Scan(&out.LeadID, &recordID); err != nil {
			return err
		}
		// An address at the site becomes one of its company's people, so
		// "Find email" shows it next time. An intermediary's doesn't.
		if recordID != nil && linkorders.AtDomain(email, donor.Domain) {
			if _, err := tx.Exec(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind, field_key)
				SELECT $1, $2, p.record_id, 'companies', 'companies' FROM coverage_publishers p
				WHERE p.id = $3 AND p.workspace_id = $1 AND p.record_id <> $2::uuid AND NOT EXISTS (SELECT 1 FROM record_relationships x
					WHERE x.workspace_id = $1 AND x.from_id = $2 AND x.to_id = p.record_id AND x.kind = 'companies' AND x.valid_to IS NULL)`,
				ws, *recordID, donor.PublisherID); err != nil {
				return err
			}
		}
		if counts, err = enrollLeads(ctx, tx, ws.String(), req.SequenceID, []string{out.LeadID}, nil, true); err != nil {
			return err
		}
		if counts.Enrolled == 0 {
			return nil
		}
		return record(ctx, tx, &a.principal, ws.String(), "sequence.leads_enrolled", "sequence", req.SequenceID, map[string]any{
			"enrolled": counts.Enrolled, "suppressed": counts.Suppressed, "excluded": counts.Excluded, "active_elsewhere": counts.ActiveElsewhere,
			"source": "link_orders", "donor_profile_id": donor.ID.String(), "domain": donor.Domain,
		})
	})
	if s.seqError(w, r, err) {
		return
	}
	switch {
	case counts.Enrolled > 0:
		out.Result = "enrolled"
	case counts.AlreadyEnrolled > 0:
		out.Result = "already_enrolled"
	case counts.Suppressed > 0:
		out.Result = "suppressed"
	case counts.Excluded > 0:
		out.Result = "excluded"
	default:
		out.Result = "active_elsewhere"
	}
	writeJSON(w, http.StatusOK, out)
}
