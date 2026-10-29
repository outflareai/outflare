package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/mailbox"
)

func (s *Server) routesMailboxes(mux *http.ServeMux) {
	read, manage := auth.ScopeMailboxesRead, auth.ScopeMailboxesManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/mailboxes", s.inBrand(read, s.listMailboxes))
	mux.HandleFunc("POST /v1/workspaces/{ws}/mailboxes", s.inWorkspace(manage, s.replans(s.createMailbox)))
	mux.HandleFunc("POST /v1/workspaces/{ws}/mailboxes/import", s.inWorkspace(manage, s.replans(s.importMailboxes)))
	mux.HandleFunc("GET /v1/workspaces/{ws}/mailboxes/{id}", s.inBrand(read, s.getMailbox))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/mailboxes/{id}", s.inWorkspace(manage, s.replans(s.updateMailbox)))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/mailboxes/{id}", s.inWorkspace(manage, s.replans(s.deleteMailbox)))
	mux.HandleFunc("POST /v1/workspaces/{ws}/mailboxes/{id}/verify", s.inWorkspace(manage, s.replans(s.verifyMailbox)))
	mux.HandleFunc("GET /v1/workspaces/{ws}/mailbox-pools", s.inBrand(read, s.listPools))
	mux.HandleFunc("POST /v1/workspaces/{ws}/mailbox-pools", s.inWorkspace(manage, s.createPool))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/mailbox-pools/{id}", s.inWorkspace(manage, s.deletePool))
}

// ---- Views ----

