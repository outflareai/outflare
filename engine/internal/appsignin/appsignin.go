// Package appsignin signs a person in to the Outflare desktop app
// (docs/desktop-app.md) without the app ever showing a sign-in form.
//
// The app starts a sign-in with a PKCE challenge (Start) and opens the
// browser on the page the server names. There the person, signed in as
// they usually are, approves the sign-in (Approve), which mints a
// one-time code the browser hands back to the app through its URL
// scheme. The app completes the sign-in with the code and the verifier it
// kept (Complete); only then does the server make a session. A code
// intercepted on the way back is useless without the verifier, which
// never left the computer. Nothing here knows about workspaces: a person
// signs in, and the session is the ordinary one a browser gets.
package appsignin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// TTL is how long a sign-in may take from start to completion.
const TTL = 10 * time.Minute

// PrefixCode starts the one-time code the browser hands the app.
const PrefixCode = "ofac_"

// Platforms the app runs on.
var Platforms = []string{"macos", "windows", "linux"}

var (
	ErrNotFound     = errors.New("no sign-in with that state or code")
	ErrExpired      = errors.New("the sign-in expired")
	ErrUsed         = errors.New("the sign-in was already approved")
	ErrNotApproved  = errors.New("the sign-in isn't approved yet")
	ErrMismatch     = errors.New("the verifier doesn't match the challenge")
	ErrBadChallenge = errors.New("code_challenge is not base64url(SHA-256) of a verifier")
)

// Querier is a pool or a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Pending is a sign-in the app just started.
type Pending struct {
	// State names it in the browser URL and in the deep link back, so the
	// app can tell its own sign-in from a stray link.
	State     string `json:"state"`
	ExpiresIn int    `json:"expires_in"`
}

// Info is what the approve page shows about the computer asking.
type Info struct {
	Name       string    `json:"name"`
	Platform   string    `json:"platform"`
	AppVersion string    `json:"app_version"`
	ExpiresAt  time.Time `json:"expires_at"`
	Approved   bool      `json:"approved"`
}

// DecodeChallenge checks a PKCE S256 challenge as the app sends it:
// base64url, no padding, of a 32-byte SHA-256.
func DecodeChallenge(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != sha256.Size {
		return nil, ErrBadChallenge
	}
	return b, nil
}

// Challenge is the S256 challenge for a verifier, as the app computes it.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func newState() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Start records a sign-in the app is starting. Rows that expired over an
// hour ago go in the same statement, so nothing sweeps the table.
func Start(ctx context.Context, q Querier, challenge []byte, name, platform, appVersion string) (Pending, error) {
	state := newState()
	_, err := q.Exec(ctx, `
		WITH gone AS (DELETE FROM app_signins WHERE expires_at < now() - interval '1 hour')
		INSERT INTO app_signins (state, code_challenge, name, platform, app_version, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + make_interval(secs => $6))`,
		state, challenge, name, platform, appVersion, TTL.Seconds())
	if err != nil {
		return Pending{}, err
	}
	return Pending{State: state, ExpiresIn: int(TTL.Seconds())}, nil
}

// Lookup is what the approve page shows for a state.
func Lookup(ctx context.Context, q Querier, state string) (Info, error) {
	var in Info
	var expired bool
	err := q.QueryRow(ctx, `SELECT name, platform, app_version, expires_at, expires_at < now(), approved_at IS NOT NULL
		FROM app_signins WHERE state = $1`, state).
		Scan(&in.Name, &in.Platform, &in.AppVersion, &in.ExpiresAt, &expired, &in.Approved)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return in, ErrNotFound
	case err != nil:
		return in, err
	case expired:
		return in, ErrExpired
	}
	return in, nil
}

// Approve binds the sign-in named by state to userID, in tx, and returns
// the one-time code for the app. Only its hash is kept.
func Approve(ctx context.Context, tx pgx.Tx, state, userID string) (code string, in Info, err error) {
	var id string
	var expired bool
	err = tx.QueryRow(ctx, `SELECT id::text, name, platform, app_version, expires_at, expires_at < now(), approved_at IS NOT NULL
		FROM app_signins WHERE state = $1 FOR UPDATE`, state).
		Scan(&id, &in.Name, &in.Platform, &in.AppVersion, &in.ExpiresAt, &expired, &in.Approved)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", in, ErrNotFound
	case err != nil:
		return "", in, err
	case expired:
		return "", in, ErrExpired
	case in.Approved:
		return "", in, ErrUsed
	}
	code, hash := auth.NewToken(PrefixCode)
	if _, err := tx.Exec(ctx, `UPDATE app_signins SET user_id = $2, approved_at = now(), code_hash = $3 WHERE id::text = $1`, id, userID, hash); err != nil {
		return "", in, err
	}
	in.Approved = true
	return code, in, nil
}

// Complete redeems a code with the verifier the app kept and returns the
// person to sign in. The row goes in the same statement that reads it, so
// a code works once whatever happens: a wrong verifier burns it too, and
// the real app simply starts again. It takes a pool, not a transaction,
// so a failure after it can't give the code back.
func Complete(ctx context.Context, q Querier, code, verifier string) (userID string, in Info, err error) {
	if !strings.HasPrefix(code, PrefixCode) {
		return "", in, ErrNotFound
	}
	var user *string
	var challenge []byte
	var expired bool
	err = q.QueryRow(ctx, `DELETE FROM app_signins WHERE code_hash = $1
		RETURNING user_id::text, code_challenge, name, platform, app_version, expires_at, expires_at < now()`, auth.HashToken(code)).
		Scan(&user, &challenge, &in.Name, &in.Platform, &in.AppVersion, &in.ExpiresAt, &expired)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", in, ErrNotFound
	case err != nil:
		return "", in, err
	}
	in.Approved = true
	switch {
	case expired:
		return "", in, ErrExpired
	case user == nil:
		return "", in, ErrNotApproved
	}
	sum := sha256.Sum256([]byte(verifier))
	if subtle.ConstantTimeCompare(sum[:], challenge) != 1 {
		return "", in, ErrMismatch
	}
	return *user, in, nil
}
