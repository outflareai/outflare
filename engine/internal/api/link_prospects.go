package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// Link prospects: one list of pages that could link to the workspace's
// SEO sites, from every way of earning links, each on its site's company
// record, scored, and followed from prospect to a live link (package
// seosync). Reads and adds use only what is stored, so nothing here costs
// money. One site is pitched by one campaign at a time.

func (s *Server) routesLinkProspects(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/link-prospects", s.inBrand(read, s.listLinkProspects))
	mux.HandleFunc("POST /v1/workspaces/{ws}/link-prospects", s.inBrand(manage, s.addLinkProspects))
	mux.HandleFunc("GET /v1/workspaces/{ws}/link-prospects/{prospect}", s.inBrand(read, s.getLinkProspect))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/link-prospects/{prospect}", s.inBrand(manage, s.updateLinkProspect))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/link-prospects/{prospect}", s.inBrand(manage, s.deleteLinkProspect))
}

type linkProspectView struct {
	ID             string                `json:"id"`
	SiteID         string                `json:"site_id"`
	BrandID        *string               `json:"brand_id"`
	RecordID       string                `json:"record_id"`
	PageURL        string                `json:"page_url"`
	Domain         string                `json:"domain"`
	Method         string                `json:"method"`
	TargetURL      string                `json:"target_url"`
	Evidence       json.RawMessage       `json:"evidence"`
	DomainRating   *float64              `json:"domain_rating"`
	Traffic        *float64              `json:"traffic"`
	SpamScore      *int                  `json:"spam_score"`
	Score          int                   `json:"score"`
	ScoreFactors   []seosync.ScoreFactor `json:"score_factors"`
	Stage          string                `json:"stage"`
	ExcludedReason *string               `json:"excluded_reason"`
	StageChangedAt time.Time             `json:"stage_changed_at"`
	PitchedAt      *time.Time            `json:"pitched_at"`
	AgreedAt       *time.Time            `json:"agreed_at"`
	LiveAt         *time.Time            `json:"live_at"`
	EndedAt        *time.Time            `json:"ended_at"`
	Source         string                `json:"source"`
	Note           string                `json:"note"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
}

const linkProspectCols = `p.id::text, p.site_id::text, p.brand_id::text, p.record_id::text, p.page_url, p.method, p.target_url, p.evidence,
	p.domain_rating, p.traffic, p.spam_score, p.score, p.score_factors, p.stage, p.excluded_reason, p.stage_changed_at,
	p.pitched_at, p.agreed_at, p.live_at, p.ended_at, p.source, p.note, p.created_at, p.updated_at`

func scanLinkProspect(row pgx.Row) (linkProspectView, error) {
	var v linkProspectView
	var factors []byte
	err := row.Scan(&v.ID, &v.SiteID, &v.BrandID, &v.RecordID, &v.PageURL, &v.Method, &v.TargetURL, &v.Evidence,
		&v.DomainRating, &v.Traffic, &v.SpamScore, &v.Score, &factors, &v.Stage, &v.ExcludedReason, &v.StageChangedAt,
		&v.PitchedAt, &v.AgreedAt, &v.LiveAt, &v.EndedAt, &v.Source, &v.Note, &v.CreatedAt, &v.UpdatedAt)
	_, v.Domain = seosync.ProspectPage(v.PageURL)
	v.ScoreFactors = []seosync.ScoreFactor{}
	_ = json.Unmarshal(factors, &v.ScoreFactors)
	return v, err
}

func (s *Server) loadLinkProspect(ctx context.Context, ws, id string, sel brandSel) (*linkProspectView, error) {
	args := []any{ws, id}
	v, err := scanLinkProspect(s.pool.QueryRow(ctx, `SELECT `+linkProspectCols+` FROM link_prospects p
		WHERE p.workspace_id = $1 AND p.id = $2`+sel.sql("p.brand_id", &args), args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}

func (s *Server) linkProspectFor(w http.ResponseWriter, r *http.Request, a *access) (*linkProspectView, bool) {
	id := r.PathValue("prospect")
	if validUUID(id) {
		p, err := s.loadLinkProspect(r.Context(), a.WorkspaceID, id, brandSel{in: a.Brands})
		if err != nil {
			s.internal(w, r, err)
			return nil, false
		}
		if p != nil {
			return p, true
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "link prospect not found")
	return nil, false
}

var linkProspectOrders = map[string]string{
	"score":   "p.score DESC, p.created_at DESC, p.id",
	"newest":  "p.created_at DESC, p.id",
	"changed": "p.stage_changed_at DESC, p.id",
}

func (s *Server) listLinkProspects(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	site, ok := siteParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	args := []any{a.WorkspaceID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := `p.workspace_id = $1`
	if site != "" {
		where += ` AND p.site_id = ` + arg(site) + `::uuid`
	}
	// stage takes several, comma-separated; by default every stage but
	// excluded.
	if v := q.Get("stage"); v != "" {
		stages := strings.Split(v, ",")
		for _, st := range stages {
			if !slices.Contains(seosync.ProspectStages, st) {
				bad("stage is " + strings.Join(seosync.ProspectStages, ", ") + ", or several of them separated by commas")
				return
			}
		}
		where += ` AND p.stage = ANY(` + arg(stages) + `)`
	} else {
		where += ` AND p.stage <> 'excluded'`
	}
	if v := q.Get("method"); v != "" {
		if !slices.Contains(seosync.ProspectMethods, v) {
			bad("method is " + strings.Join(seosync.ProspectMethods, ", "))
			return
		}
		where += ` AND p.method = ` + arg(v)
	}
	if v := q.Get("record_id"); v != "" {
		if !validUUID(v) {
			bad("record_id is a company record's id")
			return
		}
		where += ` AND p.record_id = ` + arg(v) + `::uuid`
	}
	if v := q.Get("min_score"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 100 {
			bad("min_score is a whole number from 0 to 100")
			return
		}
		where += ` AND p.score >= ` + arg(n)
	}
	order := linkProspectOrders["score"]
	if v := q.Get("order"); v != "" {
		o, ok := linkProspectOrders[v]
		if !ok {
			bad("order is score, newest or changed")
			return
		}
		order = o
	}
	where += sel.sql("p.brand_id", &args)
	limit, err := limitParam(r, 100, 500)
	if s.allocError(w, r, err) {
		return
	}
	offset, ok := offsetParam(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM link_prospects p WHERE `+where, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT `+linkProspectCols+` FROM link_prospects p WHERE `+where+`
		ORDER BY `+order+` LIMIT `+arg(limit)+` OFFSET `+arg(offset), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (linkProspectView, error) { return scanLinkProspect(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []linkProspectView{}
	}
	var next *int
	if offset+len(list) < total {
		n := offset + len(list)
		next = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_offset": next})
}

func (s *Server) getLinkProspect(w http.ResponseWriter, r *http.Request) {
	p, ok := s.linkProspectFor(w, r, accessFrom(r.Context()))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type linkProspectIn struct {
	PageURL      string         `json:"page_url"`
	Method       string         `json:"method"`
	TargetURL    string         `json:"target_url"`
	Evidence     map[string]any `json:"evidence"`
	DomainRating *float64       `json:"domain_rating"`
	Traffic      *float64       `json:"traffic"`
	SpamScore    *int           `json:"spam_score"`
	Note         string         `json:"note"`
}

// prospectTarget checks our page to offer: ” or a page on the site.
func prospectTarget(site seosync.ProspectSite, raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", true
	}
	page, d := seosync.ProspectPage(raw)
	if page == "" || !seosync.OwnDomain(site, d) {
		return "", false
	}
	return page, true
}

func (s *Server) addLinkProspects(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		SiteID    string           `json:"site_id"`
		Source    string           `json:"source"`
		Prospects []linkProspectIn `json:"prospects"`
	}
	if !decodeLarge(w, r, &req, 4<<20) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	if !validUUID(req.SiteID) {
		bad("site_id is an SEO site's id")
		return
	}
	if len(req.Prospects) == 0 || len(req.Prospects) > seosync.MaxProspectsAdded {
		bad("prospects lists 1 to " + strconv.Itoa(seosync.MaxProspectsAdded) + " pages")
		return
	}
	req.Source = strings.TrimSpace(req.Source)
	if req.Source == "" {
		req.Source = "api"
		if a.ActorType == "human" {
			req.Source = "manual"
		}
	}
	if len(req.Source) > 100 {
		bad("source is at most 100 characters")
		return
	}
	ctx := r.Context()
	sites, err := s.loadSEOSites(ctx, a.WorkspaceID, &req.SiteID, brandSel{in: a.Brands})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(sites) != 1 {
		writeError(w, r, http.StatusNotFound, "not_found", "site not found")
		return
	}
	site := seosync.ProspectSite{ID: sites[0].ID, BrandID: sites[0].BrandID, Host: sites[0].Host}
	if !a.writes(nil, site.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	list := make([]seosync.Prospect, len(req.Prospects))
	for i, p := range req.Prospects {
		at := "prospects[" + strconv.Itoa(i) + "]."
		page, d := seosync.ProspectPage(p.PageURL)
		switch {
		case page == "" || len(page) > 2048:
			bad(at + "page_url is a web page's address, like https://blog.example.org/tools")
			return
		case seosync.OwnDomain(site, d):
			bad(at + "page_url is on the site itself")
			return
		case p.Method == "":
			p.Method = "manual"
		case !slices.Contains(seosync.ProspectMethods, p.Method):
			bad(at + "method is " + strings.Join(seosync.ProspectMethods, ", "))
			return
		}
		target, ok := prospectTarget(site, p.TargetURL)
		if !ok {
			bad(at + "target_url is a page on the site, or empty")
			return
		}
		if (p.DomainRating != nil && (*p.DomainRating < 0 || *p.DomainRating > 100)) || (p.Traffic != nil && *p.Traffic < 0) ||
			(p.SpamScore != nil && (*p.SpamScore < 0 || *p.SpamScore > 100)) {
			bad(at + "domain_rating and spam_score are 0 to 100, traffic 0 or more")
			return
		}
		if len(p.Note) > 2000 {
			bad(at + "note is at most 2000 characters")
			return
		}
		list[i] = seosync.Prospect{PageURL: page, Method: p.Method, TargetURL: target, Evidence: p.Evidence,
			DomainRating: p.DomainRating, Traffic: p.Traffic, SpamScore: p.SpamScore, Note: strings.TrimSpace(p.Note)}
	}
	var res seosync.ProspectsAdded
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		res, err = seosync.AddProspects(ctx, tx, a.WorkspaceID, site, req.Source, actorRef(a), s.prospectRecorder(a), list)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT `+linkProspectCols+` FROM link_prospects p WHERE p.workspace_id = $1 AND p.id = ANY($2::uuid[])
		ORDER BY array_position($2::uuid[], p.id)`, a.WorkspaceID, uniqueIDs(res.IDs))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	data, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (linkProspectView, error) { return scanLinkProspect(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "added": res.Added, "existing": res.Existing, "excluded": res.Excluded})
}

