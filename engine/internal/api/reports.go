package api

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/reports"
)

func (s *Server) routesReports(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/reports", s.inBrand(auth.ScopeSequencesRead, s.report))
}

// parseWhen reads a date (2026-10-01, the start of that day in tz) or an
// RFC 3339 time.
func parseWhen(v string, loc *time.Location) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	tz := q.Get("timezone")
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "timezone must be an IANA name such as Australia/Sydney")
		return
	}
	to := time.Now()
	if v := q.Get("to"); v != "" {
		t, ok := parseWhen(v, loc)
		if !ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "to must be a date (2026-10-31) or an RFC 3339 time")
			return
		}
		if len(v) == len("2006-01-02") {
			t = t.AddDate(0, 0, 1) // a date includes the whole day
		}
		to = t
	}
	from := to.AddDate(0, 0, -30)
	if v := q.Get("from"); v != "" {
		t, ok := parseWhen(v, loc)
		if !ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "from must be a date (2026-10-01) or an RFC 3339 time")
			return
		}
		from = t
	}
	if !from.Before(to) || to.Sub(from) > 400*24*time.Hour {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from must be before to, and at most 400 days apart")
		return
	}
	group := q.Get("group_by")
	if group != "" && !slices.Contains(reports.Groupings, group) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "group_by must be one of "+strings.Join(reports.Groupings, ", "))
		return
	}
	for _, k := range []string{"sequence_id", "mailbox_id"} {
		if v := q.Get(k); v != "" && !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", k+" must be an id")
			return
		}
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	rep, err := reports.Run(r.Context(), s.pool, a.WorkspaceID, reports.Query{
		From: from, To: to, GroupBy: group, Timezone: tz,
		SequenceID: q.Get("sequence_id"), MailboxID: q.Get("mailbox_id"), Domain: strings.TrimSpace(q.Get("domain")),
		Brand: sel.id, BrandsIn: sel.in,
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
