package api

import (
	"net/http"

	"github.com/outflareai/outflare/engine/internal/telemetry"
)

// Usage stats and update checks for the whole server (docs/telemetry.md).
// Anyone signed in can see what is sent; only the server's owner, the person
// who set it up, can turn it on or off.
func (s *Server) routesTelemetry(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/instance/telemetry", s.requireUser(s.getTelemetry))
	mux.HandleFunc("PATCH /v1/instance/telemetry", s.requireUser(s.updateTelemetry))
	mux.HandleFunc("GET /v1/instance/telemetry/preview", s.requireUser(s.previewTelemetry))
}

type telemetryView struct {
	telemetry.Status
	// CanChange is true for the server's owner.
	CanChange bool `json:"can_change"`
}

func (s *Server) telemetryView(r *http.Request) (telemetryView, error) {
	st, err := s.telemetry.Status(r.Context())
	if err != nil {
		return telemetryView{}, err
	}
	owner, err := s.isServerOwner(r)
	return telemetryView{Status: st, CanChange: owner && (st.DisabledBy == nil || *st.DisabledBy == telemetry.OffSetting || *st.DisabledBy == telemetry.OffNoEndpoint)}, err
}

// isServerOwner reports whether the caller is the first user created on
// this server, the one who ran setup.
func (s *Server) isServerOwner(r *http.Request) (bool, error) {
	var first string
	err := s.pool.QueryRow(r.Context(), `SELECT id::text FROM users ORDER BY created_at, id LIMIT 1`).Scan(&first)
	return err == nil && first == principalFrom(r.Context()).UserID, err
}

func (s *Server) getTelemetry(w http.ResponseWriter, r *http.Request) {
	v, err := s.telemetryView(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updateTelemetry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "enabled is required")
		return
	}
	owner, err := s.isServerOwner(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !owner {
		writeError(w, r, http.StatusForbidden, "forbidden", "only the person who set up this server can change usage stats")
		return
	}
	switch {
	case s.telemetry.BuildOff:
		writeError(w, r, http.StatusConflict, "managed", "usage stats are turned off by this build")
		return
	case s.telemetry.EnvOff:
		writeError(w, r, http.StatusConflict, "managed", "usage stats are turned off by OUTFLARE_TELEMETRY or DO_NOT_TRACK on the server; change it there")
		return
	}
	if err := s.telemetry.SetEnabled(r.Context(), *req.Enabled, principalFrom(r.Context()).UserID); err != nil {
		s.internal(w, r, err)
		return
	}
	s.getTelemetry(w, r)
}

func (s *Server) previewTelemetry(w http.ResponseWriter, r *http.Request) {
	rep, err := s.telemetry.Preview(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
