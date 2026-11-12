package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/workspace"
	"github.com/outflareai/outflare/engine/internal/oauth"
)

// Reading the team's mail through the company's Google Workspace: with the
// brand's rollout pointing at a Google Workspace connection (the
// company's own service account with domain-wide delegation), every
// enabled person's mailbox is read without them signing in. Each one is a
// connection row like a person's own, so the same sync, filters, privacy
// rules and eligibility apply; a person who connected Gmail themselves
// keeps their own connection.

// workspaceSubject marks a grant read through the Workspace connection.
const workspaceSubject = "workspace:"

// checkWorkspaceConnection says whether id is a connected Google Workspace
// connection the brand may use.
func (s *Server) checkWorkspaceConnection(ctx context.Context, tx pgx.Tx, ws, brand, id string) error {
	if s.integrations == nil || !validUUID(id) {
		return errInvalid("workspace_integration_id must be a Google Workspace connection")
	}
	i, err := s.integrations.Get(ctx, tx, ws, id)
	if errors.Is(err, connectors.ErrNotFound) {
		return errInvalid("workspace_integration_id must be a Google Workspace connection")
	}
	if err != nil {
		return err
	}
	all, err := s.integrations.List(ctx, tx, ws, "")
	if err != nil {
		return err
	}
	if i.Connector != workspace.ID || i.Status != connectors.StatusConnected || !i.Serves(brand, all) {
		return errInvalid("workspace_integration_id must be a connected Google Workspace connection this brand uses")
	}
	return nil
}

// provisionWorkspaceMailboxes keeps one Workspace-read connection for each
// enabled person of the brand who hasn't connected Gmail themselves, and
// removes the rest. Run it in the rollout's transaction, after saving it.
func (s *Server) provisionWorkspaceMailboxes(ctx context.Context, tx pgx.Tx, ws, brand string, rollout crmMailRollout) (int, error) {
	if rollout.WorkspaceIntegrationID == nil || !rollout.Enabled {
		_, err := tx.Exec(ctx, `DELETE FROM crm_google_connections WHERE workspace_id=$1 AND brand_id=$2 AND workspace_integration_id IS NOT NULL`, ws, brand)
		return 0, err
	}
	id := *rollout.WorkspaceIntegrationID
	if _, err := tx.Exec(ctx, `DELETE FROM crm_google_connections WHERE workspace_id=$1 AND brand_id=$2 AND workspace_integration_id IS NOT NULL AND workspace_integration_id<>$3`, ws, brand, id); err != nil {
		return 0, err
	}
	added := 0
	for _, user := range rollout.Allowed {
		ok, err := eligibleCRMMail(ctx, tx, ws, brand, user)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		var email string
		if err := tx.QueryRow(ctx, `SELECT lower(email) FROM users WHERE id=$1`, user).Scan(&email); err != nil {
			return 0, err
		}
		var connID string
		err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&connID)
		if err != nil {
			return 0, err
		}
		raw, _ := json.Marshal(crmGoogleCredentials{Grant: oauth.PersonalGoogleGrant{Email: email, Subject: workspaceSubject + email}, Generation: 1})
		tag, err := tx.Exec(ctx, `INSERT INTO crm_google_connections(id,workspace_id,brand_id,owner_id,credentials_sealed,workspace_integration_id)VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(brand_id,owner_id) DO NOTHING`,
			connID, ws, brand, user, s.box.Seal(raw, []byte(connID)), id)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		added++
		if err := s.enqueueCRMGoogle(ctx, tx, ws, connID, time.Now(), "workspace"); err != nil {
			return 0, err
		}
		// Their calendar too, through the same connection.
		if err := s.enqueueWorkspaceCalendar(ctx, tx, ws, connID, time.Now(), "start"); err != nil {
			return 0, err
		}
	}
	return added, nil
}

// workspaceMailToken is a read-only Gmail token for a Workspace-read
// connection's owner.
func (s *Server) workspaceMailToken(ctx context.Context, ws, integrationID, email string) (string, error) {
	if s.integrations == nil {
		return "", errors.New("connections are not configured")
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, integrationID)
	if err != nil {
		return "", err
	}
	if o.Connector.Definition().ID != workspace.ID {
		return "", connectors.Errf(connectors.ErrInvalid, "the connection is not a Google Workspace connection")
	}
	return workspace.Token(ctx, o.Client, o.Creds, strings.ToLower(email))
}
