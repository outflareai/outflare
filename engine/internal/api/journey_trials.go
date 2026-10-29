package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/outflareai/outflare/engine/internal/journeys"
)

// testJourney takes one person through a journey's live or draft version
// and reports the path they would take, without sending, writing or
// recording anything (journeys.Service.Test).
func (s *Server) testJourney(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		RecordID string            `json:"record_id"`
		Version  string            `json:"version"`
		Event    map[string]any    `json:"event"`
		Paths    map[string]string `json:"paths"`
	}
	if !decode(w, r, &req) {
		return
	}
	switch {
	case !validUUID(req.RecordID):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "record_id must be a record id")
		return
	case req.Version != "" && req.Version != "live" && req.Version != "draft":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "version is live or draft")
		return
	case len(req.Paths) > 100:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "paths names at most 100 branch steps")
		return
	}
	for _, way := range req.Paths {
		if way != "yes" && way != "no" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "each of paths is yes or no")
			return
		}
	}
	if raw, _ := json.Marshal(req.Event); len(raw) > 32<<10 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "event must be at most 32 KB of JSON")
		return
	}
	// A journey not yet activated is edited in place, so its draft is the
	// journey itself.
	draft := req.Version == "draft" && cur.Status != "draft"
	svc := &journeys.Service{Pool: s.pool, Emails: s.emails, Log: s.log, Fields: s, Deals: s}
	res, err := svc.Test(r.Context(), a.WorkspaceID, cur.ID, journeys.TestInput{RecordID: req.RecordID, Draft: draft, Event: req.Event, Paths: req.Paths})
	switch {
	case errors.Is(err, journeys.ErrNoDraft):
		writeError(w, r, http.StatusNotFound, "not_found", "the journey has no draft version; start one with POST .../draft")
	case errors.Is(err, journeys.ErrNotPerson):
		writeError(w, r, http.StatusNotFound, "not_found", "record not found, or not a person")
	case err != nil:
		s.internal(w, r, err)
	case res == nil:
		writeError(w, r, http.StatusNotFound, "not_found", "journey not found")
	default:
		if req.Version == "draft" {
			res.Version = "draft"
		}
		writeJSON(w, http.StatusOK, res)
	}
}
