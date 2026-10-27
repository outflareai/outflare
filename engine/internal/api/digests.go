package api

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/digests"
)

// Digests: a daily or weekly summary of the whole workspace (package
// digests), kept here, sent as the digest.ready webhook and, when the
// workspace picks its own email provider, emailed to its members. A digest
// covers every brand, so brand-limited callers can't reach these routes.

func (s *Server) routesDigests(mux *http.ServeMux) {
	read, manage := auth.ScopeWorkspaceRead, auth.ScopeWorkspaceManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/digest-settings", s.inWorkspace(read, s.getDigestSettings))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/digest-settings", s.inWorkspace(manage, s.putDigestSettings))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/digest-settings/me", s.inWorkspace(read, s.putMyDigest))
	mux.HandleFunc("GET /v1/workspaces/{ws}/digests", s.inWorkspace(read, s.listDigests))
	mux.HandleFunc("POST /v1/workspaces/{ws}/digests", s.inWorkspace(manage, s.requestDigest))
	mux.HandleFunc("GET /v1/workspaces/{ws}/digests/preview", s.inWorkspace(read, s.previewDigest))
	mux.HandleFunc("GET /v1/workspaces/{ws}/digests/{id}", s.inWorkspace(read, s.getDigest))
}

type digestSettingsView struct {
	digests.Settings
	// Why it can't be emailed with these settings, or null.
	EmailProblem *string `json:"email_problem"`
	// How many members it goes to by email.
	RecipientCount int `json:"recipient_count"`
	// Whether the caller gets it by email (false when they turned it off
	// or aren't a recipient), and whether they turned it off.
	Me *digestMe `json:"me"`
}

type digestMe struct {
	Email    bool `json:"email"`
	OptedOut bool `json:"opted_out"`
}

// digestUser is the person the caller is: the signed-in user, or the
// user who made the API token.
func digestUser(a *access) string {
	if a.UserID != "" {
		return a.UserID
	}
	return a.TokenOwnerID
}

func (s *Server) digestSettings(r *http.Request, a *access) (digestSettingsView, error) {
	ctx := r.Context()
	set, err := digests.Load(ctx, s.pool, a.WorkspaceID)
	if err != nil {
		return digestSettingsView{}, err
	}
	v := digestSettingsView{Settings: set}
	why, err := digests.Problem(ctx, s.pool, a.WorkspaceID, set)
	if err != nil {
		return v, err
	}
	if why != "" {
		v.EmailProblem = &why
	}
	to, err := digests.Recipients(ctx, s.pool, a.WorkspaceID, set.Recipients)
	if err != nil {
		return v, err
	}
	v.RecipientCount = len(to)
	if user := digestUser(a); user != "" {
		v.Me = &digestMe{}
		for _, x := range to {
			if x.UserID == user {
				v.Me.Email = why == ""
			}
		}
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM digest_optouts WHERE workspace_id = $1 AND user_id = $2)`,
			a.WorkspaceID, user).Scan(&v.Me.OptedOut); err != nil {
			return v, err
		}
	}
	return v, nil
}

func (s *Server) getDigestSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.digestSettings(r, accessFrom(r.Context()))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) putDigestSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		Frequency   *string          `json:"frequency"`
		Weekday     *int             `json:"weekday"`
		Hour        *int             `json:"hour"`
		Timezone    *string          `json:"timezone"`
		Recipients  *string          `json:"recipients"`
		ProviderID  optional[string] `json:"provider_id"`
		FromAddress *string          `json:"from_address"`
	}
	if !decode(w, r, &req) {
		return
	}
	set, err := digests.Load(ctx, s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	if req.Frequency != nil {
		if f := *req.Frequency; f != digests.Off && f != digests.Daily && f != digests.Weekly {
			bad("frequency is off, daily or weekly")
			return
		}
		set.Frequency = *req.Frequency
	}
	if req.Weekday != nil {
		if *req.Weekday < 0 || *req.Weekday > 6 {
			bad("weekday is 0 (Sunday) to 6 (Saturday)")
			return
		}
		set.Weekday = *req.Weekday
	}
	if req.Hour != nil {
		if *req.Hour < 0 || *req.Hour > 23 {
			bad("hour is 0 to 23")
			return
		}
		set.Hour = *req.Hour
	}
	if req.Timezone != nil {
		if _, err := time.LoadLocation(*req.Timezone); err != nil || *req.Timezone == "" || *req.Timezone == "Local" {
			bad("timezone is an IANA time zone such as Australia/Sydney")
			return
		}
		set.Timezone = *req.Timezone
	}
	if req.Recipients != nil {
		if x := *req.Recipients; x != digests.ToNone && x != digests.ToAdmins && x != digests.ToMembers {
			bad("recipients is none, admins or members")
			return
		}
		set.Recipients = *req.Recipients
	}
	if req.ProviderID.Set {
		set.ProviderID = req.ProviderID.Value
		if p := set.ProviderID; p != nil {
			var ok bool
			if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM email_providers WHERE id = $1 AND workspace_id = $2)`,
				*p, a.WorkspaceID).Scan(&ok); err != nil || !validUUID(*p) || !ok {
				bad("provider_id is one of this workspace's email providers; GET .../email-providers lists them")
				return
			}
		}
	}
	if req.FromAddress != nil {
		from := strings.TrimSpace(*req.FromAddress)
		if from != "" {
			addr, err := mail.ParseAddress(from)
			if err != nil || len(from) > 320 {
				bad("from_address is an email address on one of your sending domains")
				return
			}
			from = addr.Address
		}
		set.FromAddress = from
	}
	if set.Recipients != digests.ToNone && (set.ProviderID == nil || set.FromAddress == "") {
		bad("to email the digest, pick provider_id and from_address too")
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO digest_settings (workspace_id, frequency, weekday, hour, timezone, recipients, provider_id, from_address, updated_by, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
			ON CONFLICT (workspace_id) DO UPDATE SET frequency = $2, weekday = $3, hour = $4, timezone = $5, recipients = $6,
				provider_id = $7, from_address = $8, updated_by = $9, updated_at = now()`,
			a.WorkspaceID, set.Frequency, set.Weekday, set.Hour, set.Timezone, set.Recipients, set.ProviderID, set.FromAddress, a.ActorID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "digest_settings.updated", "workspace", a.WorkspaceID,
			map[string]any{"frequency": set.Frequency, "weekday": set.Weekday, "hour": set.Hour, "recipients": set.Recipients,
				"emailed": set.Recipients != digests.ToNone})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getDigestSettings(w, r)
}

// putMyDigest turns the digest email off or back on for the caller.
func (s *Server) putMyDigest(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		Email *bool `json:"email"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Email == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "email is true or false")
		return
	}
	user := digestUser(a)
	if user == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "only a person gets the digest by email; this token has no user")
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		sql := `DELETE FROM digest_optouts WHERE workspace_id = $1 AND user_id = $2`
		action := "digest.email_turned_on"
		if !*req.Email {
			sql = `INSERT INTO digest_optouts (workspace_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`
			action = "digest.email_turned_off"
		}
		tag, err := tx.Exec(ctx, sql, a.WorkspaceID, user)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "workspace", a.WorkspaceID, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getDigestSettings(w, r)
}

