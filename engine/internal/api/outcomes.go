package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/outcomes"
)

// Outcomes: meetings and deals that outreach produced, what it cost, and
// cost per reply, meeting and deal by sequence or brand.

func (s *Server) routesOutcomes(mux *http.ServeMux) {
	read, manage := auth.ScopeOutcomesRead, auth.ScopeOutcomesManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/outcomes/report", s.inBrand(read, s.outcomesReport))
	mux.HandleFunc("GET /v1/workspaces/{ws}/outcomes/export", s.inWorkspace(read, s.outcomesExport))
	mux.HandleFunc("GET /v1/workspaces/{ws}/outcomes/syncs", s.inWorkspace(read, s.listOutcomeSyncs))
	mux.HandleFunc("POST /v1/workspaces/{ws}/outcomes/syncs/{id}/run", s.inWorkspace(manage, s.runOutcomeSync))

	mux.HandleFunc("GET /v1/workspaces/{ws}/meetings", s.inWorkspace(read, s.listMeetings))
	mux.HandleFunc("POST /v1/workspaces/{ws}/meetings", s.inWorkspace(manage, s.createMeeting))
	mux.HandleFunc("GET /v1/workspaces/{ws}/meetings/{id}", s.inWorkspace(read, s.getMeeting))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/meetings/{id}", s.inWorkspace(manage, s.updateMeeting))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/meetings/{id}", s.inWorkspace(manage, s.deleteMeeting))

	mux.HandleFunc("GET /v1/workspaces/{ws}/deals", s.inWorkspace(read, s.listDeals))
	mux.HandleFunc("POST /v1/workspaces/{ws}/deals", s.inWorkspace(manage, s.createDeal))
	mux.HandleFunc("GET /v1/workspaces/{ws}/deals/{id}", s.inWorkspace(read, s.getDeal))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/deals/{id}", s.inWorkspace(manage, s.updateDeal))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/deals/{id}", s.inWorkspace(manage, s.deleteDeal))

	mux.HandleFunc("GET /v1/workspaces/{ws}/spend", s.inWorkspace(read, s.listSpend))
	mux.HandleFunc("POST /v1/workspaces/{ws}/spend", s.inWorkspace(manage, s.createSpend))
	mux.HandleFunc("GET /v1/workspaces/{ws}/spend/{id}", s.inWorkspace(read, s.getSpend))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/spend/{id}", s.inWorkspace(manage, s.deleteSpend))
	mux.HandleFunc("GET /v1/workspaces/{ws}/spend-recurring", s.inWorkspace(read, s.listRecurring))
	mux.HandleFunc("POST /v1/workspaces/{ws}/spend-recurring", s.inWorkspace(manage, s.createRecurring))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/spend-recurring/{id}", s.inWorkspace(manage, s.updateRecurring))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/spend-recurring/{id}", s.inWorkspace(manage, s.deleteRecurring))
	mux.HandleFunc("GET /v1/workspaces/{ws}/spend-rates", s.inWorkspace(read, s.listRates))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/spend-rates", s.inWorkspace(manage, s.setRate))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/spend-rates", s.inWorkspace(manage, s.deleteRate))
}

// outcomeError writes err as a response and reports whether there was one.
func (s *Server) outcomeError(w http.ResponseWriter, r *http.Request, err error, what string) bool {
	switch {
	case err == nil:
		return false
	case outcomes.IsInvalid(err):
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, outcomes.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", what+" not found")
	default:
		s.internal(w, r, err)
	}
	return true
}

// inTx runs fn in a transaction with an audit event.
func (s *Server) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// period reads from and to (RFC 3339 or YYYY-MM-DD; to is exclusive, and a
// bare date means the whole day). The default is the last 30 days.
func period(r *http.Request) (from, to time.Time, err error) {
	parse := func(name string, endOfDay bool) (*time.Time, error) {
		v := strings.TrimSpace(r.URL.Query().Get(name))
		if v == "" {
			return nil, nil
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return &t, nil
		}
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			return nil, outcomesInvalid(name + " must be a date like 2026-10-31 or a time like 2026-10-31T09:00:00Z")
		}
		if endOfDay {
			t = t.Add(24 * time.Hour)
		}
		return &t, nil
	}
	f, err := parse("from", false)
	if err != nil {
		return
	}
	t, err := parse("to", true)
	if err != nil {
		return
	}
	to = time.Now().UTC()
	if t != nil {
		to = *t
	}
	from = to.Add(-30 * 24 * time.Hour)
	if f != nil {
		from = *f
	}
	return from, to, nil
}

