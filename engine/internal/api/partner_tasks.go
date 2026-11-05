package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/coverage"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

var audienceKinds = []string{"website", "youtube", "x", "linkedin", "instagram", "tiktok", "newsletter", "reddit", "facebook", "discord", "podcast", "other"}

type partnerTaskConfig struct {
	Title        string         `json:"title"`
	Description  string         `json:"description"`
	Category     string         `json:"category"`
	RewardKind   string         `json:"reward_kind"`
	Pricing      string         `json:"pricing"`
	Amount       int64          `json:"amount_cents"`
	MaxAmount    int64          `json:"max_amount_cents"`
	RewardLabel  string         `json:"reward_label"`
	Action       string         `json:"action"`
	ActionData   map[string]any `json:"action_data"`
	ClaimMode    string         `json:"claim_mode"`
	Verification string         `json:"verification"`
	HoldDays     int            `json:"hold_days"`
	AssetKind    string         `json:"asset_kind"`
	MinReach     int64          `json:"min_reach"`
	TargetHost   string         `json:"target_host"`
	RequiredLink string         `json:"required_link"`
	Slots        int            `json:"slots"`
}

type partnerTaskAction struct {
	Action      string `json:"action"`
	AudienceID  string `json:"audience_id"`
	EvidenceURL string `json:"evidence_url"`
	Note        string `json:"note"`
	Amount      *int64 `json:"amount_cents"`
}

