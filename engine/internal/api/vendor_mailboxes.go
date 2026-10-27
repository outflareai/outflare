package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/vendorsync"
)

// Mailbox suppliers: the mailboxes held at a connected supplier account
// (Cheap Inboxes and the like), importing them, and the supplier's webhook.
func (s *Server) routesVendorMailboxes(mux *http.ServeMux) {
	read, manage := auth.ScopeMailboxesRead, auth.ScopeMailboxesManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/integrations/{id}/mailboxes", s.inWorkspace(read, s.listVendorMailboxes))
	mux.HandleFunc("POST /v1/workspaces/{ws}/integrations/{id}/mailboxes/sync", s.inWorkspace(manage, s.syncVendorMailboxes))
	mux.HandleFunc("POST /v1/workspaces/{ws}/integrations/{id}/mailboxes/import", s.inWorkspace(manage, s.importVendorMailboxes))
	mux.HandleFunc("POST /v1/workspaces/{ws}/integrations/{id}/mailboxes/push", s.inWorkspace(manage, s.pushVendorMailboxes))
	mux.HandleFunc("GET /v1/workspaces/{ws}/integrations/{id}/mailbox-sync", s.inWorkspace(read, s.getVendorSync))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/integrations/{id}/mailbox-sync", s.inWorkspace(manage, s.updateVendorSync))
	mux.HandleFunc("POST /v1/integrations/{id}/webhook", s.vendorWebhook)
}

// loadVendor finds a supplier integration and makes sure it is syncing.
func (s *Server) loadVendor(w http.ResponseWriter, r *http.Request) (connectors.Integration, bool) {
	if s.vendors == nil || !s.needIntegrations(w, r) {
		if s.vendors == nil {
			writeError(w, r, http.StatusServiceUnavailable, "unavailable", "mailbox suppliers are not configured on this server")
		}
		return connectors.Integration{}, false
	}
	i, ok := s.loadIntegration(w, r)
	if !ok {
		return i, false
	}
	if !s.vendors.IsVendor(i.Connector) {
		writeError(w, r, http.StatusUnprocessableEntity, "not_a_supplier", "this integration is not a mailbox supplier, so it has no mailboxes to import")
		return i, false
	}
	// Integrations connected before this feature, or by an older engine,
	// start syncing on first use.
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		_, err := s.vendors.Ensure(r.Context(), tx, i.WorkspaceID, i.ID)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return i, false
	}
	return i, true
}

type vendorMailboxList struct {
	Integration connectors.Integration `json:"integration"`
	Sync        vendorsync.Settings    `json:"sync"`
	// Synced is false until the first sync has finished.
	Synced    bool                 `json:"synced"`
	Counts    vendorsync.Counts    `json:"counts"`
	Total     int                  `json:"total"`
	Mailboxes []vendorsync.Mailbox `json:"mailboxes"`
	// The next page's offset and cursor; null on the last page.
	NextOffset *int `json:"next_offset"`
	NextCursor any  `json:"next_cursor"`
}

func (s *Server) listVendorMailboxes(w http.ResponseWriter, r *http.Request) {
	i, ok := s.loadVendor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := vendorsync.Filter{ImportState: q.Get("import_state"), Status: q.Get("status"), Query: q.Get("q")}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	if f.Offset < 0 {
		f.Offset = 0
	}
	st, err := s.vendors.GetSettings(r.Context(), s.pool, i.WorkspaceID, i.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, total, counts, err := s.vendors.List(r.Context(), s.pool, i.WorkspaceID, i.ID, f)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := vendorMailboxList{Integration: i, Sync: st, Synced: st.LastSyncedAt != nil, Counts: counts, Total: total, Mailboxes: list}
	if n := f.Offset + len(list); len(list) > 0 && n < total {
		out.NextOffset = &n
		out.NextCursor = cursorFor("offset", n)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) syncVendorMailboxes(w http.ResponseWriter, r *http.Request) {
	i, ok := s.loadVendor(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if err := s.vendors.SyncNow(r.Context(), tx, i.WorkspaceID, i.ID); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, i.WorkspaceID, "integration.sync_requested", "integration", i.ID,
			map[string]any{"connector": i.Connector})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

type importVendorRequest struct {
	ExternalIDs []string `json:"external_ids"`
	Emails      []string `json:"emails"`
	All         bool     `json:"all"`
}

func (s *Server) importVendorMailboxes(w http.ResponseWriter, r *http.Request) {
	i, ok := s.loadVendor(w, r)
	if !ok {
		return
	}
	var req importVendorRequest
	if !decode(w, r, &req) {
		return
	}
	if !req.All && len(req.ExternalIDs) == 0 && len(req.Emails) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name mailboxes in external_ids or emails, or set all to true")
		return
	}
	if len(req.ExternalIDs)+len(req.Emails) > 5000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "import at most 5,000 mailboxes per request")
		return
	}
	a := accessFrom(r.Context())
	var out vendorsync.Queued
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = s.vendors.Queue(r.Context(), tx, i.WorkspaceID, i.ID, vendorsync.Request{
			ExternalIDs: req.ExternalIDs, Emails: req.Emails, All: req.All})
		if err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, i.WorkspaceID, "integration.mailboxes_import_requested", "integration", i.ID,
			map[string]any{"connector": i.Connector, "all": req.All, "queued": out.Queued, "waiting": out.Waiting})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