type invalidErr string

func (e invalidErr) Error() string { return string(e) }

func outcomesInvalid(msg string) error { return invalidErr(msg) }

func (s *Server) badQuery(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
	return true
}

// idParam reads an optional uuid query parameter.
func idParam(r *http.Request, name string) (string, error) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v != "" && !validUUID(v) {
		return "", outcomesInvalid(name + " must be an id")
	}
	return v, nil
}

func limitParam(r *http.Request, def, max int) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > max {
		return 0, outcomesInvalid(fmt.Sprintf("limit must be 1 to %d", max))
	}
	return n, nil
}

func beforeParam(r *http.Request) (*time.Time, error) {
	v := r.URL.Query().Get("before")
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return nil, outcomesInvalid("before must be a time from next_before")
	}
	return &t, nil
}

func (s *Server) reportFilter(r *http.Request) (outcomes.ReportFilter, error) {
	from, to, err := period(r)
	if err != nil {
		return outcomes.ReportFilter{}, err
	}
	f := outcomes.ReportFilter{From: from, To: to, GroupBy: r.URL.Query().Get("group_by"), Currency: r.URL.Query().Get("currency")}
	for name, dst := range map[string]*string{"sequence_id": &f.SequenceID, "mission_id": &f.MissionID} {
		if *dst, err = idParam(r, name); err != nil {
			return f, err
		}
	}
	return f, nil
}

func (s *Server) outcomesReport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	f, err := s.reportFilter(r)
	if s.badQuery(w, r, err) {
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	f.Brand, f.BrandsIn = sel.id, sel.in
	rep, err := outcomes.Summary(r.Context(), s.pool, a.WorkspaceID, f)
	if s.outcomeError(w, r, err, "report") {
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// Meetings.

func (s *Server) listMeetings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	var f outcomes.MeetingFilter
	var err error
	if q.Get("from") != "" || q.Get("to") != "" {
		from, to, err := period(r)
		if s.badQuery(w, r, err) {
			return
		}
		f.From, f.To = &from, &to
	}
	for name, dst := range map[string]*string{"sequence_id": &f.SequenceID, "mission_id": &f.MissionID} {
		if *dst, err = idParam(r, name); s.badQuery(w, r, err) {
			return
		}
	}
	if f.Limit, err = limitParam(r, 100, 500); s.badQuery(w, r, err) {
		return
	}
	if f.Before, err = beforeParam(r); s.badQuery(w, r, err) {
		return
	}
	f.Status, f.Attributed = q.Get("status"), q.Get("attributed") == "true"
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	f.Brand = sel.id
	list, err := outcomes.ListMeetings(r.Context(), s.pool, a.WorkspaceID, f)
	if s.outcomeError(w, r, err, "meeting") {
		return
	}
	var next *string
	if len(list) == f.Limit {
		v := list[len(list)-1].StartsAt.Format(time.RFC3339Nano)
		next = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

func (s *Server) getMeeting(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !validUUID(r.PathValue("id")) {
		writeError(w, r, http.StatusNotFound, "not_found", "meeting not found")
		return
	}
	m, err := outcomes.GetMeeting(r.Context(), s.pool, a.WorkspaceID, r.PathValue("id"))
	if s.outcomeError(w, r, err, "meeting") {
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) createMeeting(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req outcomes.NewMeeting
	if !decode(w, r, &req) {
		return
	}
	source := "manual"
	if a.TokenID != "" {
		source = outcomes.SourceAPI
	}
	var m outcomes.Meeting
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		if m, err = outcomes.CreateMeeting(r.Context(), tx, a.WorkspaceID, source, a.ActorID, req); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "meeting.created", "meeting", m.ID,
			map[string]any{"attribution": m.Attribution, "sequence_id": m.SequenceID, "status": m.Status})
	})
	if s.outcomeError(w, r, err, "meeting") {
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) updateMeeting(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !validUUID(r.PathValue("id")) {
		writeError(w, r, http.StatusNotFound, "not_found", "meeting not found")
		return
	}
	var req outcomes.MeetingPatch
	if !decode(w, r, &req) {
		return
	}
	var m outcomes.Meeting
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		if m, err = outcomes.UpdateMeeting(r.Context(), tx, a.WorkspaceID, r.PathValue("id"), req); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "meeting.updated", "meeting", m.ID,
			map[string]any{"status": m.Status, "sequence_id": m.SequenceID, "attribution": m.Attribution})
	})
	if s.outcomeError(w, r, err, "meeting") {
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) deleteMeeting(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "meeting not found")
		return
	}
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		if err := outcomes.DeleteMeeting(r.Context(), tx, a.WorkspaceID, id); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "meeting.deleted", "meeting", id, nil)
	})
	if s.outcomeError(w, r, err, "meeting") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Deals.

func (s *Server) listDeals(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	var f outcomes.DealFilter
	var err error
	if q.Get("from") != "" || q.Get("to") != "" {
		from, to, err := period(r)
		if s.badQuery(w, r, err) {
			return
		}
		f.From, f.To = &from, &to
	}
	for name, dst := range map[string]*string{"sequence_id": &f.SequenceID, "mission_id": &f.MissionID} {
		if *dst, err = idParam(r, name); s.badQuery(w, r, err) {
			return
		}
	}
	if f.Limit, err = limitParam(r, 100, 500); s.badQuery(w, r, err) {
		return
	}
	if f.Before, err = beforeParam(r); s.badQuery(w, r, err) {
		return
	}
	f.State, f.Attributed = q.Get("state"), q.Get("attributed") == "true"
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	f.Brand = sel.id
	list, err := outcomes.ListDeals(r.Context(), s.pool, a.WorkspaceID, f)
	if s.outcomeError(w, r, err, "deal") {
		return
	}
	var next *string
	if len(list) == f.Limit {
		v := list[len(list)-1].OpenedAt.Format(time.RFC3339Nano)
		next = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

func (s *Server) getDeal(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !validUUID(r.PathValue("id")) {
		writeError(w, r, http.StatusNotFound, "not_found", "deal not found")
		return
	}
	d, err := outcomes.GetDeal(r.Context(), s.pool, a.WorkspaceID, r.PathValue("id"))
	if s.outcomeError(w, r, err, "deal") {
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) createDeal(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req outcomes.NewDeal
	if !decode(w, r, &req) {
		return
	}
	source := "manual"
	if a.TokenID != "" {
		source = outcomes.SourceAPI
	}
	var d outcomes.Deal
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		if d, err = outcomes.CreateDeal(r.Context(), tx, a.WorkspaceID, source, a.ActorID, req); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "deal.created", "deal", d.ID,
			map[string]any{"state": d.State, "amount_cents": d.AmountCents, "sequence_id": d.SequenceID})
	})
	if s.outcomeError(w, r, err, "deal") {
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) updateDeal(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !validUUID(r.PathValue("id")) {
		writeError(w, r, http.StatusNotFound, "not_found", "deal not found")
		return
	}
	var req outcomes.DealPatch
	if !decode(w, r, &req) {
		return
	}
	var d outcomes.Deal
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		if d, err = outcomes.UpdateDeal(r.Context(), tx, a.WorkspaceID, r.PathValue("id"), req); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "deal.updated", "deal", d.ID,
			map[string]any{"state": d.State, "amount_cents": d.AmountCents, "sequence_id": d.SequenceID})
	})
	if s.outcomeError(w, r, err, "deal") {
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) deleteDeal(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "deal not found")
		return
	}
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		if err := outcomes.DeleteDeal(r.Context(), tx, a.WorkspaceID, id); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "deal.deleted", "deal", id, nil)
	})
	if s.outcomeError(w, r, err, "deal") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Spend.

func (s *Server) spendFilter(r *http.Request) (outcomes.SpendFilter, error) {
	q := r.URL.Query()
	var f outcomes.SpendFilter
	var err error
	if q.Get("from") != "" || q.Get("to") != "" {
		from, to, err := period(r)
		if err != nil {
			return f, err
		}
		f.From, f.To = &from, &to
	}
	for name, dst := range map[string]*string{"sequence_id": &f.SequenceID, "mission_id": &f.MissionID} {
		if *dst, err = idParam(r, name); err != nil {
			return f, err
		}
	}
	if f.Limit, err = limitParam(r, 100, 1000); err != nil {
		return f, err
	}
	if f.Before, err = beforeParam(r); err != nil {
		return f, err
	}
	f.Category, f.Provider = q.Get("category"), q.Get("provider")
	return f, nil
}