const digestCols = `id::text, frequency, trigger, period_start, period_end, sections, emailed, email_error, created_at`

func scanDigest(row pgx.Row) (digests.Digest, error) {
	var d digests.Digest
	err := row.Scan(&d.ID, &d.Frequency, &d.Trigger, &d.PeriodStart, &d.PeriodEnd, &d.Sections, &d.Emailed, &d.EmailError, &d.CreatedAt)
	if d.Sections == nil {
		d.Sections = []digests.Section{}
	}
	return d, err
}

// listDigests lists the newest 50 digests.
func (s *Server) listDigests(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `SELECT `+digestCols+` FROM digests WHERE workspace_id = $1 ORDER BY created_at DESC LIMIT 50`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (digests.Digest, error) { return scanDigest(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []digests.Digest{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getDigest(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if validUUID(id) {
		d, err := scanDigest(s.pool.QueryRow(r.Context(), `SELECT `+digestCols+` FROM digests WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID))
		if err == nil {
			writeJSON(w, http.StatusOK, d)
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "digest not found")
}

func digestFrequency(f string) (string, bool) {
	switch f {
	case "", digests.Weekly:
		return digests.Weekly, true
	case digests.Daily:
		return digests.Daily, true
	}
	return "", false
}

// previewDigest builds the digest of the period that ends now, without
// keeping or sending it.
func (s *Server) previewDigest(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	f, ok := digestFrequency(r.URL.Query().Get("frequency"))
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "frequency is daily or weekly")
		return
	}
	end := time.Now().UTC().Truncate(time.Minute)
	start := end.AddDate(0, 0, -7)
	if f == digests.Daily {
		start = end.AddDate(0, 0, -1)
	}
	sections, err := digests.Build(r.Context(), s.pool, a.WorkspaceID, digests.Period{Start: start, End: end})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"frequency": f, "period_start": start, "period_end": end, "sections": sections})
}

// requestDigest makes a digest of the period that ends now, kept, sent as
// a webhook and emailed like a scheduled one.
func (s *Server) requestDigest(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		Frequency string `json:"frequency"`
	}
	if !decode(w, r, &req) {
		return
	}
	f, ok := digestFrequency(req.Frequency)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "frequency is daily or weekly")
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := digests.Request(ctx, tx, a.WorkspaceID, f, a.ActorID, time.Now()); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "digest.requested", "workspace", a.WorkspaceID, map[string]any{"frequency": f})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}
