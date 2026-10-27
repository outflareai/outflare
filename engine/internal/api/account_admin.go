package api

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Account administration for operators of a server, such as the hosted
// Cloud's staff console: sending account emails on someone's behalf, and
// deleting a person or a workspace with everything that belongs to them.
// The engine owns its schema, so the rules for what a deletion removes live
// here and its tests keep them right as tables are added.

// SendPasswordReset emails userID a link to choose a new password, the same
// email as "Forgot password". It returns the address it went to.
func (a *Accounts) SendPasswordReset(ctx context.Context, userID string) (string, error) {
	var email, secret string
	err := pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT email FROM users WHERE id::text = $1 AND disabled_at IS NULL`, userID).Scan(&email); err != nil {
			return err
		}
		var err error
		secret, err = issueLink(ctx, tx, userID, email, "reset_password")
		return err
	})
	if err != nil {
		return "", err
	}
	return email, sendAccountEmail(ctx, a.Mailer, a.Log, resetMessage(strings.TrimRight(a.PublicURL, "/"), email, secret))
}

// UserDeletion says what deleting a person does to their workspaces.
type UserDeletion struct {
	// Deleted are workspaces nobody else is a member of. They go with
	// everything in them: mailboxes, leads, sequences, messages, connections.
	Deleted []DeletionWorkspace `json:"workspaces_deleted"`
	// Kept are workspaces shared with others. The person leaves them; if
	// they were the only owner, NewOwner (the longest-standing admin, else
	// the longest-standing member) becomes the owner.
	Kept []DeletionWorkspace `json:"workspaces_kept"`
}

// DeletionWorkspace is one workspace in a UserDeletion.
type DeletionWorkspace struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	NewOwner string `json:"new_owner_email,omitempty"`
}

// PlanUserDeletion works out what DeleteUser would do, without changing
// anything. It returns pgx.ErrNoRows when there is no such user.
func (a *Accounts) PlanUserDeletion(ctx context.Context, tx pgx.Tx, userID string) (UserDeletion, error) {
	plan, _, err := planUserDeletion(ctx, tx, userID)
	return plan, err
}

type promotion struct{ workspaceID, userID string }

func planUserDeletion(ctx context.Context, tx pgx.Tx, userID string) (UserDeletion, []promotion, error) {
	plan := UserDeletion{Deleted: []DeletionWorkspace{}, Kept: []DeletionWorkspace{}}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id::text = $1)`, userID).Scan(&exists); err != nil {
		return plan, nil, err
	}
	if !exists {
		return plan, nil, pgx.ErrNoRows
	}
	rows, err := tx.Query(ctx, `
		SELECT w.id, w.name,
			(SELECT count(*) FROM memberships o WHERE o.workspace_id = w.id AND o.user_id <> m.user_id),
			m.role = 'owner' AND NOT EXISTS (SELECT 1 FROM memberships o WHERE o.workspace_id = w.id AND o.user_id <> m.user_id AND o.role = 'owner'),
			(SELECT o.user_id::text FROM memberships o WHERE o.workspace_id = w.id AND o.user_id <> m.user_id
			 ORDER BY o.role = 'admin' DESC, o.created_at, o.user_id LIMIT 1),
			(SELECT u.email FROM memberships o JOIN users u ON u.id = o.user_id WHERE o.workspace_id = w.id AND o.user_id <> m.user_id
			 ORDER BY o.role = 'admin' DESC, o.created_at, o.user_id LIMIT 1)
		FROM memberships m JOIN workspaces w ON w.id = m.workspace_id
		WHERE m.user_id::text = $1
		ORDER BY w.created_at`, userID)
	if err != nil {
		return plan, nil, err
	}
	defer rows.Close()
	var promote []promotion
	for rows.Next() {
		var ws DeletionWorkspace
		var others int
		var soleOwner bool
		var heirID, heirEmail *string
		if err := rows.Scan(&ws.ID, &ws.Name, &others, &soleOwner, &heirID, &heirEmail); err != nil {
			return plan, nil, err
		}
		switch {
		case others == 0:
			plan.Deleted = append(plan.Deleted, ws)
		case soleOwner && heirID != nil:
			ws.NewOwner = *heirEmail
			promote = append(promote, promotion{ws.ID, *heirID})
			plan.Kept = append(plan.Kept, ws)
		default:
			plan.Kept = append(plan.Kept, ws)
		}
	}
	return plan, promote, rows.Err()
}

// DeleteUser deletes a person for good, in tx: their workspaces that nobody
// else uses, their sign-ins (sessions, passwords, Google, passkeys), their
// memberships and pending account emails, and agencies left with no
// members. In shared workspaces their past actions stay but are shown as
// "Deleted user", and an owner is promoted if they were the only one.
func (a *Accounts) DeleteUser(ctx context.Context, tx pgx.Tx, userID string) (UserDeletion, error) {
	plan, promote, err := planUserDeletion(ctx, tx, userID)
	if err != nil {
		return plan, err
	}
	for _, ws := range plan.Deleted {
		if err := deleteWorkspace(ctx, tx, ws.ID); err != nil {
			return plan, err
		}
	}
	for _, p := range promote {
		if _, err := tx.Exec(ctx, `UPDATE memberships SET role = 'owner' WHERE workspace_id = $1 AND user_id = $2`, p.workspaceID, p.userID); err != nil {
			return plan, err
		}
		if err := record(ctx, tx, nil, p.workspaceID, "member.promoted", "user", p.userID,
			map[string]any{"role": "owner", "reason": "previous owner's account was deleted"}); err != nil {
			return plan, err
		}
	}
	var actorID string
	if err := tx.QueryRow(ctx, `DELETE FROM users WHERE id::text = $1 RETURNING actor_id`, userID).Scan(&actorID); err != nil {
		return plan, err
	}
	// The actor row is what audit entries, sequences and the like point at in
	// workspaces that live on. It loses the name; it goes entirely when
	// nothing points at it any more.
	if _, err := tx.Exec(ctx, `UPDATE actors SET display_name = 'Deleted user' WHERE id = $1`, actorID); err != nil {
		return plan, err
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT delete_actor`); err != nil {
		return plan, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM actors WHERE id = $1`, actorID); err != nil {
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "23503" {
			return plan, err
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT delete_actor`); err != nil {
			return plan, err
		}
	}
	_, err = tx.Exec(ctx, `DELETE FROM agencies a WHERE NOT EXISTS (SELECT 1 FROM agency_members m WHERE m.agency_id = a.id)`)
	return plan, err
}

// DeleteWorkspace deletes a workspace and everything in it, in tx. Its
// members keep their accounts. It returns pgx.ErrNoRows when there is no
// such workspace.
func (a *Accounts) DeleteWorkspace(ctx context.Context, tx pgx.Tx, workspaceID string) error {
	return deleteWorkspace(ctx, tx, workspaceID)
}

// deleteWorkspace relies on every workspace-owned table referencing
// workspaces ON DELETE CASCADE (TestDeleteWorkspaceLeavesNothing checks).
func deleteWorkspace(ctx context.Context, tx pgx.Tx, workspaceID string) error {
	tag, err := tx.Exec(ctx, `DELETE FROM workspaces WHERE id::text = $1`, workspaceID)
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	return err
}
