package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/sysmail"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Open password sign-up (used by hosts such as Cloud): the account is made
// only once its address is confirmed. StartSignup answers the same way
// whether or not the address has an account, and emails either a link to
// finish signing up or, to an existing account, a note with a reset link.

const (
	signupLinkLifetime = 24 * time.Hour
	signupCookie       = "outflare_signup"
	// SignupConfirmPath is where the confirmation link points, with the
	// token in its #fragment; the host serves the page that finishes there.
	SignupConfirmPath = "/signup/confirm"
)

// ErrChoosePassword is returned by FinishSignup when the link is opened in
// another browser than the one that filled in the form: the person must
// choose a password there, since the form's may have been a stranger's.
var ErrChoosePassword = errors.New("choose a password to finish signing up")

// ErrBadSignupLink is a sign-up link that is unknown, used or expired.
var ErrBadSignupLink = errors.New("this sign-up link is invalid, already used or expired")

// StartSignup checks a password sign-up and emails a confirmation link. It
// returns an *InputError for bad input and otherwise nil, whatever the
// address: an existing account gets a different email, not a different
// answer. Asking again sends at most one email a minute and five an hour.
func (a *Accounts) StartSignup(ctx context.Context, w http.ResponseWriter, r *http.Request, in NewAccount) error {
	email, ok := validEmail(in.Email)
	name, wsName := strings.TrimSpace(in.Name), strings.TrimSpace(in.WorkspaceName)
	switch {
	case !ok:
		return &InputError{"a valid email is required"}
	case name == "" || wsName == "":
		return &InputError{"name and workspace name are required"}
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return &InputError{err.Error()}
	}
	email = strings.ToLower(email)
	publicURL := strings.TrimRight(a.PublicURL, "/")

	var userID, addr string
	err = a.Pool.QueryRow(ctx, `SELECT id, email FROM users WHERE lower(email) = $1 AND disabled_at IS NULL`, email).Scan(&userID, &addr)
	switch {
	case err == nil:
		return a.remindExisting(ctx, publicURL, userID, addr)
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}

	browser, _ := auth.NewToken("")
	secret, tokenHash := auth.NewToken(auth.PrefixVerifyEmail)
	tag, err := a.Pool.Exec(ctx, `
		INSERT INTO pending_signups (email, token_hash, name, workspace_name, password_hash, browser_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (email) DO UPDATE SET
			token_hash = EXCLUDED.token_hash, name = EXCLUDED.name, workspace_name = EXCLUDED.workspace_name,
			password_hash = EXCLUDED.password_hash, browser_hash = EXCLUDED.browser_hash, expires_at = EXCLUDED.expires_at,
			sends = CASE WHEN pending_signups.first_sent_at < now() - interval '1 hour' THEN 1 ELSE pending_signups.sends + 1 END,
			first_sent_at = CASE WHEN pending_signups.first_sent_at < now() - interval '1 hour' THEN now() ELSE pending_signups.first_sent_at END,
			last_sent_at = now()
		WHERE pending_signups.last_sent_at < now() - make_interval(secs => $8)
		  AND (pending_signups.sends < $9 OR pending_signups.first_sent_at < now() - interval '1 hour')`,
		email, tokenHash, name, wsName, hash, auth.HashToken(browser), time.Now().Add(signupLinkLifetime),
		accountEmailGap.Seconds(), accountEmailsHourly)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		emitThrottled(ctx, "", "signup", &throttledError{Reason: "cooldown"})
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: signupCookie, Value: browser, Path: "/", MaxAge: int(signupLinkLifetime.Seconds()),
		HttpOnly: true, Secure: strings.HasPrefix(publicURL, "https://"), SameSite: http.SameSiteLaxMode})
	sendAccountEmail(ctx, a.Mailer, a.Log, signupMessage(publicURL, email, secret)) //nolint:errcheck // logged; the answer must not differ
	track.Emit(ctx, track.Event{Name: "signup.confirmation_sent", ActorType: "human", RequestID: RequestID(ctx)})
	return nil
}

