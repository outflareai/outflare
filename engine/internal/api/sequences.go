package api

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/allocation"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/missions"
	"github.com/outflareai/outflare/engine/internal/sending"
)

func (s *Server) routesSequences(mux *http.ServeMux) {
	read, manage := auth.ScopeSequencesRead, auth.ScopeSequencesManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/sequences", s.inProject(read, s.listSequences))
	mux.HandleFunc("POST /v1/workspaces/{ws}/sequences", s.inProject(manage, s.createSequence))
	mux.HandleFunc("GET /v1/workspaces/{ws}/sequences/{id}", s.inProject(read, s.getSequence))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/sequences/{id}", s.inProject(manage, s.projectSequence(s.updateSequence)))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/sequences/{id}", s.inProject(manage, s.projectSequence(s.deleteSequence)))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/sequences/{id}/steps", s.inProject(manage, s.projectSequence(s.putSteps)))
	mux.HandleFunc("POST /v1/workspaces/{ws}/sequences/{id}/preview", s.inProject(read, s.projectSequence(s.previewSequence)))
	mux.HandleFunc("GET /v1/workspaces/{ws}/sequences/{id}/enrollments", s.inProject(read, s.listEnrollments))
	mux.HandleFunc("POST /v1/workspaces/{ws}/sequences/{id}/enrollments", s.inProject(manage, s.projectSequence(s.enroll)))
	mux.HandleFunc("POST /v1/workspaces/{ws}/sequences/{id}/enrollments/{eid}/stop", s.inProject(manage, s.projectSequence(s.stopEnrollment)))
	mux.HandleFunc("GET /v1/workspaces/{ws}/messages", s.inWorkspace(read, s.listMessages))
	mux.HandleFunc("POST /v1/workspaces/{ws}/mailboxes/{id}/test-send", s.inWorkspace(auth.ScopeMailboxesManage, s.testSend))
	// Public: the links in sent emails.
	mux.HandleFunc("GET /u/{token}", s.unsubscribePage)
	mux.HandleFunc("POST /u/{token}", s.unsubscribe)
}

// ---- Sequences ----

type stepView struct {
	Position  int    `json:"position"`
	DelayDays int    `json:"delay_days"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
}

type sequenceView struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Status        string  `json:"status"`
	ProjectID     *string `json:"project_id"`
	BrandID       *string `json:"brand_id"`
	PoolID        *string `json:"pool_id"`
	Timezone      string  `json:"timezone"`
	SendDays      []int   `json:"send_days"`
	SendStart     string  `json:"send_start"`
	SendEnd       string  `json:"send_end"`
	DailyNewLimit *int    `json:"daily_new_limit"`
	StopOnReply   bool    `json:"stop_on_reply"`
	// Mailbox allocation.
	TeamID             *string         `json:"team_id"`
	Priority           string          `json:"priority"`
	TargetDaily        *int            `json:"target_daily"`
	MinDaily           *int            `json:"min_daily"`
	AllocationMode     *string         `json:"allocation_mode"`
	PreferredSenderIDs []string        `json:"preferred_sender_ids"`
	StartedByAgent     bool            `json:"started_by_agent"`
	MissionID          *string         `json:"mission_id"`
	Allocation         allocationBrief `json:"allocation"`
	// Optional link settings; see the tracking package.
	TrackClicks       bool `json:"track_clicks"`
	UnsubscribeHeader bool `json:"unsubscribe_header"`
	// Platform campaigns this sequence runs as; empty when Outflare sends.
	Links     []linkView     `json:"links"`
	Steps     []stepView     `json:"steps"`
	Stats     map[string]int `json:"stats"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// allocationBrief is the sequence's mailbox capacity as of the last plan.
type allocationBrief struct {
	Mailboxes   int     `json:"mailboxes"`
	ActiveDaily int     `json:"active_daily"`
	NeededDaily *int    `json:"needed_daily"`
	Note        string  `json:"note"`
	PlannedAt   *string `json:"planned_at"`
}

var priorityNames = []string{"low", "normal", "high"}

var enrollmentStatuses = []string{"active", "completed", "stopped", "unsubscribed", "bounced", "replied", "blocked", "failed"}

// loadSequences returns the workspace's sequences but archived ones, or the
// one with id. project filters to a project's sequences ("none": shared).
func (s *Server) loadSequences(ctx context.Context, ws string, id *string, project ...string) ([]sequenceView, error) {
	proj := ""
	if len(project) > 0 {
		proj = project[0]
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, status, project_id::text, sequences.brand_id::text, pool_id, timezone, send_days::int[], to_char(send_start, 'HH24:MI'), to_char(send_end, 'HH24:MI'),
		       daily_new_limit, stop_on_reply, created_at, updated_at,
		       team_id, priority, target_daily, min_daily, allocation_mode,
		       EXISTS (SELECT 1 FROM actors ac WHERE ac.id = sequences.created_by AND ac.type = 'api_token'),
		       COALESCE(st.active_daily, 0), st.needed_daily, COALESCE(st.note, ''), to_char(st.planned_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       (SELECT count(*)::int FROM mailbox_assignments ma WHERE ma.sequence_id = sequences.id AND ma.state = 'active'),
		       ARRAY(SELECT sender_id::text FROM sequence_senders ss WHERE ss.sequence_id = sequences.id ORDER BY sender_id),
		       sequences.mission_id::text
		FROM sequences LEFT JOIN allocation_status st ON st.sequence_id = sequences.id
		WHERE sequences.workspace_id = $1 AND (($2::uuid IS NULL AND sequences.status <> 'archived') OR sequences.id = $2)
		  AND ($3 = '' OR ($3 = 'none' AND sequences.project_id IS NULL) OR sequences.project_id::text = $3)
		ORDER BY sequences.created_at DESC`, ws, id, proj)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (sequenceView, error) {
		var v sequenceView
		var prio int16
		err := r.Scan(&v.ID, &v.Name, &v.Status, &v.ProjectID, &v.BrandID, &v.PoolID, &v.Timezone, &v.SendDays, &v.SendStart, &v.SendEnd,
			&v.DailyNewLimit, &v.StopOnReply, &v.CreatedAt, &v.UpdatedAt,
			&v.TeamID, &prio, &v.TargetDaily, &v.MinDaily, &v.AllocationMode, &v.StartedByAgent,
			&v.Allocation.ActiveDaily, &v.Allocation.NeededDaily, &v.Allocation.Note, &v.Allocation.PlannedAt,
			&v.Allocation.Mailboxes, &v.PreferredSenderIDs, &v.MissionID)
		v.Priority = priorityNames[prio]
		v.Steps = []stepView{}
		v.Links = []linkView{}
		v.Stats = map[string]int{"sent": 0, "contacted": 0}
		for _, st := range enrollmentStatuses {
			v.Stats[st] = 0
		}
		return v, err
	})
	if err != nil || len(list) == 0 {
		return list, err
	}
	byID := map[string]*sequenceView{}
	ids := make([]string, len(list))
	for i := range list {
		byID[list[i].ID] = &list[i]
		ids[i] = list[i].ID
	}
	links, err := s.loadLinks(ctx, ws, ids)
	if err != nil {
		return nil, err
	}
	for sid, l := range links {
		byID[sid].Links = l
	}
	rows, err = s.pool.Query(ctx, `SELECT sequence_id::text, position, delay_days, subject, body FROM sequence_steps WHERE sequence_id = ANY($1::uuid[]) ORDER BY position`, ids)
	if err != nil {
		return nil, err
	}
	var sid string
	var st stepView
	if _, err := pgx.ForEachRow(rows, []any{&sid, &st.Position, &st.DelayDays, &st.Subject, &st.Body}, func() error {
		byID[sid].Steps = append(byID[sid].Steps, st)
		return nil
	}); err != nil {
		return nil, err
	}
	var status string
	var n int
	rows, err = s.pool.Query(ctx, `SELECT sequence_id::text, status, count(*)::int FROM enrollments WHERE sequence_id = ANY($1::uuid[]) GROUP BY 1, 2`, ids)
	if err != nil {
		return nil, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&sid, &status, &n}, func() error {
		byID[sid].Stats[status] = n
		return nil
	}); err != nil {
		return nil, err
	}
	// contacted is leads emailed at least once: the reply rate is replied
	// over contacted, as on Reports.
	var contacted int
	rows, err = s.pool.Query(ctx, `SELECT sequence_id::text, count(*)::int, count(DISTINCT enrollment_id)::int FROM messages WHERE sequence_id = ANY($1::uuid[]) AND status = 'sent' GROUP BY 1`, ids)
	if err != nil {
		return nil, err
	}
	if _, err = pgx.ForEachRow(rows, []any{&sid, &n, &contacted}, func() error {
		byID[sid].Stats["sent"] = n
		byID[sid].Stats["contacted"] = contacted
		return nil
	}); err != nil {
		return nil, err
	}
	// Link settings, and leads who clicked a tracked link (scanner clicks
	// left out).
	var track, header bool
	rows, err = s.pool.Query(ctx, `
		SELECT q.id::text, q.track_clicks, q.unsubscribe_header,
		       (SELECT count(DISTINCT t.enrollment_id)::int FROM tracked_links t JOIN link_clicks c ON c.link_id = t.id AND NOT c.likely_bot WHERE t.sequence_id = q.id)
		FROM sequences q WHERE q.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	_, err = pgx.ForEachRow(rows, []any{&sid, &track, &header, &n}, func() error {
		byID[sid].TrackClicks, byID[sid].UnsubscribeHeader, byID[sid].Stats["clicked"] = track, header, n
		return nil
	})
	return list, err
}

func (s *Server) listSequences(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	project, ok := projectFilter(w, r)
	if !ok {
		return
	}
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	list, err := s.loadSequences(r.Context(), a.WorkspaceID, nil, project)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list = slices.DeleteFunc(list, func(q sequenceView) bool { return !brand.matches(q.BrandID) })
	if list == nil {
		list = []sequenceView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getSequence(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findSequence(w, r, a.WorkspaceID, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) findSequence(w http.ResponseWriter, r *http.Request, ws, id string) (*sequenceView, bool) {
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "sequence not found")
		return nil, false
	}
	list, err := s.loadSequences(r.Context(), ws, &id)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	if len(list) == 0 || !accessFrom(r.Context()).sees(list[0].ProjectID, list[0].BrandID) {
		writeError(w, r, http.StatusNotFound, "not_found", "sequence not found")
		return nil, false
	}
	return &list[0], true
}

// sequenceSettings are the fields shared by create and update. Pointers are
// "leave as is" when absent.
type sequenceSettings struct {
	Name          *string          `json:"name"`
	ProjectID     optional[string] `json:"project_id"`
	PoolID        optional[string] `json:"pool_id"`
	Timezone      *string          `json:"timezone"`
	SendDays      []int            `json:"send_days"`
	SendStart     *string          `json:"send_start"`
	SendEnd       *string          `json:"send_end"`
	DailyNewLimit optional[int]    `json:"daily_new_limit"`
	StopOnReply   *bool            `json:"stop_on_reply"`

	TeamID             optional[string] `json:"team_id"`
	Priority           *string          `json:"priority"`
	TargetDaily        optional[int]    `json:"target_daily"`
	MinDaily           optional[int]    `json:"min_daily"`
	AllocationMode     optional[string] `json:"allocation_mode"`
	PreferredSenderIDs []string         `json:"preferred_sender_ids"`

	TrackClicks       *bool `json:"track_clicks"`
	UnsubscribeHeader *bool `json:"unsubscribe_header"`
}

// saveLinkSettings applies the optional link settings, kept apart from the
// main insert and update so they stay simple.
func saveLinkSettings(ctx context.Context, tx pgx.Tx, id string, q *sequenceSettings) error {
	if q.TrackClicks == nil && q.UnsubscribeHeader == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE sequences SET track_clicks = COALESCE($2, track_clicks), unsubscribe_header = COALESCE($3, unsubscribe_header) WHERE id = $1`,
		id, q.TrackClicks, q.UnsubscribeHeader)
	return err
}

// touchesAllocation reports whether the request changes anything the
// allocator plans from.
func (q *sequenceSettings) touchesAllocation() bool {
	return q.ProjectID.Set || q.PoolID.Set || q.DailyNewLimit.Set || q.TeamID.Set || q.Priority != nil || q.TargetDaily.Set ||
		q.MinDaily.Set || q.AllocationMode.Set || q.PreferredSenderIDs != nil
}

func (q *sequenceSettings) priority() *int {
	if q.Priority == nil {
		return nil
	}
	p := slices.Index(priorityNames, *q.Priority)
	return &p
}

func (q *sequenceSettings) validate() error {
	if q.Name != nil {
		*q.Name = strings.TrimSpace(*q.Name)
		if *q.Name == "" || len(*q.Name) > 200 {
			return errInvalid("name must be 1 to 200 characters")
		}
	}
	if q.PoolID.Value != nil && !validUUID(*q.PoolID.Value) {
		return errInvalid("pool not found")
	}
	if q.ProjectID.Value != nil && !validUUID(*q.ProjectID.Value) {
		return errInvalid("project not found")
	}
	if q.Timezone != nil {
		if _, err := time.LoadLocation(*q.Timezone); err != nil || *q.Timezone == "" || *q.Timezone == "Local" {
			return errInvalid("timezone must be an IANA name such as Australia/Sydney")
		}
	}
	if q.SendDays != nil {
		if len(q.SendDays) == 0 {
			return errInvalid("send_days needs at least one day")
		}
		for _, d := range q.SendDays {
			if d < 1 || d > 7 {
				return errInvalid("send_days are ISO weekdays, 1 (Monday) to 7 (Sunday)")
			}
		}
		slices.Sort(q.SendDays)
		q.SendDays = slices.Compact(q.SendDays)
	}
	for _, t := range []*string{q.SendStart, q.SendEnd} {
		if t != nil {
			if _, err := time.Parse("15:04", *t); err != nil {
				return errInvalid("send_start and send_end are times like 09:00")
			}
		}
	}
	if q.DailyNewLimit.Value != nil && (*q.DailyNewLimit.Value < 1 || *q.DailyNewLimit.Value > 100000) {
		return errInvalid("daily_new_limit must be 1 to 100000, or null for no limit")
	}
	if q.TeamID.Value != nil && !validUUID(*q.TeamID.Value) {
		return errInvalid("team not found")
	}
	if q.Priority != nil && !slices.Contains(priorityNames, *q.Priority) {
		return errInvalid("priority is low, normal or high")
	}
	for _, n := range []optional[int]{q.TargetDaily, q.MinDaily} {
		if n.Value != nil && (*n.Value < 1 || *n.Value > 1000000) {
			return errInvalid("target_daily and min_daily must be 1 to 1000000, or null")
		}
	}
	if q.AllocationMode.Value != nil && *q.AllocationMode.Value != allocation.ModeSuggest && *q.AllocationMode.Value != allocation.ModeAutomatic {
		return errInvalid("allocation_mode is suggest, automatic, or null to follow the team")
	}
	if len(q.PreferredSenderIDs) > 100 {
		return errInvalid("preferred_sender_ids takes at most 100 senders")
	}
	for _, id := range q.PreferredSenderIDs {
		if !validUUID(id) {
			return errInvalid("sender not found")
		}
	}
	slices.Sort(q.PreferredSenderIDs)
	q.PreferredSenderIDs = slices.Compact(q.PreferredSenderIDs)
	return nil
}

func (s *Server) checkPool(ctx context.Context, tx pgx.Tx, ws string, poolID *string) error {
	if poolID == nil {
		return nil
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mailbox_pools WHERE id = $1 AND workspace_id = $2)`, *poolID, ws).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errInvalid("pool not found")
	}
	return nil
}

// checkAllocation validates the allocation fields against the workspace and
// applies the limits of the API token making the request: a token cannot
// raise a sequence above its max_priority, and a new sequence without a
// team goes to the token's default team.
func (s *Server) checkAllocation(ctx context.Context, tx pgx.Tx, a *access, q *sequenceSettings, creating bool) error {
	if q.TeamID.Value != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM teams WHERE id = $1 AND workspace_id = $2)`, *q.TeamID.Value, a.WorkspaceID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid("team not found")
		}
	}
	if len(q.PreferredSenderIDs) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM senders WHERE id = ANY($1::uuid[]) AND workspace_id = $2`, q.PreferredSenderIDs, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n != len(q.PreferredSenderIDs) {
			return errInvalid("sender not found")
		}
	}
	if a.ActorType != "api_token" {
		return nil
	}
	var maxPrio *int16
	var team *string
	if err := tx.QueryRow(ctx, `SELECT max_priority, default_team_id FROM api_tokens WHERE id = $1`, a.TokenID).Scan(&maxPrio, &team); err != nil {
		return err
	}
	if p := q.priority(); p != nil && maxPrio != nil && *p > int(*maxPrio) {
		return errInvalid("this API token can set priority up to " + priorityNames[*maxPrio])
	}
	if creating && q.Priority == nil && maxPrio != nil && *maxPrio < allocation.PriorityNormal {
		low := priorityNames[*maxPrio]
		q.Priority = &low
	}
	if creating && !q.TeamID.Set && team != nil {
		q.TeamID = optional[string]{Set: true, Value: team}
	}
	return nil
}

