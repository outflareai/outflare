package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/aidesktop"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/features"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Outflare for desktop (plans/ai-accounts, step 4). The app links a
// computer through /v1/desktop/links and /v1/desktop/token, which anyone
// may call (the person approves the link while signed in), then reports
// with its device token (Authorization: Bearer ofd_...). People see and
// unlink their computers under /v1/workspaces/{ws}/ai-devices.

// ReportEvery is how often the app reports.
const desktopReportEvery = 10 * time.Minute

// Links one network may ask for in loginWindow.
const desktopLinksPerNetwork = 30

func (s *Server) routesAIDesktop(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/desktop/links", s.createDesktopLink)
	mux.HandleFunc("POST /v1/desktop/token", s.claimDesktopToken)
	mux.HandleFunc("GET /v1/desktop/config", s.withDevice(s.desktopConfig))
	mux.HandleFunc("POST /v1/desktop/reports", s.withDevice(s.desktopReport))
	read, manage := auth.ScopeMyAIAccountsRead, auth.ScopeMyAIAccountsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/ai-devices", s.inBrand(read, s.aiAccountsOn(s.listAIDevices)))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ai-devices", s.inBrand(manage, s.aiAccountsOn(s.approveAIDevice)))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/ai-devices/{id}", s.inBrand(manage, s.aiAccountsOn(s.unlinkAIDevice)))
}