// remindExisting tells an account's owner that someone tried to sign up
// with their address, with a link to reset the password.
func (a *Accounts) remindExisting(ctx context.Context, publicURL, userID, addr string) error {
	var secret string
	err := pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
			return err
		}
		wait, why, err := linkWait(ctx, tx, userID, "reset_password")
		if err != nil || wait > 0 {
			if wait > 0 {
				emitThrottled(ctx, userID, "signup_existing", &throttledError{Wait: wait, Reason: why})
			}
			return err
		}
		secret, err = issueLink(ctx, tx, userID, addr, "reset_password")
		return err
	})
	if err != nil || secret == "" {
		return err
	}
	sendAccountEmail(ctx, a.Mailer, a.Log, existingAccountMessage(publicURL, addr, secret)) //nolint:errcheck // logged; the answer must not differ
	emitUser(ctx, "signup.existing_account_reminded", userID, nil)
	return nil
}

func signupMessage(publicURL, email, secret string) sysmail.Message {
	return sysmail.Email{
		To:      email,
		Subject: "Confirm your email to finish signing up for Outflare",
		Preview: "One click to create your Outflare account.",
		Heading: "Confirm your email",
		Body:    []string{"Confirm that " + email + " is your email address to create your Outflare account."},
		Button:  "Create my account",
		URL:     publicURL + SignupConfirmPath + "#" + secret,
		Note:    "The link works once, for 24 hours. If you didn't sign up for Outflare, ignore this email: no account is made.",
	}.Message()
}

func existingAccountMessage(publicURL, email, secret string) sysmail.Message {
	return sysmail.Email{
		To:      email,
		Subject: "You already have an Outflare account",
		Preview: "Sign in, or choose a new password.",
		Heading: "You already have an account",
		Body: []string{"Someone, probably you, tried to sign up for Outflare with " + email + ". This address already has an account, so nothing new was made.",
			"Sign in at " + publicURL + ", or choose a new password with the button below."},
		Button: "Choose a new password",
		URL:    publicURL + "/reset-password#" + secret,
		Note:   "The link works once, for an hour. If it wasn't you, ignore this email: your account is unchanged.",
	}.Message()
}

// FinishSignup opens a sign-up link: it makes the account (its email
// confirmed) and its first workspace, and signs the person in on w, in tx
// with tx's other writes (nil for its own). password is needed only when
// the link is opened in another browser (ErrChoosePassword).
func (a *Accounts) FinishSignup(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, r *http.Request, token, password string) (userID, workspaceID string, err error) {
	finish := func(tx pgx.Tx) error {
		var email, name, wsName, hash string
		var browserHash []byte
		err := tx.QueryRow(ctx, `
			DELETE FROM pending_signups WHERE token_hash = $1 AND expires_at > now()
			RETURNING email, name, workspace_name, password_hash, browser_hash`, auth.HashToken(strings.TrimSpace(token))).
			Scan(&email, &name, &wsName, &hash, &browserHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBadSignupLink
		}
		if err != nil {
			return err
		}
		c, cerr := r.Cookie(signupCookie)
		sameBrowser := cerr == nil && subtle.ConstantTimeCompare(auth.HashToken(c.Value), browserHash) == 1
		if !sameBrowser {
			if password == "" {
				return ErrChoosePassword
			}
			if hash, err = auth.HashPassword(password); err != nil {
				return &InputError{err.Error()}
			}
		}
		if _, err := tx.Exec(ctx, `SAVEPOINT finish_signup`); err != nil {
			return err
		}
		var actorID string
		userID, actorID, err = createUser(ctx, tx, email, name, hash)
		if isUnique(err) {
			if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT finish_signup`); err != nil {
				return err
			}
			return ErrEmailTaken
		}
		if err != nil {
			return err
		}
		if err := markVerified(ctx, tx, userID, email); err != nil {
			return err
		}
		if workspaceID, err = createWorkspace(ctx, tx, wsName, userID); err != nil {
			return err
		}
		secret, err := createSession(ctx, tx, userID, r)
		if err != nil {
			return err
		}
		a.cookies().setSessionCookie(w, secret)
		p := &principal{ActorID: actorID, ActorType: "human", UserID: userID}
		return record(ctx, tx, p, workspaceID, "workspace.created", "workspace", workspaceID, map[string]any{"name": wsName, "via": "signup"})
	}
	if tx != nil {
		err = finish(tx)
	} else {
		err = pgx.BeginFunc(ctx, a.Pool, finish)
	}
	// Any error rolls the transaction back, so the link still works for the
	// next try (after ErrChoosePassword, with a password).
	if err != nil {
		return "", "", err
	}
	http.SetCookie(w, &http.Cookie{Name: signupCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	return userID, workspaceID, nil
}