func (s *Server) getVendorSync(w http.ResponseWriter, r *http.Request) {
	i, ok := s.loadVendor(w, r)
	if !ok {
		return
	}
	st, err := s.vendors.GetSettings(r.Context(), s.pool, i.WorkspaceID, i.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type vendorSyncPatch struct {
	AutoImport   *bool         `json:"auto_import"`
	AppPasswords *bool         `json:"app_passwords"`
	DailyLimit   optional[int] `json:"daily_limit"`
	Push         *bool         `json:"push"`
}

func (s *Server) updateVendorSync(w http.ResponseWriter, r *http.Request) {
	i, ok := s.loadVendor(w, r)
	if !ok {
		return
	}
	var req vendorSyncPatch
	if !decode(w, r, &req) {
		return
	}
	c := vendorsync.Change{AutoImport: req.AutoImport, AppPasswords: req.AppPasswords}
	if req.DailyLimit.Set {
		c.SetLimit = true
		c.DailyLimit = req.DailyLimit.Value
		if c.DailyLimit != nil && (*c.DailyLimit < 1 || *c.DailyLimit > 500) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "daily_limit must be between 1 and 500, or null for the supplier's limit")
			return
		}
	}
	a := accessFrom(r.Context())
	if req.Push != nil && *req.Push {
		// The supplier gets a token that manages mailboxes, and a connection
		// to an outside service is made: the caller must be able to do both.
		for _, sc := range pushScopes {
			if !a.has(sc) {
				writeError(w, r, http.StatusForbidden, "forbidden", "turning on push needs the "+sc+" scope")
				return
			}
		}
		if !a.has(auth.ScopeIntegrationsManage) {
			writeError(w, r, http.StatusForbidden, "forbidden", "turning on push needs the integrations.manage scope")
			return
		}
	}
	var st vendorsync.Settings
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if req.Push != nil {
			if err := s.setPush(r.Context(), tx, a, i, *req.Push); err != nil {
				return err
			}
		}
		if st, err = s.vendors.UpdateSettings(r.Context(), tx, i.WorkspaceID, i.ID, c); err != nil {
			return err
		}
		data := map[string]any{"connector": i.Connector}
		if req.AutoImport != nil {
			data["auto_import"] = *req.AutoImport
		}
		if req.AppPasswords != nil {
			data["app_passwords"] = *req.AppPasswords
		}
		if req.DailyLimit.Set {
			data["daily_limit"] = req.DailyLimit.Value
		}
		if req.Push != nil {
			data["push"] = *req.Push
		}
		return record(r.Context(), tx, &a.principal, i.WorkspaceID, "integration.mailbox_sync_updated", "integration", i.ID, data)
	})
	if s.pushError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// pushScopes are what the supplier's push token may do: read and add
// mailboxes through the EmailBison-compatible API, nothing else.
var pushScopes = []string{auth.ScopeMailboxesRead, auth.ScopeMailboxesManage}

