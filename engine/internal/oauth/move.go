package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/mailsignin"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Mailbox sign-ins from before they were connections are moved into the
// google_mail and outlook_mail connectors once, at start-up (MoveLegacy):
//
//   - each oauth_connections row (a Google service account or a Microsoft
//     Entra app) becomes an api_key connection, its mailboxes point at it
//     with integration_id, and the row is deleted;
//   - each mailbox that kept its own refresh token in its sealed
//     credentials becomes an oauth2 connection holding that token (or is
//     linked to the sign-in connection its address already has), and the
//     token leaves the mailbox.
//
// Secrets are opened and sealed again through the connectors store, never
// copied in SQL. Each move is one transaction that locks its row and
// checks it again, so running twice, or on two servers at once, moves
// nothing twice. The oauth_connections table and
// mailboxes.oauth_connection_id are dropped in a later release.

// Move kinds and outcomes, for metrics and events.
const (
	kindAdmin  = "admin"
	kindSignIn = "sign_in"

	movedNew    = "moved"
	movedLinked = "linked"
	movedNone   = "skipped"
	moveFailed  = "failed"
)

// MoveResult counts what MoveLegacy did.
type MoveResult struct {
	Admin   int // admin connections moved
	SignIns int // mailbox sign-ins moved to a connection of their own
	Linked  int // mailbox sign-ins linked to a connection that already existed
	Skipped int // OAuth mailboxes with nothing to move (no refresh token)
	Failed  int
}

// errSkip means the row had nothing to move.
var errSkip = errors.New("nothing to move")

// MoveLegacy moves every older mailbox sign-in into a connection. It
// returns an error only when it could not list them; a row that fails is
// logged, counted and left for the next start.
func MoveLegacy(ctx context.Context, store *connectors.Store, log *slog.Logger) (MoveResult, error) {
	var res MoveResult
	type unit struct{ id, ws string }
	list := func(sql string) ([]unit, error) {
		rows, err := store.Pool.Query(ctx, sql)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(r pgx.CollectableRow) (unit, error) {
			var u unit
			return u, r.Scan(&u.id, &u.ws)
		})
	}
	admins, err := list(`SELECT id::text, workspace_id::text FROM oauth_connections ORDER BY created_at`)
	if err != nil {
		return res, err
	}
	signIns, err := list(`SELECT id::text, workspace_id::text FROM mailboxes
		WHERE auth_method = 'oauth' AND integration_id IS NULL AND oauth_connection_id IS NULL ORDER BY created_at`)
	if err != nil {
		return res, err
	}
	done := func(kind, ws, outcome string, err error) {
		metrics.MailSignInMoved(kind, outcome)
		if outcome == moveFailed {
			res.Failed++
			track.Fail(ctx, log, track.Event{Name: "mailbox.sign_in_move_failed", WorkspaceID: ws, ActorType: "system", Surface: "system",
				Props: map[string]any{"kind": kind}}, "internal", err)
		}
	}
	for _, u := range admins {
		switch err := moveAdmin(ctx, store, u.ws, u.id); {
		case errors.Is(err, errSkip):
			res.Skipped++
			done(kindAdmin, u.ws, movedNone, nil)
		case err != nil:
			done(kindAdmin, u.ws, moveFailed, err)
		default:
			res.Admin++
			done(kindAdmin, u.ws, movedNew, nil)
		}
	}
	for _, u := range signIns {
		linked, err := moveSignIn(ctx, store, u.ws, u.id)
		switch {
		case errors.Is(err, errSkip):
			res.Skipped++
			done(kindSignIn, u.ws, movedNone, nil)
		case err != nil:
			done(kindSignIn, u.ws, moveFailed, err)
		case linked:
			res.Linked++
			done(kindSignIn, u.ws, movedLinked, nil)
		default:
			res.SignIns++
			done(kindSignIn, u.ws, movedNew, nil)
		}
	}
	if res != (MoveResult{}) {
		ev := track.Event{Name: "mailbox.sign_ins_moved", ActorType: "system", Surface: "system",
			Props: map[string]any{"admin": res.Admin, "sign_ins": res.SignIns, "linked": res.Linked, "skipped": res.Skipped, "failed": res.Failed}}
		if res.Failed > 0 {
			ev.Outcome, ev.ErrorCode = track.Error, "internal"
		}
		track.Emit(ctx, ev)
		log.Info("mailbox sign-ins moved into connections", "admin", res.Admin, "sign_ins", res.SignIns, "linked", res.Linked,
			"skipped", res.Skipped, "failed", res.Failed)
	}
	return res, nil
}

