package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/indexability"
)

// The indexability report of an SEO site: every page any source knows,
// whether it can be indexed, whether Google indexed it and the evidence,
// and how that changed day by day (engine/internal/indexability).

func (s *Server) routesIndexability(mux *http.ServeMux) {
	read := auth.ScopeSEORead
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/indexability", s.inBrand(read, s.getIndexability))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/indexability/pages", s.inBrand(read, s.listIndexabilityPages))
}

// indexabilityGap is a source the report lacks, with what to do about it.
type indexabilityGap struct {
	Source    string `json:"source"`
	Connector string `json:"connector,omitempty"`
	Message   string `json:"message"`
}

func (s *Server) getIndexability(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	is := indexability.Site{ID: site.ID, WorkspaceID: a.WorkspaceID, Host: site.Host}
	rep, err := indexability.Build(ctx, s.pool, is)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	history, err := indexability.History(ctx, s.pool, is, 90)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type issueView struct {
		indexability.Issue
		Pages int `json:"pages"`
	}
	issues := make([]issueView, 0, len(indexability.Issues))
	for _, i := range indexability.Issues {
		issues = append(issues, issueView{i, rep.Summary.Issues[i.ID]})
	}
	gaps := []indexabilityGap{}
	src := rep.Sources
	if src.Crawl.RunID == nil {
		gaps = append(gaps, indexabilityGap{Source: "crawl", Message: "Crawl the site so the report knows its pages and which can be indexed."})
	}
	if !src.SearchConsole.Connected {
		gaps = append(gaps, indexabilityGap{Source: "search_console", Connector: "google_search_console",
			Message: "Connect Google Search Console: it says which pages Google indexed and which it showed in results."})
	}
	if src.SEOTool.Connector == nil {
		gaps = append(gaps, indexabilityGap{Source: "seo_tool", Connector: "ahrefs",
			Message: "Optional: an SEO tool such as Ahrefs or DataForSEO shows pages ranking even where Search Console has nothing."})
	}
	writeJSON(w, http.StatusOK, map[string]any{"site_id": site.ID, "summary": rep.Summary, "sources": src, "issues": issues,
		"history": history, "gaps": gaps})
}

func (s *Server) listIndexabilityPages(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return
	}
	q := r.URL.Query()
	verdict, issue, ix, search := q.Get("verdict"), q.Get("issue"), q.Get("indexable"), strings.ToLower(strings.TrimSpace(q.Get("q")))
	if verdict != "" && !slices.Contains(indexability.Verdicts, verdict) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "verdict is "+strings.Join(indexability.Verdicts, ", "))
		return
	}
	if issue != "" && !slices.ContainsFunc(indexability.Issues, func(i indexability.Issue) bool { return i.ID == issue }) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "issue is one of the report's issue ids")
		return
	}
	if ix != "" && ix != "true" && ix != "false" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "indexable is true or false")
		return
	}
	limit, err := limitParam(r, 100, 1000)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	offset, ok := offsetParam(w, r)
	if !ok {
		return
	}
	rep, err := indexability.Build(r.Context(), s.pool, indexability.Site{ID: site.ID, WorkspaceID: a.WorkspaceID, Host: site.Host})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list := []indexability.Page{}
	for _, p := range rep.Pages {
		switch {
		case verdict != "" && p.Verdict != verdict,
			issue != "" && !slices.Contains(p.Issues, issue),
			ix == "true" && (p.Indexable == nil || !*p.Indexable),
			ix == "false" && (p.Indexable == nil || *p.Indexable),
			search != "" && !strings.Contains(strings.ToLower(p.URL), search):
			continue
		}
		list = append(list, p)
	}
	total := len(list)
	var next *int
	if offset >= total {
		list = []indexability.Page{}
	} else {
		end := min(offset+limit, total)
		list = list[offset:end]
		if end < total {
			next = &end
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_offset": next})
}