func writePreferred(ctx context.Context, tx pgx.Tx, seqID string, ids []string) error {
	if ids == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sequence_senders WHERE sequence_id = $1`, seqID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO sequence_senders (sequence_id, sender_id) SELECT $1, unnest($2::uuid[])`, seqID, ids)
	return err
}

func (s *Server) createSequence(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		sequenceSettings
		Steps []stepInput `json:"steps"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Steps != nil {
		if err := validateSteps(req.Steps); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	if a.ProjectID != nil {
		if req.ProjectID.Set && (req.ProjectID.Value == nil || *req.ProjectID.Value != *a.ProjectID) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a project token creates sequences only in its own project")
			return
		}
		req.ProjectID = optional[string]{Set: true, Value: a.ProjectID}
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.checkPool(ctx, tx, a.WorkspaceID, req.PoolID.Value); err != nil {
			return err
		}
		if err := s.checkAllocation(ctx, tx, a, &req.sequenceSettings, true); err != nil {
			return err
		}
		if err := checkProjectRef(ctx, tx, a.WorkspaceID, req.ProjectID.Value); err != nil {
			return err
		}
		var brand *string
		if a.ProjectID == nil {
			var err error
			if brand, err = brandWrite(ctx, tx, a, req.ProjectID.Value, nil); err != nil {
				return err
			}
		}
		days := req.SendDays
		if days == nil {
			days = []int{1, 2, 3, 4, 5}
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO sequences (workspace_id, name, pool_id, timezone, send_days, send_start, send_end, daily_new_limit, stop_on_reply,
			                       team_id, priority, target_daily, min_daily, allocation_mode, created_by, project_id, mission_id, brand_id)
			VALUES ($1, $2, $3, COALESCE($4, 'UTC'), $5, COALESCE($6::time, '09:00'), COALESCE($7::time, '17:00'), $8, COALESCE($9, true),
			        $10, COALESCE($11, 1), $12, $13, $14, $15, $16, NULLIF($17, '')::uuid, $18)
			RETURNING id`,
			a.WorkspaceID, *req.Name, req.PoolID.Value, req.Timezone, days, req.SendStart, req.SendEnd,
			req.DailyNewLimit.Value, req.StopOnReply,
			req.TeamID.Value, req.priority(), req.TargetDaily.Value, req.MinDaily.Value, req.AllocationMode.Value, a.ActorID,
			req.ProjectID.Value, missions.ID(ctx), brand).Scan(&id); err != nil {
			return err
		}
		if err := writePreferred(ctx, tx, id, req.PreferredSenderIDs); err != nil {
			return err
		}
		if err := saveLinkSettings(ctx, tx, id, &req.sequenceSettings); err != nil {
			return err
		}
		if err := writeSteps(ctx, tx, id, req.Steps); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "sequence.created", "sequence", id, map[string]any{"name": *req.Name})
	})
	if s.seqError(w, r, err) {
		return
	}
	if v, ok := s.findSequence(w, r, a.WorkspaceID, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

// projectSequence lets a project token or a brand-limited caller through
// to a sequence's handler only when it may see the sequence, and to a
// change only when it may change it; anyone else always passes.
func (s *Server) projectSequence(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		if a.ProjectID != nil || a.Brands != nil {
			if !validUUID(r.PathValue("id")) {
				writeError(w, r, http.StatusNotFound, "not_found", "sequence not found")
				return
			}
			var project, brand *string
			err := s.pool.QueryRow(r.Context(), `SELECT project_id::text, brand_id::text FROM sequences WHERE id = $1 AND workspace_id = $2`,
				r.PathValue("id"), a.WorkspaceID).Scan(&project, &brand)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(project, brand)) {
				writeError(w, r, http.StatusNotFound, "not_found", "sequence not found")
				return
			}
			if err != nil {
				s.internal(w, r, err)
				return
			}
			preview := strings.HasSuffix(r.URL.Path, "/preview")
			if r.Method != http.MethodGet && !preview && !a.writes(project, brand) {
				writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
				return
			}
		}
		next(w, r)
	}
}

