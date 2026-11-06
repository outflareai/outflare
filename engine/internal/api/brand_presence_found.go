package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
)

// Found: every page discovery found that links to or names the brand,
// grouped by site, with what reading it showed (verdict), so a person
// answers "about us" or "not us" in one click, for one page or a whole
// site. "About us" makes it a placement under the site's publisher, adding
// the publisher and its company record when the workspace has none.

func (s *Server) routesPresenceFound(m *http.ServeMux) {
	base := "/v1/brands/{brand}/coverage"
	m.HandleFunc("GET "+base+"/found", s.onBrand(auth.ScopeCoverageRead, s.coverageFound))
	m.HandleFunc("POST "+base+"/found/review", s.onBrand(auth.ScopeCoverageManage, s.coverageReviewFound))
}

// coverageFoundViews maps a view to the finds it shows.
var coverageFoundViews = map[string]string{
	"about":     "c.status='new' AND c.verdict IN ('links','mentions')",
	"maybe":     "c.status='new' AND c.verdict IN ('name_only','unreachable','unchecked')",
	"not":       "(c.status='dismissed' OR (c.status='new' AND c.verdict='absent'))",
	"confirmed": "c.status='confirmed'",
}

// foundHost is a find's site.
var foundHost = coverageHostSQL("c.url")

func (s *Server) coverageFound(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	view := q.Get("view")
	if view == "" {
		view = "about"
	}
	where, ok := coverageFoundViews[view]
	if !ok {
		writeError(w, r, 400, "invalid_request", "view is about, maybe, not or confirmed")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	// The cursor is the number of sites already listed.
	offset, _ := strconv.Atoi(q.Get("cursor"))
	if offset < 0 {
		offset = 0
	}
	search := strings.ToLower(strings.TrimSpace(q.Get("q")))
	if len(search) > 200 {
		search = search[:200]
	}
	var data json.RawMessage
	err := s.pool.QueryRow(r.Context(), `WITH c AS (SELECT c.id,c.url,c.title,c.source,c.kind,c.verdict,c.status,c.created_at,`+foundHost+` AS host
 FROM coverage_candidates c WHERE c.workspace_id=$1 AND c.brand_id=$2 AND `+where+`
 AND ($5='' OR strpos(lower(c.url),$5)>0 OR strpos(lower(c.title),$5)>0)),
 sites AS (SELECT host,count(*) AS pages,max(created_at) AS latest,
  CASE WHEN bool_or(verdict='links') THEN 'links' WHEN bool_or(verdict='mentions') THEN 'mentions' ELSE min(verdict) END AS best
  FROM c GROUP BY host ORDER BY (bool_or(verdict='links'))::int DESC,count(*) DESC,host LIMIT $3 OFFSET $4),
 counts AS (SELECT
  (SELECT count(*) FROM coverage_candidates c WHERE c.workspace_id=$1 AND c.brand_id=$2 AND `+coverageFoundViews["about"]+`) AS about,
  (SELECT count(*) FROM coverage_candidates c WHERE c.workspace_id=$1 AND c.brand_id=$2 AND `+coverageFoundViews["maybe"]+`) AS maybe,
  (SELECT count(*) FROM coverage_candidates c WHERE c.workspace_id=$1 AND c.brand_id=$2 AND `+coverageFoundViews["not"]+`) AS "not",
  (SELECT count(*) FROM coverage_candidates c WHERE c.workspace_id=$1 AND c.brand_id=$2 AND `+coverageFoundViews["confirmed"]+`) AS confirmed,
  (SELECT count(*) FROM coverage_candidates c WHERE c.workspace_id=$1 AND c.brand_id=$2 AND c.status='new' AND c.verdict='unchecked') AS checking)
 SELECT jsonb_build_object('view',$6::text,
  'counts',(SELECT to_jsonb(counts) FROM counts),
  'sites_total',(SELECT count(DISTINCT host) FROM c),
  'data',coalesce((SELECT jsonb_agg(jsonb_build_object('host',s.host,'pages',s.pages,'best',s.best,'latest',s.latest,
   'items',(SELECT jsonb_agg(jsonb_build_object('id',c.id,'url',c.url,'title',c.title,'source',c.source,'kind',c.kind,'verdict',c.verdict,'status',c.status,'found_at',c.created_at) ORDER BY c.created_at DESC) FROM (SELECT * FROM c WHERE c.host=s.host ORDER BY created_at DESC LIMIT 20) c))
   ORDER BY (s.best='links')::int DESC,s.pages DESC,s.host) FROM sites s),'[]'))`,
		a.WorkspaceID, a.Brand, limit, offset, search, view).Scan(&data)
	if s.coverageError(w, r, err) {
		return
	}
	var page struct {
		Total int `json:"sites_total"`
	}
	_ = json.Unmarshal(data, &page)
	var next any
	if offset+limit < page.Total {
		next = strconv.Itoa(offset + limit)
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	out["next_cursor"] = next
	writeJSON(w, 200, out)
}

func (s *Server) coverageReviewFound(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		IDs      []string `json:"ids"`
		Host     string   `json:"host"`
		Decision string   `json:"decision"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Decision != "about_us" && req.Decision != "not_us" {
		writeError(w, r, 400, "invalid_request", "decision is about_us or not_us")
		return
	}
	host := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(req.Host)), "www.")
	if (len(req.IDs) == 0) == (host == "") || len(req.IDs) > 500 || len(host) > 253 {
		writeError(w, r, 400, "invalid_request", "Give ids (up to 500) or one host")
		return
	}
	for _, id := range req.IDs {
		if !validUUID(id) {
			writeError(w, r, 400, "invalid_request", "Invalid id")
			return
		}
	}
	done := 0
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT c.id::text,c.url,c.title,coalesce(nullif(c.kind,''),'blog'),`+foundHost+` FROM coverage_candidates c
 WHERE c.workspace_id=$1 AND c.brand_id=$2 AND c.status<>'confirmed' AND (c.id=ANY($3::uuid[]) OR ($4<>'' AND `+foundHost+`=$4)) ORDER BY c.created_at LIMIT 500 FOR UPDATE`,
			a.WorkspaceID, a.Brand, req.IDs, host)
		if err != nil {
			return err
		}
		type find struct{ id, url, title, kind, host string }
		var finds []find
		for rows.Next() {
			var f find
			if err = rows.Scan(&f.id, &f.url, &f.title, &f.kind, &f.host); err != nil {
				rows.Close()
				return err
			}
			finds = append(finds, f)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		publishers := map[string]string{}
		for _, f := range finds {
			if req.Decision == "not_us" {
				if _, err := tx.Exec(r.Context(), "UPDATE coverage_candidates SET status='dismissed',version=version+1,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3", a.WorkspaceID, a.Brand, f.id); err != nil {
					return err
				}
				if err := coverageHistory(r.Context(), tx, a, "candidates", f.id, "dismissed", map[string]any{"by_site": host != ""}); err != nil {
					return err
				}
				done++
				continue
			}
			pub, ok := publishers[f.host]
			if !ok {
				if pub, err = coveragePublisherFor(r.Context(), tx, a, f.host); err != nil {
					return err
				}
				publishers[f.host] = pub
			}
			if _, err := tx.Exec(r.Context(), `INSERT INTO coverage_placements AS p(workspace_id,brand_id,publisher_id,title,url,kind,stage) VALUES($1,$2,$3,$4,$5,$6,'live')
 ON CONFLICT(workspace_id,brand_id,url,inventory) WHERE url<>'' DO NOTHING`, a.WorkspaceID, a.Brand, pub, f.title, f.url, f.kind); err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(), "UPDATE coverage_candidates SET status='confirmed',version=version+1,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3", a.WorkspaceID, a.Brand, f.id); err != nil {
				return err
			}
			if err := coverageHistory(r.Context(), tx, a, "candidates", f.id, "confirmed", map[string]any{"by_site": host != ""}); err != nil {
				return err
			}
			done++
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "coverage_discovery.reviewed", "brand", a.Brand, nil,
			map[string]any{"brand_id": a.Brand, "decision": req.Decision, "count": done, "by_site": host != ""})
	})
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"reviewed": done})
}

