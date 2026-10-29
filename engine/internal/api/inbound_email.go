package api

import (
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/inboundemail"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Inbound email (package inboundemail): workspaces' addresses at the
// server's inbound domain, and the mail delivered to them. It is on only
// when an extension turned it on (Outflare Cloud); elsewhere the addresses
// list says it isn't available. Each purpose (support, AI receipts)
// registers how its mail is handled.

func (s *Server) routesInboundEmail(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/inbound/email", s.receiveInboundEmail)
	mux.HandleFunc("GET /v1/workspaces/{ws}/inbound-addresses", s.inWorkspace(auth.ScopeWorkspaceRead, s.listInboundAddresses))
	mux.HandleFunc("POST /v1/workspaces/{ws}/inbound-addresses", s.inWorkspace(auth.ScopeWorkspaceManage, s.createInboundAddress))
	mux.HandleFunc("GET /v1/workspaces/{ws}/inbound-emails", s.inWorkspace(auth.ScopeAuditRead, s.listInboundEmails))
}

// receiveInboundEmail takes one message from the operator's edge (an
// extension's mail receiver), signed with the inbound secret: the raw
// message as the body, the recipient in X-Outflare-To.
func (s *Server) receiveInboundEmail(w http.ResponseWriter, r *http.Request) {
	if !s.inboundEmail.Enabled() {
		writeError(w, r, http.StatusNotFound, "not_found", "inbound email isn't available on this server")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, inboundemail.MaxBytes+1))
	if err != nil || len(raw) > inboundemail.MaxBytes {
		metrics.InboundReceived("", "too_large", 0)
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the message is larger than 25 MB")
		return
	}
	if err := s.inboundEmail.Verify(r.Header.Get("X-Outflare-Timestamp"), r.Header.Get("X-Outflare-Signature"), raw, time.Now()); err != nil {
		metrics.InboundReceived("", "bad_signature", 0)
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "the delivery's signature doesn't match")
		return
	}
	id, purpose, err := s.inboundEmail.Deliver(r.Context(), r.Header.Get("X-Outflare-To"), raw)
	switch {
	case errors.Is(err, inboundemail.ErrUnknownAddress):
		metrics.InboundReceived("", "unknown_address", 0)
		writeError(w, r, http.StatusNotFound, "not_found", "no inbound address like that: refuse the message")
	case errors.Is(err, inboundemail.ErrDuplicate):
		metrics.InboundReceived(purpose, "duplicate", 0)
		writeJSON(w, http.StatusOK, map[string]any{"queued": false})
	case err != nil:
		metrics.InboundReceived(purpose, "error", 0)
		s.internal(w, r, err)
	default:
		metrics.InboundReceived(purpose, "accepted", len(raw))
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "id": id})
	}
}

