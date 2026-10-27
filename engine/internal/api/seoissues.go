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

type crawlIssueTemplate struct {
	Template string `json:"template"`
	Pages    int    `json:"pages"`
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
	sort.Slice(list, func(i, j int) bool {
		if a, b := issueSeverityRank[list[i].Severity], issueSeverityRank[list[j].Severity]; a != b {
			return a < b
		}
		if list[i].Pages != list[j].Pages {
			return list[i].Pages > list[j].Pages
		}
		return list[i].ID < list[j].ID
	})
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
	if v := q.Get("after"); v != "" {
		after, err = strconv.ParseInt(v, 10, 64)
		if err != nil || after < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "after is an id from next_after")
			return
		}
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT i.id::text, i.page_id::text, p.url, i.template, i.detail, p.http_status
		FROM crawl_issues i JOIN crawl_pages p ON p.id = i.page_id
		WHERE i.run_id = $1 AND i.workspace_id = $2 AND i.rule = $3 AND ($4 = '' OR i.template = $4) AND i.id > $5
		ORDER BY i.id LIMIT $6`, run.ID, a.WorkspaceID, def.ID, q.Get("template"), after, limit)
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
	writeJSON(w, http.StatusOK, map[string]any{"rule": def, "data": list, "next_after": next})
}

// seoRules lists every audit rule.
func (s *Server) listSEORules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": slices.Clone(crawl.Rules)})
}