// coveragePublisherFor returns the brand's publisher for a site, adding it
// (with the site's company record, found or added) when there is none.
func coveragePublisherFor(ctx context.Context, tx pgx.Tx, a *access, host string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM coverage_publishers WHERE workspace_id=$1 AND brand_id=$2 AND lower(domain)=$3 ORDER BY created_at LIMIT 1`, a.WorkspaceID, a.Brand, host).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	domain := customers.CompanyDomain(host)
	if domain == "" {
		if u, e := url.Parse("https://" + host); e == nil {
			domain = customers.CompanyDomain(u.Hostname())
		}
	}
	if domain == "" {
		return "", errInvalid("This page's site can't be a publisher")
	}
	brand := a.Brand
	records, err := customers.EnsureCompanies(ctx, tx, a.WorkspaceID, &brand, "presence", []customers.Company{{Domain: domain, Name: domain}})
	if err != nil {
		return "", err
	}
	record := records[domain]
	if record == "" {
		return "", errInvalid("This page's site can't be a publisher")
	}
	if err := tx.QueryRow(ctx, `INSERT INTO coverage_publishers(workspace_id,brand_id,record_id,domain) VALUES($1,$2,$3,$4) RETURNING id::text`, a.WorkspaceID, a.Brand, record, host).Scan(&id); err != nil {
		return "", err
	}
	return id, coverageHistory(ctx, tx, a, "publishers", id, "created", map[string]any{"via": "found"})
}
