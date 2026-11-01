package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/fixes"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/searchinsights"
	"github.com/outflareai/outflare/engine/internal/seointel"
)

func (s *Server) routesSearchWorkspace(mux *http.ServeMux) {
	b := "/v1/brands/{brand}/seo-sites/{site}/insights"
	mux.HandleFunc("GET "+b+"/report-runs/{item}", s.onBrand(auth.ScopeSEORead, s.getSearchReportRun))
	mux.HandleFunc("GET "+b+"/workspace", s.onBrand(auth.ScopeSEORead, s.getSearchWorkspace))
	mux.HandleFunc("POST "+b+"/items", s.onBrand(auth.ScopeSEOManage, s.saveSearchWork))
	mux.HandleFunc("POST "+b+"/items/{item}/fix", s.onBrand(auth.ScopeSEOManage, s.createSearchFix))
	mux.HandleFunc("POST "+b+"/reports", s.onBrand(auth.ScopeSEOManage, s.saveSearchReport))
	mux.HandleFunc("POST "+b+"/reports/{item}/run", s.onBrand(auth.ScopeSEOManage, s.runSearchReport))
	mux.HandleFunc("DELETE "+b+"/reports/{item}", s.onBrand(auth.ScopeSEOManage, func(w http.ResponseWriter, r *http.Request) {
		s.deleteInsightResource(w, r, "search_insight_reports", "search_report.deleted")
	}))
	mux.HandleFunc("POST "+b+"/schedules", s.onBrand(auth.ScopeSEOManage, s.saveSearchSchedule))
	mux.HandleFunc("DELETE "+b+"/schedules/{item}", s.onBrand(auth.ScopeSEOManage, func(w http.ResponseWriter, r *http.Request) {
		s.deleteInsightResource(w, r, "search_insight_schedules", "search_schedule.deleted")
	}))
}