func (s *Server) listSpend(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	f, err := s.spendFilter(r)
	if s.badQuery(w, r, err) {
		return
	}
	list, err := outcomes.ListSpend(r.Context(), s.pool, a.WorkspaceID, f)
	if s.outcomeError(w, r, err, "spend entry") {
		return
	}
	var next *string
	if len(list) == f.Limit {
		v := list[len(list)-1].OccurredAt.Format(time.RFC3339Nano)
		next = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

func (s *Server) getSpend(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !validUUID(r.PathValue("id")) {
		writeError(w, r, http.StatusNotFound, "not_found", "spend entry not found")
		return
	}
	e, err := outcomes.GetSpend(r.Context(), s.pool, a.WorkspaceID, r.PathValue("id"))
	if s.outcomeError(w, r, err, "spend entry") {
		return
	}
	writeJSON(w, http.StatusOK, e)
}

type spendInput struct {
	SequenceID    *string    `json:"sequence_id"`
	MissionID     *string    `json:"mission_id"`
	MailboxID     *string    `json:"mailbox_id"`
	Domain        string     `json:"domain"`
	Category      string     `json:"category"`
	Provider      string     `json:"provider"`
	IntegrationID *string    `json:"integration_id"`
	Units         *float64   `json:"units"`
	UnitName      string     `json:"unit_name"`
	AmountCents   *int64     `json:"amount_cents"`
	Currency      string     `json:"currency"`
	OccurredAt    *time.Time `json:"occurred_at"`
	Ref           *string    `json:"ref"`
	Note          string     `json:"note"`
}

func (s *Server) createSpend(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req spendInput
	if !decode(w, r, &req) {
		return
	}
	for _, id := range []*string{req.SequenceID, req.MissionID, req.MailboxID, req.IntegrationID} {
		if id != nil && !validUUID(*id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "sequence_id, mission_id, mailbox_id and integration_id must be ids")
			return
		}
	}
	e := outcomes.Entry{WorkspaceID: a.WorkspaceID, SequenceID: req.SequenceID, MissionID: req.MissionID,
		MailboxID: req.MailboxID, Domain: req.Domain, Category: req.Category, Provider: req.Provider, IntegrationID: req.IntegrationID,
		Units: req.Units, UnitName: req.UnitName, AmountCents: req.AmountCents, Currency: req.Currency, Ref: req.Ref, Note: req.Note,
		Source: outcomes.SourceManual, ActorID: &a.ActorID}
	if req.OccurredAt != nil {
		e.OccurredAt = *req.OccurredAt
	}
	var created bool
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		if e, created, err = outcomes.Record(r.Context(), tx, e); err != nil || !created {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "spend.recorded", "spend_entry", e.ID,
			map[string]any{"category": e.Category, "provider": e.Provider, "amount_cents": e.AmountCents, "units": e.Units})
	})
	if s.outcomeError(w, r, err, "spend entry") {
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK // the ref was recorded before
	}
	writeJSON(w, status, e)
}

func (s *Server) deleteSpend(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "spend entry not found")
		return
	}
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		e, err := outcomes.GetSpend(r.Context(), tx, a.WorkspaceID, id)
		if err != nil {
			return err
		}
		if err := outcomes.DeleteSpend(r.Context(), tx, a.WorkspaceID, id); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "spend.deleted", "spend_entry", id,
			map[string]any{"category": e.Category, "provider": e.Provider, "amount_cents": e.AmountCents, "units": e.Units})
	})
	if s.outcomeError(w, r, err, "spend entry") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listRecurring(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := outcomes.ListRecurring(r.Context(), s.pool, a.WorkspaceID)
	if s.outcomeError(w, r, err, "recurring cost") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) createRecurring(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req outcomes.Recurring
	if !decode(w, r, &req) {
		return
	}
	var out outcomes.Recurring
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		if out, err = outcomes.CreateRecurring(r.Context(), tx, a.WorkspaceID, a.ActorID, req); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "spend.recurring_created", "spend_recurring", out.ID,
			map[string]any{"name": out.Name, "amount_cents": out.AmountCents, "every": out.Every})
	})
	if s.outcomeError(w, r, err, "recurring cost") {
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateRecurring(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "recurring cost not found")
		return
	}
	var req outcomes.Recurring
	if !decode(w, r, &req) {
		return
	}
	var out outcomes.Recurring
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		if out, err = outcomes.UpdateRecurring(r.Context(), tx, a.WorkspaceID, id, req); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "spend.recurring_updated", "spend_recurring", id,
			map[string]any{"name": out.Name, "amount_cents": out.AmountCents, "every": out.Every, "ends_on": out.EndsOn})
	})
	if s.outcomeError(w, r, err, "recurring cost") {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteRecurring(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "recurring cost not found")
		return
	}
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		if err := outcomes.DeleteRecurring(r.Context(), tx, a.WorkspaceID, id); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "spend.recurring_deleted", "spend_recurring", id, nil)
	})
	if s.outcomeError(w, r, err, "recurring cost") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listRates(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := outcomes.ListRates(r.Context(), s.pool, a.WorkspaceID)
	if s.outcomeError(w, r, err, "rate") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) setRate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Provider        string `json:"provider"`
		UnitName        string `json:"unit_name"`
		UnitPriceMicros int64  `json:"unit_price_micros"`
		Currency        string `json:"currency"`
	}
	if !decode(w, r, &req) {
		return
	}
	var out outcomes.Rate
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		if out, err = outcomes.SetRate(r.Context(), tx, a.WorkspaceID, outcomes.Rate{Provider: req.Provider, UnitName: req.UnitName,
			UnitPriceMicros: req.UnitPriceMicros, Currency: req.Currency}); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "spend.rate_set", "spend_rate", out.Provider+"/"+out.UnitName,
			map[string]any{"unit_price_micros": out.UnitPriceMicros, "currency": out.Currency})
	})
	if s.outcomeError(w, r, err, "rate") {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteRate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	provider, unit := r.URL.Query().Get("provider"), r.URL.Query().Get("unit_name")
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		if err := outcomes.DeleteRate(r.Context(), tx, a.WorkspaceID, provider, unit); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "spend.rate_deleted", "spend_rate", strings.ToLower(provider)+"/"+unit, nil)
	})
	if s.outcomeError(w, r, err, "rate") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Syncs.