type mailboxView struct {
	ID             string     `json:"id"`
	Email          string     `json:"email"`
	Domain         string     `json:"domain"`
	DisplayName    string     `json:"display_name"`
	Provider       string     `json:"provider"`
	AuthMethod     string     `json:"auth_method"`
	SMTPHost       string     `json:"smtp_host"`
	SMTPPort       int        `json:"smtp_port"`
	SMTPUsername   string     `json:"smtp_username"`
	IMAPHost       string     `json:"imap_host"`
	IMAPPort       int        `json:"imap_port"`
	IMAPUsername   string     `json:"imap_username"`
	Status         string     `json:"status"`
	StatusReason   *string    `json:"status_reason"`
	DailyLimit     int        `json:"daily_limit"`
	MinGapSeconds  int        `json:"min_gap_seconds"`
	SMTPOK         *bool      `json:"smtp_ok"`
	IMAPOK         *bool      `json:"imap_ok"`
	LastVerifiedAt *time.Time `json:"last_verified_at"`
	LastSyncedAt   *time.Time `json:"last_synced_at"`
	SyncError      *string    `json:"sync_error"`
	Source         string     `json:"source"`
	BrandID        *string    `json:"brand_id"`
	PoolID         *string    `json:"pool_id"`
	PoolName       *string    `json:"pool_name"`
	// The sending server (shared SMTP host and IPs) for mailboxes outside
	// Google and Microsoft; null for those.
	ServerID   *string    `json:"server_id"`
	ServerHost *string    `json:"server_host"`
	Warmup     warmupView `json:"warmup"`
	// Mailbox allocation: who it sends as, where it sends now, and any lock.
	Sender            *mailboxSender     `json:"sender"`
	SuggestedSenderID *string            `json:"suggested_sender_id"`
	Assignment        *mailboxAssignment `json:"assignment"`
	Lock              *mailboxLock       `json:"lock"`
	// Where it is in its working life; only ready mailboxes send cold email.
	Lifecycle mailboxLifecycle `json:"lifecycle"`
	// False for a mailbox Outflare only knows from an outside platform:
	// it can be allocated to that platform's campaigns but Outflare cannot
	// send, sync or warm it up itself.
	HasLogin bool `json:"has_login"`
	// What each connected platform account reports about it.
	Platforms []mailboxPlatform `json:"platforms"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type mailboxLifecycle struct {
	Stage     string     `json:"stage"`
	Reason    string     `json:"reason"`
	Until     *time.Time `json:"until"`
	ChangedAt time.Time  `json:"changed_at"`
	AutoRest  bool       `json:"auto_rest"`
	PlanID    *string    `json:"plan_id"`
}

type mailboxSender struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Confirmed bool   `json:"confirmed"`
}

type mailboxAssignment struct {
	SequenceID     string  `json:"sequence_id"`
	SequenceName   string  `json:"sequence_name"`
	State          string  `json:"state"`
	NextSequenceID *string `json:"next_sequence_id"`
	Source         string  `json:"source"`
	Reason         string  `json:"reason"`
	// The platform it sends through, or null when Outflare sends.
	Platform  *string `json:"platform"`
	Confirmed bool    `json:"confirmed"`
}

type mailboxPlatform struct {
	IntegrationID string  `json:"integration_id"`
	Platform      string  `json:"platform"`
	Status        string  `json:"status"`
	StatusReason  *string `json:"status_reason"`
	DailyLimit    *int    `json:"daily_limit"`
	// WarmupEnabled is whether the platform's own warm-up is on for it.
	WarmupEnabled bool      `json:"warmup_enabled"`
	WarmupScore   *int      `json:"warmup_score"`
	Campaigns     int       `json:"campaigns"`
	ObservedAt    time.Time `json:"observed_at"`
}

type mailboxLock struct {
	Kind       string     `json:"kind"`
	SequenceID *string    `json:"sequence_id"`
	TeamID     *string    `json:"team_id"`
	Until      *time.Time `json:"until"`
	Note       string     `json:"note"`
}

// warmupView is a mailbox's warm-up settings. A connected sequencer runs
// the warm-up; the 7-day counts are from Outflare's own warm-up, which is
// gone, so they run down to zero.
type warmupView struct {
	Enabled     bool       `json:"enabled"`
	DailyTarget int        `json:"daily_target"`
	ReplyRate   int        `json:"reply_rate"`
	StartedAt   *time.Time `json:"started_at"`
	Sent7d      int        `json:"sent_7d"`
	Inbox7d     int        `json:"inbox_7d"`
	Spam7d      int        `json:"spam_7d"`
	Missing7d   int        `json:"missing_7d"`
	Failed7d    int        `json:"failed_7d"`
	// LastError is why the latest warm-up send failed, null once one
	// succeeds again.
	LastError *string `json:"last_error"`
	// Provider is the id of the connected sequencer that runs the
	// warm-up, or "outflare" when none does.
	Provider string `json:"provider"`
	// Platform is the sequencer's own view of its warm-up, when one runs
	// it: whether it shows warm-up on, and its warm-up score.
	Platform *warmupPlatform `json:"platform"`
	// WaitingOn is platform while a mailbox has warm-up on here but its
	// sequencer doesn't show it on yet, and null otherwise.
	WaitingOn *string `json:"waiting_on"`
	// Today was the day's volume on Outflare's own ramp; always 0.
	Today int `json:"today_target"`
	// InboxRate is the share of placed messages that reached the inbox,
	// null until there are any.
	InboxRate *float64 `json:"inbox_rate"`
}

type warmupPlatform struct {
	IntegrationID string     `json:"integration_id"`
	Name          string     `json:"name"`
	Enabled       bool       `json:"enabled"`
	Score         *int       `json:"score"`
	ObservedAt    *time.Time `json:"observed_at"`
}

// scanMailbox reads one row of mailboxSelect.
func scanMailbox(r pgx.CollectableRow) (mailboxView, error) {
	var m mailboxView
	w := &m.Warmup
	var senderID, senderName *string
	var senderConfirmed bool
	var asg struct{ SequenceID, SequenceName, State, NextSequenceID, Source, Reason, Platform *string }
	var asgConfirmed bool
	var wp struct {
		id, name *string
		enabled  *bool
		score    *int
		observed *time.Time
	}
	var lock struct {
		Kind, SequenceID, TeamID *string
		Until                    *time.Time
		Note                     *string
	}
	err := r.Scan(&m.ID, &m.Email, &m.Domain, &m.DisplayName, &m.Provider, &m.AuthMethod,
		&m.SMTPHost, &m.SMTPPort, &m.SMTPUsername, &m.IMAPHost, &m.IMAPPort, &m.IMAPUsername,
		&m.Status, &m.StatusReason, &m.DailyLimit, &m.MinGapSeconds, &m.SMTPOK, &m.IMAPOK, &m.LastVerifiedAt, &m.LastSyncedAt, &m.SyncError,
		&m.Source, &m.BrandID, &m.PoolID, &m.PoolName, &m.ServerID, &m.ServerHost,
		&w.Enabled, &w.DailyTarget, &w.ReplyRate, &w.StartedAt, &w.Sent7d, &w.Inbox7d, &w.Spam7d, &w.Missing7d, &w.Failed7d, &w.LastError,
		&wp.id, &wp.name, &wp.enabled, &wp.score, &wp.observed,
		&m.CreatedAt, &m.UpdatedAt,
		&senderID, &senderName, &senderConfirmed, &m.SuggestedSenderID,
		&asg.SequenceID, &asg.SequenceName, &asg.State, &asg.NextSequenceID, &asg.Source, &asg.Reason,
		&lock.Kind, &lock.SequenceID, &lock.TeamID, &lock.Until, &lock.Note,
		&asg.Platform, &asgConfirmed, &m.HasLogin, &m.Platforms,
		&m.Lifecycle.Stage, &m.Lifecycle.Reason, &m.Lifecycle.Until, &m.Lifecycle.ChangedAt, &m.Lifecycle.AutoRest, &m.Lifecycle.PlanID)
	if senderID != nil {
		m.Sender = &mailboxSender{ID: *senderID, Name: *senderName, Confirmed: senderConfirmed}
	}
	if asg.SequenceID != nil {
		m.Assignment = &mailboxAssignment{SequenceID: *asg.SequenceID, SequenceName: *asg.SequenceName, State: *asg.State,
			NextSequenceID: asg.NextSequenceID, Source: *asg.Source, Reason: *asg.Reason, Platform: asg.Platform, Confirmed: asgConfirmed}
	}
	if lock.Kind != nil {
		m.Lock = &mailboxLock{Kind: *lock.Kind, SequenceID: lock.SequenceID, TeamID: lock.TeamID, Until: lock.Until, Note: *lock.Note}
	}
	w.Provider = "outflare"
	if wp.id != nil {
		w.Provider = *wp.id
		w.Platform = &warmupPlatform{IntegrationID: *wp.id, Name: deref(wp.name), Enabled: wp.enabled != nil && *wp.enabled,
			Score: wp.score, ObservedAt: wp.observed}
	}
	if w.Enabled && (w.Platform == nil || !w.Platform.Enabled) {
		waiting := "platform"
		w.WaitingOn = &waiting
	}
	if placed := w.Inbox7d + w.Spam7d + w.Missing7d; placed > 0 {
		rate := float64(w.Inbox7d) / float64(placed)
		w.InboxRate = &rate
	}
	return m, err
}

// Credentials are never selected here, so they cannot leak into a response.
const mailboxSelect = `
	SELECT m.id, m.email, m.domain, m.display_name, m.provider, m.auth_method,
	       m.smtp_host, m.smtp_port, m.smtp_username, m.imap_host, m.imap_port, m.imap_username,
	       m.status, m.status_reason, m.daily_limit, m.min_gap_seconds, m.smtp_ok, m.imap_ok, m.last_verified_at, m.last_synced_at, m.sync_error,
	       m.source, m.brand_id::text, m.pool_id, p.name, m.server_id, sv.host,
	       m.warmup_enabled, m.warmup_daily_target, m.warmup_reply_rate, m.warmup_started_at,
	       COALESCE(w.sent, 0), COALESCE(w.inbox, 0), COALESCE(w.spam, 0), COALESCE(w.missing, 0),
	       COALESCE(w.failed, 0), w.last_error,
	       m.warmup_integration_id::text, wi.name, wpl.warmup_enabled, wpl.warmup_score, wpl.observed_at,
	       m.created_at, m.updated_at,
	       m.sender_id::text, sn.name, m.sender_confirmed_at IS NOT NULL, m.suggested_sender_id::text,
	       ma.sequence_id::text, sq.name, ma.state, ma.next_sequence_id::text, ma.source, ma.reason,
	       lk.kind, lk.sequence_id::text, lk.team_id::text, lk.until, lk.note,
	       ai.name, ma.link_id IS NULL OR ma.confirmed_at IS NOT NULL, m.has_login,
	       COALESCE((SELECT jsonb_agg(jsonb_build_object('integration_id', mp.integration_id, 'platform', pi.name, 'status', mp.status,
	                 'status_reason', mp.status_reason, 'daily_limit', mp.daily_limit, 'warmup_enabled', mp.warmup_enabled, 'warmup_score', mp.warmup_score,
	                 'campaigns', cardinality(mp.campaign_ids), 'observed_at', mp.observed_at) ORDER BY pi.name)
	                 FROM mailbox_platforms mp JOIN integrations pi ON pi.id = mp.integration_id WHERE mp.mailbox_id = m.id), '[]'),
	       m.lifecycle_stage, m.stage_reason, m.stage_until, m.stage_changed_at, m.auto_rest, m.infra_plan_id::text
	FROM mailboxes m LEFT JOIN mailbox_pools p ON p.id = m.pool_id
	LEFT JOIN sending_servers sv ON sv.id = m.server_id
	LEFT JOIN senders sn ON sn.id = m.sender_id
	LEFT JOIN mailbox_assignments ma ON ma.mailbox_id = m.id
	LEFT JOIN sequences sq ON sq.id = ma.sequence_id
	LEFT JOIN campaign_links al ON al.id = ma.link_id LEFT JOIN integrations ai ON ai.id = al.integration_id
	LEFT JOIN mailbox_locks lk ON lk.mailbox_id = m.id AND (lk.until IS NULL OR lk.until > now())
	LEFT JOIN integrations wi ON wi.id = m.warmup_integration_id
	LEFT JOIN mailbox_platforms wpl ON wpl.mailbox_id = m.id AND wpl.integration_id = m.warmup_integration_id
	LEFT JOIN LATERAL (
		SELECT count(*) FILTER (WHERE status = 'sent') AS sent,
		       count(*) FILTER (WHERE landed = 'inbox') AS inbox,
		       count(*) FILTER (WHERE landed = 'spam') AS spam,
		       count(*) FILTER (WHERE landed = 'missing') AS missing,
		       count(*) FILTER (WHERE status = 'failed') AS failed,
		       (array_agg(error ORDER BY created_at DESC) FILTER (WHERE status IN ('sent', 'failed')))[1] AS last_error
		FROM warmup_messages WHERE from_mailbox_id = m.id AND created_at > now() - interval '7 days'
	) w ON true`

func (s *Server) listMailboxes(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	sql := mailboxSelect + ` WHERE m.workspace_id = $1`
	args := []any{a.WorkspaceID}
	if v := q.Get("status"); v != "" {
		args = append(args, v)
		sql += ` AND m.status = $` + strconv.Itoa(len(args))
	}
	if v := q.Get("stage"); v != "" {
		args = append(args, v)
		sql += ` AND m.lifecycle_stage = $` + strconv.Itoa(len(args))
	}
	if v := q.Get("pool_id"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "pool_id must be a UUID")
			return
		}
		args = append(args, v)
		sql += ` AND m.pool_id = $` + strconv.Itoa(len(args))
	}
	if v := q.Get("domain"); v != "" {
		args = append(args, strings.ToLower(v))
		sql += ` AND m.domain = $` + strconv.Itoa(len(args))
	}
	if v := q.Get("server_id"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "server_id must be a UUID")
			return
		}
		args = append(args, v)
		sql += ` AND m.server_id = $` + strconv.Itoa(len(args))
	}
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	sql += brand.sql("m.brand_id", &args)
	rows, err := s.pool.Query(r.Context(), sql+` ORDER BY lower(m.email)`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, scanMailbox)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getMailbox(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	m, ok := s.findMailbox(w, r, a.WorkspaceID, r.PathValue("id"))
	if ok {
		writeJSON(w, http.StatusOK, m)
	}
}

func (s *Server) findMailbox(w http.ResponseWriter, r *http.Request, ws, id string) (*mailboxView, bool) {
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "mailbox not found")
		return nil, false
	}
	rows, err := s.pool.Query(r.Context(), mailboxSelect+` WHERE m.workspace_id = $1 AND m.id = $2`, ws, id)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	m, err := pgx.CollectExactlyOneRow(rows, scanMailbox)
	if err == nil && !accessFrom(r.Context()).sees(m.BrandID) {
		err = pgx.ErrNoRows
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "mailbox not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return &m, true
}

// ---- Adding mailboxes ----

// maxImportBytes bounds a CSV upload; 5000 rows fit comfortably.
const maxImportBytes = 5 << 20

func (s *Server) importMailboxes(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "text/csv" && mt != "text/plain" {
		writeError(w, r, http.StatusUnsupportedMediaType, "invalid_request", "send the CSV file as the body with Content-Type: text/csv")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportBytes))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the file is larger than 5 MB")
		return
	}
	rows, bad, err := mailbox.ParseCSV(strings.NewReader(string(body)))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if pool := strings.TrimSpace(r.URL.Query().Get("pool")); pool != "" {
		for i := range rows {
			if rows[i].Pool == "" {
				rows[i].Pool = pool
			}
		}
	}
	s.addMailboxes(w, r, a, "csv", rows, bad, func(res mailbox.ImportResult) {
		writeJSON(w, http.StatusOK, res)
	})
}

type createMailboxRequest struct {
	Email        string `json:"email"`
	DisplayName  string `json:"display_name"`
	Provider     string `json:"provider"`
	Password     string `json:"password"`
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPUsername string `json:"smtp_username"`
	SMTPPassword string `json:"smtp_password"`
	IMAPHost     string `json:"imap_host"`
	IMAPPort     int    `json:"imap_port"`
	IMAPUsername string `json:"imap_username"`
	IMAPPassword string `json:"imap_password"`
	Pool         string `json:"pool"`
	DailyLimit   int    `json:"daily_limit"`
}

func (s *Server) createMailbox(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req createMailboxRequest
	if !decode(w, r, &req) {
		return
	}
	itoa := func(n int) string {
		if n == 0 {
			return ""
		}
		return strconv.Itoa(n)
	}
	row, err := mailbox.RowFromFields(map[string]string{
		"email": req.Email, "name": req.DisplayName, "provider": req.Provider, "password": req.Password,
		"smtp_host": req.SMTPHost, "smtp_port": itoa(req.SMTPPort), "smtp_username": req.SMTPUsername, "smtp_password": req.SMTPPassword,
		"imap_host": req.IMAPHost, "imap_port": itoa(req.IMAPPort), "imap_username": req.IMAPUsername, "imap_password": req.IMAPPassword,
		"pool": req.Pool, "daily_limit": itoa(req.DailyLimit),
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	s.addMailboxes(w, r, a, "api", []mailbox.Row{row}, nil, func(res mailbox.ImportResult) {
		if len(res.Errors) > 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", res.Errors[0].Error)
			return
		}
		m, ok := s.findMailbox(w, r, a.WorkspaceID, res.IDs[0])
		if !ok {
			return
		}
		status := http.StatusOK
		if res.Created == 1 {
			status = http.StatusCreated
		}
		writeJSON(w, status, m)
	})
}

// addMailboxes upserts rows in one transaction with an audit event, then
// hands the result to respond.
func (s *Server) addMailboxes(w http.ResponseWriter, r *http.Request, a *access, source string, rows []mailbox.Row, bad []mailbox.RowError, respond func(mailbox.ImportResult)) {
	if s.mail == nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "mailboxes are not configured on this server")
		return
	}
	res, err := s.importRows(r.Context(), a, source, rows, bad)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	respond(res)
}

// importRows prepares and upserts rows in one transaction with an audit
// event. bad carries rows already rejected, reported in the result.
func (s *Server) importRows(ctx context.Context, a *access, source string, rows []mailbox.Row, bad []mailbox.RowError) (mailbox.ImportResult, error) {
	rows, unresolved := s.mail.Prepare(ctx, a.Test, rows)
	bad = append(bad, unresolved...)
	var res mailbox.ImportResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if res, err = s.mail.Import(ctx, tx, a.WorkspaceID, source, rows); err != nil {
			return err
		}
		res.Errors = bad
		if res.Created+res.Updated == 0 {
			return nil
		}
		action, target := "mailboxes.imported", ""
		if source == "api" || source == "oauth" {
			action, target = "mailbox.added", res.IDs[0]
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "mailbox", target, map[string]any{
			"created": res.Created, "updated": res.Updated, "failed": len(res.Errors), "source": source,
		})
	})
	if res.Errors == nil {
		res.Errors = []mailbox.RowError{}
	}
	return res, err
}

// ---- Changing mailboxes ----

// optional tells an absent JSON field apart from an explicit null.
type optional[T any] struct {
	Set   bool
	Value *T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

func (s *Server) updateMailbox(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "mailbox not found")
		return
	}
	var req struct {
		DisplayName *string          `json:"display_name"`
		DailyLimit  *int             `json:"daily_limit"`
		MinGap      *int             `json:"min_gap_seconds"`
		PoolID      optional[string] `json:"pool_id"`
		Status      *string          `json:"status"`
		Warmup      *warmupSettings  `json:"warmup"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Warmup != nil {
		if msg := req.Warmup.validate(); msg != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
			return
		}
	}
	if req.DailyLimit != nil && (*req.DailyLimit < 1 || *req.DailyLimit > 500) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "daily_limit must be 1 to 500")
		return
	}
	if req.MinGap != nil && (*req.MinGap < 0 || *req.MinGap > 3600) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "min_gap_seconds must be 0 to 3600")
		return
	}
	if req.PoolID.Value != nil && !validUUID(*req.PoolID.Value) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "pool not found")
		return
	}
	if req.Status != nil && *req.Status != "paused" && *req.Status != "active" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status can be set to paused, or to active to resume")
		return
	}

	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var oldStatus string
		err := tx.QueryRow(ctx, `SELECT status FROM mailboxes WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID).Scan(&oldStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if req.PoolID.Value != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mailbox_pools WHERE id = $1 AND workspace_id = $2)`, *req.PoolID.Value, a.WorkspaceID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errInvalid("pool not found")
			}
		}
		// Resuming re-verifies: the mailbox only sends again once its
		// connection checks pass.
		newStatus := oldStatus
		if req.Status != nil {
			switch {
			case *req.Status == "paused":
				newStatus = "paused"
			case oldStatus == "paused" || oldStatus == "error":
				newStatus = "verifying"
			}
		}
		_, err = tx.Exec(ctx, `
			UPDATE mailboxes SET
				display_name = COALESCE($3, display_name),
				daily_limit = COALESCE($4, daily_limit),
				pool_id = CASE WHEN $5 THEN $6 ELSE pool_id END,
				status = $7,
				status_reason = CASE WHEN $7 = status THEN status_reason ELSE NULL END,
				min_gap_seconds = COALESCE($8, min_gap_seconds),
				updated_at = now()
			WHERE id = $1 AND workspace_id = $2`,
			id, a.WorkspaceID, req.DisplayName, req.DailyLimit, req.PoolID.Set, req.PoolID.Value, newStatus, req.MinGap)
		if err != nil {
			return err
		}
		if newStatus == "verifying" && oldStatus != "verifying" {
			if err := s.mail.QueueVerify(ctx, tx, a.WorkspaceID, id); err != nil {
				return err
			}
		}
		if req.Warmup != nil {
			msg, err := s.checkProvider(ctx, tx, a.WorkspaceID, req.Warmup, []string{id})
			if err != nil {
				return err
			}
			if msg != "" {
				return errInvalid(msg)
			}
			if _, err := s.resolveWarmup(ctx, tx, a.WorkspaceID, req.Warmup, []string{id}); err != nil {
				return err
			}
			if _, err := req.Warmup.apply(ctx, tx, a.WorkspaceID, []string{id}); err != nil {
				return err
			}
		}
		data := map[string]any{}
		if req.Warmup != nil {
			data["warmup"] = req.Warmup
		}
		if req.DisplayName != nil {
			data["display_name"] = *req.DisplayName
		}
		if req.DailyLimit != nil {
			data["daily_limit"] = *req.DailyLimit
		}
		if req.MinGap != nil {
			data["min_gap_seconds"] = *req.MinGap
		}
		if req.PoolID.Set {
			data["pool_id"] = req.PoolID.Value
		}
		if newStatus != oldStatus {
			data["status"] = newStatus
		}
		action := "mailbox.updated"
		switch {
		case newStatus == "paused" && oldStatus != "paused":
			action = "mailbox.paused"
		case oldStatus == "paused" && newStatus != "paused":
			action = "mailbox.resumed"
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "mailbox", id, data)
	})
	var inv errInvalid
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "mailbox not found")
		return
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	s.getMailbox(w, r)
}

