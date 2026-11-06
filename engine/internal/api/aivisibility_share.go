package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Sharing a brand's AI visibility by link: a manager makes one link per
// brand, with a password if the page should not be open to anyone who
// gets the link, and anyone with it sees the brand's overview, trend and
// questions read-only at /share/ai/<token>, for the last 28 days. The
// token is kept hashed, like a dashboard share's; the password as an
// argon2id hash. The link opens only these numbers: no record, person or
// other page of the brand.

const aiShareDays = 28

func (s *Server) routesAIVisibilityShares(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/brands/{brand}/ai-visibility/share", s.onBrand(read, s.getAIVisibilityShare))
	mux.HandleFunc("POST /v1/brands/{brand}/ai-visibility/share", s.onBrand(manage, s.shareAIVisibility))
	mux.HandleFunc("DELETE /v1/brands/{brand}/ai-visibility/share", s.onBrand(manage, s.unshareAIVisibility))
	// Public: the token is the credential.
	mux.HandleFunc("POST /v1/shared/ai-visibility/{token}", s.openSharedAIVisibility)
}

func (s *Server) loadAIVisibilityShare(r *http.Request, ws, brand string) (*shareView, error) {
	var v shareView
	err := s.pool.QueryRow(r.Context(), `SELECT password_hash IS NOT NULL, expires_at, views, last_viewed_at, created_at FROM ai_visibility_shares
		WHERE workspace_id = $1 AND brand_id = $2`, ws, brand).Scan(&v.HasPassword, &v.ExpiresAt, &v.Views, &v.LastViewedAt, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}

// getAIVisibilityShare says whether the brand has a link, never the token.
func (s *Server) getAIVisibilityShare(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.loadAIVisibilityShare(r, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"share": v})
}

// shareAIVisibility makes the brand's link, replacing one it had.
func (s *Server) shareAIVisibility(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in shareInput
	if !decode(w, r, &in) {
		return
	}
	switch {
	case in.Password != "" && (len(in.Password) < 8 || len(in.Password) > 200):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "password is 8 to 200 characters, or empty for a link that opens on its own")
		return
	case in.ExpiresInDays < 0 || in.ExpiresInDays > 365:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "expires_in_days is 1 to 365, or 0 for a link that doesn't expire")
		return
	}
	var hash *string
	if in.Password != "" {
		h, err := auth.HashPassword(in.Password)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		hash = &h
	}
	var expires *time.Time
	if in.ExpiresInDays > 0 {
		t := time.Now().UTC().AddDate(0, 0, in.ExpiresInDays)
		expires = &t
	}
	token, tokenHash := auth.NewToken("ofv_")
	var view shareView
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `DELETE FROM ai_visibility_shares WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `INSERT INTO ai_visibility_shares (workspace_id, brand_id, token_hash, password_hash, expires_at, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at`, a.WorkspaceID, a.Brand, tokenHash, hash, expires, nullIfEmpty(a.ActorID)).Scan(&view.CreatedAt); err != nil {
			return err
		}
		view.HasPassword, view.ExpiresAt = hash != nil, expires
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "ai_visibility.shared", "brand", a.Brand,
			map[string]any{"brand_id": a.Brand, "has_password": hash != nil, "expires_at": expires},
			map[string]any{"has_password": hash != nil, "expires_in_days": in.ExpiresInDays})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "url": s.publicURL + "/share/ai/" + token, "share": view})
}

// unshareAIVisibility takes the link back: it stops working at once.
func (s *Server) unshareAIVisibility(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `DELETE FROM ai_visibility_shares WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "ai_visibility.share_revoked", "brand", a.Brand, map[string]any{"brand_id": a.Brand})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type sharedAIVisibility struct {
	ws, brand    string
	passwordHash *string
	tokenHash    []byte
}

func (d sharedAIVisibility) viewerKey() string {
	if d.passwordHash == nil {
		return ""
	}
	m := hmac.New(sha256.New, []byte(*d.passwordHash))
	m.Write(d.tokenHash)
	return hex.EncodeToString(m.Sum(nil))
}