func (s *Server) createDesktopLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		Platform   string `json:"platform"`
		AppVersion string `json:"app_version"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	switch {
	case req.Name == "" || len(req.Name) > 100:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is the computer's name, 1 to 100 characters")
		return
	case !slices.Contains(aidesktop.Platforms, req.Platform):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "platform is macos, windows or linux")
		return
	case req.AppVersion == "" || len(req.AppVersion) > 40:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "app_version is 1 to 40 characters")
		return
	}
	key := "desktop-link:" + networkKey(clientIP(r))
	if s.limiter.over(r.Context(), key, desktopLinksPerNetwork) {
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many computers linking from this network; try again in 15 minutes")
		return
	}
	s.limiter.fail(r.Context(), key)
	link, err := aidesktop.NewLink(r.Context(), s.pool, req.Name, req.Platform, req.AppVersion)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	metrics.DesktopLinked("asked")
	writeJSON(w, http.StatusCreated, map[string]any{
		"device_code": link.DeviceCode, "user_code": link.UserCode, "expires_in": link.ExpiresIn, "interval": link.Interval,
		"verification_url": s.publicURL + "/connect/computer?code=" + link.UserCode,
	})
}

func (s *Server) claimDesktopToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !strings.HasPrefix(req.DeviceCode, aidesktop.PrefixDeviceCode) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "device_code is the one POST /v1/desktop/links gave")
		return
	}
	var token string
	var d aidesktop.Device
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		token, d, err = aidesktop.Claim(r.Context(), tx, req.DeviceCode)
		return err
	})
	switch {
	case errors.Is(err, aidesktop.ErrPending):
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "pending", "interval": int(aidesktop.PollInterval.Seconds())})
		return
	case errors.Is(err, aidesktop.ErrNoLink):
		writeError(w, r, http.StatusNotFound, "not_found", "no link with this device code")
		return
	case errors.Is(err, aidesktop.ErrLinkExpired), errors.Is(err, aidesktop.ErrLinkUsed):
		writeError(w, r, http.StatusGone, "link_expired", "this link expired or was already used; start linking again")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	metrics.DesktopLinked("linked")
	ws, err := s.workspaceBrief(r.Context(), d.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "device_id": d.ID, "workspace": ws})
}

type ctxDeviceKey struct{}

func deviceFrom(ctx context.Context) aidesktop.Device {
	d, _ := ctx.Value(ctxDeviceKey{}).(aidesktop.Device)
	return d
}

// withDevice runs next for a linked computer whose workspace has AI
// Accounts on. An unknown or unlinked token is a 401, so the app knows to
// link again.
func (s *Server) withDevice(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		d, err := aidesktop.Authenticate(r.Context(), s.pool, strings.TrimSpace(token))
		if errors.Is(err, aidesktop.ErrBadToken) {
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "this computer isn't linked; link it again from Outflare for desktop")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		on, err := features.Enabled(r.Context(), s.pool, d.WorkspaceID, features.AIAccounts)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if !on {
			writeError(w, r, http.StatusNotFound, "not_found", "AI Accounts isn't switched on for this workspace")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxDeviceKey{}, d)))
	}
}

func (s *Server) desktopConfig(w http.ResponseWriter, r *http.Request) {
	d := deviceFrom(r.Context())
	off, err := aidesktop.SourcesOff(r.Context(), s.pool)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ws, err := s.workspaceBrief(r.Context(), d.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var name, email string
	if err := s.pool.QueryRow(r.Context(), `SELECT name, email FROM users WHERE id::text = $1`, d.UserID).Scan(&name, &email); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id":            d.ID,
		"report_every_seconds": int(desktopReportEvery.Seconds()),
		"providers":            aidesktop.Providers,
		"sources_off":          off,
		"workspace":            ws,
		"user":                 map[string]string{"name": name, "email": email},
		"ai_accounts_url":      s.publicURL + "/" + ws["slug"].(string) + "/settings/ai-accounts",
	})
}

func (s *Server) desktopReport(w http.ResponseWriter, r *http.Request) {
	d := deviceFrom(r.Context())
	var rep aidesktop.Report
	if !decodeLarge(w, r, &rep, 256<<10) {
		metrics.DesktopReport("invalid")
		return
	}
	if err := rep.Validate(time.Now()); err != nil {
		metrics.DesktopReport("invalid")
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var applied []aidesktop.Applied
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		applied, err = aidesktop.Apply(r.Context(), tx, d, rep)
		return err
	})
	if err != nil {
		metrics.DesktopReport("error")
		s.internal(w, r, err)
		return
	}
	metrics.DesktopReport("ok")
	for _, a := range rep.Accounts {
		metrics.DesktopReadings(a.Provider, a.Source, len(a.Windows))
	}
	for _, e := range rep.Errors {
		metrics.DesktopReadFailed(e.Provider, e.Code)
		track.Emit(r.Context(), track.Event{Name: "ai_device.read_failed", WorkspaceID: d.WorkspaceID, UserID: d.UserID,
			ActorType: "human", Surface: "desktop", Outcome: "error", ErrorCode: e.Code, RequestID: RequestID(r.Context()),
			Props: map[string]any{"provider": e.Provider, "device_id": d.ID}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": applied, "report_every_seconds": int(desktopReportEvery.Seconds())})
}

// workspaceBrief is a workspace's id, name and slug.
func (s *Server) workspaceBrief(ctx context.Context, id string) (map[string]any, error) {
	var name, slug string
	if err := s.pool.QueryRow(ctx, `SELECT name, slug FROM workspaces WHERE id::text = $1`, id).Scan(&name, &slug); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "name": name, "slug": slug}, nil
}

func (s *Server) listAIDevices(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	x := aiAccess(a)
	user := x.me
	if x.seeAll && r.URL.Query().Get("everyone") == "true" {
		user = ""
	} else if user == "" {
		writeJSON(w, http.StatusOK, map[string]any{"data": []aidesktop.Device{}})
		return
	}
	list, err := aidesktop.List(r.Context(), s.pool, a.WorkspaceID, user)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) approveAIDevice(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	me := a.personID()
	if me == "" {
		writeError(w, r, http.StatusForbidden, "forbidden", "a computer is linked to a person: approve it while signed in, or with a token a person made")
		return
	}
	var req struct {
		UserCode string `json:"user_code"`
	}
	if !decode(w, r, &req) {
		return
	}
	key := "desktop-code:" + me
	if s.limiter.blocked(r.Context(), key) {
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many wrong codes; try again in 15 minutes")
		return
	}
	var approval aidesktop.Approval
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		approval, err = aidesktop.Approve(r.Context(), tx, a.WorkspaceID, me, req.UserCode)
		return err
	})
	switch {
	case errors.Is(err, aidesktop.ErrNoLink):
		s.limiter.fail(r.Context(), key)
		writeError(w, r, http.StatusNotFound, "not_found", "no computer is waiting with this code; check the code in Outflare for desktop")
		return
	case errors.Is(err, aidesktop.ErrLinkExpired), errors.Is(err, aidesktop.ErrLinkUsed):
		writeError(w, r, http.StatusGone, "link_expired", "this code expired or was already used; start linking again in the app")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	s.limiter.reset(r.Context(), key)
	metrics.DesktopLinked("approved")
	writeJSON(w, http.StatusCreated, approval)
}

func (s *Server) unlinkAIDevice(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	x := aiAccess(a)
	id := r.PathValue("id")
	owner, err := aidesktop.Owner(r.Context(), s.pool, a.WorkspaceID, id)
	if errors.Is(err, aidesktop.ErrNotFound) || (err == nil && owner != x.me && !x.seeAll) {
		writeError(w, r, http.StatusNotFound, "not_found", "no linked computer with this id")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if owner != x.me && !x.manageAll {
		writeError(w, r, http.StatusForbidden, "forbidden", "unlinking someone else's computer needs ai_accounts.manage")
		return
	}
	err = s.inTx(r.Context(), func(tx pgx.Tx) error {
		_, err := aidesktop.Unlink(r.Context(), tx, a.WorkspaceID, id, x.me)
		return err
	})
	if errors.Is(err, aidesktop.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no linked computer with this id")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
