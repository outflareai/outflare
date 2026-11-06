package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/coverage"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

func (s *Server) routesCoverageActions(m *http.ServeMux) {
	base := "/v1/brands/{brand}/coverage"
	m.HandleFunc("GET "+base+"/outcomes", s.onBrand(auth.ScopeCoverageRead, s.coverageOutcomes))
	m.HandleFunc("POST "+base+"/documents/{id}/extract", s.onBrand(auth.ScopeCoverageFinanceManage, s.coverageQueueExtraction))
	m.HandleFunc("POST "+base+"/placements/{id}/review", s.onBrand(auth.ScopeCoverageManage, s.coverageQueueReview))
	m.HandleFunc("GET "+base+"/schema", s.onBrand(auth.ScopeCoverageRead, s.coverageSchema))
	m.HandleFunc("GET "+base+"/report-snapshots", s.onBrand(auth.ScopeCoverageRead, s.coverageListSnapshots))
	m.HandleFunc("GET "+base+"/report-snapshots/{id}", s.onBrand(auth.ScopeCoverageRead, s.coverageGetSnapshot))
	m.HandleFunc("POST "+base+"/report-snapshots", s.onBrand(auth.ScopeCoverageManage, s.coverageSaveSnapshot))
	m.HandleFunc("GET "+base+"/reports", s.onBrand(auth.ScopeCoverageRead, s.coverageReports))
	m.HandleFunc("GET "+base+"/finance-report", s.onBrand(auth.ScopeCoverageFinanceRead, s.coverageFinanceReport))
	m.HandleFunc("GET "+base+"/{resource}/{id}/history", s.onBrand(auth.ScopeCoverageRead, s.coverageReadHistory))
	m.HandleFunc("GET "+base+"/placements/{id}/observations", s.onBrand(auth.ScopeCoverageRead, s.coverageObservations))
	m.HandleFunc("POST "+base+"/placements/{id}/check", s.onBrand(auth.ScopeCoverageManage, s.coverageQueueCheck))
	m.HandleFunc("POST "+base+"/agreements/{id}/approve", s.onBrand(auth.ScopeCoverageApprove, s.coverageApprove))
	m.HandleFunc("POST "+base+"/discovery/reconcile", s.onBrand(auth.ScopeCoverageManage, s.coverageReconcile))
	m.HandleFunc("POST "+base+"/candidates/{id}/confirm", s.onBrand(auth.ScopeCoverageManage, s.coverageConfirm))
	m.HandleFunc("POST "+base+"/import", s.onBrand(auth.ScopeCoverageManage, s.coverageImport))
}
func (s *Server) coverageSchema(w http.ResponseWriter, r *http.Request) {
	var data []map[string]any
	_ = json.Unmarshal([]byte(coverageSchemaJSON), &data)
	a := accessFrom(r.Context())
	out := []map[string]any{}
	for _, d := range data {
		if d["finance"] == true && !auth.Grants(a.Scopes, auth.ScopeCoverageFinanceRead) {
			continue
		}
		out = append(out, d)
	}
	writeJSON(w, 200, map[string]any{"data": out})
}
func (s *Server) coverageRows(w http.ResponseWriter, r *http.Request, query string, args ...any) {
	rows, err := s.pool.Query(r.Context(), query, args...)
	if s.coverageError(w, r, err) {
		return
	}
	defer rows.Close()
	data := []json.RawMessage{}
	for rows.Next() {
		var b json.RawMessage
		if err = rows.Scan(&b); err != nil {
			s.internal(w, r, err)
			return
		}
		data = append(data, b)
	}
	if err = rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": data})
}
func (s *Server) coverageReadHistory(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	name := r.PathValue("resource")
	res, ok := coverageResources[name]
	if !ok || !validUUID(r.PathValue("id")) {
		writeError(w, r, 404, "not_found", "not found")
		return
	}
	if res.Finance && !auth.Grants(a.Scopes, auth.ScopeCoverageFinanceRead) {
		writeError(w, r, 403, "forbidden", "coverage.finance.read required")
		return
	}
	// Private saved views must not expose their activity through another route.
	where, args := coverageVisible(name, a)
	args = append(args, r.PathValue("id"))
	var exists bool
	query := "SELECT EXISTS(SELECT 1 FROM " + res.Table + " WHERE " + where + " AND id=$" + strconv.Itoa(len(args)) + ")"
	if err := s.pool.QueryRow(r.Context(), query, args...).Scan(&exists); s.coverageError(w, r, err) {
		return
	}
	if !exists {
		writeError(w, r, 404, "not_found", "not found")
		return
	}
	s.coverageRows(w, r, "SELECT to_jsonb(h)-'workspace_id' FROM coverage_history h WHERE workspace_id=$1 AND brand_id=$2 AND resource=$3 AND resource_id=$4 ORDER BY created_at DESC LIMIT 200", a.WorkspaceID, a.Brand, name, r.PathValue("id"))
}
func (s *Server) coverageObservations(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !validUUID(r.PathValue("id")) {
		writeError(w, r, 404, "not_found", "not found")
		return
	}
	rows, err := s.pool.Query(r.Context(), "SELECT to_jsonb(o)-'workspace_id' FROM coverage_observations o WHERE workspace_id=$1 AND brand_id=$2 AND placement_id=$3 ORDER BY created_at DESC LIMIT 100", a.WorkspaceID, a.Brand, r.PathValue("id"))
	if s.coverageError(w, r, err) {
		return
	}
	data := []map[string]any{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		var item map[string]any
		if err = json.Unmarshal(raw, &item); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		data = append(data, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	for _, item := range data {
		ev, _ := item["evidence"].(map[string]any)
		assessment, _ := ev["assessment"].(map[string]any)
		if assessment == nil {
			continue
		}
		var matches bool
		err = s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM coverage_observations o JOIN coverage_placements p ON p.id=o.placement_id AND p.workspace_id=o.workspace_id AND p.brand_id=o.brand_id WHERE o.workspace_id=$1 AND o.brand_id=$2 AND o.id=$3 AND o.evidence->>'url'=p.url)`, a.WorkspaceID, a.Brand, ev["observation_id"]).Scan(&matches)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if !matches {
			delete(ev, "assessment")
			ev["status"] = "page_changed"
			continue
		}
		if !auth.Grants(a.Scopes, auth.ScopeWorkspaceRead) {
			delete(ev, "assessment")
			ev["status"] = "knowledge_access_needed"
			continue
		}
		if versions, ok := ev["knowledge_versions"].(map[string]any); ok {
			for id, used := range versions {
				var version int
				e := s.pool.QueryRow(r.Context(), "SELECT version FROM knowledge_items WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND deleted_at IS NULL AND status='published' AND visibility IN ('public','customers_ai')", a.WorkspaceID, a.Brand, id).Scan(&version)
				if errors.Is(e, pgx.ErrNoRows) {
					delete(ev, "assessment")
					ev["status"] = "source_unavailable"
					break
				}
				if e != nil {
					s.internal(w, r, e)
					return
				}
				if float64(version) != used {
					ev["status"] = "source_changed"
				}
			}
		}
		findings, _ := assessment["findings"].([]any)
		for _, f := range findings {
			finding, _ := f.(map[string]any)
			var version int
			err = s.pool.QueryRow(r.Context(), "SELECT version FROM knowledge_items WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND deleted_at IS NULL AND status='published' AND visibility IN ('public','customers_ai')", a.WorkspaceID, a.Brand, finding["item_id"]).Scan(&version)
			if errors.Is(err, pgx.ErrNoRows) {
				delete(ev, "assessment")
				ev["status"] = "source_unavailable"
				break
			}
			if err != nil {
				s.internal(w, r, err)
				return
			}
			if float64(version) != finding["version"] {
				finding["stale"] = true
				ev["status"] = "source_changed"
			}
		}
	}
	writeJSON(w, 200, map[string]any{"data": data})
}
func (s *Server) coverageQueueCheck(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	reader := r.URL.Query().Get("reader")
	if reader == "" {
		reader = "native"
	}
	if reader != "native" && reader != "connected" {
		writeError(w, r, 400, "invalid_request", "reader must be native or connected")
		return
	}
	if !validUUID(id) {
		writeError(w, r, 404, "not_found", "not found")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var page string
		if err := tx.QueryRow(r.Context(), "SELECT url FROM coverage_placements WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 FOR UPDATE", a.WorkspaceID, a.Brand, id).Scan(&page); err != nil {
			return err
		}
		if page == "" {
			return errInvalid("Add a page URL before checking")
		}
		if _, err := jobs.Enqueue(r.Context(), tx, jobs.NewJob{Kind: jobCoverageCheck, WorkspaceID: &a.WorkspaceID, Payload: map[string]string{"placement_id": id, "brand_id": a.Brand, "reader": reader}, IdempotencyKey: "coverage.check:" + id + ":" + reader + ":" + time.Now().UTC().Format("200601021504"), MaxAttempts: 3}); err != nil {
			return err
		}
		return coverageHistory(r.Context(), tx, a, "placements", id, "check_queued", nil)
	})
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 202, map[string]bool{"queued": true})
}
func (s *Server) coverageApprove(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if a.personID() == "" {
		writeError(w, r, 403, "forbidden", "A person must approve terms")
		return
	}
	var req struct {
		Version int `json:"version"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !validUUID(r.PathValue("id")) {
		writeError(w, r, 404, "not_found", "not found")
		return
	}
	var data json.RawMessage
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var version int
		if err := tx.QueryRow(r.Context(), "SELECT version FROM coverage_agreements WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 FOR UPDATE", a.WorkspaceID, a.Brand, r.PathValue("id")).Scan(&version); err != nil {
			return err
		}
		if version != req.Version {
			return errCoverageConflict
		}
		if err := tx.QueryRow(r.Context(), "UPDATE coverage_agreements a SET approved_version=version,approved_by=$4,approved_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 RETURNING to_jsonb(a)-'workspace_id'", a.WorkspaceID, a.Brand, r.PathValue("id"), a.ActorID).Scan(&data); err != nil {
			return err
		}
		return coverageHistory(r.Context(), tx, a, "agreements", r.PathValue("id"), "approved", map[string]any{"version": version})
	})
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 200, data)
}
func (s *Server) coverageReports(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	data, err := s.coverageReport(r.Context(), a, q.Get("from"), q.Get("to"), q.Get("filters"))
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 200, data)
}
func (s *Server) coverageFinanceReport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	s.coverageRows(w, r, `WITH currencies AS (SELECT currency FROM coverage_agreements WHERE workspace_id=$1 AND brand_id=$2 UNION SELECT currency FROM coverage_invoices WHERE workspace_id=$1 AND brand_id=$2 UNION SELECT currency FROM coverage_budgets WHERE workspace_id=$1 AND brand_id=$2)
 SELECT jsonb_build_object('currency',c.currency,
 'committed',coalesce((SELECT sum(amount_minor) FROM coverage_agreements WHERE workspace_id=$1 AND brand_id=$2 AND currency=c.currency AND status IN ('active','completed')),0),
 'invoiced',coalesce((SELECT sum(CASE WHEN kind='credit' THEN -amount_minor ELSE amount_minor END) FROM coverage_invoices WHERE workspace_id=$1 AND brand_id=$2 AND currency=c.currency AND status IN ('received','disputed')),0),
 'paid',coalesce((SELECT sum(CASE WHEN kind='reversal' THEN -amount_minor ELSE amount_minor END) FROM coverage_payments WHERE workspace_id=$1 AND brand_id=$2 AND currency=c.currency),0),
 'budget',coalesce((SELECT sum(amount_minor) FROM coverage_budgets WHERE workspace_id=$1 AND brand_id=$2 AND currency=c.currency AND starts_on<=current_date AND ends_on>=current_date),0)) FROM currencies c ORDER BY currency`, a.WorkspaceID, a.Brand)
}
func (s *Server) coverageReconcile(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !auth.Grants(a.Scopes, auth.ScopeSEORead) {
		writeError(w, r, 403, "forbidden", "seo.read required to import discovery results")
		return
	}
	var found map[string]int
	var processed int
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if found, processed, err = coverageDiscover(r.Context(), tx, a); err != nil {
			return err
		}
		// The new finds are read to see whether they're about the brand.
		return enqueueCoverageVerify(r.Context(), tx, a.WorkspaceID, a.Brand, "now:"+time.Now().UTC().Format(time.RFC3339Nano))
	})
	if s.coverageError(w, r, err) {
		return
	}
	added := 0
	for source, n := range found {
		metrics.CoverageFound(source, n)
		added += n
	}
	writeJSON(w, 200, map[string]any{"added": added, "processed": processed, "has_more": processed == coverageDiscoverBatch})
}

