package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/mailbox"
)

// Test workspaces are the sandbox: everything works as in a live one, but
// mailboxes are simulated (sends are recorded and delivered nowhere),
// verification is made up from the address, and purchases are refused.
// These endpoints play the outside world, so replies, bounces and
// opt-outs can be tried on demand, and a sequence can run in a minute.

func (s *Server) routesSandbox(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/workspaces/{ws}/test/inbound", s.inWorkspace(auth.ScopeInboxManage, s.simulateInbound))
	mux.HandleFunc("POST /v1/workspaces/{ws}/test/send-now", s.inWorkspace(auth.ScopeSequencesManage, s.sendNow))
}

// testOnly answers 409 live_workspace outside a test workspace.
func testOnly(w http.ResponseWriter, r *http.Request, a *access, what string) bool {
	if a.Test {
		return true
	}
	writeError(w, r, http.StatusConflict, "live_workspace", what+" only works in a test workspace, where nothing reaches a real inbox; create one with POST /v1/workspaces and \"test\": true")
	return false
}

// simulateInbound delivers a made-up reply, auto-reply, opt-out or bounce
// to a sent email, as if the lead's server had sent it. It is handled like
// real mail: the inbox shows it, a reply stops the sequence, an opt-out
// or bounce suppresses the address, and webhooks fire.
func (s *Server) simulateInbound(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !testOnly(w, r, a, "Simulating inbound mail") {
		return
	}
	var req struct {
		MessageID *string `json:"message_id"`
		LeadID    *string `json:"lead_id"`
		Kind      string  `json:"kind"`
		Body      string  `json:"body"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Kind == "" {
		req.Kind = inbox.SimReply
	}
	if !slices.Contains(inbox.SimKinds, req.Kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind must be one of "+strings.Join(inbox.SimKinds, ", "))
		return
	}
	if (req.MessageID == nil) == (req.LeadID == nil) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "message_id or lead_id is required (one of them): the sent email to answer, or the lead whose latest email to answer")
		return
	}
	for _, id := range []*string{req.MessageID, req.LeadID} {
		if id != nil && !validUUID(*id) {
			writeError(w, r, http.StatusNotFound, "not_found", "no sent email to answer")
			return
		}
	}
	ctx := r.Context()
	var sent inbox.Sent
	var mailboxID *string
	err := s.pool.QueryRow(ctx, `
		SELECT message_id, from_email, to_email, subject, mailbox_id::text FROM messages
		WHERE workspace_id = $1 AND status IN ('sent', 'unknown')
		  AND ($2::uuid IS NULL OR id = $2) AND ($3::uuid IS NULL OR lead_id = $3)
		ORDER BY created_at DESC LIMIT 1`, a.WorkspaceID, req.MessageID, req.LeadID,
	).Scan(&sent.MessageID, &sent.From, &sent.To, &sent.Subject, &mailboxID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && mailboxID == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "no sent email to answer: send one first (POST .../test/send-now sends a sequence's next step at once)")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	raw, err := inbox.Simulate(req.Kind, sent, req.Body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	id, err := s.inbox.Deliver(ctx, a.WorkspaceID, *mailboxID, raw)
	if errors.Is(err, mailbox.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "the mailbox that sent it was deleted")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return record(ctx, tx, &a.principal, a.WorkspaceID, "inbound.simulated", "inbound_message", id, map[string]any{"kind": req.Kind})
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, inboundSelect+` WHERE i.id = $1 AND i.workspace_id = $2`, id, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[inboundView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// sendNow sends the next step of every active enrollment at once, without
// waiting for delays or the sending window.
func (s *Server) sendNow(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !testOnly(w, r, a, "Sending without waiting") {
		return
	}
	var req struct {
		SequenceID *string `json:"sequence_id"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if req.SequenceID != nil && !validUUID(*req.SequenceID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sequence_id must be a UUID")
		return
	}
	n, err := s.send.SendNow(r.Context(), a.WorkspaceID, req.SequenceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	data := map[string]any{"queued": n}
	target := ""
	if req.SequenceID != nil {
		target = *req.SequenceID
		data["sequence_id"] = target
	}
	if err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "sequence.sent_now", "sequence", target, data)
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": n})
}

// Sample data for a new test workspace: three mailboxes and leads at a
// made-up company, enough to build a sequence and try every outcome.
var sampleLeads = []leads.Lead{
	{Email: "ana@northwind.example", FirstName: "Ana", LastName: "Silva", Company: "Northwind", Title: "Head of Growth"},
	{Email: "ben@northwind.example", FirstName: "Ben", LastName: "Okafor", Company: "Northwind", Title: "VP Sales"},
	{Email: "chloe@contoso.example", FirstName: "Chloe", LastName: "Martin", Company: "Contoso", Title: "Founder"},
	{Email: "dev@contoso.example", FirstName: "Dev", LastName: "Patel", Company: "Contoso", Title: "CTO"},
	{Email: "emma@fabrikam.example", FirstName: "Emma", LastName: "Novak", Company: "Fabrikam", Title: "Marketing Director"},
	{Email: "farid@fabrikam.example", FirstName: "Farid", LastName: "Haddad", Company: "Fabrikam", Title: "Head of Partnerships"},
	{Email: "grace@tailspin.example", FirstName: "Grace", LastName: "Kim", Company: "Tailspin", Title: "COO"},
	{Email: "hugo@tailspin.example", FirstName: "Hugo", LastName: "Laurent", Company: "Tailspin", Title: "Sales Lead"},
	{Email: "risky@tailspin.example", FirstName: "Rita", LastName: "Risk", Company: "Tailspin", Title: "Catch-all example"},
	{Email: "undeliverable@contoso.example", FirstName: "Uma", LastName: "Bounce", Company: "Contoso", Title: "Undeliverable example"},
}

func (s *Server) addSampleData(ctx context.Context, a *access) error {
	var rows []mailbox.Row
	for i, name := range []string{"alex", "sam", "jordan"} {
		rows = append(rows, mailbox.Row{Line: i + 1, Email: name + "@outreach.example", DisplayName: strings.ToUpper(name[:1]) + name[1:] + " at Example"})
	}
	res, err := s.importRows(ctx, a, "sample", rows, nil)
	if err != nil {
		return err
	}
	if len(res.Errors) > 0 {
		return fmt.Errorf("sample mailboxes: %s", res.Errors[0].Error)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := leads.Import(ctx, tx, a.WorkspaceID, "sample", nil, slices.Clone(sampleLeads)); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "workspace.sample_data_added", "workspace", a.WorkspaceID,
			map[string]any{"mailboxes": len(rows), "leads": len(sampleLeads)})
	})
}
