package api

import (
	"context"
	"errors"
	"github.com/outflareai/outflare/engine/internal/quota"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/mailbox"
)

// Mailbox sign-ins are connections like any other: the google_mail and
// outlook_mail connectors (engine/internal/connectors/mailsignin). Signing
// in through the connector OAuth flow connects one mailbox; an admin
// connection (a service account or an Entra app, connected with its
// fields) adds any mailbox of its organisation with
// POST /integrations/{id}/mailboxes. A mailbox points at its connection
// with mailboxes.integration_id and gets its XOAUTH2 token from it
// (oauth.Tokens).

func (s *Server) routesMailSignIn(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/workspaces/{ws}/integrations/{id}/mailboxes", s.inWorkspace(auth.ScopeMailboxesManage, s.addIntegrationMailboxes))
}

// mailSignIn is the connector's mailbox sign-in, when it is one.
func (s *Server) mailSignIn(connector string) (connectors.MailSignIn, bool) {
	if s.integrations == nil {
		return nil, false
	}
	c, ok := s.integrations.Registry.Get(connector)
	if !ok {
		return nil, false
	}
	ms, ok := c.(connectors.MailSignIn)
	return ms, ok
}

// mailboxRefusal says why the caller may not add mailboxes, or "".
// Mailboxes belong to the whole workspace.
func mailboxRefusal(a *access) string {
	if !a.has(auth.ScopeMailboxesManage) {
		return "adding mailboxes needs the mailboxes.manage scope"
	}
	if a.Brands != nil {
		return "adding mailboxes needs access to every brand"
	}
	return ""
}

// errMailboxesUseIt refuses removing a connection mailboxes sign in through.
var errMailboxesUseIt = errors.New("remove the mailboxes that sign in through this connection first")

// mailboxesUsing counts the mailboxes that sign in through a connection.
func mailboxesUsing(ctx context.Context, q pgx.Tx, ws, id string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM mailboxes WHERE workspace_id = $1 AND integration_id = $2`, ws, id).Scan(&n)
	return n, err
}

// finishMailSignIn saves a mailbox sign-in and adds its mailbox. Signing
// in again to a mailbox that already has a connection replaces that
// connection's credentials instead of making a second one. It returns the
// connection id, or a message for the person when the mailbox could not
// be added.
func (s *Server) finishMailSignIn(ctx context.Context, st integrationState, def connectors.Definition, ms connectors.MailSignIn, creds connectors.Creds, account connectors.Account, actorType, name string) (string, string, error) {
	p := &principal{ActorID: st.Actor, ActorType: actorType}
	email := strings.ToLower(account.Email)
	var id string
	err := s.pool.QueryRow(ctx, `SELECT id::text FROM integrations
		WHERE workspace_id = $1 AND connector = $2 AND auth_method = 'oauth2' AND lower(account->>'id') = $3
		ORDER BY created_at LIMIT 1`, st.Workspace, def.ID, email).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		out, err := s.saveIntegration(ctx, p, st.Workspace, def, connectors.AuthOAuth2, name, creds, account, "oauth", st.Request, st.share())
		if err != nil {
			return "", "", err
		}
		id = out.ID
	case err != nil:
		return "", "", err
	default:
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if err := s.integrations.SetCreds(ctx, tx, st.Workspace, id, connectors.AuthOAuth2, creds); err != nil {
				return err
			}
			if err := s.integrations.Checked(ctx, tx, st.Workspace, id, &account, nil); err != nil {
				return err
			}
			i, err := s.integrations.Get(ctx, tx, st.Workspace, id)
			if err != nil {
				return err
			}
			if err := s.integrations.Reconnected(ctx, tx, i); err != nil {
				return err
			}
			return record(ctx, tx, p, st.Workspace, "integration.updated", "integration", id,
				map[string]any{"changed": []string{"credentials"}, "via": "oauth"})
		})
		if err != nil {
			return "", "", err
		}
	}
	a := &access{principal: *p, WorkspaceID: st.Workspace}
	if a.Test, err = mailbox.IsTestWorkspace(ctx, s.pool, st.Workspace); err != nil {
		return "", "", err
	}
	row := mailbox.Row{Email: email, DisplayName: account.Name, Provider: ms.MailProvider(), SMTPUsername: email, IMAPUsername: email,
		Pool: st.Pool, IntegrationID: &id}
	// The sign-in stays connected, but the mailbox isn't added while the
	// workspace may have no more (package quota): the reason is shown, and
	// signing in again adds it once it may.
	if err := quota.Check(ctx, s.quota, s.log, st.Workspace, quota.Mailboxes, 1); err != nil {
		return id, err.Error(), nil
	}
	res, err := s.importRows(ctx, a, "oauth", []mailbox.Row{row}, nil)
	if err != nil {
		return "", "", err
	}
	if len(res.Errors) > 0 {
		return id, res.Errors[0].Error, nil
	}
	return id, "", nil
}

// maxConnectionMailboxes bounds one bulk add.
const maxConnectionMailboxes = 5000

// addIntegrationMailboxes adds mailboxes that sign in through an admin
// connection: a Google service account or a Microsoft Entra app.
func (s *Server) addIntegrationMailboxes(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.needIntegrations(w, r) {
		return
	}
	if s.mail == nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "mailboxes are not configured on this server")
		return
	}
	var req struct {
		Mailboxes []struct {
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
		} `json:"mailboxes"`
		Pool       string `json:"pool"`
		DailyLimit int    `json:"daily_limit"`
	}
	if !decodeLarge(w, r, &req, maxImportBytes) {
		return
	}
	if len(req.Mailboxes) == 0 || len(req.Mailboxes) > maxConnectionMailboxes {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send 1 to 5000 mailboxes")
		return
	}
	if req.DailyLimit != 0 && (req.DailyLimit < 1 || req.DailyLimit > 500) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "daily_limit must be 1 to 500")
		return
	}
	i, ok := s.loadIntegration(w, r)
	if !ok {
		return
	}
	ms, ok := s.mailSignIn(i.Connector)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "this connection does not sign mailboxes in; connect Gmail or Outlook")
		return
	}
	if i.AuthMethod != connectors.AuthAPIKey {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a sign-in connects only its own mailbox; sign in to each mailbox, or connect a service account or Entra app to add many")
		return
	}
	var rows []mailbox.Row
	var bad []mailbox.RowError
	for n, m := range req.Mailboxes {
		email, ok := validEmail(m.Email)
		if !ok {
			bad = append(bad, mailbox.RowError{Line: n + 1, Email: m.Email, Error: "invalid email"})
			continue
		}
		email = strings.ToLower(email)
		rows = append(rows, mailbox.Row{
			Line: n + 1, Email: email, DisplayName: strings.TrimSpace(m.DisplayName), Provider: ms.MailProvider(),
			SMTPUsername: email, IMAPUsername: email, Pool: strings.TrimSpace(req.Pool), DailyLimit: req.DailyLimit,
			IntegrationID: &i.ID,
		})
	}
	s.addMailboxes(w, r, a, "integration", rows, bad, func(res mailbox.ImportResult) {
		writeJSON(w, http.StatusOK, res)
	})
}