const coverageDiscoverBatch = 2000

// coverageDiscover brings the brand's new finds from the modules that
// already watch the web into its review list: news coverage, backlinks,
// link prospects, posts its social watches found, and the pages AI answers
// cite when they name the brand. Each find gets the type its address
// suggests. It handles one batch, and there is more when it returns a full
// batch. It returns how many pages were new to the list, by source.
func coverageDiscover(ctx context.Context, tx pgx.Tx, a *access) (map[string]int, int, error) {
	rows, err := tx.Query(ctx, `WITH sources AS (
 SELECT n.url,n.title,'news' AS source,c.id::text AS source_id FROM coverage c JOIN news_articles n ON n.id=c.article_id AND n.workspace_id=c.workspace_id WHERE c.workspace_id=$1 AND c.brand_id=$2 AND c.status<>'dismissed'
 UNION ALL SELECT page_url,left(page_url,500),'link_prospects',id::text FROM link_prospects WHERE workspace_id=$1 AND brand_id=$2 AND stage<>'excluded'
 UNION ALL SELECT source_url,left(source_url,500),'backlinks',id::text FROM backlinks WHERE workspace_id=$1 AND brand_id=$2
 UNION ALL SELECT url,left(coalesce(nullif(title,''),url),500),'social',id::text FROM social_items WHERE workspace_id=$1 AND brand_id=$2 AND status<>'ignored'
 UNION ALL SELECT DISTINCT ON (ci.url) ci.url,left(coalesce(nullif(ci.title,''),ci.url),500),'ai_citations',md5(ci.url) FROM ai_citations ci JOIN ai_cohorts co ON co.id=ci.cohort_id AND co.workspace_id=ci.workspace_id
  WHERE ci.workspace_id=$1 AND co.brand_id=$2 AND ci.owner='other' AND EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id=ci.sample_id AND m.entity='brand')
 ) SELECT url,title,source,source_id FROM sources s WHERE NOT EXISTS (
 SELECT 1 FROM coverage_discovery_receipts r WHERE r.workspace_id=$1 AND r.brand_id=$2 AND r.source=s.source AND r.source_id=s.source_id
 ) ORDER BY source,source_id LIMIT 2000`, a.WorkspaceID, a.Brand)
	if err != nil {
		return nil, 0, err
	}
	hits := []coverageFind{}
	for rows.Next() {
		var h coverageFind
		if err = rows.Scan(&h.URL, &h.Title, &h.Source, &h.ID); err != nil {
			rows.Close()
			return nil, 0, err
		}
		hits = append(hits, h)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	found, added := map[string]int{}, 0
	for _, h := range hits {
		created, err := coverageAddFind(ctx, tx, a, h)
		if err != nil {
			return nil, 0, err
		}
		if created {
			found[h.Source]++
			added++
		}
	}
	if err := recordWithProps(ctx, tx, coverageActor(a), a.WorkspaceID, "coverage_discovery.reconciled", "brand", a.Brand, nil, map[string]any{"count": added}); err != nil {
		return nil, 0, err
	}
	return found, len(hits), nil
}

// coverageFind is one page a source found: its URL and title, the source
// module and that module's id for it.
type coverageFind struct{ URL, Title, Source, ID string }

// coverageAddFind adds a find to the review list once, with the type its
// address suggests, and remembers the source's record so it is never
// brought in again. It reports whether the page was new to the list.
func coverageAddFind(ctx context.Context, tx pgx.Tx, a *access, h coverageFind) (bool, error) {
	u, e := coverageURL(h.URL)
	if e != nil {
		_, err := tx.Exec(ctx, "INSERT INTO coverage_discovery_receipts(workspace_id,brand_id,source,source_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", a.WorkspaceID, a.Brand, h.Source, h.ID)
		return false, err
	}
	kind := coverage.Classify(u)
	if kind == "" && h.Source == "news" {
		kind = "news"
	}
	title := strings.TrimSpace(h.Title)
	if title == "" {
		title = u
	}
	if len(title) > 500 {
		title = title[:500]
	}
	var candidate string
	var created bool
	if err := tx.QueryRow(ctx, `INSERT INTO coverage_candidates(workspace_id,brand_id,url,title,source,source_id,kind) VALUES($1,$2,$3,$4,$5,$6,$7)
 ON CONFLICT(workspace_id,brand_id,url) DO UPDATE SET updated_at=now(),kind=CASE WHEN coverage_candidates.kind='' THEN EXCLUDED.kind ELSE coverage_candidates.kind END RETURNING id::text,(xmax=0)`,
		a.WorkspaceID, a.Brand, u, title, h.Source, h.ID, kind).Scan(&candidate, &created); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO coverage_discovery_receipts(workspace_id,brand_id,source,source_id,candidate_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", a.WorkspaceID, a.Brand, h.Source, h.ID, candidate); err != nil {
		return false, err
	}
	var known bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM coverage_history WHERE workspace_id=$1 AND brand_id=$2 AND resource='candidates' AND resource_id=$3 AND action='discovered' AND detail->>'source'=$4 AND detail->>'source_id'=$5)", a.WorkspaceID, a.Brand, candidate, h.Source, h.ID).Scan(&known); err != nil {
		return false, err
	}
	if !known {
		if err := coverageHistory(ctx, tx, a, "candidates", candidate, "discovered", map[string]any{"source": h.Source, "source_id": h.ID, "kind": kind}); err != nil {
			return false, err
		}
	}
	return created, nil
}

