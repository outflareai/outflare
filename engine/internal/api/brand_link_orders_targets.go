package api

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"

	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
)

// Link targets: the brand's own article and product pages a paid link can
// point at, the ranked suggestions from stored competitor data, and the
// anchor choices for a page. Nothing here calls an SEO provider.

func (s *Server) routesLinkOrderTargets(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/link-orders/targets", s.onBrand(auth.ScopeCoverageRead, s.listLinkOrderTargets))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/targets", s.onBrand(auth.ScopeCoverageManage, s.saveLinkOrderTarget))
	mux.HandleFunc("PATCH /v1/brands/{brand}/link-orders/targets/{id}", s.onBrand(auth.ScopeCoverageManage, s.updateLinkOrderTarget))
	mux.HandleFunc("GET /v1/brands/{brand}/link-orders/targets/suggestions", s.onBrand(auth.ScopeCoverageRead, s.suggestLinkOrderTargets))
	mux.HandleFunc("GET /v1/brands/{brand}/link-orders/targets/anchors", s.onBrand(auth.ScopeCoverageRead, s.linkOrderTargetAnchors))
}

func linkTargetKindParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	k := r.URL.Query().Get("kind")
	if k != "" && k != linkorders.TargetArticle && k != linkorders.TargetProduct {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is article or product")
		return "", false
	}
	return k, true
}

func (s *Server) listLinkOrderTargets(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	kind, ok := linkTargetKindParam(w, r)
	if !ok {
		return
	}
	f := linkorders.TargetFilter{Kind: kind}
	switch r.URL.Query().Get("selected") {
	case "":
	case "true":
		t := true
		f.Selected = &t
	case "false":
		v := false
		f.Selected = &v
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "selected is true or false")
		return
	}
	list, err := linkorders.ListTargets(r.Context(), s.pool, ws, brand, f)
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) saveLinkOrderTarget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, actor, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var in struct {
		URL      string  `json:"url"`
		Kind     *string `json:"kind"`
		Title    *string `json:"title"`
		Selected *bool   `json:"selected"`
		Source   string  `json:"source"`
	}
	if !decode(w, r, &in) {
		return
	}
	var out *linkorders.Target
	var created bool
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		t, c, err := linkorders.SaveTarget(r.Context(), tx, ws, brand, actor, linkorders.TargetInput{
			URL: in.URL, Kind: in.Kind, Title: in.Title, Selected: in.Selected, Source: in.Source,
		})
		if err != nil {
			return err
		}
		out, created = t, c
		props := map[string]any{"brand_id": a.Brand, "kind": t.Kind, "selected": t.Selected, "source": t.Source, "created": c}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "link_orders.target_saved", "brand", a.Brand, props, props)
	})
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}

func (s *Server) updateLinkOrderTarget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, actor, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	id, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Kind     *string `json:"kind"`
		Title    *string `json:"title"`
		Selected *bool   `json:"selected"`
	}
	if !decode(w, r, &in) {
		return
	}
	var out *linkorders.Target
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		t, err := linkorders.PatchTarget(r.Context(), tx, ws, brand, id, actor, in.Kind, in.Title, in.Selected)
		if err != nil {
			return err
		}
		out = t
		props := map[string]any{"brand_id": a.Brand, "kind": t.Kind, "selected": t.Selected}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "link_orders.target_updated", "brand", a.Brand, props, props)
	})
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) suggestLinkOrderTargets(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	kind, ok := linkTargetKindParam(w, r)
	if !ok {
		return
	}
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	out, err := linkorders.SuggestTargets(r.Context(), s.pool, ws, brand, kind, limit)
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) linkOrderTargetAnchors(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	u, opts, err := linkorders.AnchorsFor(r.Context(), s.pool, ws, brand, r.URL.Query().Get("page"))
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": u, "anchors": opts})
}