// openSharedAIVisibility is the page behind the link: the brand's name,
// its overview for the last 28 days (every question set), the trend
// against competitors and each question's results. A link with a
// password answers 401 password_required until the password comes, then
// gives a key to open it again without it.
func (s *Server) openSharedAIVisibility(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
		Key      string `json:"key"`
	}
	if !decode(w, r, &in) {
		return
	}
	token := r.PathValue("token")
	if !strings.HasPrefix(token, "ofv_") || len(token) > 120 {
		writeError(w, r, http.StatusNotFound, "not_found", "this link doesn't open anything")
		return
	}
	d := sharedAIVisibility{tokenHash: auth.HashToken(token)}
	err := s.pool.QueryRow(r.Context(), `SELECT workspace_id::text, brand_id::text, password_hash FROM ai_visibility_shares
		WHERE token_hash = $1 AND (expires_at IS NULL OR expires_at > now())`, d.tokenHash).Scan(&d.ws, &d.brand, &d.passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "this link doesn't open anything")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if d.passwordHash != nil {
		switch {
		case in.Key != "":
			if !hmac.Equal([]byte(in.Key), []byte(d.viewerKey())) {
				writeError(w, r, http.StatusUnauthorized, "password_required", "this page asks for its password")
				return
			}
		case in.Password == "":
			writeError(w, r, http.StatusUnauthorized, "password_required", "this page asks for its password")
			return
		default:
			limitKey := "share:" + hex.EncodeToString(d.tokenHash[:8]) + ":" + networkKey(clientIP(r))
			if s.limiter.blocked(r.Context(), limitKey) {
				track.Emit(r.Context(), track.Event{Name: "ai_visibility_share.password_failed", WorkspaceID: d.ws, ActorType: "human", Surface: "web",
					Outcome: track.Failure, ErrorCode: "rate_limited", RequestID: RequestID(r.Context())})
				writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many wrong passwords; try again in 15 minutes")
				return
			}
			if !auth.CheckPassword(*d.passwordHash, in.Password) {
				s.limiter.fail(r.Context(), limitKey)
				track.Emit(r.Context(), track.Event{Name: "ai_visibility_share.password_failed", WorkspaceID: d.ws, ActorType: "human", Surface: "web",
					Outcome: track.Failure, ErrorCode: "wrong_password", RequestID: RequestID(r.Context())})
				writeError(w, r, http.StatusUnauthorized, "wrong_password", "that isn't the password")
				return
			}
		}
	}
	var brandName string
	if err := s.pool.QueryRow(r.Context(), `SELECT name FROM brands WHERE id = $1 AND workspace_id = $2`, d.brand, d.ws).Scan(&brandName); err != nil {
		s.internal(w, r, err)
		return
	}
	from := time.Now().UTC().AddDate(0, 0, -aiShareDays)
	overview, err := s.aiVisibilityOverview(r.Context(), d.ws, brandSel{id: d.brand}, from)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	questions, err := s.aiVisibilityQuestions(r.Context(), d.ws, d.brand, "", from, from.AddDate(0, 0, -aiShareDays))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	series, weeks, err := s.aiVisibilitySeries(r.Context(), d.ws, d.brand, "", "competitor", from)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if _, err := s.pool.Exec(r.Context(), `UPDATE ai_visibility_shares SET views = views + 1, last_viewed_at = now() WHERE token_hash = $1`, d.tokenHash); err != nil {
		s.internal(w, r, err)
		return
	}
	track.Emit(r.Context(), track.Event{Name: "ai_visibility_share.opened", WorkspaceID: d.ws, ActorType: "human", Surface: "web",
		Outcome: track.Success, RequestID: RequestID(r.Context()), Props: map[string]any{"has_password": d.passwordHash != nil, "questions": len(questions)}})
	if overview == nil {
		overview = []aiVisCohort{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"brand": brandName, "days": aiShareDays, "from": from.Format(time.RFC3339), "key": d.viewerKey(),
		"data": overview, "questions": questions, "weeks": weeks, "series": series})
}