// seqError writes err as a response and reports whether there was one.
func (s *Server) seqError(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv errInvalid
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "sequence not found")
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	default:
		var pgErr interface{ SQLState() string }
		if errors.As(err, &pgErr) && pgErr.SQLState() == "23514" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "send_start must be before send_end")
			return true
		}
		s.internal(w, r, err)
	}
	return true
}

func (s *Server) updateSequence(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "sequence not found")
		return
	}
	var req struct {
		sequenceSettings
		Status *string `json:"status"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Status != nil && !slices.Contains([]string{"active", "paused", "archived"}, *req.Status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status can be set to active, paused or archived")
		return
	}
	if a.ProjectID != nil && req.ProjectID.Set && (req.ProjectID.Value == nil || *req.ProjectID.Value != *a.ProjectID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a project token cannot move sequences between projects")
		return
	}
	if a.Brands != nil && req.ProjectID.Set && req.ProjectID.Value == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "you can only keep sequences in your brands' projects")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var old string
		err := tx.QueryRow(ctx, `SELECT status FROM sequences WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID).Scan(&old)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if req.Status != nil && *req.Status != old {
			if p, err := linkedPlatform(ctx, tx, id); err != nil {
				return err
			} else if p != "" {
				return errLinked(p)
			}
		}
		if err := s.checkPool(ctx, tx, a.WorkspaceID, req.PoolID.Value); err != nil {
			return err
		}
		if err := s.checkAllocation(ctx, tx, a, &req.sequenceSettings, false); err != nil {
			return err
		}
		if err := checkProjectRef(ctx, tx, a.WorkspaceID, req.ProjectID.Value); err != nil {
			return err
		}
		if a.Brands != nil && req.ProjectID.Value != nil {
			if _, err := brandWrite(ctx, tx, a, req.ProjectID.Value, nil); err != nil {
				return err
			}
		}
		var days []int
		if req.SendDays != nil {
			days = req.SendDays
		}
		_, err = tx.Exec(ctx, `
			UPDATE sequences SET
				name = COALESCE($3, name),
				pool_id = CASE WHEN $4 THEN $5::uuid ELSE pool_id END,
				timezone = COALESCE($6, timezone),
				send_days = COALESCE($7::smallint[], send_days),
				send_start = COALESCE($8::time, send_start),
				send_end = COALESCE($9::time, send_end),
				daily_new_limit = CASE WHEN $10 THEN $11::int ELSE daily_new_limit END,
				stop_on_reply = COALESCE($12, stop_on_reply),
				status = COALESCE($13, status),
				team_id = CASE WHEN $14 THEN $15::uuid ELSE team_id END,
				priority = COALESCE($16, priority),
				target_daily = CASE WHEN $17 THEN $18::int ELSE target_daily END,
				min_daily = CASE WHEN $19 THEN $20::int ELSE min_daily END,
				allocation_mode = CASE WHEN $21 THEN $22::text ELSE allocation_mode END,
				project_id = CASE WHEN $23 THEN $24::uuid ELSE project_id END,
				updated_at = now()
			WHERE id = $1 AND workspace_id = $2`,
			id, a.WorkspaceID, req.Name, req.PoolID.Set, req.PoolID.Value, req.Timezone, days, req.SendStart, req.SendEnd,
			req.DailyNewLimit.Set, req.DailyNewLimit.Value, req.StopOnReply, req.Status,
			req.TeamID.Set, req.TeamID.Value, req.priority(), req.TargetDaily.Set, req.TargetDaily.Value,
			req.MinDaily.Set, req.MinDaily.Value, req.AllocationMode.Set, req.AllocationMode.Value,
			req.ProjectID.Set, req.ProjectID.Value)
		if err != nil {
			return err
		}
		if err := saveLinkSettings(ctx, tx, id, &req.sequenceSettings); err != nil {
			return err
		}
		if err := writePreferred(ctx, tx, id, req.PreferredSenderIDs); err != nil {
			return err
		}
		if req.Status != nil && *req.Status == "active" && old != "active" {
			if err := checkActivation(ctx, tx, id); err != nil {
				return err
			}
		}
		if req.Status != nil || req.touchesAllocation() {
			if err := allocation.Request(ctx, tx, a.WorkspaceID); err != nil {
				return err
			}
		}
		action := "sequence.updated"
		if req.Status != nil && *req.Status != old {
			action = "sequence." + map[string]string{"active": "activated", "paused": "paused", "archived": "archived"}[*req.Status]
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "sequence", id, nil)
	})
	if s.seqError(w, r, err) {
		return
	}
	if req.Status != nil && *req.Status == "active" {
		// Plan now so the sequence has its mailboxes before the next send tick.
		if _, err := s.alloc.Run(ctx, a.WorkspaceID); err != nil {
			s.log.Error("allocation after activation", "sequence_id", id, "err", err)
		}
	}
	s.getSequence(w, r)
}

