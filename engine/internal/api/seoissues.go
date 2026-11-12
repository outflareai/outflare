package api

import (
	"net/http"
	"slices"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/crawl"
)

// The audit's results: the issues a finished crawl found, grouped by rule
// and by URL template, and the pages behind each.

type crawlIssueLocale struct {
	Locale   string `json:"locale"`
	Template string `json:"template"`
	Pages    int    `json:"pages"`
}

type crawlIssueTemplate struct {
	Template string             `json:"template"`
	Pages    int                `json:"pages"`
	Locales  []crawlIssueLocale `json:"locales,omitempty"`
}

type crawlIssueView struct {
	crawl.Rule
	Pages     int                  `json:"pages"`
	Templates []crawlIssueTemplate `json:"templates"`
}

var issueSeverityRank = map[string]int{crawl.SevError: 0, crawl.SevWarning: 1, crawl.SevNotice: 2}

func (s *Server) listCrawlIssues(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	run, _, ok := s.crawlFor(w, r, a)
	if !ok {
		return
	}
	sev := r.URL.Query().Get("severity")
	if _, ok := issueSeverityRank[sev]; sev != "" && !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "severity is error, warning or notice")
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT rule, template, count(DISTINCT page_id)::int FROM crawl_issues
		WHERE run_id = $1 AND workspace_id = $2 AND ($3 = '' OR severity = $3)
		GROUP BY rule, template`, run.ID, a.WorkspaceID, sev)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	byRule := map[string]*crawlIssueView{}
	var rule, template string
	var n int
	_, err = pgx.ForEachRow(rows, []any{&rule, &template, &n}, func() error {
		v := byRule[rule]
		if v == nil {
			def, ok := crawl.RuleFor(rule)
			if !ok {
				def = crawl.Rule{ID: rule, Severity: crawl.SevNotice}
			}
			v = &crawlIssueView{Rule: def, Templates: []crawlIssueTemplate{}}
			byRule[rule] = v
		}
		v.Pages += n
		v.Templates = append(v.Templates, crawlIssueTemplate{Template: template, Pages: n})
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list := make([]crawlIssueView, 0, len(byRule))
	for _, v := range byRule {
		sort.Slice(v.Templates, func(i, j int) bool {
			if v.Templates[i].Pages != v.Templates[j].Pages {
				return v.Templates[i].Pages > v.Templates[j].Pages
			}
			return v.Templates[i].Template < v.Templates[j].Template
		})
		list = append(list, *v)
	}
	fold := r.URL.Query().Get("fold_locales") == "true" || r.URL.Query().Get("fold_locales") == "1"
	if fold {
		list = foldIssueTemplates(list)
		for i := range list {
			sort.Slice(list[i].Templates, func(a, b int) bool {
				if list[i].Templates[a].Pages != list[i].Templates[b].Pages {
					return list[i].Templates[a].Pages > list[i].Templates[b].Pages
				}
				return list[i].Templates[a].Template < list[i].Templates[b].Template
			})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if a, b := issueSeverityRank[list[i].Severity], issueSeverityRank[list[j].Severity]; a != b {
			return a < b
		}
		if list[i].Pages != list[j].Pages {
			return list[i].Pages > list[j].Pages
		}
		return list[i].ID < list[j].ID
	})
	if err := s.fillIssuePageCounts(r.Context(), a.WorkspaceID, run); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "crawl": run})
}

type crawlIssuePage struct {
	ID         string `json:"id"`
	PageID     string `json:"page_id"`
	URL        string `json:"url"`
	Template   string `json:"template"`
	Detail     string `json:"detail"`
	HTTPStatus *int   `json:"http_status"`
}

func (s *Server) getCrawlIssue(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	run, _, ok := s.crawlFor(w, r, a)
	if !ok {
		return
	}
	def, known := crawl.RuleFor(r.PathValue("rule"))
	if !known {
		writeError(w, r, http.StatusNotFound, "not_found", "no such rule; GET .../issues lists them")
		return
	}
	q := r.URL.Query()
	limit, err := limitParam(r, 100, 1000)
	if s.allocError(w, r, err) {
		return
	}
	var after int64
	if v, ok := pageCursor(w, r, pageAfter); !ok {
		return
	} else if v != "" {
		after, err = strconv.ParseInt(v, 10, 64)
		if err != nil || after < 0 {
			invalidCursor(w, r)
			return
		}
	}
	templates, err := s.templatesForIssue(r.Context(), run.ID, a.WorkspaceID, def.ID, q.Get("template"), q.Get("locale"))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT i.id::text, i.page_id::text, p.url, i.template, i.detail, p.http_status
		FROM crawl_issues i JOIN crawl_pages p ON p.id = i.page_id
		WHERE i.run_id = $1 AND i.workspace_id = $2 AND i.rule = $3
		  AND (cardinality($4::text[]) = 0 OR i.template = ANY($4)) AND i.id > $5
		ORDER BY i.id LIMIT $6`, run.ID, a.WorkspaceID, def.ID, templates, after, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (crawlIssuePage, error) {
		var v crawlIssuePage
		return v, row.Scan(&v.ID, &v.PageID, &v.URL, &v.Template, &v.Detail, &v.HTTPStatus)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) == limit {
		next = &list[len(list)-1].ID
	}
	if list == nil {
		list = []crawlIssuePage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule": def, "data": list, "next_cursor": nextCursor(pageAfter, next)})
}

// seoRules lists every audit rule.
func (s *Server) listSEORules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": slices.Clone(crawl.Rules)})
}
