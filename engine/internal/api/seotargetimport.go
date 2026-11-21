package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/seointel"
)

// getPageTarget reads one target with its summary.
func (s *Server) getPageTarget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return
	}
	id := r.PathValue("target")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "target not found")
		return
	}
	s.writeTarget(w, r, site, id)
}

type targetImportError struct {
	Index   int    `json:"index"`
	URL     string `json:"url"`
	Message string `json:"message"`
}

// importPageTargets sets many pages' targets at once, such as a site's
// keyword map from a spreadsheet: each row a page with its keyword, its
// secondary keywords, intent, page type and summary. A page that has a
// target keeps whatever a row leaves out; a new page needs a keyword. Rows
// that are wrong are skipped and listed; the rest are saved together.
func (s *Server) importPageTargets(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Targets []targetInput `json:"targets"`
	}
	if !decode(w, r, &in) {
		return
	}
	site, ok := s.targetSite(w, r)
	if !ok {
		return
	}
	if len(in.Targets) == 0 || len(in.Targets) > maxTargetBatch {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "targets has 1 to "+strconv.Itoa(maxTargetBatch)+" pages; send a bigger map in parts")
		return
	}

	type row struct {
		index                     int
		url                       string
		keyword                   *string
		secondary                 *[]string
		status                    *string
		intent, pageType, summary *string
	}
	var rows []row
	errs := []targetImportError{}
	seen := map[string]bool{}
	for i := range in.Targets {
		t := &in.Targets[i]
		raw := ""
		if t.URL != nil {
			raw = *t.URL
		}
		fail := func(msg string) { errs = append(errs, targetImportError{Index: i, URL: raw, Message: msg}) }
		page, ok := targetURL(site, raw)
		if !ok {
			fail("url is a page of the site (" + site.Host + ")")
			continue
		}
		if seen[page] {
			fail("the page is in the import twice")
			continue
		}
		msg, kw, sec := importKeywords(t)
		if msg == "" {
			msg = t.passport()
		}
		if msg == "" && t.Status != nil && *t.Status != "set" && *t.Status != "dismissed" {
			msg = "status is set or dismissed"
		}
		if msg != "" {
			fail(msg)
			continue
		}
		seen[page] = true
		rows = append(rows, row{i, page, kw, sec, t.Status, t.Intent, t.PageType, t.Summary})
	}

	ctx := r.Context()
	created, updated := 0, 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var createdBy *string
		if a.ActorID != "" {
			createdBy = &a.ActorID
		}
		for _, x := range rows {
			// A new page needs a keyword unless it is dismissed.
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM page_targets WHERE workspace_id = $1 AND site_id = $2 AND url = $3)`,
				a.WorkspaceID, site.ID, x.url).Scan(&exists); err != nil {
				return err
			}
			dismissed := x.status != nil && *x.status == "dismissed"
			if !exists && !dismissed && (x.keyword == nil || *x.keyword == "") {
				errs = append(errs, targetImportError{Index: x.index, URL: x.url, Message: "a new page needs a keyword"})
				continue
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO page_targets (workspace_id, site_id, url, keyword, secondary, status, origin, created_by, intent, page_type, summary)
				VALUES ($1, $2, $3, COALESCE($4, ''), COALESCE($5, '{}'::text[]), COALESCE($6, 'set'), 'manual', $7, COALESCE($8, ''), COALESCE($9, ''), COALESCE($10, ''))
				ON CONFLICT (site_id, url) DO UPDATE SET
					keyword = COALESCE($4, page_targets.keyword), secondary = COALESCE($5, page_targets.secondary),
					status = COALESCE($6, CASE WHEN page_targets.status = 'suggested' THEN 'set' ELSE page_targets.status END),
					origin = CASE WHEN $4::text IS NULL THEN page_targets.origin ELSE 'manual' END,
					intent = COALESCE($8, page_targets.intent), page_type = COALESCE($9, page_targets.page_type),
					summary = COALESCE($10, page_targets.summary), updated_at = now()
`,
				a.WorkspaceID, site.ID, x.url, x.keyword, x.secondary, x.status, createdBy, x.intent, x.pageType, x.summary)
			if err != nil {
				return err
			}
			if exists {
				updated++
			} else {
				created++
			}
		}
		// A page whose keyword ended up empty while set is not a target.
		var empty int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM page_targets WHERE workspace_id = $1 AND site_id = $2 AND status = 'set' AND keyword = ''`,
			a.WorkspaceID, site.ID).Scan(&empty); err != nil {
			return err
		}
		if empty > 0 {
			return errTargetNoKeyword
		}
		if created+updated == 0 {
			return nil
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "page_target.imported", "seo_site", site.ID,
			map[string]any{"created": created, "updated": updated, "skipped": len(errs)}); err != nil {
			return err
		}
		_, err := seointel.Refresh(ctx, tx, a.WorkspaceID, site.ID, "", seointel.TriggerChange)
		return err
	})
	switch {
	case errors.Is(err, errTargetNoKeyword):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a target that is set needs a keyword; nothing was imported")
		return
	case s.allocError(w, r, err):
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "updated": updated, "skipped": len(errs), "errors": errs})
}

// importKeywords checks a row's keyword and secondary keywords as a write
// does, returning a message for the first that is wrong.
func importKeywords(t *targetInput) (string, *string, *[]string) {
	var kw *string
	if t.Keyword != nil {
		k := seointel.Keyword(*t.Keyword)
		if len(k) > seointel.MaxKeyword {
			return "keyword is at most 200 characters", nil, nil
		}
		kw = &k
	}
	var sec *[]string
	if t.Secondary != nil {
		if len(*t.Secondary) > 10 {
			return "secondary has at most 10 keywords", nil, nil
		}
		list := []string{}
		for _, x := range *t.Secondary {
			k := seointel.Keyword(x)
			if len(k) > seointel.MaxKeyword {
				return "a secondary keyword is at most 200 characters", nil, nil
			}
			if k != "" && (kw == nil || k != *kw) && !slices.Contains(list, k) {
				list = append(list, k)
			}
		}
		sec = &list
	}
	return "", kw, sec
}