// Public addresses only. Fetches run through a workspace-owned page reader;
// website ownership uses DNS, never an arbitrary HTTP fetch from the server.
func partnerAudienceURL(raw string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || len(raw) > 2000 {
		return "", "", errInvalid("use a public https URL without credentials or a port")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.ContainsAny(host, " \\/:") {
		return "", "", errInvalid("use a public website hostname")
	}
	if host == "" {
		return "", "", errInvalid("a hostname is required")
	}
	u.Host, u.Fragment = host, ""
	if u.Path == "/" {
		u.Path = ""
	}
	return u.String(), host, nil
}

func (v *partnerTaskConfig) check() error {
	v.Title, v.Description = strings.TrimSpace(v.Title), strings.TrimSpace(v.Description)
	if v.Category == "" {
		v.Category = "other"
	}
	if v.RewardKind == "" {
		v.RewardKind = "free"
	}
	if v.Pricing == "" {
		v.Pricing = "fixed"
	}
	if v.ClaimMode == "" {
		v.ClaimMode = "instant"
	}
	if v.Verification == "" {
		v.Verification = "human"
	}
	if v.Slots == 0 {
		v.Slots = 100
	}
	if v.Title == "" || len(v.Title) > 160 || v.Description == "" || len(v.Description) > 6000 || len(v.RewardLabel) > 200 || len(v.Action) > 60 {
		return errInvalid("a title (160 characters) and description (6,000 characters) are required")
	}
	if !slices.Contains([]string{"blog", "video", "social", "newsletter", "banner", "community", "other"}, v.Category) || !slices.Contains([]string{"free", "cash", "action"}, v.RewardKind) || !slices.Contains([]string{"fixed", "range", "custom"}, v.Pricing) || !slices.Contains([]string{"instant", "application"}, v.ClaimMode) || !slices.Contains([]string{"human", "auto", "auto_hold"}, v.Verification) {
		return errInvalid("choose valid task, reward, pricing, claim and verification options")
	}
	if v.Amount < 0 || v.MaxAmount < 0 || v.Amount > 1e8 || v.MaxAmount > 1e8 || v.MinReach < 0 || v.HoldDays < 0 || v.HoldDays > 365 || v.Slots < 1 || v.Slots > 10000 {
		return errInvalid("invalid reward, reach, hold or slot limit")
	}
	if v.AssetKind != "" && !slices.Contains(audienceKinds, v.AssetKind) {
		return errInvalid("choose a supported audience kind")
	}
	if v.RewardKind == "free" {
		v.Amount, v.MaxAmount, v.Pricing = 0, 0, "fixed"
	}
	if v.RewardKind == "cash" && (v.Pricing == "fixed" && v.Amount == 0 || v.Pricing == "range" && (v.Amount == 0 || v.MaxAmount < v.Amount)) {
		return errInvalid("cash needs a positive fixed price or a valid range")
	}
	if v.RewardKind == "action" {
		v.Amount, v.MaxAmount = 0, 0
	}
	if v.RewardKind == "action" && (v.Action == "" || v.RewardLabel == "") {
		return errInvalid("a product reward needs an action and a reward label")
	}
	if v.RewardKind != "cash" && v.Pricing != "fixed" {
		return errInvalid("price ranges and custom prices apply to cash rewards")
	}
	if v.Pricing != "fixed" {
		v.ClaimMode = "application"
	}
	if v.RequiredLink != "" {
		var err error
		v.RequiredLink, _, err = partnerAudienceURL(v.RequiredLink)
		if err != nil {
			return err
		}
	}
	if v.Verification != "human" && (v.RequiredLink == "" || v.AssetKind != "website") {
		return errInvalid("automatic checks require a website asset and a required link; other categories use human review")
	}
	if v.Verification == "auto_hold" && v.HoldDays < 1 {
		return errInvalid("choose at least one day to hold automatically checked work for human review")
	}
	if v.TargetHost != "" {
		_, host, err := partnerAudienceURL("https://" + strings.TrimSpace(v.TargetHost))
		if err != nil || host != strings.ToLower(strings.TrimSpace(v.TargetHost)) {
			return errInvalid("target_host is a hostname, such as example.com")
		}
		v.TargetHost = host
		v.AssetKind = "website"
		v.Slots = 1
	}
	if v.ActionData == nil {
		v.ActionData = map[string]any{}
	}
	return nil
}

func (s *Server) routesPartnerTasks(mux *http.ServeMux) {
	base := "partner-programs/{id}"
	s.twinRoute(mux, "GET", base+"/tasks", s.inBrand, auth.ScopePartnersRead, s.listPartnerTasks)
	s.twinRoute(mux, "POST", base+"/tasks", s.inBrand, auth.ScopePartnersManage, s.createPartnerTask)
	s.twinRoute(mux, "POST", base+"/tasks/{task}", s.inBrand, auth.ScopePartnersManage, s.updatePartnerTask)
	s.twinRoute(mux, "POST", base+"/task-submissions/{submission}", s.inBrand, auth.ScopePartnersManage, s.reviewPartnerTask)
	s.twinRoute(mux, "GET", base+"/audiences", s.inBrand, auth.ScopePartnersRead, s.listPartnerAudiences)
	mux.HandleFunc("GET /p/{program}/api/tasks", s.inPortal(s.portalTasks))
	mux.HandleFunc("POST /p/{program}/api/tasks/{task}", s.inPortal(s.portalTaskAction))
	mux.HandleFunc("GET /p/{program}/api/audiences", s.inPortal(s.portalAudiences))
	mux.HandleFunc("POST /p/{program}/api/audiences", s.inPortal(s.portalAddAudience))
	mux.HandleFunc("POST /p/{program}/api/audiences/{audience}", s.inPortal(s.portalAudienceAction))
}

func (s *Server) createPartnerTask(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	p, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in partnerTaskConfig
	if !decode(w, r, &in) || s.allocError(w, r, in.check()) {
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		raw, _ := json.Marshal(in)
		if err := tx.QueryRow(ctx, `INSERT INTO partner_tasks(workspace_id,brand_id,program_id,config) VALUES($1,$2,$3,$4) RETURNING id::text`, a.WorkspaceID, p.BrandID, p.ID, raw).Scan(&id); err != nil {
			return err
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_task.created", "partner_task", id, map[string]any{"kind": in.RewardKind, "program_id": p.ID})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "status": "active", "config": in})
}

// JSON views use explicit keys: challenges and private proposals never leak into another partner's view.
const taskViewSQL = `SELECT jsonb_build_object('id',t.id,'status',t.status,'config',t.config,'created_at',t.created_at) FROM partner_tasks t
 WHERE t.workspace_id=$1 AND t.program_id=$2 AND t.brand_id IS NOT DISTINCT FROM $3::uuid`
const submissionViewSQL = `SELECT jsonb_build_object('id',x.id,'task_id',x.task_id,'membership_id',x.membership_id,'audience_id',x.audience_id,'status',x.status,'amount_cents',x.amount_cents,'evidence_url',x.evidence_url,'note',x.note,'check_status',x.check_status,'hold_until',x.hold_until,'reward_id',x.reward_id) FROM partner_task_submissions x
 WHERE x.workspace_id=$1 AND x.program_id=$2 AND x.brand_id IS NOT DISTINCT FROM $3::uuid`
const audienceViewSQL = `SELECT jsonb_build_object('id',x.id,'membership_id',x.membership_id,'url',x.url,'host',x.host,'kind',x.kind,'role',x.role,'topics',x.topics,'reach',x.reach,'reach_source',x.reach_source,'measured_at',x.measured_at,'verified_at',x.verified_at,'last_error',x.last_error) FROM partner_audiences x
 WHERE x.workspace_id=$1 AND x.program_id=$2 AND x.brand_id IS NOT DISTINCT FROM $3::uuid`

func (s *Server) partnerTaskRows(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (map[string]any, error) {
		var v map[string]any
		err := row.Scan(&v)
		return v, err
	})
}
func (s *Server) listPartnerTasks(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	p, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	data, err := s.partnerTaskRows(r.Context(), taskViewSQL+` ORDER BY t.created_at DESC LIMIT 500`, a.WorkspaceID, p.ID, p.BrandID)
	if s.allocError(w, r, err) {
		return
	}
	submissions, err := s.partnerTaskRows(r.Context(), submissionViewSQL+` ORDER BY x.updated_at DESC LIMIT 500`, a.WorkspaceID, p.ID, p.BrandID)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"data": data, "submissions": submissions})
}
func (s *Server) listPartnerAudiences(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	p, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	data, err := s.partnerTaskRows(r.Context(), audienceViewSQL+` ORDER BY x.created_at DESC LIMIT 500`, a.WorkspaceID, p.ID, p.BrandID)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"data": data})
}
func (s *Server) updatePartnerTask(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	p, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in partnerTaskAction
	if !decode(w, r, &in) {
		return
	}
	if in.Action != "pause" && in.Action != "resume" {
		s.allocError(w, r, errInvalid("choose pause or resume"))
		return
	}
	status := "active"
	if in.Action == "pause" {
		status = "paused"
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `UPDATE partner_tasks SET status=$5 WHERE workspace_id=$1 AND program_id=$2 AND brand_id IS NOT DISTINCT FROM $3::uuid AND id::text=$4`, a.WorkspaceID, p.ID, p.BrandID, r.PathValue("task"), status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return partnerRecord(r.Context(), tx, &a.principal, a.WorkspaceID, "partner_task.updated", "partner_task", r.PathValue("task"), map[string]any{"status": status})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"status": status})
}

