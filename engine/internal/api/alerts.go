package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Alerts: what needs someone now, from every module, and who hears about
// it by email.
func (s *Server) routesAlerts(mux *http.ServeMux) {
	read, manage := auth.ScopeWorkspaceRead, auth.ScopeWorkspaceManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/alerts", s.inBrand(read, s.listAlerts))
	mux.HandleFunc("GET /v1/workspaces/{ws}/alerts/settings", s.inBrand(read, s.getAlertSettings))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/alerts/settings", s.inWorkspace(manage, s.setAlertSettings))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/alerts/mutes", s.inBrand(read, s.setAlertMute))
	mux.HandleFunc("GET /v1/workspaces/{ws}/alerts/channels", s.inWorkspace(manage, s.listAlertChannels))
}

type alertView struct {
	alerts.Row
	alerts.View
}

// alertKindsFor are the kinds a caller may see: each kind needs the scope
// of the module it comes from.
func alertKindsFor(a *access) []string {
	var out []string
	for _, k := range alerts.Kinds() {
		if k.Scope == "" || a.has(k.Scope) {
			out = append(out, k.ID)
		}
	}
	return out
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	kinds := alertKindsFor(a)
	if k := r.URL.Query().Get("kind"); k != "" {
		if _, ok := alerts.Find(k); !ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is not a kind of alert; GET .../alerts/settings lists them")
			return
		}
		if !slices.Contains(kinds, k) {
			kinds = nil
		} else {
			kinds = []string{k}
		}
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 200")
			return
		}
		limit = n
	}
	var before *time.Time
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "before must be a time such as 2026-01-31T09:00:00Z")
			return
		}
		before = &t
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM alerts
		WHERE workspace_id = $1 AND kind = ANY ($2) AND ($3::uuid[] IS NULL OR brand_id IS NULL OR brand_id = ANY ($3))
		  AND ($4::timestamptz IS NULL OR created_at < $4)
		ORDER BY created_at DESC LIMIT $5`, a.WorkspaceID, kinds, a.Brands, before, limit+1)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []alertView{}
	for i, id := range ids {
		if i == limit {
			break
		}
		row, err := alerts.Load(ctx, s.pool, a.WorkspaceID, id)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		out = append(out, alertView{Row: row, View: alerts.Describe(ctx, s.pool, row)})
	}
	var next *string
	if len(ids) > limit {
		v := out[len(out)-1].CreatedAt.Format(time.RFC3339Nano)
		next = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_before": next})
}

type alertSetting struct {
	BrandID          *string   `json:"brand_id"`
	Kind             string    `json:"kind"`
	Email            *string   `json:"email"`
	SlackIntegration *string   `json:"slack_integration_id"`
	SlackChannel     *string   `json:"slack_channel"`
	SlackChannelName *string   `json:"slack_channel_name"`
	UpdatedAt        time.Time `json:"updated_at"`
}

const alertSettingCols = `brand_id::text, kind, email, slack_integration_id::text, slack_channel, slack_channel_name, updated_at`

func scanAlertSetting(row pgx.Row) (alertSetting, error) {
	var v alertSetting
	return v, row.Scan(&v.BrandID, &v.Kind, &v.Email, &v.SlackIntegration, &v.SlackChannel, &v.SlackChannelName, &v.UpdatedAt)
}

func (s *Server) getAlertSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	// ?brand= keeps the workspace's settings and that brand's own; none
	// keeps only the workspace's.
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	var picked *string
	if sel.id != "" && sel.id != "none" {
		picked = &sel.id
	}
	rows, err := s.pool.Query(ctx, `SELECT `+alertSettingCols+` FROM alert_settings
		WHERE workspace_id = $1 AND ($2::uuid[] IS NULL OR brand_id IS NULL OR brand_id = ANY ($2))
			AND ($3::uuid IS NULL OR brand_id IS NULL OR brand_id = $3) AND (NOT $4 OR brand_id IS NULL)
		ORDER BY brand_id NULLS FIRST, kind`, a.WorkspaceID, a.Brands, picked, sel.id == "none")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	settings, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (alertSetting, error) { return scanAlertSetting(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	muted := []string{}
	if u := alertUser(a); u != "" {
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(array_agg(kind ORDER BY kind), '{}') FROM alert_mutes WHERE workspace_id = $1 AND user_id = $2`,
			a.WorkspaceID, u).Scan(&muted); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"kinds": alerts.Kinds(), "default_email": alerts.ToOwner,
		"daily_email_limit": alerts.DailyCap, "settings": settings, "muted": muted})
}

// optString is a JSON field that can be left out, set, or set to null.
type optString struct {
	Set   bool
	Value *string
}

func (o *optString) UnmarshalJSON(b []byte) error {
	o.Set = true
	return json.Unmarshal(b, &o.Value)
}

