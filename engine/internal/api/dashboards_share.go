package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Sharing a dashboard by link (docs/insights.md): a manager makes one
// link per dashboard, with a password if the page should not be open to
// anyone who gets the link, and anyone with it sees the dashboard
// read-only at /share/<token>, every tile run live. The token is kept
// hashed, like an invite's; the password as an argon2id hash. The link
// opens only the dashboard: no record, person or other page of the brand.

func (s *Server) routesDashboardShares(mux *http.ServeMux) {
	manage := auth.ScopeAnalyticsManage
	mux.HandleFunc("POST /v1/brands/{brand}/dashboards/{dashboard}/share", s.onBrand(manage, s.shareDashboard))
	mux.HandleFunc("DELETE /v1/brands/{brand}/dashboards/{dashboard}/share", s.onBrand(manage, s.unshareDashboard))
	// Public: the token is the credential.
	mux.HandleFunc("POST /v1/shared/dashboards/{token}", s.openSharedDashboard)
	mux.HandleFunc("POST /v1/shared/dashboards/{token}/tiles/{tile}", s.runSharedTile)
}

// shareView is what the brand sees of its link: never the token again.
type shareView struct {
	HasPassword  bool       `json:"has_password"`
	ExpiresAt    *time.Time `json:"expires_at"`
	Views        int        `json:"views"`
	LastViewedAt *time.Time `json:"last_viewed_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

// shareColumns reads a dashboard's link beside its columns; null when
// there is none.
const shareColumns = `(SELECT json_build_object('has_password', sh.password_hash IS NOT NULL, 'expires_at', sh.expires_at, 'views', sh.views,
	'last_viewed_at', sh.last_viewed_at, 'created_at', sh.created_at) FROM dashboard_shares sh WHERE sh.dashboard_id = d.id)`

type shareInput struct {
	Password      string `json:"password"`
	ExpiresInDays int    `json:"expires_in_days"`
}

// shareDashboard makes the dashboard's link, replacing one it had.
func (s *Server) shareDashboard(w http.ResponseWriter, r *http.Request) {
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
	token, tokenHash := auth.NewToken("ofd_")
	var view shareView
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		d, err := lockDashboard(r, tx, a.WorkspaceID, a.Brand, r.PathValue("dashboard"))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM dashboard_shares WHERE dashboard_id = $1`, d.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `INSERT INTO dashboard_shares (workspace_id, brand_id, dashboard_id, token_hash, password_hash, expires_at, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at`, a.WorkspaceID, a.Brand, d.ID, tokenHash, hash, expires, nullIfEmpty(a.ActorID)).Scan(&view.CreatedAt); err != nil {
			return err
		}
		view.HasPassword, view.ExpiresAt = hash != nil, expires
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "dashboard.shared", "dashboard", d.ID,
			map[string]any{"brand_id": a.Brand, "has_password": hash != nil, "expires_at": expires},
			map[string]any{"has_password": hash != nil, "expires_in_days": in.ExpiresInDays})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "url": s.publicURL + "/share/" + token, "share": view})
}

// unshareDashboard takes the link back: it stops working at once.
func (s *Server) unshareDashboard(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		d, err := lockDashboard(r, tx, a.WorkspaceID, a.Brand, r.PathValue("dashboard"))
		if err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `DELETE FROM dashboard_shares WHERE dashboard_id = $1`, d.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "dashboard.share_revoked", "dashboard", d.ID, map[string]any{"brand_id": a.Brand})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sharedDashboard is a link's dashboard as found by its token.
type sharedDashboard struct {
	ws, brand, dashboard string
	passwordHash         *string
	tokenHash            []byte
}

// findShare loads the link for a token, or errNotFound when there is
// none, or it has expired.
func (s *Server) findShare(r *http.Request, token string) (sharedDashboard, error) {
	var d sharedDashboard
	if !strings.HasPrefix(token, "ofd_") || len(token) > 120 {
		return d, errNotFound
	}
	d.tokenHash = auth.HashToken(token)
	err := s.pool.QueryRow(r.Context(), `SELECT workspace_id::text, brand_id::text, dashboard_id::text, password_hash FROM dashboard_shares
		WHERE token_hash = $1 AND (expires_at IS NULL OR expires_at > now())`, d.tokenHash).Scan(&d.ws, &d.brand, &d.dashboard, &d.passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, errNotFound
	}
	return d, err
}

// viewerKey is what a viewer holds after giving the right password, so
// each tile doesn't check it again: an HMAC of the link under its
// password hash. Changing the password or the link ends it.
func (d sharedDashboard) viewerKey() string {
	if d.passwordHash == nil {
		return ""
	}
	m := hmac.New(sha256.New, []byte(*d.passwordHash))
	m.Write(d.tokenHash)
	return hex.EncodeToString(m.Sum(nil))
}

// admit checks a viewer's password or key against the link. Wrong
// passwords are limited per link and per network, like sign-ins.
func (s *Server) admit(w http.ResponseWriter, r *http.Request, d sharedDashboard, password, key string) bool {
	if d.passwordHash == nil {
		return true
	}
	if key != "" {
		if hmac.Equal([]byte(key), []byte(d.viewerKey())) {
			return true
		}
		writeError(w, r, http.StatusUnauthorized, "password_required", "this dashboard asks for its password")
		return false
	}
	if password == "" {
		writeError(w, r, http.StatusUnauthorized, "password_required", "this dashboard asks for its password")
		return false
	}
	limitKey := "share:" + hex.EncodeToString(d.tokenHash[:8]) + ":" + networkKey(clientIP(r))
	if s.limiter.blocked(r.Context(), limitKey) {
		track.Emit(r.Context(), track.Event{Name: "dashboard_share.password_failed", WorkspaceID: d.ws, ActorType: "human", Surface: "web",
			Outcome: track.Failure, ErrorCode: "rate_limited", RequestID: RequestID(r.Context())})
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many wrong passwords; try again in 15 minutes")
		return false
	}
	if !auth.CheckPassword(*d.passwordHash, password) {
		s.limiter.fail(r.Context(), limitKey)
		track.Emit(r.Context(), track.Event{Name: "dashboard_share.password_failed", WorkspaceID: d.ws, ActorType: "human", Surface: "web",
			Outcome: track.Failure, ErrorCode: "wrong_password", RequestID: RequestID(r.Context())})
		writeError(w, r, http.StatusUnauthorized, "wrong_password", "that isn't the password")
		return false
	}
	return true
}

