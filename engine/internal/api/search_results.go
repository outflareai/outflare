package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/ranks"
)

// Search results: one search engine results page for any query, on the
// workspace's own SERP connection (DataForSEO or Geonode), paid within its
// budget (package ranks). Link building playbooks use it to find resource
// pages, the pages that rank for a topic and pages that name a brand.

func (s *Server) routesSearchResults(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/workspaces/{ws}/search-results", s.inWorkspace(auth.ScopeSEOManage, s.readSearchResults))
}

func (s *Server) readSearchResults(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Query         string `json:"query"`
		Country       string `json:"country"`
		Device        string `json:"device"`
		Depth         int    `json:"depth"`
		Engine        string `json:"engine"`
		IntegrationID string `json:"integration_id"`
		EstimateOnly  bool   `json:"estimate_only"`
	}
	if !decode(w, r, &in) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	in.Query = strings.TrimSpace(in.Query)
	in.Country = strings.ToUpper(strings.TrimSpace(in.Country))
	if in.Country == "" {
		in.Country = "US"
	}
	if in.Device == "" {
		in.Device = "desktop"
	}
	if in.Depth == 0 {
		in.Depth = ranks.DefaultSearchDepth
	}
	switch {
	case in.Query == "" || len(in.Query) > 400:
		bad("query is 1 to 400 characters, as you'd type it into the search box")
		return
	case !countryCode.MatchString(in.Country):
		bad("country is a two-letter code, such as US or GB")
		return
	case in.Device != "desktop" && in.Device != "mobile":
		bad("device is desktop or mobile")
		return
	case in.Depth < ranks.MinSearchDepth || in.Depth > ranks.MaxSearchDepth:
		bad("depth is 10 to 100 results")
		return
	case in.IntegrationID != "" && !validUUID(in.IntegrationID):
		bad("integration_id is a SERP connection's id")
		return
	}
	conns, err := s.rankConnections(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(conns) == 0 {
		writeError(w, r, http.StatusConflict, "connector_needed", "searching needs a SERP connection: connect DataForSEO or Geonode on the Connectors page")
		return
	}
	id := conns[0].IntegrationID
	if in.IntegrationID != "" {
		if !slices.ContainsFunc(conns, func(c rankConnection) bool { return c.IntegrationID == in.IntegrationID }) {
			bad("integration_id is a connected SERP connection: DataForSEO or Geonode")
			return
		}
		id = in.IntegrationID
	}
	ctx := r.Context()
	o, err := s.integrations.Open(ctx, s.pool, a.WorkspaceID, id)
	if err != nil {
		connectorError(w, r, err)
		return
	}
	q := ranks.SearchQuery{SERPQuery: connectors.SERPQuery{Keyword: in.Query, Country: in.Country, Device: in.Device, Depth: in.Depth}, Engine: in.Engine}
	connector := o.Connector.Definition().ID
	if in.EstimateOnly {
		est, budget, err := ranks.EstimateSearch(ctx, s.pool, o, q)
		if err != nil {
			s.seoFailed(w, r, o, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"query": in.Query, "integration_id": o.ID, "connector": connector, "estimate": est, "budget": budget})
		return
	}
	res, err := ranks.Search(ctx, s.pool, s.integrations, o, q)
	if err != nil {
		s.seoFailed(w, r, o, err)
		return
	}
	props := map[string]any{"connector": connector, "engine": res.Engine, "country": in.Country, "device": in.Device, "depth": in.Depth,
		"results": len(res.Items), "spent": res.Spent, "unit": res.Unit}
	data := map[string]any{"query": in.Query}
	for k, v := range props {
		data[k] = v
	}
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "web_search.done", "integration", o.ID, data, props)
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": in.Query, "integration_id": o.ID, "connector": connector, "engine": res.Engine,
		"country": in.Country, "device": in.Device, "items": res.Items, "partial": res.Partial, "spent": res.Spent, "unit": res.Unit})
}
