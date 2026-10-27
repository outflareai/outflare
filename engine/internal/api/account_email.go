package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/sysmail"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Account emails: confirming an address, and resetting a forgotten password.
// They go through the transactional sender (sysmail), never a campaign
// mailbox.

const (
	verifyLinkLifetime = 48 * time.Hour
	resetLinkLifetime  = time.Hour

	// One person gets at most one link of a kind a minute, five an hour and
	// ten a day, however often they (or someone else) ask.
	accountEmailGap     = time.Minute
	accountEmailsHourly = 5
	accountEmailsDaily  = 10
)

// linkWait says how long before another link for purpose may be emailed to
// userID, and which limit applies (cooldown, hourly or daily); 0 means now.
// It counts the links already issued, so restarts and replicas agree.
func linkWait(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, userID, purpose string) (time.Duration, string, error) {
	rows, err := q.Query(ctx, `
		SELECT created_at FROM account_tokens
		WHERE user_id = $1 AND purpose = $2 AND created_at > now() - interval '1 day'
		ORDER BY created_at DESC LIMIT $3`, userID, purpose, accountEmailsDaily)
	if err != nil {
		return 0, "", err
	}
	sent, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
	if err != nil {
		return 0, "", err
	}
	wait, why := nextLinkAt(sent, time.Now())
	return wait, why, nil
}

// nextLinkAt applies the limits to sent, the latest links first.
func nextLinkAt(sent []time.Time, now time.Time) (time.Duration, string) {
	var wait time.Duration
	reason := ""
	later := func(d time.Duration, why string) {
		if d > wait {
			wait, reason = d, why
		}
	}
	if len(sent) > 0 {
		later(sent[0].Add(accountEmailGap).Sub(now), "cooldown")
	}
	if len(sent) >= accountEmailsHourly {
		later(sent[accountEmailsHourly-1].Add(time.Hour).Sub(now), "hourly")
	}
	if len(sent) >= accountEmailsDaily {
		later(sent[accountEmailsDaily-1].Add(24*time.Hour).Sub(now), "daily")
	}
	return wait, reason
}

// throttledError is a link that was not sent because one went out recently.
type throttledError struct {
	Wait   time.Duration
	Reason string
}

func (e *throttledError) Error() string { return "account email throttled (" + e.Reason + ")" }

// seconds rounds up, so a client waiting that long is never early.
func seconds(d time.Duration) int { return int((d + time.Second - 1) / time.Second) }

// emitThrottled records an account email that was held back.
func emitThrottled(ctx context.Context, userID, purpose string, t *throttledError) {
	track.Emit(ctx, track.Event{Name: "account_email.throttled", UserID: userID, ActorType: "human", Outcome: track.Failure,
		ErrorCode: "rate_limited", RequestID: RequestID(ctx),
		Props: map[string]any{"purpose": purpose, "limit": t.Reason, "retry_after": seconds(t.Wait)}})
}

func (s *Server) routesAccountEmail(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/password-reset", s.requestPasswordReset)
	mux.HandleFunc("POST /v1/auth/password-reset/confirm", s.confirmPasswordReset)
	mux.HandleFunc("POST /v1/auth/verify-email", s.verifyEmail)
	mux.HandleFunc("POST /v1/me/email-verification", s.requireUser(s.resendVerification))
}