func (s *Server) setAlertSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		BrandID          *string   `json:"brand_id"`
		Kind             string    `json:"kind"`
		Email            optString `json:"email"`
		SlackIntegration optString `json:"slack_integration_id"`
		SlackChannel     optString `json:"slack_channel"`
	}
	if !decode(w, r, &req) {
		return
	}
	if _, ok := alerts.Find(req.Kind); !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is not a kind of alert; GET .../alerts/settings lists them")
		return
	}
	if req.Email.Value != nil && !slices.Contains([]string{alerts.ToOwner, alerts.ToAdmins, alerts.ToOff}, *req.Email.Value) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "email must be owner, admins or off, or null to follow the workspace")
		return
	}
	if req.SlackIntegration.Set != req.SlackChannel.Set || (req.SlackIntegration.Value == nil) != (req.SlackChannel.Value == nil) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send slack_integration_id and slack_channel together, or both null to stop posting")
		return
	}
	if !req.Email.Set && !req.SlackIntegration.Set {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send email, or slack_integration_id and slack_channel")
		return
	}
	ctx := r.Context()
	var channelName *string
	if req.SlackIntegration.Value != nil {
		o, ok := s.openNotifier(w, r, a, *req.SlackIntegration.Value)
		if !ok {
			return
		}
		list, err := o.n.Channels(ctx, o.Client, o.Creds)
		if err != nil {
			s.connectorFailed(w, r, err)
			return
		}
		i := slices.IndexFunc(list, func(c connectors.Channel) bool { return c.ID == *req.SlackChannel.Value })
		if i < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "slack_channel is not a channel this Slack connection can see; GET .../alerts/channels lists them")
			return
		}
		channelName = &list[i].Name
	}
	var out *alertSetting
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if req.BrandID != nil {
			var ok bool
			if validUUID(*req.BrandID) {
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM brands WHERE id = $1 AND workspace_id = $2)`, *req.BrandID, a.WorkspaceID).Scan(&ok); err != nil {
					return err
				}
			}
			if !ok {
				return errInvalid("brand_id is not a brand in this workspace")
			}
		}
		match := `workspace_id = $1 AND brand_id IS NOT DISTINCT FROM $2::uuid AND kind = $3`
		var actor *string
		if a.ActorID != "" {
			actor = &a.ActorID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO alert_settings (workspace_id, brand_id, kind, updated_by)
			SELECT $1, $2, $3, $4 WHERE NOT EXISTS (SELECT 1 FROM alert_settings WHERE `+match+`)`,
			a.WorkspaceID, req.BrandID, req.Kind, actor); err != nil {
			return err
		}
		v, err := scanAlertSetting(tx.QueryRow(ctx, `UPDATE alert_settings SET
				email = CASE WHEN $4 THEN $5 ELSE email END,
				slack_integration_id = CASE WHEN $6 THEN $7::uuid ELSE slack_integration_id END,
				slack_channel = CASE WHEN $6 THEN $8 ELSE slack_channel END,
				slack_channel_name = CASE WHEN $6 THEN $9 ELSE slack_channel_name END,
				updated_by = $10, updated_at = now()
			WHERE `+match+` RETURNING `+alertSettingCols,
			a.WorkspaceID, req.BrandID, req.Kind, req.Email.Set, req.Email.Value,
			req.SlackIntegration.Set, req.SlackIntegration.Value, req.SlackChannel.Value, channelName, actor))
		if err != nil {
			return err
		}
		data := map[string]any{"brand_id": req.BrandID, "kind": req.Kind, "email": v.Email, "slack": v.SlackIntegration != nil}
		// Nothing chosen any more: the row goes, so it follows the next one up.
		if v.Email == nil && v.SlackIntegration == nil {
			if _, err := tx.Exec(ctx, `DELETE FROM alert_settings WHERE `+match, a.WorkspaceID, req.BrandID, req.Kind); err != nil {
				return err
			}
		} else {
			out = &v
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "alert_settings.updated", "workspace", a.WorkspaceID, data)
	})
	var inv errInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	case err != nil:
		s.internal(w, r, err)
	case out == nil:
		writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
	default:
		writeJSON(w, http.StatusOK, out)
	}
}

type openedNotifier struct {
	*connectors.Opened
	n connectors.Notifier
}

// openNotifier opens one of the workspace's connections that can post
// alerts, or answers why not.
func (s *Server) openNotifier(w http.ResponseWriter, r *http.Request, a *access, id string) (openedNotifier, bool) {
	if !validUUID(id) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "slack_integration_id is not a connection in this workspace")
		return openedNotifier{}, false
	}
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, id)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "slack_integration_id is not a connection in this workspace")
		return openedNotifier{}, false
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return openedNotifier{}, false
	}
	n, ok := o.Connector.(connectors.Notifier)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "that connection can't post alerts; connect Slack under Connectors")
		return openedNotifier{}, false
	}
	return openedNotifier{o, n}, true
}

// listAlertChannels lists where a chat connection can post.
func (s *Server) listAlertChannels(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	o, ok := s.openNotifier(w, r, a, r.URL.Query().Get("integration_id"))
	if !ok {
		return
	}
	list, err := o.n.Channels(r.Context(), o.Client, o.Creds)
	_ = s.integrations.Checked(r.Context(), s.pool, a.WorkspaceID, o.ID, nil, err)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	if list == nil {
		list = []connectors.Channel{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// alertUser is the person a mute is for: the signed-in user, or who made
// the token.
func alertUser(a *access) string {
	if a.UserID != "" {
		return a.UserID
	}
	return a.TokenOwnerID
}

func (s *Server) setAlertMute(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Kind  string `json:"kind"`
		Muted bool   `json:"muted"`
	}
	if !decode(w, r, &req) {
		return
	}
	if _, ok := alerts.Find(req.Kind); !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is not a kind of alert; GET .../alerts/settings lists them")
		return
	}
	user := alertUser(a)
	if user == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "alerts are muted for a person; this token has no person behind it")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q, action := `DELETE FROM alert_mutes WHERE workspace_id = $1 AND user_id = $2 AND kind = $3`, "alert.unmuted"
		if req.Muted {
			q, action = `INSERT INTO alert_mutes (workspace_id, user_id, kind) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, "alert.muted"
		}
		tag, err := tx.Exec(ctx, q, a.WorkspaceID, user, req.Kind)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "user", user, map[string]any{"kind": req.Kind})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"kind": req.Kind, "muted": req.Muted})
}