func (s *Server) outcomeConnectorIDs() []string {
	if s.integrations == nil {
		return []string{}
	}
	return outcomes.ConnectorIDs(s.integrations.Registry)
}

func (s *Server) listOutcomeSyncs(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := outcomes.ListSyncs(r.Context(), s.pool, a.WorkspaceID, s.outcomeConnectorIDs())
	if s.outcomeError(w, r, err, "sync") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// runOutcomeSync queues a sync of one calendar or CRM now.
func (s *Server) runOutcomeSync(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) || s.integrations == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return
	}
	i, err := s.integrations.Get(r.Context(), s.pool, a.WorkspaceID, id)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	def, _ := s.integrations.Registry.Definition(i.Connector)
	if !def.Has(outcomes.CapMeetings) && !def.Has(outcomes.CapDeals) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", def.Name+" does not sync meetings or deals")
		return
	}
	var job int64
	err = s.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		if job, err = outcomes.Enqueue(r.Context(), tx, a.WorkspaceID, id, ""); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "outcomes.sync_requested", "integration", id, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ref := jobRef(r.PathValue("ws"), job)
	w.Header().Set("Location", ref["url"].(string))
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "job": ref})
}

// Export: rows for a warehouse or a spreadsheet, as CSV or NDJSON.

func (s *Server) outcomesExport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	dataset, format := q.Get("dataset"), q.Get("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "ndjson" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "format must be csv or ndjson")
		return
	}
	from, to, err := period(r)
	if s.badQuery(w, r, err) {
		return
	}
	const maxRows = 100000
	var header []string
	var rows [][]string
	var objects []any
	ctx := r.Context()
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	tm := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	num := func(p *int64) string {
		if p == nil {
			return ""
		}
		return strconv.FormatInt(*p, 10)
	}
	switch dataset {
	case "meetings":
		header = []string{"id", "starts_at", "booked_at", "status", "qualified", "attendee_email", "lead_id", "sequence_id", "sequence_name",
			"attribution", "source", "title"}
		var before *time.Time
		for len(objects) < maxRows {
			page, err := outcomes.ListMeetings(ctx, s.pool, a.WorkspaceID, outcomes.MeetingFilter{From: &from, To: &to, Limit: 500, Before: before})
			if err != nil {
				s.internal(w, r, err)
				return
			}
			for _, m := range page {
				q := ""
				if m.Qualified != nil {
					q = strconv.FormatBool(*m.Qualified)
				}
				objects = append(objects, m)
				rows = append(rows, []string{m.ID, tm(&m.StartsAt), tm(&m.BookedAt), m.Status, q, m.AttendeeEmail, str(m.LeadID), str(m.SequenceID),
					str(m.SequenceName), m.Attribution, m.Source, m.Title})
			}
			if len(page) < 500 {
				break
			}
			before = &page[len(page)-1].StartsAt
		}
	case "deals":
		header = []string{"id", "opened_at", "closed_at", "name", "stage", "state", "amount_cents", "currency", "lead_id", "sequence_id",
			"sequence_name", "attribution", "source"}
		var before *time.Time
		for len(objects) < maxRows {
			page, err := outcomes.ListDeals(ctx, s.pool, a.WorkspaceID, outcomes.DealFilter{From: &from, To: &to, Limit: 500, Before: before})
			if err != nil {
				s.internal(w, r, err)
				return
			}
			for _, d := range page {
				objects = append(objects, d)
				rows = append(rows, []string{d.ID, tm(&d.OpenedAt), tm(d.ClosedAt), d.Name, d.Stage, d.State, num(d.AmountCents), d.Currency,
					str(d.LeadID), str(d.SequenceID), str(d.SequenceName), d.Attribution, d.Source})
			}
			if len(page) < 500 {
				break
			}
			before = &page[len(page)-1].OpenedAt
		}
	case "spend":
		header = []string{"id", "occurred_at", "category", "provider", "amount_cents", "currency", "units", "unit_name",
			"sequence_id", "mission_id", "mailbox_id", "domain", "source", "ref", "note"}
		var before *time.Time
		for len(objects) < maxRows {
			page, err := outcomes.ListSpend(ctx, s.pool, a.WorkspaceID, outcomes.SpendFilter{From: &from, To: &to, Limit: 1000, Before: before})
			if err != nil {
				s.internal(w, r, err)
				return
			}
			for _, e := range page {
				units := ""
				if e.Units != nil {
					units = strconv.FormatFloat(*e.Units, 'f', -1, 64)
				}
				objects = append(objects, e)
				rows = append(rows, []string{e.ID, tm(&e.OccurredAt), e.Category, e.Provider, num(e.AmountCents), e.Currency, units, e.UnitName,
					str(e.SequenceID), str(e.MissionID), str(e.MailboxID), e.Domain, e.Source, str(e.Ref), e.Note})
			}
			if len(page) < 1000 {
				break
			}
			before = &page[len(page)-1].OccurredAt
		}
	case "report":
		f, err := s.reportFilter(r)
		if s.badQuery(w, r, err) {
			return
		}
		if f.GroupBy == "" {
			f.GroupBy = outcomes.GroupSequence
		}
		sel, ok := s.brandFilter(w, r)
		if !ok {
			return
		}
		f.Brand, f.BrandsIn = sel.id, sel.in
		rep, err := outcomes.Summary(ctx, s.pool, a.WorkspaceID, f)
		if s.outcomeError(w, r, err, "report") {
			return
		}
		header = []string{"key", "name", "sent", "replies", "meetings", "held", "no_shows", "qualified", "deals", "won", "pipeline_cents",
			"won_cents", "spend_cents", "direct_cents", "shared_cents", "estimated_cents", "cost_per_reply_cents", "cost_per_meeting_cents",
			"cost_per_deal_cents", "currency", "from", "to"}
		for _, row := range rep.Rows {
			objects = append(objects, map[string]any{"row": row, "currency": rep.Currency, "from": rep.From, "to": rep.To})
			i := strconv.Itoa
			rows = append(rows, []string{row.Key, row.Name, i(row.Sent), i(row.Replies), i(row.Meetings), i(row.Held), i(row.NoShows),
				i(row.Qualified), i(row.Deals), i(row.Won), num(&row.PipelineCents), num(&row.WonCents), num(&row.Spend.TotalCents),
				num(&row.Spend.DirectCents), num(&row.Spend.SharedCents), num(&row.Spend.EstimatedCents), num(row.CostPerReply),
				num(row.CostPerMeeting), num(row.CostPerDeal), rep.Currency, tm(&rep.From), tm(&rep.To)})
		}
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "dataset must be meetings, deals, spend or report")
		return
	}
	name := fmt.Sprintf("outflare-%s-%s-to-%s", dataset, from.UTC().Format(time.DateOnly), to.UTC().Format(time.DateOnly))
	if format == "ndjson" {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.ndjson"`)
		enc := json.NewEncoder(w)
		for _, o := range objects {
			enc.Encode(o) //nolint:errcheck
		}
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
	cw := csv.NewWriter(w)
	cw.Write(header) //nolint:errcheck
	for _, row := range rows {
		for i, v := range row {
			row[i] = csvSafe(v)
		}
		cw.Write(row) //nolint:errcheck
	}
	cw.Flush()
}

// csvSafe stops spreadsheet apps reading a cell as a formula.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return "'" + v
		}
	}
	return v
}
