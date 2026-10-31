package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/reviewrequests"
)

func (s *Server) routesReviewRequests(mux *http.ServeMux) {
	base := "/v1/brands/{brand}/review-requests"
	mux.HandleFunc("GET "+base+"/settings", s.onBrand(auth.ScopeEmailsRead, s.getReviewRequestSettings))
	mux.HandleFunc("PUT "+base+"/settings", s.onBrand(auth.ScopeEmailsManage, s.putReviewRequestSettings))
	mux.HandleFunc("GET "+base+"/campaigns", s.onBrand(auth.ScopeEmailsRead, s.listReviewRequestCampaigns))
	mux.HandleFunc("POST "+base+"/campaigns", s.onBrand(auth.ScopeEmailsManage, s.createReviewRequestCampaign))
	mux.HandleFunc("PUT "+base+"/campaigns/{campaign}", s.onBrand(auth.ScopeEmailsManage, s.updateReviewRequestCampaign))
	mux.HandleFunc("POST "+base+"/campaigns/{campaign}/preview", s.onBrand(auth.ScopeEmailsManage, s.previewReviewRequestCampaign))
	mux.HandleFunc("POST "+base+"/campaigns/{campaign}/action", s.onBrand(auth.ScopeEmailsSend, s.actOnReviewRequestCampaign))
	mux.HandleFunc("POST "+base+"/campaigns/{campaign}/test", s.onBrand(auth.ScopeEmailsSend, s.testReviewRequestCampaign))
	mux.HandleFunc("GET "+base, s.onBrand(auth.ScopeEmailsRead, s.listReviewRequests))
}
func (s *Server) reviewRequests() *reviewrequests.Service {
	return &reviewrequests.Service{Pool: s.pool, Mail: s.emails}
}
func (s *Server) getReviewRequestSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	settings, err := reviewrequests.LoadSettings(r.Context(), s.pool, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"settings": settings, "platforms": reviewrequests.Platforms})
}
func (s *Server) putReviewRequestSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var settings reviewrequests.Settings
	if !decode(w, r, &settings) {
		return
	}
	if err := settings.Check(); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	raw, _ := json.Marshal(settings)
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `INSERT INTO review_request_settings(workspace_id,brand_id,config) VALUES($1,$2,$3) ON CONFLICT(brand_id) DO UPDATE SET config=EXCLUDED.config WHERE review_request_settings.workspace_id=$1`, a.WorkspaceID, a.Brand, raw); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "review_request_settings.updated", "brand", a.Brand, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getReviewRequestSettings(w, r)
}

type reviewCampaign struct {
	Stats     map[string]int        `json:"stats"`
	ID        string                `json:"id"`
	BrandID   string                `json:"brand_id"`
	Status    string                `json:"status"`
	Config    reviewrequests.Config `json:"config"`
	CreatedAt time.Time             `json:"created_at"`
	SendAt    *time.Time            `json:"send_at"`
}

func scanReviewCampaign(row pgx.Row) (reviewCampaign, error) {
	var c reviewCampaign
	var raw, stats []byte
	err := row.Scan(&c.ID, &c.BrandID, &c.Status, &raw, &c.CreatedAt, &c.SendAt, &stats)
	if err == nil {
		err = json.Unmarshal(raw, &c.Config)
		if err == nil {
			err = json.Unmarshal(stats, &c.Stats)
		}
	}
	return c, err
}