// setPush turns push on (minting the supplier's token) or off.
func (s *Server) setPush(ctx context.Context, tx pgx.Tx, a *access, i connectors.Integration, on bool) error {
	if !on {
		return s.vendors.DisablePush(ctx, tx, i.WorkspaceID, i.ID)
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT push_state FROM vendor_syncs WHERE workspace_id = $1 AND integration_id = $2`,
		i.WorkspaceID, i.ID).Scan(&state); err != nil {
		return err
	}
	if state == vendorsync.PushOn || state == vendorsync.PushConnecting {
		return nil // already on
	}
	name := "Push from " + i.Connector
	if c, ok := s.integrations.Registry.Get(i.Connector); ok {
		name = "Push from " + c.Definition().Name
	}
	var actorID string
	if err := tx.QueryRow(ctx, `INSERT INTO actors (workspace_id, type, display_name) VALUES ($1, 'api_token', $2) RETURNING id`,
		i.WorkspaceID, name).Scan(&actorID); err != nil {
		return err
	}
	secret, hash := auth.NewToken(auth.PrefixAPIToken)
	var createdBy *string
	if a.UserID != "" {
		createdBy = &a.UserID
	}
	var tokenID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO api_tokens (workspace_id, actor_id, name, prefix, token_hash, scopes, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		i.WorkspaceID, actorID, name, secret[:len(auth.PrefixAPIToken)+6], hash, pushScopes, createdBy).Scan(&tokenID); err != nil {
		return err
	}
	if err := record(ctx, tx, &a.principal, i.WorkspaceID, "token.created", "api_token", tokenID,
		map[string]any{"name": name, "scopes": pushScopes, "for": "supplier_push"}); err != nil {
		return err
	}
	// The supplier's sequencer login wants an email address: the person's,
	// or for a token the person who made it.
	var email string
	if createdBy != nil {
		_ = tx.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, *createdBy).Scan(&email)
	}
	return s.vendors.EnablePush(ctx, tx, i.WorkspaceID, i.ID, tokenID, secret, email)
}

// pushError answers push failures; it reports whether it wrote one.
func (s *Server) pushError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, vendorsync.ErrPushUnavailable):
		writeError(w, r, http.StatusUnprocessableEntity, "push_unavailable", err.Error())
	case errors.Is(err, vendorsync.ErrPushBusy):
		writeError(w, r, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, vendorsync.ErrPushNotOn):
		writeError(w, r, http.StatusConflict, "push_not_on", err.Error())
	default:
		s.internal(w, r, err)
	}
	return true
}

type pushVendorRequest struct {
	ExternalIDs []string `json:"external_ids"`
	All         bool     `json:"all"`
}

// pushVendorMailboxes asks the supplier to push mailboxes in through the
// push connection.
func (s *Server) pushVendorMailboxes(w http.ResponseWriter, r *http.Request) {
	i, ok := s.loadVendor(w, r)
	if !ok {
		return
	}
	var req pushVendorRequest
	if !decode(w, r, &req) {
		return
	}
	if !req.All && len(req.ExternalIDs) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name mailboxes in external_ids, or set all to true")
		return
	}
	if len(req.ExternalIDs) > 5000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "push at most 5,000 mailboxes per request")
		return
	}
	a := accessFrom(r.Context())
	var n int
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if n, err = s.vendors.PushNow(r.Context(), tx, i.WorkspaceID, i.ID, req.All, req.ExternalIDs); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, i.WorkspaceID, "integration.mailboxes_push_requested", "integration", i.ID,
			map[string]any{"connector": i.Connector, "all": req.All, "mailboxes": n})
	})
	if s.pushError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "mailboxes": n})
}

// vendorWebhook receives a supplier's events. It is not authenticated by
// session or token: the body's signature, keyed with the secret the
// supplier gave us, is the proof.
func (s *Server) vendorWebhook(w http.ResponseWriter, r *http.Request) {
	if s.vendors == nil || !validUUID(r.PathValue("id")) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such webhook")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "body too large")
		return
	}
	err = s.vendors.HandleWebhook(r.Context(), r.PathValue("id"), r.Header, body)
	switch {
	case errors.Is(err, connectors.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "no such webhook")
	case errors.Is(err, vendorsync.ErrBadSignature):
		writeError(w, r, http.StatusUnauthorized, "bad_signature", "the signature does not match")
	case connectors.KindOf(err) == connectors.ErrInvalid:
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