// coverageActor is who an access acts as in the audit log: nobody for the
// system (a scheduled job), else the person or token.
func coverageActor(a *access) *principal {
	if a.ActorType == "" {
		return nil
	}
	return &a.principal
}
func (s *Server) coverageConfirm(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Publisher string `json:"publisher_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !validUUID(req.Publisher) || !validUUID(r.PathValue("id")) {
		writeError(w, r, 400, "invalid_request", "Valid publisher and candidate IDs required")
		return
	}
	var data json.RawMessage
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var url, title, status, kind string
		if err := tx.QueryRow(r.Context(), "SELECT url,title,status,coalesce(nullif(kind,''),'blog') FROM coverage_candidates WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 FOR UPDATE", a.WorkspaceID, a.Brand, r.PathValue("id")).Scan(&url, &title, &status, &kind); err != nil {
			return err
		}
		if err := coverageReferences(r.Context(), tx, a, coverageResources["placements"], map[string]any{"publisher_id": req.Publisher}); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `INSERT INTO coverage_placements AS p(workspace_id,brand_id,publisher_id,title,url,kind) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(workspace_id,brand_id,url,inventory) WHERE url<>'' DO UPDATE SET updated_at=p.updated_at RETURNING to_jsonb(p)-'workspace_id'`, a.WorkspaceID, a.Brand, req.Publisher, title, url, kind).Scan(&data); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), "UPDATE coverage_candidates SET status='confirmed',version=version+1,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3", a.WorkspaceID, a.Brand, r.PathValue("id")); err != nil {
			return err
		}
		return coverageHistory(r.Context(), tx, a, "candidates", r.PathValue("id"), "confirmed", nil)
	})
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 200, data)
}
func (s *Server) coverageImport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Rows   []map[string]any `json:"rows"`
		Commit bool             `json:"commit"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Rows) > 200 || len(req.Rows) == 0 {
		writeError(w, r, 400, "invalid_request", "Import 1 to 200 rows at a time")
		return
	}
	results := []map[string]any{}
	res := coverageResources["placements"]
	for i, body := range req.Rows {
		row := map[string]any{"row": i + 1, "status": "valid"}
		err := coverageValidate(res, body, false)
		if err == nil {
			err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
				if e := coverageReferences(r.Context(), tx, a, res, body); e != nil {
					return e
				}
				if e := coverageFinancialRules(r.Context(), tx, a, "placements", body); e != nil {
					return e
				}
				if u, ok := body["url"].(string); ok && u != "" {
					var exists bool
					inv, _ := body["inventory"].(string)
					if e := tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM coverage_placements WHERE workspace_id=$1 AND brand_id=$2 AND url=$3 AND inventory=$4)", a.WorkspaceID, a.Brand, u, inv).Scan(&exists); e != nil {
						return e
					}
					if exists {
						row["status"] = "duplicate"
						return nil
					}
				}
				if !req.Commit {
					return nil
				}
				cols := []string{"workspace_id", "brand_id"}
				vals := []string{"$1", "$2"}
				args := []any{a.WorkspaceID, a.Brand}
				for k, v := range body {
					cols = append(cols, k)
					args = append(args, v)
					vals = append(vals, "$"+strconv.Itoa(len(args)))
				}
				var id string
				if e := tx.QueryRow(r.Context(), "INSERT INTO coverage_placements ("+strings.Join(cols, ",")+") VALUES("+strings.Join(vals, ",")+") RETURNING id::text", args...).Scan(&id); e != nil {
					return e
				}
				row["id"] = id
				row["status"] = "imported"
				return coverageHistory(r.Context(), tx, a, "placements", id, "imported", nil)
			})
		}
		if err != nil {
			row["status"] = "invalid"
			row["error"] = "Could not import row; check its fields and references"
			if inv, ok := err.(errInvalid); ok {
				row["error"] = string(inv)
			}
		}
		results = append(results, row)
	}
	writeJSON(w, 200, map[string]any{"data": results, "committed": req.Commit})
}