func portalTaskWrite(w http.ResponseWriter, r *http.Request, c *portalCaller) bool {
	if c.readOnly || c.partner.Status != "active" || c.program.status != "active" {
		writeError(w, r, 403, "forbidden", "an active partnership is required; previews are read-only")
		return false
	}
	return true
}

func (s *Server) portalTasks(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	data, err := s.partnerTaskRows(r.Context(), taskViewSQL+` AND (t.status='active' OR EXISTS(SELECT 1 FROM partner_task_submissions x WHERE x.task_id=t.id AND x.membership_id=$4))
 AND (COALESCE(t.config->>'target_host','')='' OR EXISTS(SELECT 1 FROM partner_audiences x WHERE x.workspace_id=t.workspace_id AND x.program_id=t.program_id AND x.membership_id=$4 AND x.kind='website' AND x.host=t.config->>'target_host' AND x.verified_at IS NOT NULL)) ORDER BY t.created_at DESC LIMIT 500`, c.program.ws, c.program.id, c.partner.brandID, c.partner.ID)
	if s.allocError(w, r, err) {
		return
	}
	subs, err := s.partnerTaskRows(r.Context(), submissionViewSQL+` AND x.membership_id=$4 ORDER BY x.created_at DESC LIMIT 500`, c.program.ws, c.program.id, c.partner.brandID, c.partner.ID)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"data": data, "submissions": subs})
}
func (s *Server) portalAudiences(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	data, err := s.partnerTaskRows(r.Context(), strings.Replace(audienceViewSQL, "'last_error',x.last_error)", "'last_error',x.last_error,'challenge',x.challenge,'dns_name','_outflare-partner.'||x.host)", 1)+` AND x.membership_id=$4 ORDER BY x.created_at DESC LIMIT 100`, c.program.ws, c.program.id, c.partner.brandID, c.partner.ID)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"data": data})
}
func (s *Server) portalAddAudience(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	if !portalTaskWrite(w, r, c) {
		return
	}
	var in struct {
		URL    string `json:"url"`
		Kind   string `json:"kind"`
		Role   string `json:"role"`
		Topics string `json:"topics"`
		Reach  int64  `json:"reach"`
	}
	if !decode(w, r, &in) {
		return
	}
	u, host, err := partnerAudienceURL(in.URL)
	if s.allocError(w, r, err) {
		return
	}
	if in.Role == "" {
		in.Role = "owner"
	}
	if !slices.Contains(audienceKinds, in.Kind) || !slices.Contains([]string{"owner", "admin", "moderator", "contributor"}, in.Role) || len(in.Topics) > 500 || in.Reach < 0 || in.Reach > 1e10 {
		s.allocError(w, r, errInvalid("choose a valid asset kind, role and reach"))
		return
	}
	ctx := r.Context()
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock the membership to bound both concurrent asset additions and per-partner storage.
		if _, err := tx.Exec(ctx, `SELECT id FROM partner_memberships WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, c.program.ws, c.partner.ID); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM partner_audiences WHERE workspace_id=$1 AND program_id=$2 AND membership_id=$3`, c.program.ws, c.program.id, c.partner.ID).Scan(&n); err != nil {
			return err
		}
		if n >= 100 {
			return errInvalid("at most 100 profiles per partner")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO partner_audiences(workspace_id,brand_id,program_id,membership_id,record_id,url,host,kind,role,topics,reach) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id::text`, c.program.ws, c.partner.brandID, c.program.id, c.partner.ID, c.partner.RecordID, u, host, in.Kind, in.Role, in.Topics, in.Reach).Scan(&id); err != nil {
			return err
		}
		ids, err := s.pageReaders(ctx, tx, c.program.ws)
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := enqueuePartnerTaskCheck(ctx, tx, c.program.ws, "refresh", id); err != nil {
				return err
			}
		}
		return partnerRecord(ctx, tx, nil, c.program.ws, "partner_audience.created", "partner_audience", id, map[string]any{"kind": in.Kind, "partner_id": c.partner.ID})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) portalAudienceAction(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	if !portalTaskWrite(w, r, c) {
		return
	}
	var in partnerTaskAction
	if !decode(w, r, &in) {
		return
	}
	if !slices.Contains([]string{"verify", "refresh", "remove"}, in.Action) {
		s.allocError(w, r, errInvalid("choose verify, refresh or remove"))
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id, kind string
		err := tx.QueryRow(ctx, `SELECT id::text,kind FROM partner_audiences WHERE workspace_id=$1 AND program_id=$2 AND membership_id=$3 AND id::text=$4 AND brand_id IS NOT DISTINCT FROM $5::uuid FOR UPDATE`, c.program.ws, c.program.id, c.partner.ID, r.PathValue("audience"), c.partner.brandID).Scan(&id, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if in.Action == "remove" {
			var used bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM partner_task_submissions WHERE workspace_id=$1 AND audience_id=$2 AND status NOT IN ('approved','rejected'))`, c.program.ws, id).Scan(&used); err != nil {
				return err
			}
			if used {
				return errInvalid("this profile has an active task")
			}
			if _, err := tx.Exec(ctx, `DELETE FROM partner_audiences WHERE workspace_id=$1 AND id=$2`, c.program.ws, id); err != nil {
				return err
			}
		} else {
			if in.Action == "verify" && kind != "website" {
				return errInvalid("DNS ownership verification is for websites; social reach does not prove ownership")
			}
			if in.Action == "refresh" {
				ids, err := s.pageReaders(ctx, tx, c.program.ws)
				if err != nil {
					return err
				}
				if len(ids) == 0 {
					return errInvalid("connect your workspace's own Geonode account in Connections to refresh reach")
				}
			}
			if err := enqueuePartnerTaskCheck(ctx, tx, c.program.ws, in.Action, id); err != nil {
				return err
			}
		}
		return partnerRecord(ctx, tx, nil, c.program.ws, "partner_audience.updated", "partner_audience", id, map[string]any{"kind": in.Action, "partner_id": c.partner.ID})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"queued": in.Action != "remove"})
}