const reviewCampaignSelect = `SELECT c.id::text,c.brand_id::text,c.status,c.config,c.created_at,c.send_at,
 (SELECT jsonb_build_object('sent',count(*) FILTER(WHERE e.sent_at IS NOT NULL),'clicked',count(*) FILTER(WHERE EXISTS(SELECT 1 FROM tracked_links tl JOIN link_clicks lc ON lc.link_id=tl.id AND lc.workspace_id=r.workspace_id AND NOT lc.likely_bot WHERE tl.email_id=e.id AND EXISTS(SELECT 1 FROM jsonb_array_elements(r.destinations) d WHERE d->>'url'=tl.url))),'skipped',count(*) FILTER(WHERE r.status='skipped' OR e.status='suppressed'),'failed',count(*) FILTER(WHERE r.status='failed' OR e.status='failed'),'pending',count(*) FILTER(WHERE r.status='pending' OR e.status IN ('queued','scheduled','sending')))
 FROM review_requests r LEFT JOIN emails e ON e.id=r.email_id AND e.workspace_id=r.workspace_id WHERE r.workspace_id=c.workspace_id AND r.brand_id=c.brand_id AND r.campaign_id=c.id) FROM review_request_campaigns c WHERE c.workspace_id=$1 AND c.brand_id=$2`