func (s *Server) listInboundAddresses(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	out := map[string]any{"available": s.inboundEmail.Enabled(), "domain": nil, "purposes": []string{}, "data": []inboundemail.Address{}}
	if !s.inboundEmail.Enabled() {
		writeJSON(w, http.StatusOK, out)
		return
	}
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	list, err := s.inboundEmail.List(r.Context(), s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// With a brand picked, its addresses and the workspace's own.
	list = slices.DeleteFunc(list, func(ad inboundemail.Address) bool { return ad.BrandID != nil && !brand.matches(ad.BrandID) })
	purposes := []string{}
	for _, p := range s.inboundEmail.Purposes() {
		if on, err := s.inboundPurposeOn(r, p); err != nil {
			s.internal(w, r, err)
			return
		} else if on {
			purposes = append(purposes, p)
		}
	}
	out["domain"], out["purposes"], out["data"] = s.inboundEmail.Domain(), purposes, list
	writeJSON(w, http.StatusOK, out)
}

// inboundPurposeOn reports whether a registered purpose is on for the
// caller's workspace.
func (s *Server) inboundPurposeOn(r *http.Request, purpose string) (bool, error) {
	p, ok := s.inboundEmail.PurposeOf(purpose)
	if !ok {
		return false, nil
	}
	if p.On == nil {
		return true, nil
	}
	return p.On(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID)
}

// createInboundAddress returns the workspace's address for a purpose (and
// brand, for support), making one when there is none. name picks a
// readable purpose's address, which renames it; replace swaps an address
// for a new one, refusing mail to the old one.
func (s *Server) createInboundAddress(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Purpose string `json:"purpose"`
		Brand   string `json:"brand"`
		Name    string `json:"name"`
		Replace bool   `json:"replace"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !s.inboundEmail.Enabled() {
		writeError(w, r, http.StatusNotFound, "not_found", "inbound email is only available on Outflare Cloud")
		return
	}
	p, ok := s.inboundEmail.PurposeOf(req.Purpose)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "purpose must be one of "+strings.Join(s.inboundEmail.Purposes(), ", "))
		return
	}
	if on, err := s.inboundPurposeOn(r, req.Purpose); err != nil {
		s.internal(w, r, err)
		return
	} else if !on {
		writeError(w, r, http.StatusNotFound, "not_found", "that isn't switched on for this workspace")
		return
	}
	if p.Suffix == "" && (req.Brand != "" || req.Name != "") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", req.Purpose+" addresses are one per workspace, with a name Outflare picks: leave out brand and name")
		return
	}
	want := inboundemail.Want{WorkspaceID: a.WorkspaceID, Purpose: req.Purpose, Name: req.Name, Replace: req.Replace || req.Name != "", By: a.personID()}
	if b := strings.TrimSpace(req.Brand); b != "" {
		id, err := s.brandIDFor(r.Context(), a.WorkspaceID, b)
		if errors.Is(err, errNotFound) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "brand not found")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		want.BrandID = &id
	}
	var addr inboundemail.Address
	var made bool
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if req.Name != "" {
			// Naming it the same as now changes nothing.
			cur, err := s.inboundEmail.Current(r.Context(), tx, want.WorkspaceID, want.Purpose, want.BrandID)
			if err != nil {
				return err
			}
			if cur != nil && strings.EqualFold(strings.Split(cur.Email, "@")[0], strings.TrimSpace(req.Name)) {
				addr = *cur
				return nil
			}
		}
		if addr, made, err = s.inboundEmail.Ensure(r.Context(), tx, want); err != nil || !made {
			return err
		}
		action := "inbound_address.created"
		switch {
		case req.Name != "":
			action = "inbound_address.renamed"
		case req.Replace:
			action = "inbound_address.replaced"
		}
		props := map[string]any{"purpose": addr.Purpose}
		data := map[string]any{"purpose": addr.Purpose, "brand_id": addr.BrandID}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, action, "inbound_address", addr.ID, data, props)
	})
	switch {
	case errors.Is(err, inboundemail.ErrBadName):
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	case errors.Is(err, inboundemail.ErrNameTaken):
		writeError(w, r, http.StatusConflict, "conflict", "that address is taken: pick another name")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	status := http.StatusOK
	if made {
		status = http.StatusCreated
	}
	writeJSON(w, status, addr)
}

type inboundEmailView struct {
	ID          string    `json:"id"`
	Purpose     string    `json:"purpose"`
	ReceivedAt  time.Time `json:"received_at"`
	From        *string   `json:"from"`
	ForwardedBy *string   `json:"forwarded_by"`
	Subject     *string   `json:"subject"`
	DKIM        []string  `json:"dkim_domains"`
	Status      string    `json:"status"`
	Reason      *string   `json:"reason"`
	SizeBytes   int       `json:"size_bytes"`
}

// listInboundEmails is the latest 100 messages and what became of them.
func (s *Server) listInboundEmails(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	// With a brand picked, mail to its addresses and the workspace's own.
	args := []any{a.WorkspaceID}
	filter := ""
	if !brand.all() {
		filter = ` AND (ad.brand_id IS NULL OR (TRUE` + brand.sql("ad.brand_id", &args) + `))`
	}
	rows, err := s.pool.Query(r.Context(), `SELECT m.id::text, m.purpose, m.received_at, m.from_address, m.forwarded_by, m.subject, m.dkim_domains, m.status, m.reason, m.size_bytes
		FROM inbound_emails m JOIN inbound_addresses ad ON ad.id = m.address_id
		WHERE m.workspace_id = $1`+filter+` ORDER BY m.received_at DESC LIMIT 100`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (inboundEmailView, error) {
		var v inboundEmailView
		return v, row.Scan(&v.ID, &v.Purpose, &v.ReceivedAt, &v.From, &v.ForwardedBy, &v.Subject, &v.DKIM, &v.Status, &v.Reason, &v.SizeBytes)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}
