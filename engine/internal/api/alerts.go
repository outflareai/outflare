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
	// An alert belongs to the brand of what raised it: listed under the
	// brand, and under the workspace until the contract step
	// (brand_activation.go). A brand's settings override the workspace's
	// defaults; mutes are each brand's, and chat channels the team's.
	for _, b := range s.activationBases("alerts", s.inBrand) {
		mux.HandleFunc("GET "+b.path, b.on(read, s.listAlerts))
		mux.HandleFunc("GET "+b.path+"/settings", b.on(read, s.getAlertSettings))
	}
	mux.HandleFunc("PUT /v1/brands/{brand}/alerts/settings", s.onBrand(manage, s.setAlertSettings))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/alerts/settings", s.inWorkspace(manage, s.setAlertSettings))
	mux.HandleFunc("PUT /v1/brands/{brand}/alerts/mutes", s.onBrand(read, s.setAlertMute))
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
	if v, ok := pageCursor(w, r, pageBefore); !ok {
		return
	} else if v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			invalidCursor(w, r)
			return
		}
		before = &t
	}
	// One brand's alerts: the route's, or on the workspace route the one
	// ?brand= picks, else the first brand's.
	sel, ok := s.dataBrand(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, kinds, before, limit + 1}
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM alerts
		WHERE workspace_id = $1 AND kind = ANY ($2) AND ($3::timestamptz IS NULL OR created_at < $3)`+sel.sql("brand_id", &args)+`
		ORDER BY created_at DESC LIMIT $4`, args...)
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
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nextCursor(pageBefore, next)})
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
	// The settings are one brand's: the route's, else ?brand=, else the
	// workspace's first. Brands share none.
	brand, ok := s.settingsBrand(w, r)
	if !ok {
		return
	}
	var picked *string
	if brand != "" {
		picked = &brand
	}
	rows, err := s.pool.Query(ctx, `SELECT `+alertSettingCols+` FROM alert_settings
		WHERE workspace_id = $1 AND brand_id IS NOT DISTINCT FROM $2::uuid
		ORDER BY kind`, a.WorkspaceID, picked)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	settings, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (alertSetting, error) { return scanAlertSetting(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// Mutes are the caller's for one brand: the one picked, else the
	// workspace's first.
	muted := []string{}
	if u := alertUser(a); u != "" {
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(array_agg(kind ORDER BY kind), '{}') FROM brand_alert_mutes
			WHERE workspace_id = $1 AND brand_id::text = $2 AND user_id = $3`, a.WorkspaceID, brand, u).Scan(&muted); err != nil {
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
	// The setting is one brand's own: the route's, else brand_id, else the
	// workspace's first. Brands share none.
	if a.Brand != "" {
		if req.BrandID != nil && *req.BrandID != a.Brand {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_id can only be this route's brand")
			return
		}
		req.BrandID = &a.Brand
	}
	if req.BrandID == nil {
		b, ok := s.settingsBrand(w, r)
		if !ok {
			return
		}
		if b != "" {
			req.BrandID = &b
		}
	}
	if req.Email.Value != nil && !slices.Contains([]string{alerts.ToOwner, alerts.ToAdmins, alerts.ToOff}, *req.Email.Value) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "email must be owner, admins or off, or null for the default (the owner)")
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
		// Nothing chosen any more: the row goes, so the default applies.
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
	// A mute is for one brand: the route's, else ?brand=, else the
	// workspace's first.
	brand, ok := s.settingsBrand(w, r)
	if !ok {
		return
	}
	if brand == "" {
		writeError(w, r, http.StatusConflict, "conflict", "the workspace has no brand yet: add one first")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q, action := `DELETE FROM brand_alert_mutes WHERE workspace_id = $1 AND user_id = $2 AND kind = $3 AND brand_id = $4`, "alert.unmuted"
		if req.Muted {
			q, action = `INSERT INTO brand_alert_mutes (workspace_id, user_id, kind, brand_id) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, "alert.muted"
		}
		tag, err := tx.Exec(ctx, q, a.WorkspaceID, user, req.Kind, brand)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "user", user, map[string]any{"kind": req.Kind, "brand_id": brand})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"kind": req.Kind, "muted": req.Muted})
}