// checkActivation refuses to start a sequence that has nothing to send or
// no way to get mailboxes. It runs after the update, so it sees new values.
func checkActivation(ctx context.Context, tx pgx.Tx, id string) error {
	var steps int
	var demand, assigned bool
	if err := tx.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM sequence_steps WHERE sequence_id = s.id),
		       s.target_daily IS NOT NULL OR s.daily_new_limit IS NOT NULL,
		       EXISTS (SELECT 1 FROM mailbox_assignments WHERE sequence_id = s.id AND state = 'active')
		FROM sequences s WHERE s.id = $1`, id).Scan(&steps, &demand, &assigned); err != nil {
		return err
	}
	if steps == 0 {
		return errInvalid("add at least one step before activating")
	}
	if !demand && !assigned {
		return errInvalid("set a daily new-lead limit or a daily send target so Outflare knows how many mailboxes to allocate, or assign mailboxes by hand")
	}
	return nil
}

func (s *Server) deleteSequence(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "sequence not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var name, status string
		err := tx.QueryRow(ctx, `SELECT name, status FROM sequences WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID).Scan(&name, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if p, err := linkedPlatform(ctx, tx, id); err != nil {
			return err
		} else if p != "" {
			return errLinked(p)
		}
		if status == "active" {
			return errInvalid("pause the sequence before deleting it")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sequences WHERE id = $1`, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "sequence.deleted", "sequence", id, map[string]any{"name": name})
	})
	if s.seqError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Steps ----

type stepInput struct {
	DelayDays int    `json:"delay_days"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
}

const maxSteps = 20

func validateSteps(steps []stepInput) error {
	if len(steps) > maxSteps {
		return errInvalid(fmt.Sprintf("a sequence can have at most %d steps", maxSteps))
	}
	for i, st := range steps {
		switch {
		case i == 0 && strings.TrimSpace(st.Subject) == "":
			return errInvalid("the first step needs a subject; later steps without one reply in the same thread")
		case strings.TrimSpace(st.Body) == "":
			return errInvalid(fmt.Sprintf("step %d has no body", i+1))
		case len(st.Body) > 20000 || len(st.Subject) > 500:
			return errInvalid(fmt.Sprintf("step %d is too long", i+1))
		case st.DelayDays < 0 || st.DelayDays > 365:
			return errInvalid("delay_days must be 0 to 365")
		case i > 0 && st.DelayDays == 0:
			return errInvalid(fmt.Sprintf("step %d needs a delay of at least one day", i+1))
		case strings.Count(st.Body, "{{") != strings.Count(st.Body, "}}") || strings.Count(st.Subject, "{{") != strings.Count(st.Subject, "}}"):
			return errInvalid(fmt.Sprintf("step %d has an unclosed {{variable}}", i+1))
		}
	}
	return nil
}

func writeSteps(ctx context.Context, tx pgx.Tx, seqID string, steps []stepInput) error {
	if _, err := tx.Exec(ctx, `DELETE FROM sequence_steps WHERE sequence_id = $1`, seqID); err != nil {
		return err
	}
	for i, st := range steps {
		if _, err := tx.Exec(ctx, `INSERT INTO sequence_steps (sequence_id, position, delay_days, subject, body) VALUES ($1, $2, $3, $4, $5)`,
			seqID, i, st.DelayDays, strings.TrimSpace(st.Subject), st.Body); err != nil {
			return err
		}
	}
	return nil
}

// putSteps replaces a sequence's steps. Enrolled leads continue from the
// step number they reached; ones past the new last step complete.
func (s *Server) putSteps(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "sequence not found")
		return
	}
	var req struct {
		Steps []stepInput `json:"steps"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := validateSteps(req.Steps); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM sequences WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if p, err := linkedPlatform(ctx, tx, id); err != nil {
			return err
		} else if p != "" {
			return errLinked(p)
		}
		if status == "active" && len(req.Steps) == 0 {
			return errInvalid("an active sequence needs at least one step")
		}
		if err := writeSteps(ctx, tx, id, req.Steps); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "sequence.steps_updated", "sequence", id, map[string]any{"steps": len(req.Steps)})
	})
	if s.seqError(w, r, err) {
		return
	}
	s.getSequence(w, r)
}

// previewSequence renders every step for one lead, without sending.
func (s *Server) previewSequence(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	seq, ok := s.findSequence(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		LeadID string `json:"lead_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	l, ok := s.findLead(w, r, a.WorkspaceID, req.LeadID)
	if !ok {
		return
	}
	var senderName, senderEmail string
	err := s.pool.QueryRow(r.Context(), `
		SELECT display_name, email FROM mailboxes WHERE workspace_id = $1 AND ($2::uuid IS NULL OR pool_id = $2)
		ORDER BY status = 'active' DESC, email LIMIT 1`, a.WorkspaceID, seq.PoolID).Scan(&senderName, &senderEmail)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, err)
		return
	}
	vars := map[string]string{
		"email": l.Email, "first_name": l.FirstName, "last_name": l.LastName, "company": l.Company, "title": l.Title,
		"phone": l.Phone, "linkedin_url": l.LinkedInURL, "website": l.Website, "domain": l.Domain,
		"sender_name": senderName, "sender_first_name": strings.SplitN(senderName+" ", " ", 2)[0], "sender_email": senderEmail,
		"unsubscribe_url": strings.TrimRight(s.publicURL, "/") + "/u/preview",
	}
	for k, v := range l.Custom {
		if _, ok := vars[k]; !ok {
			vars[k] = v
		}
	}
	type rendered struct {
		Position int      `json:"position"`
		Subject  string   `json:"subject"`
		Body     string   `json:"body"`
		Missing  []string `json:"missing"`
	}
	out := []rendered{}
	thread := ""
	for _, st := range seq.Steps {
		subj := st.Subject
		if subj == "" {
			subj = "Re: " + thread
		}
		subj, m1 := sending.Render(subj, vars)
		body, m2 := sending.Render(st.Body, vars)
		if st.Position == 0 {
			thread = subj
		}
		missing := append(m1, m2...)
		if missing == nil {
			missing = []string{}
		}
		out = append(out, rendered{st.Position, subj, body, missing})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "suppressed": l.Suppressed})
}