func (s *Server) findReviewCampaign(w http.ResponseWriter, r *http.Request) (reviewCampaign, bool) {
	a := accessFrom(r.Context())
	id := r.PathValue("campaign")
	if !validUUID(id) {
		writeError(w, r, 404, "not_found", "review campaign not found")
		return reviewCampaign{}, false
	}
	c, err := scanReviewCampaign(s.pool.QueryRow(r.Context(), reviewCampaignSelect+` AND id=$3`, a.WorkspaceID, a.Brand, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, 404, "not_found", "review campaign not found")
		return c, false
	}
	if err != nil {
		s.internal(w, r, err)
		return c, false
	}
	return c, true
}
func (s *Server) listReviewRequestCampaigns(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), reviewCampaignSelect+` ORDER BY created_at DESC LIMIT 500`, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (reviewCampaign, error) { return scanReviewCampaign(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []reviewCampaign{}
	}
	writeJSON(w, 200, map[string]any{"data": list})
}
func validateReviewConfig(c reviewrequests.Config) error {
	if err := c.Check(); err != nil {
		return err
	}
	if c.Audience.ListID != "" && !validUUID(c.Audience.ListID) {
		return errInvalid("list_id must be a customer list id")
	}
	for _, id := range c.Audience.RecordIDs {
		if !validUUID(id) {
			return errInvalid("record_ids must contain customer ids")
		}
	}
	return nil
}
func (s *Server) createReviewRequestCampaign(w http.ResponseWriter, r *http.Request) {
	s.saveReviewRequestCampaign(w, r, false)
}
func (s *Server) updateReviewRequestCampaign(w http.ResponseWriter, r *http.Request) {
	s.saveReviewRequestCampaign(w, r, true)
}
func (s *Server) saveReviewRequestCampaign(w http.ResponseWriter, r *http.Request, update bool) {
	a := accessFrom(r.Context())
	var c reviewrequests.Config
	if !decode(w, r, &c) {
		return
	}
	if err := validateReviewConfig(c); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	var cur reviewCampaign
	if update {
		var ok bool
		cur, ok = s.findReviewCampaign(w, r)
		if !ok {
			return
		}
	}
	raw, _ := json.Marshal(c)
	id := cur.ID
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		action := "review_campaign.created"
		if update {
			var status string
			var previous []byte
			if err := tx.QueryRow(r.Context(), `SELECT status,config FROM review_request_campaigns WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 FOR UPDATE`, a.WorkspaceID, a.Brand, id).Scan(&status, &previous); err != nil {
				return err
			}
			var old reviewrequests.Config
			_ = json.Unmarshal(previous, &old)
			if status != "draft" && (old.Mode != "workflow" || status != "paused" || c.Mode != old.Mode) {
				return errConflict
			}
			if _, err := tx.Exec(r.Context(), `UPDATE review_request_campaigns SET config=$4,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, id, raw); err != nil {
				return err
			}
			action = "review_campaign.updated"
		} else {
			if err := tx.QueryRow(r.Context(), `INSERT INTO review_request_campaigns(workspace_id,brand_id,config) VALUES($1,$2,$3) RETURNING id::text`, a.WorkspaceID, a.Brand, raw).Scan(&id); err != nil {
				return err
			}
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, action, "review_campaign", id, nil)
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, 409, "conflict", "edit a draft or paused workflow campaign; duplicate a launched one-time campaign")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	r.SetPathValue("campaign", id)
	if v, ok := s.findReviewCampaign(w, r); ok {
		status := 201
		if update {
			status = 200
		}
		writeJSON(w, status, v)
	}
}
func (s *Server) previewReviewRequestCampaign(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	c, ok := s.findReviewCampaign(w, r)
	if !ok {
		return
	}
	counts := map[string]int{}
	total, eligible := 0, 0
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		settings, err := reviewrequests.LoadSettings(r.Context(), tx, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		dests, err := c.Config.Destinations(settings)
		if err != nil {
			return errInvalid(err.Error())
		}
		if c.Config.Mode == "workflow" {
			return nil
		}
		ids, err := reviewrequests.AudienceIDs(r.Context(), tx, a.WorkspaceID, a.Brand, c.Config.Audience)
		if err != nil {
			return errInvalid(err.Error())
		}
		total = len(ids)
		for _, id := range ids {
			_, why, err := reviewrequests.Eligible(r.Context(), tx, a.WorkspaceID, a.Brand, id, "", settings, dests)
			if err != nil {
				return err
			}
			if why == "" {
				eligible++
			} else {
				counts[why]++
			}
		}
		return nil
	})
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, 400, "invalid_request", string(inv))
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"total": total, "eligible": eligible, "excluded": counts})
}
func (s *Server) actOnReviewRequestCampaign(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	c, ok := s.findReviewCampaign(w, r)
	if !ok {
		return
	}
	var in struct {
		Action string     `json:"action"`
		SendAt *time.Time `json:"send_at"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.SendAt != nil && (in.Action != "launch" || in.SendAt.Before(time.Now()) || in.SendAt.After(time.Now().Add(365*24*time.Hour))) {
		writeError(w, r, 400, "invalid_request", "schedule a launch within the next year")
		return
	}
	id := c.ID
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		locked, err := scanReviewCampaign(tx.QueryRow(r.Context(), reviewCampaignSelect+` AND id=$3 FOR UPDATE`, a.WorkspaceID, a.Brand, c.ID))
		if err != nil {
			return err
		}
		c = locked
		status := c.Status
		switch in.Action {
		case "duplicate":
			clone := c.Config
			clone.Name += " (copy)"
			raw, _ := json.Marshal(clone)
			err = tx.QueryRow(r.Context(), `INSERT INTO review_request_campaigns(workspace_id,brand_id,config) VALUES($1,$2,$3) RETURNING id::text`, a.WorkspaceID, a.Brand, raw).Scan(&id)
			if err != nil {
				return err
			}
		case "launch":
			if status != "draft" {
				return errConflict
			}
			if c.Config.Mode == "workflow" && in.SendAt != nil {
				return errInvalid("workflow timing is configured in the workflow")
			}
			settings, err := reviewrequests.LoadSettings(r.Context(), tx, a.WorkspaceID, a.Brand)
			if err != nil {
				return err
			}
			dests, err := c.Config.Destinations(settings)
			if err != nil {
				return errInvalid(err.Error())
			}
			// Validate provider/sender/content without sending a test or contacting anyone.
			_, err = s.reviewRequests().Compose(r.Context(), tx, a.WorkspaceID, a.Brand, c.Config, dests, reviewrequests.Person{Email: "person@example.com", Attributes: map[string]any{}}, true)
			if err != nil {
				return err
			}
			if c.Config.Mode == "manual" {
				ids, err := reviewrequests.AudienceIDs(r.Context(), tx, a.WorkspaceID, a.Brand, c.Config.Audience)
				if err != nil {
					return errInvalid(err.Error())
				}
				if len(ids) == 0 {
					return errInvalid("the audience has no customers")
				}
				for _, rec := range ids {
					if _, err = reviewrequests.Add(r.Context(), tx, a.WorkspaceID, a.Brand, c.ID, rec, "launch:"+rec, c.Config, dests); err != nil {
						return err
					}
				}
				at := time.Time{}
				if in.SendAt != nil {
					at = *in.SendAt
				}
				if err = reviewrequests.Enqueue(r.Context(), tx, a.WorkspaceID, a.Brand, c.ID, at); err != nil {
					return err
				}
			}
			status = "active"
		case "pause":
			if status != "active" {
				return errConflict
			}
			status = "paused"
		case "resume":
			if status != "paused" {
				return errConflict
			}
			status = "active"
			at := time.Time{}
			if c.SendAt != nil {
				at = *c.SendAt
			}
			if err = reviewrequests.Enqueue(r.Context(), tx, a.WorkspaceID, a.Brand, c.ID, at); err != nil {
				return err
			}
		case "archive":
			if status == "archived" {
				return errConflict
			}
			status = "archived"
		default:
			return errInvalid("action is launch, pause, resume, archive, or duplicate")
		}
		if in.Action != "duplicate" {
			if _, err = tx.Exec(r.Context(), `UPDATE review_request_campaigns SET status=$4,send_at=CASE WHEN $5='launch' THEN $6 ELSE send_at END,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, c.ID, status, in.Action, in.SendAt); err != nil {
				return err
			}
			if status == "paused" || status == "archived" {
				// Cancel queued email jobs through email state; sends already handed to
				// the provider cannot be recalled. Resume regenerates only these requests.
				if _, err = tx.Exec(r.Context(), `UPDATE emails e SET status='cancelled',status_reason='review_campaign_paused',updated_at=now() FROM review_requests r WHERE r.workspace_id=$1 AND r.brand_id=$2 AND r.campaign_id=$3 AND r.email_id=e.id AND e.status IN ('queued','scheduled')`, a.WorkspaceID, a.Brand, c.ID); err != nil {
					return err
				}
				next := "pending"
				if status == "archived" {
					next = "skipped"
				}
				if _, err = tx.Exec(r.Context(), `UPDATE review_requests r SET status=$4,email_id=NULL,reason=CASE WHEN $4='skipped' THEN 'campaign_archived' ELSE '' END WHERE r.workspace_id=$1 AND r.brand_id=$2 AND r.campaign_id=$3 AND (r.status='pending' OR EXISTS(SELECT 1 FROM emails e WHERE e.id=r.email_id AND e.status='cancelled' AND e.status_reason='review_campaign_paused'))`, a.WorkspaceID, a.Brand, c.ID, next); err != nil {
					return err
				}
			}
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "review_campaign."+map[string]string{"launch": "launched", "pause": "paused", "resume": "resumed", "archive": "archived", "duplicate": "duplicated"}[in.Action], "review_campaign", id, nil)
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, 409, "conflict", "this action is not available for the campaign's current status")
		return
	}
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, 400, "invalid_request", string(inv))
		return
	}
	if !s.composeOK(w, r, err) {
		return
	}
	r.SetPathValue("campaign", id)
	if v, ok := s.findReviewCampaign(w, r); ok {
		writeJSON(w, 200, v)
	}
}
func (s *Server) testReviewRequestCampaign(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	c, ok := s.findReviewCampaign(w, r)
	if !ok {
		return
	}
	var in struct {
		To string `json:"to"`
	}
	if !decode(w, r, &in) {
		return
	}
	var id string
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		settings, err := reviewrequests.LoadSettings(r.Context(), tx, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		dests, err := c.Config.Destinations(settings)
		if err != nil {
			return errInvalid(err.Error())
		}
		e, err := s.reviewRequests().Compose(r.Context(), tx, a.WorkspaceID, a.Brand, c.Config, dests, reviewrequests.Person{Name: "Example customer", Email: strings.TrimSpace(in.To), Attributes: map[string]any{"first_name": "Example"}}, true)
		if err != nil {
			return err
		}
		e.Trigger = "test"
		e.Surface = "web"
		e.TrackClicks = false
		id, err = s.emails.Queue(r.Context(), tx, s.emailWho(r.Context(), a), e)
		return err
	})
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, 400, "invalid_request", string(inv))
		return
	}
	if !s.composeOK(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"email_id": id})
}
