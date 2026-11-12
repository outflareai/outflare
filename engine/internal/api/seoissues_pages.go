package api

import (
	"context"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/crawl"
)

// fillIssuePageCounts sets how many distinct pages have each severity of
// issue, so a crawl card can show pages rather than rule hits (one page
// can break a hreflang rule once per language pair).
func (s *Server) fillIssuePageCounts(ctx context.Context, ws string, runs ...*crawlRunView) error {
	ids := make([]string, 0, len(runs))
	byID := map[string]*crawlRunView{}
	for _, r := range runs {
		if r == nil {
			continue
		}
		r.Issues.ErrorPages, r.Issues.WarningPages, r.Issues.NoticePages = 0, 0, 0
		ids = append(ids, r.ID)
		byID[r.ID] = r
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT run_id::text, severity, count(DISTINCT page_id)::int
		FROM crawl_issues
		WHERE workspace_id = $1 AND run_id = ANY($2::uuid[])
		GROUP BY 1, 2`, ws, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, sev string
		var n int
		if err := rows.Scan(&id, &sev, &n); err != nil {
			return err
		}
		r := byID[id]
		if r == nil {
			continue
		}
		switch sev {
		case crawl.SevError:
			r.Issues.ErrorPages = n
		case crawl.SevWarning:
			r.Issues.WarningPages = n
		case crawl.SevNotice:
			r.Issues.NoticePages = n
		}
	}
	return rows.Err()
}

func fillIssuePageCountsList(ctx context.Context, s *Server, ws string, list []crawlRunView) error {
	runs := make([]*crawlRunView, len(list))
	for i := range list {
		runs[i] = &list[i]
	}
	return s.fillIssuePageCounts(ctx, ws, runs...)
}

// foldIssueTemplates merges templates that differ only by a leading
// locale segment, and records each locale's page count on the folded
// row.
func foldIssueTemplates(list []crawlIssueView) []crawlIssueView {
	for i := range list {
		v := &list[i]
		byFold := map[string]int{}
		var out []crawlIssueTemplate
		for _, t := range v.Templates {
			folded, loc, ok := crawl.FoldLocaleTemplate(t.Template)
			if !ok {
				out = append(out, t)
				continue
			}
			idx, seen := byFold[folded]
			if !seen {
				idx = len(out)
				byFold[folded] = idx
				out = append(out, crawlIssueTemplate{Template: folded})
			}
			out[idx].Pages += t.Pages
			out[idx].Locales = append(out[idx].Locales, crawlIssueLocale{Locale: loc, Template: t.Template, Pages: t.Pages})
		}
		for i := range out {
			sort.Slice(out[i].Locales, func(a, b int) bool {
				if out[i].Locales[a].Pages != out[i].Locales[b].Pages {
					return out[i].Locales[a].Pages > out[i].Locales[b].Pages
				}
				return out[i].Locales[a].Locale < out[i].Locales[b].Locale
			})
		}
		v.Templates = out
	}
	return list
}

// templatesForIssue lists the stored templates that a pages query should
// match: one exact template, or every locale variant of a folded one.
func (s *Server) templatesForIssue(ctx context.Context, runID, ws, rule, template, locale string) ([]string, error) {
	if template == "" || !crawl.IsFoldedLocaleTemplate(template) {
		if template == "" {
			// An empty slice, not nil: getCrawlIssue binds this as text[]
			// and keeps every row only when cardinality($4) = 0. A nil
			// slice becomes SQL NULL, cardinality(NULL) is NULL, and the
			// filter would hide every page.
			return []string{}, nil
		}
		return []string{template}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT template FROM crawl_issues
		WHERE run_id = $1 AND workspace_id = $2 AND rule = $3`, runID, ws, rule)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		var t string
		return t, row.Scan(&t)
	})
	if err != nil {
		return nil, err
	}
	var out []string
	want := strings.ToLower(locale)
	for _, t := range all {
		folded, loc, ok := crawl.FoldLocaleTemplate(t)
		if !ok || folded != template {
			continue
		}
		if want != "" && loc != want {
			continue
		}
		out = append(out, t)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}
