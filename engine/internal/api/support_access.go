package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Support access: a server's operator, such as Outflare Cloud, lets its
// staff view an account as its person sees it, to help them. It is on for
// every account until the person turns it off in Account settings, and a
// view is always read-only and never shows inbox or email content.
//
// The engine has no staff of its own: an extension decides who may view
// (Accounts.StartSupportView). The view's secret goes in its own cookie
// beside the staff member's session, so it works only in their browser,
// while they stay signed in, and a version without support views ignores
// it. Each view lasts supportViewLifetime at most, and the person sees
// every view in their settings.

const supportViewLifetime = time.Hour

func (s *Server) routesSupportAccess(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/support-access", s.requireUser(s.getSupportAccess))
	mux.HandleFunc("PATCH /v1/me/support-access", s.requireUser(s.updateSupportAccess))
	mux.HandleFunc("DELETE /v1/me/support-view", s.requireUser(s.endSupportView))
}

// viewAllows says whether a request may run during a support view: reads,
// and ending the view. Signing out ends it too, and needs no principal.
func viewAllows(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return (r.Method == http.MethodDelete && r.URL.Path == "/v1/me/support-view") || readsOnly(r)
}

// hiddenInViews are the areas a support view never shows: received mail
// and replies, which can be Google user data people at the operator have
// promised not to read.
var hiddenInViews = []string{"inbox"}

// viewScopes is what a support view may do with scopes: read every area
// they reach, except the hidden ones, and change nothing.
func viewScopes(scopes []string) []string {
	out := []string{}
	for _, sc := range scopes {
		area, _, _ := strings.Cut(sc, ".")
		read := area + ".read"
		if slices.Contains(hiddenInViews, area) || !auth.ValidScope(read) || slices.Contains(out, read) {
			continue
		}
		out = append(out, read)
	}
	return out
}

// supportViewFor turns p, a staff member's session, into the person they
// view with secret, when the view is theirs, still running, and the person
// still allows it. Otherwise it is p unchanged.
func (s *Server) supportViewFor(ctx context.Context, p *principal, secret string) (*principal, error) {
	v := &principal{ActorType: "human", SessionID: p.SessionID, ViewerID: p.UserID}
	err := s.pool.QueryRow(ctx, `
		SELECT v.id, u.id, u.actor_id FROM support_views v JOIN users u ON u.id = v.user_id
		WHERE v.token_hash = $1 AND v.viewer_id = $2 AND v.ended_at IS NULL AND v.expires_at > now()
			AND u.support_access AND u.disabled_at IS NULL`, auth.HashToken(secret), p.UserID,
	).Scan(&v.ViewID, &v.UserID, &v.ActorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// ErrSupportAccessOff is StartSupportView's answer for a person who turned
// support access off, or whose account is disabled.
var ErrSupportAccessOff = errors.New("this person has turned support access off")

// SupportView is a view StartSupportView began.
type SupportView struct {
	ID        string
	ExpiresAt time.Time
}

// StartSupportView lets viewerID, a staff member the caller has already
// checked, see userID's account read-only in this browser, by setting the
// view's cookie on w. It ends any other view by the same staff member. The
// caller records who started it and why.
func (a *Accounts) StartSupportView(ctx context.Context, w http.ResponseWriter, viewerID, userID string) (SupportView, error) {
	var v SupportView
	if viewerID == userID {
		return v, errors.New("you can't view your own account")
	}
	secret, hash := auth.NewToken(auth.PrefixSupportView)
	err := pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		var allowed bool
		err := tx.QueryRow(ctx, `SELECT support_access AND disabled_at IS NULL FROM users WHERE id = $1 FOR SHARE`, userID).Scan(&allowed)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
			return ErrSupportAccessOff
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE support_views SET ended_at = now() WHERE viewer_id = $1 AND ended_at IS NULL`, viewerID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO support_views (token_hash, user_id, viewer_id, expires_at) VALUES ($1, $2, $3, now() + $4::interval)
			RETURNING id, expires_at`, hash, userID, viewerID, supportViewLifetime.String()).Scan(&v.ID, &v.ExpiresAt)
	})
	if err != nil {
		return v, err
	}
	a.cookies().setViewCookie(w, secret, supportViewLifetime)
	return v, nil
}

// EndSupportViews ends every view by viewerID, such as when they stop
// being staff.
func (a *Accounts) EndSupportViews(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, viewerID string) error {
	_, err := q.Exec(ctx, `UPDATE support_views SET ended_at = now() WHERE viewer_id = $1 AND ended_at IS NULL`, viewerID)
	return err
}

func (s *Server) setViewCookie(w http.ResponseWriter, secret string, ttl time.Duration) {
	c := &http.Cookie{Name: viewCookie, Value: secret, Path: "/", MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode}
	if secret == "" {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

type supportViewEntry struct {
	ID        string     `json:"id"`
	StartedAt time.Time  `json:"started_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	EndedAt   *time.Time `json:"ended_at"`
}

type supportAccessView struct {
	// Staff may view the account read-only to help.
	Enabled bool `json:"enabled"`
	// The views so far, newest first (at most 50).
	Views []supportViewEntry `json:"views"`
}

func (s *Server) loadSupportAccess(ctx context.Context, userID string) (supportAccessView, error) {
	var v supportAccessView
	if err := s.pool.QueryRow(ctx, `SELECT support_access FROM users WHERE id = $1`, userID).Scan(&v.Enabled); err != nil {
		return v, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, created_at, expires_at, ended_at FROM support_views
		WHERE user_id = $1 ORDER BY created_at DESC LIMIT 50`, userID)
	if err != nil {
		return v, err
	}
	v.Views, err = pgx.CollectRows(rows, pgx.RowToStructByPos[supportViewEntry])
	return v, err
}

func (s *Server) getSupportAccess(w http.ResponseWriter, r *http.Request) {
	v, err := s.loadSupportAccess(r.Context(), principalFrom(r.Context()).UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updateSupportAccess(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "enabled is required")
		return
	}
	ctx := r.Context()
	p := principalFrom(ctx)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET support_access = $2 WHERE id = $1 AND support_access <> $2`, p.UserID, *req.Enabled)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		action := "support_access.turned_on"
		if !*req.Enabled {
			action = "support_access.turned_off"
			// Turning it off stops a view in progress straight away.
			if _, err := tx.Exec(ctx, `UPDATE support_views SET ended_at = now() WHERE user_id = $1 AND ended_at IS NULL`, p.UserID); err != nil {
				return err
			}
		}
		return record(ctx, tx, p, "", action, "user", p.UserID, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getSupportAccess(w, r)
}

// endSupportView ends the view in progress in this browser.
func (s *Server) endSupportView(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.ViewID == "" {
		writeError(w, r, http.StatusNotFound, "not_found", "you are not viewing an account")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `UPDATE support_views SET ended_at = now() WHERE id = $1 AND ended_at IS NULL`, p.ViewID); err != nil {
		s.internal(w, r, err)
		return
	}
	s.setViewCookie(w, "", 0)
	writeJSON(w, http.StatusOK, map[string]any{"ended": true})
}

// supportViewInfo is what /v1/me says about a view in progress, so the app
// can show a banner with a way out.
type supportViewInfo struct {
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Server) supportViewOf(ctx context.Context, p *principal) (*supportViewInfo, error) {
	if p.ViewID == "" {
		return nil, nil
	}
	v := &supportViewInfo{}
	err := s.pool.QueryRow(ctx, `SELECT expires_at FROM support_views WHERE id = $1`, p.ViewID).Scan(&v.ExpiresAt)
	return v, err
}