type errInvalid string

func (e errInvalid) Error() string { return string(e) }

func (s *Server) deleteMailbox(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "mailbox not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var email string
		err := tx.QueryRow(ctx, `DELETE FROM mailboxes WHERE id = $1 AND workspace_id = $2 RETURNING email`, id, a.WorkspaceID).Scan(&email)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox.deleted", "mailbox", id, map[string]any{"email": email})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "mailbox not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) verifyMailbox(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "mailbox not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var domain string
		err := tx.QueryRow(ctx, `
			UPDATE mailboxes SET status = CASE WHEN status = 'paused' THEN status ELSE 'verifying' END, updated_at = now()
			WHERE id = $1 AND workspace_id = $2 RETURNING domain`, id, a.WorkspaceID).Scan(&domain)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if err := s.mail.QueueVerify(ctx, tx, a.WorkspaceID, id); err != nil {
			return err
		}
		return s.mail.QueueDomainCheck(ctx, tx, a.WorkspaceID, domain)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "mailbox not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}

// ---- Pools ----

type poolView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Mailboxes int       `json:"mailboxes"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) listPools(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `
		SELECT p.id, p.name, count(m.id)::int, p.created_at
		FROM mailbox_pools p LEFT JOIN mailboxes m ON m.pool_id = p.id
		WHERE p.workspace_id = $1 GROUP BY p.id ORDER BY lower(p.name)`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[poolView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) createPool(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name must be 1 to 100 characters")
		return
	}
	ctx := r.Context()
	p := poolView{Name: name}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO mailbox_pools (workspace_id, name) VALUES ($1, $2) RETURNING id, created_at`,
			a.WorkspaceID, name).Scan(&p.ID, &p.CreatedAt)
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox_pool.created", "mailbox_pool", p.ID, map[string]any{"name": name})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeError(w, r, http.StatusConflict, "conflict", "a pool with that name already exists")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) deletePool(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "pool not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `DELETE FROM mailbox_pools WHERE id = $1 AND workspace_id = $2 RETURNING name`, id, a.WorkspaceID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox_pool.deleted", "mailbox_pool", id, map[string]any{"name": name})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "pool not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