type searchWorkItem struct {
	ID             string              `json:"id"`
	Report         string              `json:"report"`
	Key            string              `json:"key"`
	Page           string              `json:"page"`
	Query          string              `json:"query"`
	Title          string              `json:"title"`
	Recommendation string              `json:"recommendation"`
	Evidence       seointel.InsightRow `json:"evidence"`
	Tags           []string            `json:"tags"`
	Status         string              `json:"status"`
	Note           string              `json:"note"`
	FixID          *string             `json:"fix_id"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
}
type searchReportRun struct {
	ID          string                  `json:"id"`
	ReportID    string                  `json:"report_id"`
	ReportName  string                  `json:"report_name"`
	Status      string                  `json:"status"`
	Error       string                  `json:"error"`
	Filters     map[string]any          `json:"filters"`
	Result      *seointel.InsightReport `json:"result"`
	CreatedAt   time.Time               `json:"created_at"`
	CompletedAt *time.Time              `json:"completed_at"`
}
type searchSavedReport struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Tags      []string         `json:"tags"`
	Filters   map[string]any   `json:"filters"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	LatestRun *searchReportRun `json:"latest_run"`
}
type searchCollectionRun struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	MaxAttempts   int       `json:"max_attempts"`
	Error         string    `json:"error"`
	RunAt         time.Time `json:"run_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Page          string    `json:"page"`
	Device        string    `json:"device"`
	IntegrationID string    `json:"integration_id"`
	ScheduleID    string    `json:"schedule_id"`
}

const workCols = `id::text,report,key,page,query,title,recommendation,evidence,tags,status,note,fix_id::text,created_at,updated_at`

func scanSearchWork(r pgx.CollectableRow) (searchWorkItem, error) {
	var x searchWorkItem
	var raw []byte
	e := r.Scan(&x.ID, &x.Report, &x.Key, &x.Page, &x.Query, &x.Title, &x.Recommendation, &raw, &x.Tags, &x.Status, &x.Note, &x.FixID, &x.CreatedAt, &x.UpdatedAt)
	if e == nil {
		e = json.Unmarshal(raw, &x.Evidence)
	}
	return x, e
}
func (s *Server) getSearchWorkspace(w http.ResponseWriter, r *http.Request) {
	site, ok := s.insightSite(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	a := accessFrom(ctx)
	args := []any{a.WorkspaceID, a.Brand, site.ID}
	rows, e := s.pool.Query(ctx, `SELECT `+workCols+` FROM search_insight_items WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 ORDER BY updated_at DESC LIMIT 1000`, args...)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	items, e := pgx.CollectRows(rows, scanSearchWork)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	rows, e = s.pool.Query(ctx, `SELECT id::text,name,tags,filters,created_at,updated_at FROM search_insight_reports WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 ORDER BY updated_at DESC LIMIT 100`, args...)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	reports, e := pgx.CollectRows(rows, func(r pgx.CollectableRow) (searchSavedReport, error) {
		var x searchSavedReport
		var raw []byte
		e := r.Scan(&x.ID, &x.Name, &x.Tags, &raw, &x.CreatedAt, &x.UpdatedAt)
		if e == nil {
			e = json.Unmarshal(raw, &x.Filters)
		}
		return x, e
	})
	if e != nil {
		s.internal(w, r, e)
		return
	}
	rows, e = s.pool.Query(ctx, `SELECT x.id::text,x.report_id::text,p.name,x.status,x.error,x.filters,NULL::jsonb,x.created_at,x.completed_at FROM search_report_runs x JOIN search_insight_reports p ON p.id=x.report_id WHERE x.workspace_id=$1 AND x.brand_id=$2 AND x.site_id=$3 ORDER BY x.created_at DESC LIMIT 50`, args...)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	runs, e := pgx.CollectRows(rows, func(r pgx.CollectableRow) (searchReportRun, error) {
		var x searchReportRun
		var filters, result []byte
		e := r.Scan(&x.ID, &x.ReportID, &x.ReportName, &x.Status, &x.Error, &filters, &result, &x.CreatedAt, &x.CompletedAt)
		if e == nil {
			e = json.Unmarshal(filters, &x.Filters)
		}
		if e == nil && len(result) > 0 {
			e = json.Unmarshal(result, &x.Result)
		}
		return x, e
	})
	if e != nil {
		s.internal(w, r, e)
		return
	}

	rows, e = s.pool.Query(ctx, `SELECT DISTINCT ON (report_id) id::text,report_id::text,status,error,filters,created_at,completed_at FROM search_report_runs WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 ORDER BY report_id,created_at DESC,id DESC`, args...)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	latest, e := pgx.CollectRows(rows, func(r pgx.CollectableRow) (searchReportRun, error) {
		var x searchReportRun
		var raw []byte
		err := r.Scan(&x.ID, &x.ReportID, &x.Status, &x.Error, &raw, &x.CreatedAt, &x.CompletedAt)
		if err == nil {
			err = json.Unmarshal(raw, &x.Filters)
		}
		return x, err
	})
	if e != nil {
		s.internal(w, r, e)
		return
	}
	byReport := map[string]searchReportRun{}
	for _, x := range latest {
		byReport[x.ReportID] = x
	}
	for i := range reports {
		if x, ok := byReport[reports[i].ID]; ok {
			x.ReportName = reports[i].Name
			reports[i].LatestRun = &x
		}
	}

	rows, e = s.pool.Query(ctx, `SELECT id::text,name,COALESCE(integration_id::text,''),page,device,frequency,weekday,hour,timezone,next_run_at,revision,created_at FROM search_insight_schedules WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 ORDER BY created_at DESC`, args...)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	schedules, e := pgx.CollectRows(rows, scanSearchSchedule)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	rows, e = s.pool.Query(ctx, `SELECT id::text,kind,CASE WHEN payload->>'skipped'='true' THEN 'skipped' ELSE status END,attempts,max_attempts,COALESCE(left(last_error,500),''),run_at,created_at,updated_at,COALESCE(payload->>'page',''),COALESCE(payload->>'device',''),COALESCE(payload->>'integration_id',''),COALESCE(payload->>'schedule_id','') FROM jobs WHERE workspace_id=$1 AND kind='search_insights.refresh' AND payload->>'brand_id'=$2 AND payload->>'site_id'=$3 ORDER BY id DESC LIMIT 100`, args...)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	activity, e := pgx.CollectRows(rows, func(r pgx.CollectableRow) (searchCollectionRun, error) {
		var x searchCollectionRun
		e := r.Scan(&x.ID, &x.Kind, &x.Status, &x.Attempts, &x.MaxAttempts, &x.Error, &x.RunAt, &x.CreatedAt, &x.UpdatedAt, &x.Page, &x.Device, &x.IntegrationID, &x.ScheduleID)
		return x, e
	})
	if e != nil {
		s.internal(w, r, e)
		return
	}
	src, e := seointel.SourceForBrand(ctx, s.pool, a.WorkspaceID, a.Brand, site.Host)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "reports": reports, "report_runs": runs, "schedules": schedules, "activity": activity, "search_source": src, "updated_at": time.Now().UTC()})
}
func cleanInsightTags(tags []string) ([]string, error) {
	if len(tags) > 20 {
		return nil, fmt.Errorf("Use at most 20 tags")
	}
	out := []string{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if len(t) > 40 {
			return nil, fmt.Errorf("Tags are at most 40 characters")
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}
func insightFilters(raw map[string]any) (url.Values, error) {
	allowed := map[string]bool{}
	for _, k := range []string{"report", "from", "to", "country", "device", "group_id", "bucket", "migration_id", "integration_id", "currency", "threshold", "weeks", "growth", "conversion_rate", "order_value"} {
		allowed[k] = true
	}
	q := url.Values{}
	for k, v := range raw {
		if !allowed[k] {
			return nil, fmt.Errorf("unknown filter %s", k)
		}
		switch x := v.(type) {
		case string:
			if len(x) > 500 {
				return nil, fmt.Errorf("filter too long")
			}
			if x != "" {
				q.Set(k, x)
			}
		case float64:
			q.Set(k, strconv.FormatFloat(x, 'f', -1, 64))
		default:
			return nil, fmt.Errorf("invalid filter %s", k)
		}
	}
	return q, nil
}
func withInsightFilters(r *http.Request, q url.Values) *http.Request {
	copy := r.Clone(r.Context())
	u := *r.URL
	u.RawQuery = q.Encode()
	copy.URL = &u
	return copy
}
func (s *Server) saveSearchWork(w http.ResponseWriter, r *http.Request) {
	site, ok := s.insightSite(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	var req struct {
		Report  string         `json:"report"`
		Key     string         `json:"key"`
		Tags    *[]string      `json:"tags"`
		Status  string         `json:"status"`
		Note    *string        `json:"note"`
		Filters map[string]any `json:"filters"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Key == "" || len(req.Key) > 2000 || len(req.Report) > 40 || req.Report == "" || req.Note != nil && len(*req.Note) > 2000 || req.Status != "" && !slices.Contains([]string{"open", "in_progress", "done", "dismissed"}, req.Status) {
		writeError(w, r, 400, "invalid_request", "Choose a finding, a valid work status and a note up to 2,000 characters.")
		return
	}
	var x searchWorkItem
	row := s.pool.QueryRow(r.Context(), `SELECT `+workCols+` FROM search_insight_items WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND report=$4 AND key=$5`, a.WorkspaceID, a.Brand, site.ID, req.Report, req.Key)
	// QueryRow and CollectableRow expose Scan; use the scanner through a tiny adapter.
	var raw []byte
	e := row.Scan(&x.ID, &x.Report, &x.Key, &x.Page, &x.Query, &x.Title, &x.Recommendation, &raw, &x.Tags, &x.Status, &x.Note, &x.FixID, &x.CreatedAt, &x.UpdatedAt)
	if e == nil {
		e = json.Unmarshal(raw, &x.Evidence)
	} else if errors.Is(e, pgx.ErrNoRows) {
		if req.Filters == nil {
			req.Filters = map[string]any{}
		}
		req.Filters["report"] = req.Report
		q, err := insightFilters(req.Filters)
		if err != nil {
			writeError(w, r, 400, "invalid_request", err.Error())
			return
		}
		report, ok := s.buildSearchInsights(w, withInsightFilters(r, q))
		if !ok {
			return
		}
		found := false
		for _, v := range report.Data {
			if v.Key == req.Key {
				x = searchWorkItem{Report: req.Report, Key: req.Key, Page: v.Page, Query: v.Query, Evidence: v, Tags: []string{}, Status: "open"}
				x.Title, x.Recommendation = searchRecommendation(req.Report, v)
				found = true
				break
			}
		}
		if !found {
			writeError(w, r, 409, "conflict", "This finding is no longer in the current report. Refresh the report before saving it.")
			return
		}
		e = nil
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if req.Tags != nil {
		tags, err := cleanInsightTags(*req.Tags)
		if err != nil {
			writeError(w, r, 400, "invalid_request", err.Error())
			return
		}
		x.Tags = tags
	}
	if req.Status != "" {
		x.Status = req.Status
	}
	if req.Note != nil {
		x.Note = *req.Note
	}
	evidence, _ := json.Marshal(x.Evidence)
	e = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `INSERT INTO search_insight_items(workspace_id,brand_id,site_id,report,key,page,query,title,recommendation,evidence,tags,status,note) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(site_id,report,key) DO UPDATE SET tags=EXCLUDED.tags,status=EXCLUDED.status,note=EXCLUDED.note,updated_at=now() RETURNING id::text,created_at,updated_at,fix_id::text`, a.WorkspaceID, a.Brand, site.ID, x.Report, x.Key, x.Page, x.Query, x.Title, x.Recommendation, evidence, x.Tags, x.Status, x.Note).Scan(&x.ID, &x.CreatedAt, &x.UpdatedAt, &x.FixID); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "search_work.saved", "search_work", x.ID, map[string]any{"brand_id": a.Brand, "site_id": site.ID, "status": x.Status, "tags": len(x.Tags)})
	})
	if e != nil {
		s.internal(w, r, e)
		return
	}
	writeJSON(w, 200, x)
}
func searchRecommendation(report string, x seointel.InsightRow) (string, string) {
	switch report {
	case "opportunities":
		if x.MissedClicks > 0 {
			return "Improve this search snippet", "Review the page title and description against the query intent. Compare CTR after a title or description change; click gains are estimates."
		}
		return "Strengthen a page near the top results", "Improve the answer and add relevant internal links. Use Rankings to track the query and an experiment to measure the change."
	case "decay":
		return "Refresh declining content", "Compare lost queries and recent site changes, check indexing, then update outdated content. Measure the same complete periods after the change."
	case "vitals":
		return "Improve real-user page experience", "Check the affected device and metric against recent deploys. Improve loading, main-thread work or layout stability, then collect a later field window."
	case "migrations":
		return "Review this migration mapping", "Check the old URL's permanent redirect, target status, indexability and canonical. Re-crawl both sites to verify the result."
	case "attribution":
		return "Improve a search landing page", "Compare its search intent with its GA4 key events. Improve the conversion path; query-level outcomes are estimated allocations."
	default:
		return "Investigate this search finding", "Review the evidence, verify the source and compare the same date range before making a change."
	}
}
func (s *Server) createSearchFix(w http.ResponseWriter, r *http.Request) {
	site, ok := s.insightSite(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	id := r.PathValue("item")
	if !validUUID(id) {
		writeError(w, r, 404, "not_found", "finding not found")
		return
	}
	e := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var page, title, note string
		var fix *string
		if err := tx.QueryRow(r.Context(), `SELECT page,title,recommendation,fix_id::text FROM search_insight_items WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND id=$4 FOR UPDATE`, a.WorkspaceID, a.Brand, site.ID, id).Scan(&page, &title, &note, &fix); err != nil {
			return err
		}
		if fix != nil {
			return nil
		}
		var pass string
		if err := tx.QueryRow(r.Context(), `SELECT gen_random_uuid()::text`).Scan(&pass); err != nil {
			return err
		}
		if _, err := fixes.Sync(r.Context(), tx, fixes.Run{Workspace: a.WorkspaceID, BrandID: &a.Brand, Source: seointel.FixSource, SubjectType: "seo_site", SubjectID: site.ID, RunID: pass, Rules: []string{}, Reopen: false}, []fixes.Found{{Rule: "search_recommendation", Severity: "notice", Group: id, Count: 1, Unit: "findings"}}); err != nil {
			return err
		}
		var fid string
		if err := tx.QueryRow(r.Context(), `SELECT id::text FROM fixes WHERE workspace_id=$1 AND brand_id=$2 AND subject_id=$3 AND rule='search_recommendation' AND group_key=$4`, a.WorkspaceID, a.Brand, site.ID, id).Scan(&fid); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE fixes SET note=$3 WHERE workspace_id=$1 AND brand_id=$2 AND id=$4`, a.WorkspaceID, a.Brand, title+"\n\n"+note, fid); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE search_insight_items SET fix_id=$4,status='in_progress',updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, id, fid); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "search_work.fix_created", "search_work", id, map[string]any{"brand_id": a.Brand, "fix_id": fid})
	})
	if errors.Is(e, pgx.ErrNoRows) {
		writeError(w, r, 404, "not_found", "finding not found")
		return
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	rows, e := s.pool.Query(r.Context(), `SELECT `+workCols+` FROM search_insight_items WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND id=$4`, a.WorkspaceID, a.Brand, site.ID, id)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	list, e := pgx.CollectRows(rows, scanSearchWork)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	writeJSON(w, 200, list[0])
}

// Saved report failures are retained without exposing credentials or SQL errors.
type insightErrorCapture struct {
	header http.Header
	status int
	bytes.Buffer
}

func (c *insightErrorCapture) Header() http.Header { return c.header }
func (c *insightErrorCapture) WriteHeader(n int)   { c.status = n }
func (s *Server) saveSearchReport(w http.ResponseWriter, r *http.Request) {
	site, ok := s.insightSite(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	var x struct {
		ID      string         `json:"id"`
		Name    string         `json:"name"`
		Tags    []string       `json:"tags"`
		Filters map[string]any `json:"filters"`
	}
	if !decode(w, r, &x) {
		return
	}
	tags, e := cleanInsightTags(x.Tags)
	if e != nil || strings.TrimSpace(x.Name) == "" || len(x.Name) > 120 {
		writeError(w, r, 400, "invalid_request", "Give the report a name up to 120 characters and valid tags.")
		return
	}
	if x.Filters == nil {
		x.Filters = map[string]any{"report": "opportunities"}
	}
	q, e := insightFilters(x.Filters)
	if e != nil {
		writeError(w, r, 400, "invalid_request", e.Error())
		return
	}
	if _, ok = s.buildSearchInsights(w, withInsightFilters(r, q)); !ok {
		return
	}
	raw, _ := json.Marshal(x.Filters)
	e = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if x.ID == "" {
			if err := tx.QueryRow(r.Context(), `INSERT INTO search_insight_reports(workspace_id,brand_id,site_id,name,tags,filters) VALUES($1,$2,$3,$4,$5,$6) RETURNING id::text`, a.WorkspaceID, a.Brand, site.ID, strings.TrimSpace(x.Name), tags, raw).Scan(&x.ID); err != nil {
				return err
			}
		} else {
			if !validUUID(x.ID) {
				return errInsightNotFound
			}
			tag, err := tx.Exec(r.Context(), `UPDATE search_insight_reports SET name=$5,tags=$6,filters=$7,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND id=$4`, a.WorkspaceID, a.Brand, site.ID, x.ID, strings.TrimSpace(x.Name), tags, raw)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errInsightNotFound
			}
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "search_report.saved", "search_report", x.ID, map[string]any{"brand_id": a.Brand, "site_id": site.ID})
	})
	if errors.Is(e, errInsightNotFound) {
		writeError(w, r, 404, "not_found", "report not found")
		return
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	var out searchSavedReport
	var filters []byte
	e = s.pool.QueryRow(r.Context(), `SELECT id::text,name,tags,filters,created_at,updated_at FROM search_insight_reports WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, x.ID).Scan(&out.ID, &out.Name, &out.Tags, &filters, &out.CreatedAt, &out.UpdatedAt)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	json.Unmarshal(filters, &out.Filters)
	writeJSON(w, 200, out)
}
func (s *Server) runSearchReport(w http.ResponseWriter, r *http.Request) {
	site, ok := s.insightSite(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	id := r.PathValue("item")
	if !validUUID(id) {
		writeError(w, r, 404, "not_found", "report not found")
		return
	}
	var x searchReportRun
	var raw []byte
	e := s.pool.QueryRow(r.Context(), `SELECT name,filters FROM search_insight_reports WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND id=$4`, a.WorkspaceID, a.Brand, site.ID, id).Scan(&x.ReportName, &raw)
	if errors.Is(e, pgx.ErrNoRows) {
		writeError(w, r, 404, "not_found", "report not found")
		return
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	x.ReportID = id
	json.Unmarshal(raw, &x.Filters)
	q, e := insightFilters(x.Filters)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	capture := &insightErrorCapture{header: http.Header{}, status: 500}
	result, ok := s.buildSearchInsights(capture, withInsightFilters(r, q))
	x.Status = "succeeded"
	if !ok {
		x.Status = "failed"
		x.Error = "This report could not be generated. Review its saved filters and data connections."
		if capture.status < 500 {
			var detail struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(capture.Bytes(), &detail) == nil && detail.Error.Message != "" {
				x.Error = detail.Error.Message
			}
		}
	} else {
		x.Result = result
	}
	resultRaw, _ := json.Marshal(x.Result)
	if !ok {
		resultRaw = nil
	}
	e = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `INSERT INTO search_report_runs(workspace_id,brand_id,site_id,report_id,status,error,filters,result,completed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,now()) RETURNING id::text,created_at,completed_at`, a.WorkspaceID, a.Brand, site.ID, id, x.Status, x.Error, raw, resultRaw).Scan(&x.ID, &x.CreatedAt, &x.CompletedAt); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "search_report.ran", "search_report", id, map[string]any{"brand_id": a.Brand, "status": x.Status})
	})
	if e != nil {
		s.internal(w, r, e)
		return
	}
	writeJSON(w, 200, x)
}