// issueLink stores a one-time link for userID and returns its secret.
func issueLink(ctx context.Context, tx pgx.Tx, userID, email, purpose string) (string, error) {
	prefix, life := auth.PrefixVerifyEmail, verifyLinkLifetime
	if purpose == "reset_password" {
		prefix, life = auth.PrefixResetPassword, resetLinkLifetime
	}
	secret, hash := auth.NewToken(prefix)
	// A new link replaces any earlier one for the same purpose.
	if _, err := tx.Exec(ctx, `UPDATE account_tokens SET used_at = now() WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL`, userID, purpose); err != nil {
		return "", err
	}
	_, err := tx.Exec(ctx, `INSERT INTO account_tokens (token_hash, user_id, purpose, email, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		hash, userID, purpose, email, time.Now().Add(life))
	return secret, err
}

// useLink marks a one-time link used and returns whose it is. The link must
// be unused, unexpired, for this purpose, and still for the person's email.
func useLink(ctx context.Context, tx pgx.Tx, secret, purpose string) (userID, email string, err error) {
	err = tx.QueryRow(ctx, `
		UPDATE account_tokens t SET used_at = now()
		FROM users u
		WHERE t.token_hash = $1 AND t.purpose = $2 AND t.used_at IS NULL AND t.expires_at > now()
		  AND u.id = t.user_id AND lower(u.email) = lower(t.email) AND u.disabled_at IS NULL
		RETURNING u.id, u.email`, auth.HashToken(strings.TrimSpace(secret)), purpose).Scan(&userID, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errBadLink
	}
	return
}

var errBadLink = errors.New("invalid link")

// sendAccountEmail sends after the transaction that made the link has
// committed, and outlives the request so a closed tab doesn't cancel it.
func sendAccountEmail(ctx context.Context, mailer sysmail.Sender, log *slog.Logger, m sysmail.Message) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	err := mailer.Send(ctx, m)
	if err != nil {
		log.Error("account email failed", "to", m.To, "subject", m.Subject, "provider", mailer.Name(), "err", err)
	}
	return err
}

func (s *Server) sendAccountEmail(ctx context.Context, m sysmail.Message) error {
	return sendAccountEmail(ctx, s.mailer, s.log, m)
}

func verifyMessage(publicURL, email, secret string) sysmail.Message {
	return sysmail.Email{
		To:      email,
		Subject: "Confirm your email for Outflare",
		Preview: "One click to confirm " + email + ".",
		Heading: "Confirm your email",
		Body:    []string{"Confirm that " + email + " is your email address to finish setting up your Outflare account."},
		Button:  "Confirm email",
		URL:     publicURL + "/verify-email#" + secret,
		Note:    "The link works once, for 48 hours. If you didn't create an Outflare account, you can ignore this email.",
	}.Message()
}

// sendVerification makes a confirmation link for a new account and emails
// it. Call it after the account's transaction commits.
func (s *Server) sendVerification(ctx context.Context, userID, email string) error {
	return sendVerification(ctx, s.pool, s.mailer, s.log, s.publicURL, userID, email)
}

// It returns a *throttledError, having sent nothing, while the limits on
// account emails hold the next one back.
func sendVerification(ctx context.Context, pool *pgxpool.Pool, mailer sysmail.Sender, log *slog.Logger, publicURL, userID, email string) error {
	var secret string
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// Serialises concurrent asks, so two clicks can't both pass the check.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
			return err
		}
		wait, why, err := linkWait(ctx, tx, userID, "verify_email")
		if err != nil {
			return err
		}
		if wait > 0 {
			return &throttledError{Wait: wait, Reason: why}
		}
		secret, err = issueLink(ctx, tx, userID, email, "verify_email")
		return err
	})
	var t *throttledError
	if errors.As(err, &t) {
		emitThrottled(ctx, userID, "verify_email", t)
	}
	if err != nil {
		return err
	}
	return sendAccountEmail(ctx, mailer, log, verifyMessage(publicURL, email, secret))
}

func (s *Server) resendVerification(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	ctx := r.Context()
	var email string
	var verified bool
	if err := s.pool.QueryRow(ctx, `SELECT email, email_verified_at IS NOT NULL FROM users WHERE id = $1`, p.UserID).Scan(&email, &verified); err != nil {
		s.internal(w, r, err)
		return
	}
	if verified {
		writeJSON(w, http.StatusOK, map[string]any{"email": email, "email_verified": true})
		return
	}
	// Per network as well as per person, so many accounts on one network
	// can't be used to send a stream of email.
	ipKey := "verify-ip:" + networkKey(clientIP(r))
	if s.emailLimiter.over(r.Context(), ipKey, accountEmailsPerNetwork) {
		retryLater(w, r, accountEmailWindow, "too many confirmation emails from your network; try again in 15 minutes")
		return
	}
	err := s.sendVerification(ctx, p.UserID, email)
	var t *throttledError
	switch {
	case errors.As(err, &t):
		retryLater(w, r, t.Wait, throttleMessage(t))
		return
	case err != nil:
		writeError(w, r, http.StatusBadGateway, "email_failed", "the confirmation email could not be sent; try again later")
		return
	}
	s.emailLimiter.fail(r.Context(), ipKey)
	writeJSON(w, http.StatusAccepted, map[string]any{"email": email, "email_verified": false, "resend_after": seconds(accountEmailGap)})
}

// retryLater answers 429 with Retry-After.
func retryLater(w http.ResponseWriter, r *http.Request, wait time.Duration, msg string) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, seconds(wait))))
	writeError(w, r, http.StatusTooManyRequests, "rate_limited", msg)
}

func throttleMessage(t *throttledError) string {
	switch t.Reason {
	case "cooldown":
		return "we just sent you a link; you can ask for another in " + strconv.Itoa(seconds(t.Wait)) + " seconds"
	case "hourly":
		return "you've asked for several links this hour; you can ask again in " + strconv.Itoa((seconds(t.Wait)+59)/60) + " minutes"
	}
	return "you've asked for too many links today; open the newest one, or try again tomorrow"
}

type tokenRequest struct {
	Token string `json:"token"`
}

func (s *Server) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	var email string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		userID, addr, err := useLink(ctx, tx, req.Token, "verify_email")
		if err != nil {
			return err
		}
		email = addr
		tag, err := tx.Exec(ctx, `UPDATE users SET email_verified_at = now() WHERE id = $1 AND email_verified_at IS NULL`, userID)
		if err == nil && tag.RowsAffected() > 0 {
			emitUser(ctx, "user.email_verified", userID, map[string]any{"via": accountVia(ctx)})
		}
		return err
	})
	if errors.Is(err, errBadLink) {
		writeError(w, r, http.StatusNotFound, "invalid_link", "this link is invalid, already used or expired")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"email": email, "email_verified": true})
}

func resetMessage(publicURL, email, secret string) sysmail.Message {
	return sysmail.Email{
		To:      email,
		Subject: "Reset your Outflare password",
		Preview: "Choose a new password for " + email + ".",
		Heading: "Reset your password",
		Body:    []string{"Someone asked to reset the password for " + email + " on Outflare. To choose a new one, use the button below."},
		Button:  "Choose a new password",
		URL:     publicURL + "/reset-password#" + secret,
		Note:    "The link works once, for an hour. If it wasn't you, ignore this email: your password stays the same.",
	}.Message()
}

type resetRequest struct {
	Email string `json:"email"`
}

// requestPasswordReset always answers the same way, so it never tells
// anyone whether an email has an account.
func (s *Server) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req resetRequest
	if !decode(w, r, &req) {
		return
	}
	email, ok := validEmail(req.Email)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a valid email is required")
		return
	}
	// Both counts are the same whether or not the address has an account.
	key, ipKey := "reset:"+strings.ToLower(email), "reset-ip:"+networkKey(clientIP(r))
	if s.emailLimiter.blocked(r.Context(), key) || s.emailLimiter.over(r.Context(), ipKey, accountEmailsPerNetwork) {
		retryLater(w, r, accountEmailWindow, "too many reset requests; try again in 15 minutes")
		return
	}
	s.emailLimiter.fail(r.Context(), key)
	s.emailLimiter.fail(r.Context(), ipKey)

	ctx := r.Context()
	var userID, addr, secret string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id, email FROM users WHERE lower(email) = lower($1) AND disabled_at IS NULL`, email).Scan(&userID, &addr)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
			return err
		}
		// A link went out recently: send nothing, but answer the same, so
		// the answer never says the address has an account.
		wait, why, err := linkWait(ctx, tx, userID, "reset_password")
		if err != nil || wait > 0 {
			if wait > 0 {
				emitThrottled(ctx, userID, "reset_password", &throttledError{Wait: wait, Reason: why})
			}
			return err
		}
		secret, err = issueLink(ctx, tx, userID, addr, "reset_password")
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if secret != "" {
		s.sendAccountEmail(ctx, resetMessage(s.publicURL, addr, secret)) //nolint:errcheck // logged; the answer must not differ
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sent_if_registered"})
}

type resetConfirmRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// confirmPasswordReset sets the new password, signs out every other
// session, and signs this browser in.
func (s *Server) confirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req resetConfirmRequest
	if !decode(w, r, &req) {
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ctx := r.Context()
	var userID, email, secret string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if userID, email, err = useLink(ctx, tx, req.Token, "reset_password"); err != nil {
			return err
		}
		var actorID string
		// Opening the link proves the address, too.
		if err := tx.QueryRow(ctx, `UPDATE users SET password_hash = $2, email_verified_at = coalesce(email_verified_at, now()) WHERE id = $1 RETURNING actor_id`,
			userID, hash).Scan(&actorID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
			return err
		}
		if secret, err = createSession(ctx, tx, userID, r); err != nil {
			return err
		}
		p := &principal{ActorID: actorID, ActorType: "human", UserID: userID}
		return record(ctx, tx, p, "", "user.password_reset", "user", userID, nil)
	})
	if errors.Is(err, errBadLink) {
		writeError(w, r, http.StatusNotFound, "invalid_link", "this reset link is invalid, already used or expired; ask for a new one")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.limiter.reset(r.Context(), strings.ToLower(email))
	s.setSessionCookie(w, secret)
	writeJSON(w, http.StatusOK, map[string]string{"user_id": userID})
}