type sharedTile struct {
	ID   string  `json:"id"`
	Kind *string `json:"kind"`
	Name *string `json:"name"`
	// Display is how a trend is drawn (line, bar, number, table).
	Display *string `json:"display"`
	Note    string  `json:"note"`
	X       int     `json:"x"`
	Y       int     `json:"y"`
	W       int     `json:"w"`
	H       int     `json:"h"`
}

type sharedView struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Brand       string          `json:"brand"`
	Settings    json.RawMessage `json:"settings"`
	TileList    []sharedTile    `json:"tile_list"`
	// Key lets the viewer run the tiles without the password again.
	Key string `json:"key"`
}

type openInput struct {
	Password string `json:"password"`
	Key      string `json:"key"`
}

// openSharedDashboard answers the dashboard behind a link: its tiles
// without their specs (the viewer runs each by its id), and the key for
// the tiles when the link has a password.
func (s *Server) openSharedDashboard(w http.ResponseWriter, r *http.Request) {
	var in openInput
	if !decode(w, r, &in) {
		return
	}
	d, err := s.findShare(r, r.PathValue("token"))
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "this link is invalid, expired or was taken back")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// Without a password or key, a link with a password answers only that
	// it needs one.
	if !s.admit(w, r, d, in.Password, in.Key) {
		return
	}
	var v sharedView
	err = s.pool.QueryRow(r.Context(), `SELECT d.name, d.description, b.name, d.settings FROM dashboards d JOIN brands b ON b.id = d.brand_id
		WHERE d.id = $1 AND d.workspace_id = $2 AND d.brand_id = $3`, d.dashboard, d.ws, d.brand).Scan(&v.Name, &v.Description, &v.Brand, &v.Settings)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT t.id::text, i.kind, i.name, i.spec->>'display', t.note, t.x, t.y, t.w, t.h
		FROM dashboard_tiles t LEFT JOIN insights i ON i.id = t.insight_id WHERE t.dashboard_id = $1 ORDER BY t.y, t.x, t.created_at`, d.dashboard)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v.TileList, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (sharedTile, error) {
		var t sharedTile
		return t, row.Scan(&t.ID, &t.Kind, &t.Name, &t.Display, &t.Note, &t.X, &t.Y, &t.W, &t.H)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v.TileList = nonNil(v.TileList)
	v.Key = d.viewerKey()
	if in.Key == "" {
		// A view is one opening, not every tile.
		if _, err := s.pool.Exec(r.Context(), `UPDATE dashboard_shares SET views = views + 1, last_viewed_at = now() WHERE token_hash = $1`, d.tokenHash); err != nil {
			s.internal(w, r, err)
			return
		}
		track.Emit(r.Context(), track.Event{Name: "dashboard_share.viewed", WorkspaceID: d.ws, ActorType: "human", Surface: "web",
			Outcome: track.Success, RequestID: RequestID(r.Context()), Props: map[string]any{"has_password": d.passwordHash != nil, "tiles": len(v.TileList)}})
	}
	writeJSON(w, http.StatusOK, v)
}

type sharedTileInput struct {
	Key       string     `json:"key"`
	Password  string     `json:"password"`
	DateRange *dateRange `json:"date_range"`
}

// runSharedTile runs one tile of a shared dashboard with the dashboard's
// dates, or the dates the viewer picked.
func (s *Server) runSharedTile(w http.ResponseWriter, r *http.Request) {
	var in sharedTileInput
	if !decode(w, r, &in) {
		return
	}
	d, err := s.findShare(r, r.PathValue("token"))
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "this link is invalid, expired or was taken back")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !s.admit(w, r, d, in.Password, in.Key) {
		return
	}
	var kind string
	var spec, settings []byte
	err = s.pool.QueryRow(r.Context(), `SELECT i.kind, i.spec, d.settings FROM dashboard_tiles t JOIN insights i ON i.id = t.insight_id JOIN dashboards d ON d.id = t.dashboard_id
		WHERE t.dashboard_id = $1 AND t.id = $2 AND i.brand_id = $3`, d.dashboard, validOrNil(r.PathValue("tile")), d.brand).Scan(&kind, &spec, &settings)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "tile not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var st dashboardSettings
	if err := json.Unmarshal(settings, &st); err != nil {
		s.internal(w, r, err)
		return
	}
	rng := st.DateRange
	if in.DateRange != nil {
		rng = in.DateRange
	}
	start := time.Now()
	var res any
	err = s.readInsight(r.Context(), func(tx pgx.Tx) error {
		var err error
		res, err = runSavedInsight(r.Context(), tx, d.ws, d.brand, kind, spec, rng, st.Compare, start)
		return err
	})
	if err != nil {
		metrics.InsightRun("shared", s.insightError(w, r, err), time.Since(start))
		return
	}
	metrics.InsightRun("shared", "success", time.Since(start))
	writeJSON(w, http.StatusOK, res)
}
