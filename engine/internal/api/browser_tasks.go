package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/browser"
)

// Browser tasks (docs/browser-workers.md): bounded work a browser worker
// does for a brand on sites with no API. A task is a capability (the
// account, the hosts, what it may do, the approved content, ceilings);
// the worker enforces it. An agent creates one within its grants like
// any other change; the task runs in a job on a worker with a browser.

func (s *Server) routesBrowserTasks(mux *http.ServeMux) {
	read, manage := auth.ScopeBrowserRead, auth.ScopeBrowserManage
	mux.HandleFunc("GET /v1/brands/{brand}/browser-tasks", s.onBrand(read, s.listBrowserTasks))
	mux.HandleFunc("POST /v1/brands/{brand}/browser-tasks", s.onBrand(manage, s.createBrowserTask))
	mux.HandleFunc("GET /v1/brands/{brand}/browser-tasks/{id}", s.onBrand(read, s.getBrowserTask))
	mux.HandleFunc("POST /v1/brands/{brand}/browser-tasks/{id}/cancel", s.onBrand(manage, s.cancelBrowserTask))
}

type browserTaskView struct {
	ID         string            `json:"id"`
	BrandID    string            `json:"brand_id"`
	AccountID  *string           `json:"account_id"`
	Goal       string            `json:"goal"`
	Hosts      []string          `json:"hosts"`
	Operations []string          `json:"operations"`
	Content    map[string]string `json:"content"`
	MaxSteps   int               `json:"max_steps"`
	MaxMinutes int               `json:"max_minutes"`
	MaxCents   int64             `json:"max_cents"`
	Status     string            `json:"status"`
	Summary    string            `json:"summary"`
	FinalURL   string            `json:"final_url"`
	Evidence   map[string]any    `json:"evidence"`
	TakeoverID *string           `json:"takeover_id"`
	Runs       int               `json:"runs"`
	Steps      int               `json:"steps"`
	AICents    int64             `json:"ai_cents"`
	CreatedBy  *string           `json:"created_by"`
	CreatedAt  time.Time         `json:"created_at"`
	StartedAt  *time.Time        `json:"started_at"`
	FinishedAt *time.Time        `json:"finished_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	// Log is the task's steps, on one task.
	Log []browserStepView `json:"log,omitempty"`
}

type browserStepView struct {
	Run    int            `json:"run"`
	N      int            `json:"n"`
	Tool   string         `json:"tool"`
	Input  map[string]any `json:"input"`
	Result string         `json:"result"`
	At     time.Time      `json:"at"`
}

const browserTaskCols = `t.id::text, t.brand_id::text, t.account_id::text, t.goal, t.hosts, t.operations, t.content, t.max_steps, t.max_minutes, t.max_cents, t.status, t.summary, t.final_url, t.evidence, t.takeover_id::text,
	t.runs, t.steps, t.ai_cents, t.created_by::text, t.created_at, t.started_at, t.finished_at, t.updated_at`

func scanBrowserTask(row pgx.CollectableRow) (browserTaskView, error) {
	var v browserTaskView
	var content, evidence []byte
	err := row.Scan(&v.ID, &v.BrandID, &v.AccountID, &v.Goal, &v.Hosts, &v.Operations, &content, &v.MaxSteps, &v.MaxMinutes, &v.MaxCents, &v.Status, &v.Summary, &v.FinalURL, &evidence, &v.TakeoverID,
		&v.Runs, &v.Steps, &v.AICents, &v.CreatedBy, &v.CreatedAt, &v.StartedAt, &v.FinishedAt, &v.UpdatedAt)
	_ = json.Unmarshal(content, &v.Content)
	_ = json.Unmarshal(evidence, &v.Evidence)
	if v.Content == nil {
		v.Content = map[string]string{}
	}
	if v.Evidence == nil {
		v.Evidence = map[string]any{}
	}
	return v, err
}

func (s *Server) listBrowserTasks(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	where := `t.workspace_id = $1 AND t.brand_id = $2`
	args := []any{a.WorkspaceID, a.Brand}
	switch v := r.URL.Query().Get("status"); {
	case v == "":
	case slices.Contains([]string{"queued", "running", "waiting_person", "done", "failed", "cancelled"}, v):
		where += ` AND t.status = $3`
		args = append(args, v)
	case v == "open":
		where += ` AND t.status IN ('queued', 'running', 'waiting_person')`
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is queued, running, waiting_person, done, failed, cancelled or open")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+browserTaskCols+` FROM browser_tasks t WHERE `+where+` ORDER BY t.created_at DESC LIMIT 200`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, scanBrowserTask)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []browserTaskView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) loadBrowserTask(ctx context.Context, a *access, id string, withLog bool) (browserTaskView, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+browserTaskCols+` FROM browser_tasks t WHERE t.id = $1 AND t.workspace_id = $2 AND t.brand_id = $3`, id, a.WorkspaceID, a.Brand)
	if err != nil {
		return browserTaskView{}, err
	}
	v, err := pgx.CollectOneRow(rows, scanBrowserTask)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	if err != nil || !withLog {
		return v, err
	}
	lrows, err := s.pool.Query(ctx, `SELECT run, n, tool, input, result, at FROM browser_task_steps WHERE task_id = $1 ORDER BY run, n`, id)
	if err != nil {
		return v, err
	}
	v.Log, err = pgx.CollectRows(lrows, func(row pgx.CollectableRow) (browserStepView, error) {
		var st browserStepView
		var in []byte
		err := row.Scan(&st.Run, &st.N, &st.Tool, &in, &st.Result, &st.At)
		_ = json.Unmarshal(in, &st.Input)
		if st.Input == nil {
			st.Input = map[string]any{}
		}
		return st, err
	})
	if v.Log == nil {
		v.Log = []browserStepView{}
	}
	return v, err
}

func (s *Server) getBrowserTask(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "task not found")
		return
	}
	v, err := s.loadBrowserTask(r.Context(), a, id, true)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// createBrowserTask queues a task. The hosts are the ones given plus the
// account's own site and the sites of the identities it signs in through
// (its depends_on chain), so a social sign-in can reach the provider.
func (s *Server) createBrowserTask(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Goal       string            `json:"goal"`
		AccountID  *string           `json:"account_id"`
		Hosts      []string          `json:"hosts"`
		Operations []string          `json:"operations"`
		Content    map[string]string `json:"content"`
		MaxSteps   int               `json:"max_steps"`
		MaxMinutes int               `json:"max_minutes"`
		MaxCents   int64             `json:"max_cents"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Goal = strings.TrimSpace(req.Goal)
	hosts := map[string]bool{}
	for _, h := range req.Hosts {
		h = browser.NormalizeHost(h)
		if h == "" || strings.ContainsAny(h, " \t\n") || !strings.Contains(h, ".") && h != "localhost" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "hosts are host names, such as community.example.com")
			return
		}
		hosts[h] = true
	}
	if len(req.Operations) == 0 {
		req.Operations = []string{browser.OpRead, browser.OpAct}
		if req.AccountID != nil {
			req.Operations = append(req.Operations, browser.OpSignIn)
		}
	}
	for _, op := range req.Operations {
		if !slices.Contains(browser.Operations, op) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "operations are read, act and sign_in")
			return
		}
	}
	if !slices.Contains(req.Operations, browser.OpRead) {
		req.Operations = append([]string{browser.OpRead}, req.Operations...)
	}
	total := 0
	for k, v := range req.Content {
		total += len(v)
		if strings.TrimSpace(k) == "" || len(k) > 60 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "content names are 1 to 60 characters")
			return
		}
	}
	if req.MaxSteps == 0 {
		req.MaxSteps = 40
	}
	if req.MaxMinutes == 0 {
		req.MaxMinutes = 10
	}
	if req.MaxCents == 0 {
		req.MaxCents = 100
	}
	switch {
	case req.Goal == "" || len(req.Goal) > 4000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "goal is 1 to 4000 characters")
		return
	case len(hosts) == 0 && req.AccountID == nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "hosts lists the sites the task may use, or account_id names an account whose site it uses")
		return
	case len(hosts) > 20 || len(req.Content) > 20 || total > 50000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "at most 20 hosts and 20 approved texts of 50,000 characters in all")
		return
	case req.AccountID != nil && !validUUID(*req.AccountID):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id is one of the brand's accounts")
		return
	case slices.Contains(req.Operations, browser.OpSignIn) && req.AccountID == nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sign_in needs account_id: the account to sign in as")
		return
	case req.MaxSteps < 1 || req.MaxSteps > 200 || req.MaxMinutes < 1 || req.MaxMinutes > 60 || req.MaxCents < 1 || req.MaxCents > 10000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "max_steps is 1 to 200, max_minutes 1 to 60 and max_cents 1 to 10000")
		return
	}
	if req.Content == nil {
		req.Content = map[string]string{}
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if req.AccountID != nil {
			// The account's own site, and the sites of the identities it
			// signs in through, are in the boundary.
			rows, err := tx.Query(ctx, `WITH RECURSIVE chain AS (
					SELECT id, url, depends_on, status, 0 AS depth FROM brand_accounts WHERE id = $1 AND workspace_id = $2 AND brand_id = $3
					UNION ALL SELECT a.id, a.url, a.depends_on, a.status, c.depth + 1 FROM brand_accounts a JOIN chain c ON a.id = c.depends_on WHERE c.depth < 5)
				SELECT url, status, depth FROM chain`, *req.AccountID, a.WorkspaceID, a.Brand)
			if err != nil {
				return err
			}
			n := 0
			for rows.Next() {
				var u, status string
				var depth int
				if err := rows.Scan(&u, &status, &depth); err != nil {
					rows.Close()
					return err
				}
				n++
				if depth == 0 && (status == "archived" || status == "blocked") {
					rows.Close()
					return errInvalid("the account is " + status + "; a person unblocks it first")
				}
				if h := browser.NormalizeHost(u); h != "" {
					hosts[h] = true
				}
			}
			rows.Close()
			if n == 0 {
				return errInvalid("account_id is one of the brand's accounts")
			}
			if len(hosts) == 0 {
				return errInvalid("the account has no url; give hosts, or set the account's url")
			}
		}
		list := make([]string, 0, len(hosts))
		for h := range hosts {
			list = append(list, h)
		}
		slices.Sort(list)
		content, _ := json.Marshal(req.Content)
		if err := tx.QueryRow(ctx, `INSERT INTO browser_tasks (workspace_id, brand_id, account_id, goal, hosts, operations, content, max_steps, max_minutes, max_cents, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id::text`,
			a.WorkspaceID, a.Brand, req.AccountID, req.Goal, list, req.Operations, content, req.MaxSteps, req.MaxMinutes, req.MaxCents, actorRef(a)).Scan(&id); err != nil {
			return err
		}
		if err := browser.Enqueue(ctx, tx, a.WorkspaceID, id); err != nil {
			return err
		}
		props := map[string]any{"brand_id": a.Brand, "account": req.AccountID != nil, "hosts": len(list), "operations": req.Operations, "max_steps": req.MaxSteps, "max_minutes": req.MaxMinutes, "max_cents": req.MaxCents}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "browser_task.created", "browser_task", id,
			map[string]any{"brand_id": a.Brand, "account_id": req.AccountID, "hosts": list, "operations": req.Operations}, props)
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.loadBrowserTask(ctx, a, id, false)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// cancelBrowserTask stops a task: one waiting (queued or for a person)
// is cancelled at once; one running is told to stop at its next step.
func (s *Server) cancelBrowserTask(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "task not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM browser_tasks WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 FOR UPDATE`, id, a.WorkspaceID, a.Brand).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		switch status {
		case "queued", "waiting_person":
			_, err = tx.Exec(ctx, `UPDATE browser_tasks SET status = 'cancelled', summary = 'Cancelled by a person.', finished_at = now(), updated_at = now() WHERE id = $1`, id)
		case "running":
			_, err = tx.Exec(ctx, `UPDATE browser_tasks SET cancel_requested = true, updated_at = now() WHERE id = $1`, id)
		default:
			return errInvalid("the task is " + status + " already")
		}
		if err != nil {
			return err
		}
		props := map[string]any{"brand_id": a.Brand, "was": status}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "browser_task.cancelled", "browser_task", id, props, props)
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.loadBrowserTask(ctx, a, id, false)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}