func enqueuePartnerTaskCheck(ctx context.Context, tx pgx.Tx, ws, action, id string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnerTaskCheck, WorkspaceID: &ws, Payload: map[string]string{"action": action, "id": id}, IdempotencyKey: "partner.task:" + action + ":" + id + ":" + time.Now().UTC().Format("200601021504"), MaxAttempts: 3})
	return err
}

func (s *Server) portalTaskAction(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	if !portalTaskWrite(w, r, c) {
		return
	}
	var in partnerTaskAction
	if !decode(w, r, &in) {
		return
	}
	if !slices.Contains([]string{"apply", "accept", "submit"}, in.Action) || len(in.Note) > 2000 {
		s.allocError(w, r, errInvalid("choose apply, accept or submit"))
		return
	}
	ctx := r.Context()
	var out map[string]any
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var taskID, status string
		var cfg partnerTaskConfig
		err := tx.QueryRow(ctx, `SELECT id::text,status,config FROM partner_tasks WHERE workspace_id=$1 AND program_id=$2 AND brand_id IS NOT DISTINCT FROM $3::uuid AND id::text=$4 FOR UPDATE`, c.program.ws, c.program.id, c.partner.brandID, r.PathValue("task")).Scan(&taskID, &status, &cfg)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if status != "active" && in.Action != "submit" {
			return errInvalid("this task is paused")
		}
		var audience *string
		if in.AudienceID != "" {
			audience = &in.AudienceID
		}
		// Eligibility is checked before work starts; a later reach refresh must not block agreed work.
		if in.Action != "submit" && (cfg.AssetKind != "" || cfg.TargetHost != "" || cfg.MinReach > 0 || audience != nil) {
			var eligible bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM partner_audiences WHERE workspace_id=$1 AND program_id=$2 AND membership_id=$3 AND id::text=$4 AND brand_id IS NOT DISTINCT FROM $5::uuid AND ($6='' OR kind=$6) AND reach >= $7 AND ($8='' OR (host=$8 AND kind='website' AND verified_at IS NOT NULL)))`, c.program.ws, c.program.id, c.partner.ID, in.AudienceID, c.partner.brandID, cfg.AssetKind, cfg.MinReach, cfg.TargetHost).Scan(&eligible)
			if err != nil {
				return err
			}
			if !eligible {
				return errInvalid("select an eligible profile; targeted website offers require verified ownership")
			}
		}
		var id, cur string
		var amount int64
		var savedAudience *string
		err = tx.QueryRow(ctx, `SELECT id::text,status,amount_cents,audience_id::text FROM partner_task_submissions WHERE workspace_id=$1 AND task_id=$2 AND membership_id=$3 FOR UPDATE`, c.program.ws, taskID, c.partner.ID).Scan(&id, &cur, &amount, &savedAudience)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if id == "" {
			if in.Action == "submit" {
				return errInvalid("accept the task before submitting work")
			}
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM partner_task_submissions WHERE workspace_id=$1 AND task_id=$2 AND status<>'rejected'`, c.program.ws, taskID).Scan(&count); err != nil {
				return err
			}
			if count >= cfg.Slots {
				return errInvalid("all task slots are taken")
			}
			cur = "accepted"
			if cfg.ClaimMode == "application" || cfg.Pricing != "fixed" {
				cur = "applied"
			}
			amount = cfg.Amount
			err = tx.QueryRow(ctx, `INSERT INTO partner_task_submissions(workspace_id,brand_id,program_id,task_id,membership_id,record_id,audience_id,status,amount_cents,note) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id::text`, c.program.ws, c.partner.brandID, c.program.id, taskID, c.partner.ID, c.partner.RecordID, audience, cur, amount, in.Note).Scan(&id)
			if err != nil {
				return err
			}
		} else {
			// Once applied, the asset is part of the proposal; it cannot be swapped for a larger quoted fee.
			if (savedAudience == nil) != (audience == nil) || (savedAudience != nil && *savedAudience != *audience) {
				return errInvalid("use the profile chosen for this proposal")
			}
			switch in.Action {
			case "accept":
				if cur != "offered" {
					return errInvalid("only a priced offer can be accepted")
				}
				cur = "accepted"
			case "submit":
				if cur != "accepted" {
					return errInvalid("only accepted tasks can be submitted")
				}
				if in.EvidenceURL != "" || cfg.Verification != "human" {
					evidence, _, err := partnerAudienceURL(in.EvidenceURL)
					if err != nil {
						return err
					}
					in.EvidenceURL = evidence
				} else if strings.TrimSpace(in.Note) == "" {
					return errInvalid("add a link or a note describing how you completed the task")
				}
				cur = "submitted"
			default:
				return errInvalid("you already have an assignment for this task")
			}
			if _, err := tx.Exec(ctx, `UPDATE partner_task_submissions SET status=$3,evidence_url=CASE WHEN $4='' THEN evidence_url ELSE $4 END,note=$5,updated_at=now() WHERE workspace_id=$1 AND id=$2`, c.program.ws, id, cur, in.EvidenceURL, in.Note); err != nil {
				return err
			}
			if cur == "submitted" && cfg.Verification != "human" {
				if err := enqueuePartnerTaskCheck(ctx, tx, c.program.ws, "submission", id); err != nil {
					return err
				}
			}
		}
		out = map[string]any{"id": id, "status": cur, "amount_cents": amount}
		return partnerRecord(ctx, tx, nil, c.program.ws, "partner_task_submission.updated", "partner_task_submission", id, map[string]any{"status": cur, "partner_id": c.partner.ID, "program_id": c.program.id})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) reviewPartnerTask(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	p, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in partnerTaskAction
	if !decode(w, r, &in) {
		return
	}
	if !slices.Contains([]string{"offer", "approve", "reject"}, in.Action) || len(in.Note) > 2000 {
		s.allocError(w, r, errInvalid("choose offer, approve or reject"))
		return
	}
	ctx := r.Context()
	var status string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		var cfg partnerTaskConfig
		var amount int64
		err := tx.QueryRow(ctx, `SELECT x.id::text,x.status,x.amount_cents,t.config FROM partner_task_submissions x JOIN partner_tasks t ON t.id=x.task_id WHERE x.workspace_id=$1 AND x.program_id=$2 AND x.brand_id IS NOT DISTINCT FROM $3::uuid AND x.id::text=$4 FOR UPDATE OF x`, a.WorkspaceID, p.ID, p.BrandID, r.PathValue("submission")).Scan(&id, &status, &amount, &cfg)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		switch in.Action {
		case "offer":
			if status != "applied" {
				return errInvalid("only applications can receive a price offer")
			}
			if in.Amount != nil {
				amount = *in.Amount
			}
			if amount < 0 || amount > 1e8 || cfg.RewardKind == "cash" && amount == 0 || cfg.RewardKind != "cash" && amount != 0 || cfg.Pricing == "fixed" && amount != cfg.Amount || cfg.Pricing == "range" && (amount < cfg.Amount || amount > cfg.MaxAmount) {
				return errInvalid("offer a valid price within this task's reward terms")
			}
			status = "offered"
		case "approve":
			if status != "submitted" {
				return errInvalid("only submitted work can be approved")
			}
			if err := s.approveTaskSubmission(ctx, tx, &a.principal, a.WorkspaceID, id, cfg, "manual"); err != nil {
				return err
			}
			status = "approved"
		case "reject":
			if status == "approved" || status == "rejected" {
				return errInvalid("this assignment is already final")
			}
			status = "rejected"
		}
		if _, err := tx.Exec(ctx, `UPDATE partner_task_submissions SET status=$3,amount_cents=$4,note=$5,updated_at=now() WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, id, status, amount, in.Note); err != nil {
			return err
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_task_submission.reviewed", "partner_task_submission", id, map[string]any{"status": status, "program_id": p.ID})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"status": status})
}

// The assignment lock and final status make reward creation exactly once, including concurrent human and automatic checks.
func (s *Server) approveTaskSubmission(ctx context.Context, tx pgx.Tx, actor *principal, ws, id string, cfg partnerTaskConfig, by string) error {
	var program, member, recipient, currency, status, evidenceURL, check, programKind string
	var amount int64
	var reward, brand *string
	var submitted time.Time
	err := tx.QueryRow(ctx, `SELECT x.program_id::text,x.membership_id::text,x.record_id::text,p.currency,x.status,x.amount_cents,x.reward_id::text,x.evidence_url,x.check_status,p.kind,x.brand_id::text,x.updated_at FROM partner_task_submissions x JOIN partner_memberships m ON m.id=x.membership_id JOIN partner_programs p ON p.id=x.program_id WHERE x.workspace_id=$1 AND x.id=$2 FOR UPDATE OF x`, ws, id).Scan(&program, &member, &recipient, &currency, &status, &amount, &reward, &evidenceURL, &check, &programKind, &brand, &submitted)
	if err != nil {
		return err
	}
	if status != "submitted" || reward != nil {
		return nil
	}
	if cfg.RewardKind != "free" {
		kind := "fee"
		if cfg.RewardKind == "action" {
			kind = "action"
			amount = 0
		}
		rid, err := insertPartnerReward(ctx, tx, ws, rewardEntry{program: program, membership: member, recipient: recipient, side: "partner", kind: kind, amount: amount, currency: currency, action: cfg.Action, actionData: cfg.ActionData, note: cfg.Title, hold: time.Now()})
		if err != nil {
			return err
		}
		v, err := loadReward(ctx, tx, ws, rid)
		if err != nil {
			return err
		}
		d, err := rewardAuditData(ctx, tx, v)
		if err != nil {
			return err
		}
		d["task_submission_id"] = id
		if err := partnerRecord(ctx, tx, actor, ws, "partner_reward.created", "partner_reward", rid, d); err != nil {
			return err
		}
		if err := approvePartnerReward(ctx, tx, actor, ws, rid, by); err != nil {
			return err
		}
		reward = &rid
	}
	if _, err = tx.Exec(ctx, `UPDATE partner_task_submissions SET status='approved',reward_id=$3,updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, id, reward); err != nil {
		return err
	}
	// The partner's post or page, approved by a person who saw it (or
	// checked for the required link), is an appearance of the brand:
	// coverage keeps it as a live placement.
	if brand != nil && strings.HasPrefix(evidenceURL, "http") {
		kind, arrangement := partnerPlacementKind(cfg, programKind)
		evidence := map[string]any{"mention_present": true, "task_submission_id": id, "asset_kind": cfg.AssetKind, "check_status": check}
		if cfg.RequiredLink != "" {
			evidence["link_present"] = check == "passed"
		}
		if _, _, err := coverage.Record(ctx, tx, ws, *brand, coverage.Placement{URL: evidenceURL, Title: cfg.Title, Kind: kind, Arrangement: arrangement,
			TargetURL: cfg.RequiredLink, ExpectedLink: cfg.RequiredLink != "", Source: "partner_tasks", SourceID: id, PublishedOn: &submitted, Evidence: evidence}); err != nil && !errors.Is(err, coverage.ErrNoURL) {
			return err
		}
	}
	return nil
}

// partnerPlacementKind is the coverage placement kind and arrangement of
// a partner's approved task: by where the partner published (a video, a
// newsletter issue, a podcast, a social or community post, their own
// site) and how the program rewards them.
func partnerPlacementKind(cfg partnerTaskConfig, programKind string) (kind, arrangement string) {
	switch cfg.AssetKind {
	case "youtube", "tiktok":
		kind = "video"
	case "newsletter":
		kind = "newsletter"
	case "podcast":
		kind = "podcast"
	case "x", "linkedin", "instagram", "reddit", "facebook", "discord":
		kind = "community"
	default:
		kind = "partner"
		if programKind == "influencer" {
			kind = "blog"
		}
	}
	switch {
	case programKind == "affiliate":
		arrangement = "affiliate"
	case cfg.RewardKind == "free":
		arrangement = "earned"
	default:
		arrangement = "paid"
	}
	return kind, arrangement
}
