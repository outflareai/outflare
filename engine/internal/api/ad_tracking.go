package api

import (
	"net/http"

	"github.com/outflareai/outflare/engine/internal/adtrack"
	"github.com/outflareai/outflare/engine/internal/auth"
)

// First-party ad tracking: the tracking template for each ad network, from
// the adtrack catalog. Paid media owns connections, cost and send-back.

func (s *Server) routesAdTracking(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/tracking-templates", s.inWorkspace(auth.ScopeAnalyticsRead, s.listAdTrackingTemplates))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/tracking-health", s.inWorkspace(auth.ScopeAnalyticsRead, s.adTrackingHealth))
}

type adTrackingTemplateView struct {
	Network   string         `json:"network"`
	Label     string         `json:"label"`
	Connector *string        `json:"connector"`
	Field     *string        `json:"field"`
	Template  string         `json:"template"`
	Macros    adtrack.Macros `json:"macros"`
	ClickIDs  []string       `json:"click_ids"`
	Cookies   []string       `json:"cookies"`
	Note      *string        `json:"note"`
}

func (s *Server) listAdTrackingTemplates(w http.ResponseWriter, r *http.Request) {
	only := r.URL.Query().Get("network")
	if _, ok := adtrack.NetworkOf(only); only != "" && !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "network is one of the ids GET .../ads/tracking-templates lists")
		return
	}
	nilIfEmpty := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	out := []adTrackingTemplateView{}
	for _, n := range adtrack.Networks {
		if only != "" && n.ID != only {
			continue
		}
		v := adTrackingTemplateView{Network: n.ID, Label: n.Label, Connector: nilIfEmpty(n.Connector), Field: nilIfEmpty(n.Field),
			Template: n.Template(), Macros: n.Macros, ClickIDs: []string{}, Cookies: []string{}, Note: nilIfEmpty(n.Note)}
		for _, c := range adtrack.ClickIDs {
			if c.Network == n.ID {
				v.ClickIDs = append(v.ClickIDs, c.Param)
			}
		}
		for _, c := range adtrack.Cookies {
			if c.Network == n.ID {
				v.Cookies = append(v.Cookies, c.Name)
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}