// legacySecret is what oauth_connections.secret held, sealed with the
// connection id.
type legacySecret struct {
	ServiceAccount json.RawMessage `json:"service_account"`
	TenantID       string          `json:"tenant_id"`
	ClientID       string          `json:"client_id"`
	ClientSecret   string          `json:"client_secret"`
}

// moveAdmin moves one oauth_connections row into an api_key connection.
func moveAdmin(ctx context.Context, store *connectors.Store, ws, id string) error {
	return pgx.BeginFunc(ctx, store.Pool, func(tx pgx.Tx) error {
		var provider, name string
		var sealed []byte
		err := tx.QueryRow(ctx, `SELECT provider, name, secret FROM oauth_connections WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, id, ws).
			Scan(&provider, &name, &sealed)
		if errors.Is(err, pgx.ErrNoRows) {
			return errSkip // moved by another server meanwhile
		}
		if err != nil {
			return err
		}
		plain, err := store.Box.Open(sealed, []byte(id))
		if err != nil {
			return fmt.Errorf("open oauth connection %s: %w", id, err)
		}
		var sec legacySecret
		if err := json.Unmarshal(plain, &sec); err != nil {
			return fmt.Errorf("read oauth connection %s: %w", id, err)
		}
		var connector string
		var creds connectors.Creds
		var account connectors.Account
		switch provider {
		case "google":
			var sa struct {
				ClientEmail string `json:"client_email"`
			}
			if err := json.Unmarshal(sec.ServiceAccount, &sa); err != nil || sa.ClientEmail == "" {
				return fmt.Errorf("oauth connection %s has no service account key", id)
			}
			connector = mailsignin.GoogleID
			creds = connectors.Creds{"service_account_json": string(sec.ServiceAccount)}
			account = connectors.Account{ID: sa.ClientEmail, Name: sa.ClientEmail, Details: map[string]string{"kind": "service_account"}}
		case "microsoft":
			if sec.TenantID == "" || sec.ClientID == "" || sec.ClientSecret == "" {
				return fmt.Errorf("oauth connection %s has no Entra app", id)
			}
			connector = mailsignin.MicrosoftID
			creds = connectors.Creds{"tenant_id": sec.TenantID, "client_id": sec.ClientID, "client_secret": sec.ClientSecret}
			account = connectors.Account{ID: sec.TenantID, Name: sec.TenantID, Details: map[string]string{"kind": "entra_app", "client_id": sec.ClientID}}
		default:
			return fmt.Errorf("oauth connection %s has unknown provider %q", id, provider)
		}
		i, err := insertConnection(ctx, store, tx, ws, connector, connectors.AuthAPIKey, name, creds, account)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE mailboxes SET integration_id = $3, oauth_connection_id = NULL, updated_at = now()
			WHERE workspace_id = $1 AND oauth_connection_id = $2`, ws, id, i.ID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM oauth_connections WHERE id = $1 AND workspace_id = $2`, id, ws); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "integration.connected",
			TargetType: "integration", TargetID: i.ID,
			Data:  map[string]any{"connector": connector, "auth_method": connectors.AuthAPIKey, "name": i.Name, "via": "moved", "mailboxes": tag.RowsAffected()},
			Props: map[string]any{"connector": connector, "via": "moved", "mailboxes": tag.RowsAffected()}})
	})
}

// moveSignIn moves one mailbox's own refresh token into an oauth2
// connection, or links the mailbox to the sign-in connection its address
// already has. It reports whether it linked.
func moveSignIn(ctx context.Context, store *connectors.Store, ws, id string) (bool, error) {
	var linked bool
	err := pgx.BeginFunc(ctx, store.Pool, func(tx pgx.Tx) error {
		var email, display, provider string
		var sealed []byte
		err := tx.QueryRow(ctx, `SELECT email, display_name, provider, credentials FROM mailboxes
			WHERE id = $1 AND workspace_id = $2 AND auth_method = 'oauth' AND integration_id IS NULL AND oauth_connection_id IS NULL
			FOR UPDATE`, id, ws).Scan(&email, &display, &provider, &sealed)
		if errors.Is(err, pgx.ErrNoRows) {
			return errSkip
		}
		if err != nil {
			return err
		}
		plain, err := store.Box.Open(sealed, []byte(id))
		if err != nil {
			return fmt.Errorf("open mailbox %s credentials: %w", id, err)
		}
		// Kept as a map so whatever else the blob holds survives.
		var stored map[string]any
		if err := json.Unmarshal(plain, &stored); err != nil {
			return fmt.Errorf("read mailbox %s credentials: %w", id, err)
		}
		refresh, _ := stored["refresh_token"].(string)
		if refresh == "" {
			return errSkip
		}
		var connector string
		switch provider {
		case "google":
			connector = mailsignin.GoogleID
		case "microsoft":
			connector = mailsignin.MicrosoftID
		default:
			return fmt.Errorf("mailbox %s signs in with OAuth through unknown provider %q", id, provider)
		}
		email = strings.ToLower(email)
		// One connection per signed-in address, as signing in does.
		var conn string
		err = tx.QueryRow(ctx, `SELECT id::text FROM integrations
			WHERE workspace_id = $1 AND connector = $2 AND auth_method = 'oauth2' AND lower(account->>'id') = $3
			ORDER BY created_at LIMIT 1`, ws, connector, email).Scan(&conn)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// No access token yet: the first sign-in refreshes it.
			creds := connectors.Creds{"refresh_token": refresh}
			def, _ := store.Registry.Get(connector)
			name := email
			if def != nil {
				name = def.Definition().Name + " (" + email + ")"
			}
			i, err := insertConnection(ctx, store, tx, ws, connector, connectors.AuthOAuth2, name, creds,
				connectors.Account{ID: email, Name: display, Email: email})
			if err != nil {
				return err
			}
			conn = i.ID
			if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "integration.connected",
				TargetType: "integration", TargetID: conn,
				Data:  map[string]any{"connector": connector, "auth_method": connectors.AuthOAuth2, "name": i.Name, "via": "moved", "mailbox_id": id},
				Props: map[string]any{"connector": connector, "via": "moved", "mailboxes": 1}}); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			linked = true
		}
		delete(stored, "refresh_token")
		b, err := json.Marshal(stored)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE mailboxes SET integration_id = $3, credentials = $4, updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, id, ws, conn, store.Box.Seal(b, []byte(id))); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "mailbox.updated",
			TargetType: "mailbox", TargetID: id,
			Data:  map[string]any{"changed": []string{"integration_id"}, "via": "moved", "integration_id": conn},
			Props: map[string]any{"via": "moved"}})
	})
	return linked, err
}

// insertConnection saves a moved connection under a name no other
// connection of the workspace has, and runs the connect hooks.
func insertConnection(ctx context.Context, store *connectors.Store, tx pgx.Tx, ws, connector, method, base string, creds connectors.Creds, account connectors.Account) (connectors.Integration, error) {
	if _, ok := store.Registry.Get(connector); !ok {
		return connectors.Integration{}, fmt.Errorf("the %s connector is not available", connector)
	}
	base = strings.TrimSpace(base)
	if base == "" {
		base = account.ID
	}
	name := base
	for n := 2; ; n++ {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM integrations WHERE workspace_id = $1 AND lower(name) = lower($2))`, ws, name).Scan(&taken); err != nil {
			return connectors.Integration{}, err
		}
		if !taken {
			break
		}
		name = base + " " + strconv.Itoa(n)
	}
	i, err := store.Insert(ctx, tx, ws, connector, method, name, creds, account, "")
	if err != nil {
		return i, err
	}
	return i, store.Connected(ctx, tx, i)
}