// uniqueIDs keeps the first of each id, in order.
func uniqueIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func actorRef(a *access) *string {
	if a.ActorID == "" {
		return nil
	}
	return &a.ActorID
}

func (s *Server) prospectRecorder(a *access) seosync.Recorder {
	return func(ctx context.Context, tx pgx.Tx, action, id string, data, props map[string]any) error {
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, seosync.TargetOf(action), id, data, props)
	}
}

var errExcludedProspect = errors.New("excluded prospect")

// stageTimes are the columns a move to each stage sets.
var stageTimes = map[string]string{"pitched": "pitched_at", "agreed": "agreed_at", "live": "live_at", "declined": "ended_at"}

func (s *Server) updateLinkProspect(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Stage          string  `json:"stage"`
		ExcludedReason *string `json:"excluded_reason"`
		TargetURL      *string `json:"target_url"`
		Note           *string `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	if req.Stage == "" && req.TargetURL == nil && req.Note == nil {
		bad("set stage, target_url or note")
		return
	}
	if req.Stage != "" && !slices.Contains(seosync.ProspectStages, req.Stage) {
		bad("stage is " + strings.Join(seosync.ProspectStages, ", "))
		return
	}
	if req.Stage == "excluded" && (req.ExcludedReason == nil || !slices.Contains(seosync.ExcludedReasons, *req.ExcludedReason)) {
		bad("excluded_reason is " + strings.Join(seosync.ExcludedReasons, ", ") + " when stage is excluded")
		return
	}
	if req.ExcludedReason != nil && req.Stage != "excluded" {
		bad("excluded_reason goes with stage excluded")
		return
	}
	if req.Note != nil {
		*req.Note = strings.TrimSpace(*req.Note)
		if len(*req.Note) > 2000 {
			bad("note is at most 2000 characters")
			return
		}
	}
	p, ok := s.linkProspectFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(nil, p.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	if req.TargetURL != nil {
		sites, err := s.loadSEOSites(ctx, a.WorkspaceID, &p.SiteID, brandSel{})
		if err != nil || len(sites) != 1 {
			s.internal(w, r, errors.Join(err, errNotFound))
			return
		}
		t, ok := prospectTarget(seosync.ProspectSite{ID: sites[0].ID, Host: sites[0].Host}, *req.TargetURL)
		if !ok {
			bad("target_url is a page on the site, or empty")
			return
		}
		req.TargetURL = &t
	}
	moved := req.Stage != "" && req.Stage != p.Stage
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Entering outreach (by a pitch, or a reply or agreement that came
		// some other way) keeps to one site at a time.
		if moved && slices.Contains(seosync.InOutreach, req.Stage) && !slices.Contains(seosync.InOutreach, p.Stage) {
			if p.Stage == "excluded" {
				return errExcludedProspect
			}
			if err := seosync.CheckPitch(ctx, tx, a.WorkspaceID, p.ID, p.RecordID, p.Domain); err != nil {
				return err
			}
		}
		stamp := ""
		if col, ok := stageTimes[req.Stage]; ok && moved {
			stamp = `, ` + col + ` = now()`
		}
		var reason *string
		if req.Stage == "excluded" {
			reason = req.ExcludedReason
		}
		if _, err := tx.Exec(ctx, `UPDATE link_prospects SET
				stage = CASE WHEN $3 = '' THEN stage ELSE $3 END,
				excluded_reason = CASE WHEN $3 = '' THEN excluded_reason ELSE $4 END,
				stage_changed_at = CASE WHEN $3 = '' OR $3 = stage THEN stage_changed_at ELSE now() END,
				target_url = COALESCE($5, target_url), note = COALESCE($6, note), updated_at = now()`+stamp+`
			WHERE id = $1 AND workspace_id = $2`, p.ID, a.WorkspaceID, req.Stage, reason, req.TargetURL, req.Note); err != nil {
			return err
		}
		data := map[string]any{"site_id": p.SiteID, "brand_id": p.BrandID, "record_id": p.RecordID, "method": p.Method,
			"stage": p.Stage, "target_url_set": req.TargetURL != nil, "note_set": req.Note != nil}
		action := "link_prospect.updated"
		if moved {
			data["stage"], data["was"], data["excluded_reason"] = req.Stage, p.Stage, reason
			action = "link_prospect." + req.Stage
			if req.Stage == "prospect" {
				action = "link_prospect.reopened"
			}
		}
		props := map[string]any{"site_id": p.SiteID, "record_id": p.RecordID, "method": p.Method, "stage": data["stage"], "was": data["was"]}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, "link_prospect", p.ID, data, props)
	})
	switch {
	case errors.Is(err, seosync.ErrSiteInOutreach):
		writeError(w, r, http.StatusConflict, "site_in_outreach", "another prospect on "+p.Domain+" is being pitched, or was declined within the "+
			"workspace's cool-down (link building settings); one site is pitched by one campaign at a time")
		return
	case errors.Is(err, errExcludedProspect):
		writeError(w, r, http.StatusConflict, "conflict", "this prospect is excluded ("+deref(p.ExcludedReason)+"); move it back to prospect first")
		return
	case errors.Is(err, seosync.ErrSiteOptedOut):
		writeError(w, r, http.StatusConflict, "conflict", p.Domain+" is on the suppression list, so it is never pitched")
		return
	case s.allocError(w, r, err):
		return
	}
	p, err = s.loadLinkProspect(ctx, a.WorkspaceID, p.ID, brandSel{})
	if err != nil || p == nil {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deleteLinkProspect(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	p, ok := s.linkProspectFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(nil, p.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM link_prospects WHERE id = $1 AND workspace_id = $2`, p.ID, a.WorkspaceID); err != nil {
			return err
		}
		data := map[string]any{"site_id": p.SiteID, "record_id": p.RecordID, "method": p.Method, "stage": p.Stage}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "link_prospect.deleted", "link_prospect", p.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