type searchSchedule struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	IntegrationID string     `json:"integration_id"`
	Page          string     `json:"page"`
	Device        string     `json:"device"`
	Frequency     string     `json:"frequency"`
	Weekday       int        `json:"weekday"`
	Hour          int        `json:"hour"`
	Timezone      string     `json:"timezone"`
	NextRunAt     *time.Time `json:"next_run_at"`
	Revision      int64      `json:"revision"`
	CreatedAt     time.Time  `json:"created_at"`
}

func scanSearchSchedule(r pgx.CollectableRow) (searchSchedule, error) {
	var x searchSchedule
	e := r.Scan(&x.ID, &x.Name, &x.IntegrationID, &x.Page, &x.Device, &x.Frequency, &x.Weekday, &x.Hour, &x.Timezone, &x.NextRunAt, &x.Revision, &x.CreatedAt)
	return x, e
}
func (s *Server) saveSearchSchedule(w http.ResponseWriter, r *http.Request) {
	site, ok := s.insightSite(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	var x searchSchedule
	if !decode(w, r, &x) {
		return
	}
	if strings.TrimSpace(x.Name) == "" || len(x.Name) > 120 {
		writeError(w, r, 400, "invalid_request", "Give the schedule a name up to 120 characters.")
		return
	}
	next, e := searchinsights.NextCollection(x.Frequency, x.Weekday, x.Hour, x.Timezone, time.Now())
	if e != nil {
		writeError(w, r, 400, "invalid_request", e.Error())
		return
	}
	p := searchinsights.Request{IntegrationID: x.IntegrationID, Page: x.Page, Device: x.Device}
	if !s.validateSearchCollection(w, r, site, &p) {
		return
	}
	x.Page, x.Device = p.Page, p.Device
	var conn *string
	if x.IntegrationID != "" {
		conn = &x.IntegrationID
	}
	e = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if x.ID == "" {
			if err := tx.QueryRow(r.Context(), `INSERT INTO search_insight_schedules(workspace_id,brand_id,site_id,name,integration_id,page,device,frequency,weekday,hour,timezone,next_run_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id::text,revision,created_at`, a.WorkspaceID, a.Brand, site.ID, x.Name, conn, x.Page, x.Device, x.Frequency, x.Weekday, x.Hour, x.Timezone, next).Scan(&x.ID, &x.Revision, &x.CreatedAt); err != nil {
				return err
			}
		} else {
			if !validUUID(x.ID) {
				return errInsightNotFound
			}
			if err := tx.QueryRow(r.Context(), `UPDATE search_insight_schedules SET name=$5,integration_id=$6,page=$7,device=$8,frequency=$9,weekday=$10,hour=$11,timezone=$12,next_run_at=$13,revision=revision+1 WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND id=$4 RETURNING revision,created_at`, a.WorkspaceID, a.Brand, site.ID, x.ID, x.Name, conn, x.Page, x.Device, x.Frequency, x.Weekday, x.Hour, x.Timezone, next).Scan(&x.Revision, &x.CreatedAt); err != nil {
				return err
			}
		}
		x.NextRunAt = next
		if next != nil {
			request := searchinsights.ScheduleRequest{WorkspaceID: a.WorkspaceID, BrandID: a.Brand, SiteID: site.ID, ScheduleID: x.ID, Revision: x.Revision, At: *next}
			if _, err := jobs.Enqueue(r.Context(), tx, jobs.NewJob{Kind: "search_insights.schedule", WorkspaceID: &a.WorkspaceID, Payload: request, RunAt: *next, IdempotencyKey: request.Key()}); err != nil {
				return err
			}
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "search_schedule.saved", "search_schedule", x.ID, map[string]any{"brand_id": a.Brand, "frequency": x.Frequency})
	})
	if errors.Is(e, pgx.ErrNoRows) || errors.Is(e, errInsightNotFound) {
		writeError(w, r, 404, "not_found", "schedule not found")
		return
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	writeJSON(w, 200, x)
}

func (s *Server) getSearchReportRun(w http.ResponseWriter, r *http.Request) {
	site, ok := s.insightSite(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	id := r.PathValue("item")
	if !validUUID(id) {
		writeError(w, r, 404, "not_found", "snapshot not found")
		return
	}
	var x searchReportRun
	var filters, result []byte
	e := s.pool.QueryRow(r.Context(), `SELECT x.id::text,x.report_id::text,p.name,x.status,x.error,x.filters,x.result,x.created_at,x.completed_at FROM search_report_runs x JOIN search_insight_reports p ON p.id=x.report_id WHERE x.workspace_id=$1 AND x.brand_id=$2 AND x.site_id=$3 AND x.id=$4`, a.WorkspaceID, a.Brand, site.ID, id).Scan(&x.ID, &x.ReportID, &x.ReportName, &x.Status, &x.Error, &filters, &result, &x.CreatedAt, &x.CompletedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		writeError(w, r, 404, "not_found", "snapshot not found")
		return
	}
	if e == nil {
		e = json.Unmarshal(filters, &x.Filters)
	}
	if e == nil && len(result) > 0 {
		e = json.Unmarshal(result, &x.Result)
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	writeJSON(w, 200, x)
}
