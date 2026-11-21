package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// What the platform did with a conversion after accepting it. "Accepted"
// in the send log only means the request was well formed; the platform
// processes it on its own time, and this asks it how that went.

func (s *Server) routesAdSendStatus(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/ads/conversion-sends/{id}/status", s.onBrand(auth.ScopeAnalyticsRead, s.getAdConversionSendStatus))
}

type adSendStatusCount struct {
	Reason string `json:"reason"`
	Label  string `json:"label"`
	Count  int64  `json:"count"`
}

type adSendStatus struct {
	// State is processing, processed, partly_processed, failed or unknown
	// (the send went by a path the platform reports nothing for).
	State     string              `json:"state"`
	Detail    string              `json:"detail"`
	RequestID string              `json:"request_id"`
	Records   int64               `json:"records"`
	Errors    []adSendStatusCount `json:"errors"`
	Warnings  []adSendStatusCount `json:"warnings"`
	CheckedAt time.Time           `json:"checked_at"`
}

// reasonLabel turns the platform's code into words: EVENT_TIME_INVALID
// reads "Event time invalid".
func reasonLabel(code string) string {
	w := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "_", " "))
	if w == "" {
		return "No reason given"
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

func (s *Server) getAdConversionSendStatus(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "send not found")
		return
	}
	var status, requestID, integrationID string
	err := s.pool.QueryRow(r.Context(), `SELECT q.status, q.request_id, x.integration_id::text
		FROM ad_conversion_sends q JOIN ad_conversion_rules r ON r.id = q.rule_id JOIN ad_accounts x ON x.id = r.ad_account_id
		WHERE q.workspace_id = $1 AND q.id = $2 AND x.brand_id = $3`, a.WorkspaceID, id, a.Brand).Scan(&status, &requestID, &integrationID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "send not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := adSendStatus{State: "unknown", RequestID: requestID, Errors: []adSendStatusCount{}, Warnings: []adSendStatusCount{}, CheckedAt: time.Now().UTC()}
	if status != "sent" {
		out.Detail = "This conversion was not sent, so the platform has nothing to say about it."
		writeJSON(w, http.StatusOK, out)
		return
	}
	if requestID == "" {
		out.Detail = "This conversion went through the platform's older upload, which reports nothing after accepting it."
		writeJSON(w, http.StatusOK, out)
		return
	}
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "not_active", "the ad account's platform is no longer connected")
		return
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	rd, ok := o.Connector.(connectors.FeedStatusReader)
	if !ok {
		out.Detail = o.Connector.Definition().Name + " reports nothing after accepting a conversion."
		writeJSON(w, http.StatusOK, out)
		return
	}
	st, err := rd.RequestStatus(r.Context(), o.Client, o.Creds, requestID)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	out.Records = st.Records
	for _, e := range st.Errors {
		out.Errors = append(out.Errors, adSendStatusCount{Reason: e.Reason, Label: reasonLabel(e.Reason), Count: e.Count})
	}
	for _, e := range st.Warnings {
		out.Warnings = append(out.Warnings, adSendStatusCount{Reason: e.Reason, Label: reasonLabel(e.Reason), Count: e.Count})
	}
	switch st.Status {
	case "SUCCESS":
		out.State, out.Detail = "processed", "The platform processed the request this conversion went in, with no errors."
	case "PARTIAL_SUCCESS":
		out.State, out.Detail = "partly_processed", "The platform processed the request this conversion went in, and refused some of its records."
	case "FAILED":
		out.State, out.Detail = "failed", "The platform refused the request this conversion went in."
	case "PROCESSING":
		out.State, out.Detail = "processing", "The platform is still processing the request. It answers from about 30 minutes after a send."
	default:
		out.Detail = "The platform gave a status Outflare does not know: " + reasonLabel(st.Status) + "."
	}
	writeJSON(w, http.StatusOK, out)
}