// ---- Enrollments ----

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "sequence not found")
		return
	}
	var req struct {
		LeadIDs []string `json:"lead_ids"`
		ListID  *string  `json:"list_id"`
		// Leave out leads already active in another sequence, so nobody
		// gets two sequences at once. Default true.
		SkipActiveElsewhere *bool `json:"skip_active_elsewhere"`
	}
	if !decode(w, r, &req) {
		return
	}
	if (len(req.LeadIDs) == 0) == (req.ListID == nil) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send either lead_ids or list_id")
		return
	}
	if len(req.LeadIDs) > 10000 || slices.ContainsFunc(req.LeadIDs, func(s string) bool { return !validUUID(s) }) || (req.ListID != nil && !validUUID(*req.ListID)) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "lead_ids must be up to 10000 lead ids, or list_id a list id")
		return
	}
	skip := req.SkipActiveElsewhere == nil || *req.SkipActiveElsewhere
	ctx := r.Context()
	var res struct {
		Enrolled        int `json:"enrolled"`
		AlreadyEnrolled int `json:"already_enrolled"`
		Suppressed      int `json:"suppressed"`
		ActiveElsewhere int `json:"active_elsewhere"`
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sequences WHERE id = $1 AND workspace_id = $2 AND status <> 'archived')`, id, a.WorkspaceID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errNotFound
		}
		if p, err := linkedPlatform(ctx, tx, id); err != nil {
			return err
		} else if p != "" {
			return errLinked(p)
		}
		if a.ProjectID != nil && req.ListID != nil {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lead_lists WHERE id = $1 AND workspace_id = $2 AND project_id = $3)`,
				*req.ListID, a.WorkspaceID, *a.ProjectID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errInvalid("list_id is not a list in this token's project")
			}
		}
		if a.Brands != nil && req.ListID != nil && validUUID(*req.ListID) {
			var brand *string
			err := tx.QueryRow(ctx, `SELECT brand_id::text FROM lead_lists WHERE id = $1 AND workspace_id = $2`, *req.ListID, a.WorkspaceID).Scan(&brand)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(nil, brand)) {
				return errInvalid("list_id is not a list in your brands")
			}
			if err != nil {
				return err
			}
		}
		// Candidates: workspace leads from the ids or the list, classified in
		// one pass; only the eligible ones are inserted.
		err := tx.QueryRow(ctx, `
			WITH cand AS (
				SELECT l.id, l.email,
					EXISTS (SELECT 1 FROM enrollments e WHERE e.sequence_id = $1 AND e.lead_id = l.id) AS here,
					EXISTS (`+leads.SuppressedSQL("l.workspace_id", "l.email")+`) AS suppressed,
					EXISTS (SELECT 1 FROM enrollments e JOIN sequences q ON q.id = e.sequence_id
						WHERE e.lead_id = l.id AND e.status = 'active' AND e.sequence_id <> $1 AND q.status IN ('active', 'paused')) AS elsewhere
				FROM leads l
				WHERE l.workspace_id = $2 AND (
					l.id = ANY($3::uuid[]) OR
					($4::uuid IS NOT NULL AND EXISTS (SELECT 1 FROM lead_list_members m JOIN lead_lists ll ON ll.id = m.list_id
						WHERE m.lead_id = l.id AND m.list_id = $4 AND ll.workspace_id = $2)))
				  -- A project token reaches only leads on its project's lists.
				  AND ($6::uuid IS NULL OR EXISTS (SELECT 1 FROM lead_list_members m JOIN lead_lists ll ON ll.id = m.list_id
						WHERE m.lead_id = l.id AND ll.project_id = $6))
			), ins AS (
				INSERT INTO enrollments (workspace_id, sequence_id, lead_id, next_send_at)
				SELECT $2, $1, id, now() FROM cand
				WHERE NOT here AND NOT suppressed AND NOT (elsewhere AND $5)
				ON CONFLICT DO NOTHING
				RETURNING 1
			)
			SELECT (SELECT count(*) FROM ins),
			       count(*) FILTER (WHERE here),
			       count(*) FILTER (WHERE NOT here AND suppressed),
			       count(*) FILTER (WHERE NOT here AND NOT suppressed AND elsewhere AND $5)
			FROM cand`, id, a.WorkspaceID, req.LeadIDs, req.ListID, skip, a.ProjectID,
		).Scan(&res.Enrolled, &res.AlreadyEnrolled, &res.Suppressed, &res.ActiveElsewhere)
		if err != nil {
			return err
		}
		if res.Enrolled == 0 {
			return nil
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "sequence.leads_enrolled", "sequence", id, map[string]any{
			"enrolled": res.Enrolled, "suppressed": res.Suppressed, "active_elsewhere": res.ActiveElsewhere,
		})
	})
	if s.seqError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type enrollmentView struct {
	ID           string     `json:"id"`
	LeadID       string     `json:"lead_id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	Company      string     `json:"company"`
	Status       string     `json:"status"`
	StatusReason *string    `json:"status_reason"`
	NextStep     int        `json:"next_step"`
	NextSendAt   *time.Time `json:"next_send_at"`
	MailboxEmail *string    `json:"mailbox_email"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (s *Server) listEnrollments(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "sequence not found")
		return
	}
	if a.ProjectID != nil || a.Brands != nil {
		if _, ok := s.findSequence(w, r, a.WorkspaceID, id); !ok {
			return
		}
	}
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPage {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 500")
			return
		}
		limit = n
	}
	where := ` WHERE e.sequence_id = $1 AND e.workspace_id = $2`
	args := []any{id, a.WorkspaceID}
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	if v := q.Get("status"); v != "" {
		where += ` AND e.status = ` + arg(v)
	}
	if v := q.Get("after"); v != "" {
		where += ` AND lower(l.email) > ` + arg(strings.ToLower(v))
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT e.id, e.lead_id, l.email, trim(l.first_name || ' ' || l.last_name), l.company, e.status, e.status_reason,
		       e.next_step, e.next_send_at, m.email, e.created_at
		FROM enrollments e JOIN leads l ON l.id = e.lead_id LEFT JOIN mailboxes m ON m.id = e.mailbox_id`+where+`
		ORDER BY lower(l.email) LIMIT `+arg(limit+1), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[enrollmentView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) > limit {
		list = list[:limit]
		e := strings.ToLower(list[limit-1].Email)
		next = &e
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_after": next})
}

func (s *Server) stopEnrollment(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id, eid := r.PathValue("id"), r.PathValue("eid")
	if !validUUID(id) || !validUUID(eid) {
		writeError(w, r, http.StatusNotFound, "not_found", "enrollment not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE enrollments SET status = 'stopped', status_reason = 'stopped manually', next_send_at = NULL, updated_at = now()
			WHERE id = $1 AND sequence_id = $2 AND workspace_id = $3 AND status = 'active'`, eid, id, a.WorkspaceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "enrollment.stopped", "enrollment", eid, nil)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no active enrollment with that id")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Messages ----

type messageView struct {
	ID           string     `json:"id"`
	SequenceID   *string    `json:"sequence_id"`
	EnrollmentID *string    `json:"enrollment_id"`
	StepPosition *int       `json:"step_position"`
	LeadID       *string    `json:"lead_id"`
	MailboxID    *string    `json:"mailbox_id"`
	From         string     `json:"from"`
	To           string     `json:"to"`
	Subject      string     `json:"subject"`
	Body         string     `json:"body"`
	MessageID    string     `json:"message_id"`
	Status       string     `json:"status"`
	Error        *string    `json:"error"`
	CreatedAt    time.Time  `json:"created_at"`
	SentAt       *time.Time `json:"sent_at"`
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if a.ViewID != "" {
		// Sent emails, with their bodies, are email content: a support
		// view never shows it.
		writeError(w, r, http.StatusForbidden, "support_view_hidden", "sent emails are hidden while staff view this account")
		return
	}
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 200")
			return
		}
		limit = n
	}
	where := ` WHERE workspace_id = $1`
	args := []any{a.WorkspaceID}
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	for _, f := range []string{"sequence_id", "lead_id", "mailbox_id", "enrollment_id"} {
		if v := q.Get(f); v != "" {
			if !validUUID(v) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", f+" must be a UUID")
				return
			}
			where += ` AND ` + f + ` = ` + arg(v)
		}
	}
	if v := q.Get("status"); v != "" {
		where += ` AND status = ` + arg(v)
	}
	if v := q.Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "before must be an RFC 3339 time")
			return
		}
		where += ` AND created_at < ` + arg(t)
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	// A message belongs to its sequence's brand.
	where += sel.sql(`(SELECT q.brand_id FROM sequences q WHERE q.id = messages.sequence_id)`, &args)
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, sequence_id, enrollment_id, step_position, lead_id, mailbox_id, from_email, to_email, subject, body,
		       message_id, status, error, created_at, sent_at
		FROM messages`+where+` ORDER BY created_at DESC LIMIT `+arg(limit+1), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[messageView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) > limit {
		list = list[:limit]
		t := list[limit-1].CreatedAt.Format(time.RFC3339Nano)
		next = &t
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

// testSend sends one email from a mailbox now, through the send policy.
func (s *Server) testSend(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "mailbox not found")
		return
	}
	var req struct {
		To      string `json:"to"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if !decode(w, r, &req) {
		return
	}
	to, ok := validEmail(req.To)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a valid to address is required")
		return
	}
	if req.Subject == "" {
		req.Subject = "Outflare test email"
	}
	if req.Body == "" {
		req.Body = "This is a test email sent from Outflare to check that this mailbox can send."
	}
	msgID, err := s.send.TestSend(r.Context(), a.WorkspaceID, id, to, req.Subject, req.Body)
	var blocked sending.ErrBlocked
	var ce *mailbox.ConnError
	switch {
	case errors.Is(err, mailbox.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "mailbox not found")
		return
	case errors.As(err, &blocked):
		writeError(w, r, http.StatusConflict, "blocked", blocked.Error())
		return
	case errors.As(err, &ce):
		writeError(w, r, http.StatusBadGateway, "send_failed", ce.Error())
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	ctx := r.Context()
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox.test_sent", "mailbox", id, map[string]any{"to": to})
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message_id": msgID, "status": "sent"})
}

// ---- Unsubscribe (public) ----

var unsubscribeTmpl = template.Must(template.New("u").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Unsubscribe</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1rem;color:#222}
button{font:inherit;padding:.5rem 1rem;border-radius:.4rem;border:1px solid #222;background:#222;color:#fff;cursor:pointer}
@media(prefers-color-scheme:dark){body{background:#111;color:#eee}button{background:#eee;color:#111;border-color:#eee}}</style>
</head><body>
{{if .Done}}<h1>You're unsubscribed</h1><p>You won't receive any more of these emails.</p>
{{else if .Invalid}}<h1>Link not recognised</h1><p>This unsubscribe link is not valid. Reply to the email and ask to be removed, and the sender will take you off their list.</p>
{{else}}<h1>Unsubscribe?</h1><p>Stop receiving these emails.</p>
<form method="post"><button type="submit">Unsubscribe</button></form>{{end}}
</body></html>`))

func (s *Server) unsubscribePage(w http.ResponseWriter, r *http.Request) {
	// The page only asks: mail scanners follow links, and a GET that
	// unsubscribed would remove people who never clicked.
	// Lifecycle emails (internal/emails) get their preference page.
	if s.emails != nil {
		if id, ok := s.emails.ParseUnsubscribe(r.PathValue("token")); ok {
			s.preferencesPage(w, r, id, "")
			return
		}
	}
	_, ok := s.send.ParseUnsubscribeToken(r.PathValue("token"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
	}
	unsubscribeTmpl.Execute(w, map[string]bool{"Invalid": !ok}) //nolint:errcheck
}

// unsubscribe handles the page's button and RFC 8058 one-click POSTs from
// mail providers.
func (s *Server) unsubscribe(w http.ResponseWriter, r *http.Request) {
	// Lifecycle emails (internal/emails) share these links.
	if s.emails != nil {
		if id, ok := s.emails.ParseUnsubscribe(r.PathValue("token")); ok {
			s.savePreferences(w, r, id)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	id, ok := s.send.ParseUnsubscribeToken(r.PathValue("token"))
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		unsubscribeTmpl.Execute(w, map[string]bool{"Invalid": true}) //nolint:errcheck
		return
	}
	if err := s.send.Unsubscribe(r.Context(), id); err != nil {
		s.internal(w, r, err)
		return
	}
	unsubscribeTmpl.Execute(w, map[string]bool{"Done": true}) //nolint:errcheck
}
