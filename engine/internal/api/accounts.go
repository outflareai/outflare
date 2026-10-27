package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/oauth"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/sysmail"
)

// Accounts lets builds on top of the engine, such as the hosted Cloud, sign
// people up and recognise them with the engine's own users and sessions,
// without copying its password or token formats. Reached through
// outflare.Host.Accounts.
type Accounts struct {
	Pool      *pgxpool.Pool
	PublicURL string
	Box       *secrets.Box // opens Google sign-ins (NewAccount.Identity)
	Mailer    sysmail.Sender
	Log       *slog.Logger
	// SignedInCookieDomain is Options.SignedInCookieDomain.
	SignedInCookieDomain string
}

// cookies sets session cookies the way the server does.
func (a *Accounts) cookies() *Server {
	return &Server{secureCookies: strings.HasPrefix(a.PublicURL, "https://"), signedInDomain: a.SignedInCookieDomain}
}

// SendVerification emails userID a link to confirm their address, unless
// it is confirmed already (as with a Google sign-up). Call it after the
// transaction that created the account commits.
func (a *Accounts) SendVerification(ctx context.Context, userID string) error {
	var email string
	var verified bool
	if err := a.Pool.QueryRow(ctx, `SELECT email, email_verified_at IS NOT NULL FROM users WHERE id = $1`, userID).Scan(&email, &verified); err != nil {
		return err
	}
	if verified {
		return nil
	}
	return sendVerification(ctx, a.Pool, a.Mailer, a.Log, strings.TrimRight(a.PublicURL, "/"), userID, email)
}

// SessionUser is the signed-in person behind r's session, as a user id and
// email. Both are empty for an anonymous request or an API token.
func (a *Accounts) SessionUser(r *http.Request) (userID, email string, err error) {
	p, err := (&Server{pool: a.Pool}).authenticate(r)
	if err != nil || p == nil || p.UserID == "" {
		return "", "", err
	}
	err = a.Pool.QueryRow(r.Context(), `SELECT email FROM users WHERE id = $1`, p.UserID).Scan(&email)
	return p.UserID, email, err
}

// ErrEmailTaken is returned by CreateAccount when a user already has the email.
var ErrEmailTaken = errors.New("an account with this email already exists")

// NewAccount is a person signing up with their first workspace.
type NewAccount struct {
	Email, Name, Password, WorkspaceName string
	// Identity, instead of Password, is a Google sign-in from
	// /v1/auth/google/callback (its identity parameter). Its verified email
	// is the account's; Email may be empty or must match it.
	Identity string
}

// CreateAccount creates a user who owns a new workspace and signs them in on
// w, in one transaction with tx's other writes (pass nil for its own). Bad
// input returns an error whose message can be shown to the person.
func (a *Accounts) CreateAccount(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, r *http.Request, in NewAccount) (userID, workspaceID string, err error) {
	var id *oauth.Identity
	if in.Identity != "" {
		if id, err = openIdentity(a.Box, in.Identity); err != nil {
			return "", "", err
		}
		if in.Email != "" && !strings.EqualFold(strings.TrimSpace(in.Email), id.Email) {
			return "", "", &InputError{"you signed in with Google as " + id.Email + ", not " + strings.TrimSpace(in.Email)}
		}
		in.Email = id.Email
		if strings.TrimSpace(in.Name) == "" {
			in.Name = id.Name
		}
	}
	email, ok := validEmail(in.Email)
	name, wsName := strings.TrimSpace(in.Name), strings.TrimSpace(in.WorkspaceName)
	switch {
	case !ok:
		return "", "", &InputError{"a valid email is required"}
	case name == "" || wsName == "":
		return "", "", &InputError{"name and workspace name are required"}
	}
	var hash string
	if id == nil {
		if hash, err = auth.HashPassword(in.Password); err != nil {
			return "", "", &InputError{err.Error()}
		}
	}
	var secret string
	create := func(tx pgx.Tx) error {
		var actorID string
		if _, err := tx.Exec(ctx, `SAVEPOINT create_account`); err != nil {
			return err
		}
		userID, actorID, err = createUser(ctx, tx, email, name, hash)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT create_account`); err != nil {
				return err
			}
			return ErrEmailTaken
		}
		if err != nil {
			return err
		}
		if id != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO user_identities (provider, subject, user_id, email) VALUES ('google', $1, $2, $3)`,
				id.Subject, userID, id.Email); err != nil {
				return err
			}
			if err := markVerified(ctx, tx, userID, id.Email); err != nil {
				return err
			}
		}
		if workspaceID, err = createWorkspace(ctx, tx, wsName, userID); err != nil {
			return err
		}
		if secret, err = createSession(ctx, tx, userID, r); err != nil {
			return err
		}
		p := &principal{ActorID: actorID, ActorType: "human", UserID: userID}
		return record(ctx, tx, p, workspaceID, "workspace.created", "workspace", workspaceID, map[string]any{"name": wsName, "via": "signup"})
	}
	if tx != nil {
		err = create(tx)
	} else {
		err = pgx.BeginFunc(ctx, a.Pool, create)
	}
	if err != nil {
		return "", "", err
	}
	a.cookies().setSessionCookie(w, secret)
	return userID, workspaceID, nil
}

// InputError is a validation failure whose message is safe to show.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }
