package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
)

// Background work runs as jobs on the Postgres queue. These routes let a
// caller that got a 202 see how that work is going, and an agent see what
// is still running, without a status endpoint per feature.
func (s *Server) routesJobs(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/jobs", s.inWorkspace(auth.ScopeWorkspaceRead, s.listJobs))
	mux.HandleFunc("GET /v1/workspaces/{ws}/jobs/{id}", s.inWorkspace(auth.ScopeWorkspaceRead, s.getJob))
}

type jobView struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	MaxAttempts int       `json:"max_attempts"`
	Error       *string   `json:"error"`
	RunAt       time.Time `json:"run_at"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// jobRef is what a 202 answer carries to point at its job.
func jobRef(ws string, id int64) map[string]any {
	jid := "job_" + strconv.FormatInt(id, 10)
	return map[string]any{"id": jid, "url": "/v1/workspaces/" + ws + "/jobs/" + jid}
}

const jobCols = `id, kind, CASE status WHEN 'dead' THEN 'failed' ELSE status END, attempts, max_attempts,
	left(last_error, 500), run_at, created_at, updated_at`

func scanJob(row pgx.Row) (jobView, error) {
	var v jobView
	var id int64
	err := row.Scan(&id, &v.Kind, &v.Status, &v.Attempts, &v.MaxAttempts, &v.Error, &v.RunAt, &v.CreatedAt, &v.UpdatedAt)
	v.ID = "job_" + strconv.FormatInt(id, 10)
	return v, err
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id, err := strconv.ParseInt(strings.TrimPrefix(r.PathValue("id"), "job_"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "not_found", "job not found")
		return
	}
	v, err := scanJob(s.pool.QueryRow(r.Context(), `SELECT `+jobCols+` FROM jobs WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "job not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	status := q.Get("status")
	if status != "" && status != "queued" && status != "running" && status != "succeeded" && status != "failed" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is queued, running, succeeded or failed")
		return
	}
	if status == "failed" {
		status = "dead"
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 || limit > 200 {
		limit = 50
	}
	cur, ok := pageCursor(w, r, pageBefore)
	if !ok {
		return
	}
	before, _ := strconv.ParseInt(strings.TrimPrefix(cur, "job_"), 10, 64)
	rows, err := s.pool.Query(r.Context(), `SELECT `+jobCols+` FROM jobs
		WHERE workspace_id = $1 AND ($2 = '' OR status = $2) AND ($3 = '' OR kind = $3) AND ($4 = 0 OR id < $4)
		ORDER BY id DESC LIMIT $5`, a.WorkspaceID, status, q.Get("kind"), before, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (jobView, error) { return scanJob(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) == limit {
		next = &list[len(list)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}

// PruneJobs deletes finished jobs after 14 days, a batch at a time, and
// returns how many it deleted. Jobs with an idempotency key stay, because
// the key is what stops that work being queued twice, except the kinds in
// prunableKeyedJobs: one runs per event or per batch of events, so they
// grow with traffic, and their keys name an id made in the same
// transaction (an event, a batch) or a time slot long past, so nothing
// asks for them again after a day, let alone 14.
func (s *Server) PruneJobs(ctx context.Context) (int64, error) {
	var total int64
	for range 100 {
		tag, err := s.pool.Exec(ctx, `DELETE FROM jobs WHERE id IN (
			SELECT id FROM jobs WHERE status IN ('succeeded', 'dead') AND updated_at < now() - interval '14 days'
				AND (idempotency_key IS NULL OR kind = ANY($1))
			LIMIT 5000)`, prunableKeyedJobs)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < 5000 {
			break
		}
	}
	return total, nil
}

// prunableKeyedJobs are the job kinds with an idempotency key that
// PruneJobs still deletes once finished; see PruneJobs. Add a kind only
// when nothing could queue the same key again after it finished.
var prunableKeyedJobs = []string{
	jobEventsProcess,            // "batch:<new id>", "more:<brand>:<now>", "stale:<brand>:<5-minute slot>", "replay:<new id>"
	customers.JobMetricEvent,    // "metric-event:<event id>", queued where the event is stored
	customers.JobLifecycleEvent, // "lifecycle-event:<event id>"
	customers.JobJourneyEvent,   // "journey-event:<event id>"
	customers.JobRuleEvent,      // "rule-event:<event id>"
	customers.JobRuleAudience,   // "rule-audience:<list>:<change>:<time>:<offset>"
}
