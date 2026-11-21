package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Support imports: a brand's conversations in another support tool
// (Intercom, with its AI agent Fin) copied into the support inbox and kept
// in step, so Outflare holds the same history as if the team answered
// here. Copied conversations are read-only: they are answered in the tool.
//
// A sync works in passes. Each pass pages through the conversations the
// tool changed since the last pass started (all of them, or those since
// history_from, the first time), one job per page, and reads in full only
// those changed since Outflare last copied them. Between passes the
// scheduler (RunSupportImports) starts the next one every few minutes.

const (
	jobSupportImport = "support.import"
	// supportImportPage is how many changed conversations one job reads.
	supportImportPage = 50
	// supportImportEvery is how often a caught-up import looks again.
	supportImportEvery = 5 * time.Minute
	// supportImportOverlap is how far back each pass looks before the last
	// one started, so a change the tool saved late isn't missed.
	supportImportOverlap = 2 * time.Minute
	// A pass with no step for this long is stuck (a job lost with its
	// server) and is started again.
	supportImportStuck = 30 * time.Minute
)

type supportImportView struct {
	ID            string     `json:"id"`
	BrandID       string     `json:"brand_id"`
	IntegrationID string     `json:"integration_id"`
	Source        string     `json:"source"`
	Status        string     `json:"status"`
	HistoryFrom   *time.Time `json:"history_from"`
	SyncedThrough *time.Time `json:"synced_through"`
	Conversations int        `json:"conversations"`
	Messages      int        `json:"messages"`
	LastSyncedAt  *time.Time `json:"last_synced_at"`
	Error         *string    `json:"error"`
	ErrorCode     *string    `json:"error_code"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

const supportImportSelect = `SELECT id::text, brand_id::text, integration_id::text, source, status, history_from, synced_through,
	conversations, messages, last_synced_at, error, error_code, created_at, updated_at FROM support_imports`

func scanSupportImport(row pgx.CollectableRow) (supportImportView, error) {
	var v supportImportView
	err := row.Scan(&v.ID, &v.BrandID, &v.IntegrationID, &v.Source, &v.Status, &v.HistoryFrom, &v.SyncedThrough,
		&v.Conversations, &v.Messages, &v.LastSyncedAt, &v.Error, &v.ErrorCode, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (s *Server) routesSupportImports(mux *http.ServeMux) {
	read, manage := auth.ScopeInboxRead, auth.ScopeInboxManage
	base := "/v1/brands/{brand}/support/imports"
	mux.HandleFunc("GET "+base, s.onBrand(read, s.listSupportImports))
	mux.HandleFunc("POST "+base, s.onBrand(manage, s.createSupportImport))
	mux.HandleFunc("GET "+base+"/{id}", s.onBrand(read, s.getSupportImport))
	mux.HandleFunc("PATCH "+base+"/{id}", s.onBrand(manage, s.updateSupportImport))
	mux.HandleFunc("POST "+base+"/{id}/sync", s.onBrand(manage, s.syncSupportImport))
	mux.HandleFunc("DELETE "+base+"/{id}", s.onBrand(manage, s.deleteSupportImport))
}

func (s *Server) registerSupportImportJobs(q *jobs.Queue) {
	q.Handle(jobSupportImport, s.supportImportJob)
}

// watchSupportConnections starts copying conversations as soon as a support
// tool is connected for one brand. A workspace connection waits for a
// person to pick the brand under Support settings.
func (s *Server) watchSupportConnections() {
	if s.integrations == nil || s.integrations.Registry == nil {
		return
	}
	s.integrations.OnConnect = append(s.integrations.OnConnect, func(ctx context.Context, tx pgx.Tx, i connectors.Integration) error {
		d, ok := s.integrations.Registry.Definition(i.Connector)
		if !ok || !d.Has(connectors.CapImportSupport) || i.BrandID == nil {
			return nil
		}
		id, made, err := insertSupportImport(ctx, tx, i.WorkspaceID, *i.BrandID, i.ID, i.Connector, nil, nil)
		if err != nil || !made {
			return err
		}
		return record(ctx, tx, nil, i.WorkspaceID, "support_import.created", "support_import", id,
			map[string]any{"source": i.Connector, "integration_id": i.ID, "brand_id": *i.BrandID, "automatic": true})
	})
}

// insertSupportImport adds an import and queues its first pass, unless the
// connection has one already (made is false then).
func insertSupportImport(ctx context.Context, tx pgx.Tx, ws, brand, integration, source string, historyFrom *time.Time, by *string) (string, bool, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO support_imports (workspace_id, brand_id, integration_id, source, history_from, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (integration_id) DO NOTHING RETURNING id::text`,
		ws, brand, integration, source, historyFrom, by).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, queueSupportImport(ctx, tx, ws, id, "start")
}

// queueSupportImport queues one step; key keeps a step from running twice.
func queueSupportImport(ctx context.Context, tx pgx.Tx, ws, id, key string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobSupportImport, WorkspaceID: &ws, MaxAttempts: 3,
		Payload: map[string]string{"import_id": id}, IdempotencyKey: jobSupportImport + ":" + id + ":" + key})
	return err
}

func (s *Server) listSupportImports(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), supportImportSelect+` WHERE workspace_id = $1 AND brand_id = $2 ORDER BY created_at`, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, scanSupportImport)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []supportImportView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) supportImport(ctx context.Context, q pgx.Tx, a *access, id string) (supportImportView, error) {
	if !validUUID(id) {
		return supportImportView{}, errNotFound
	}
	var rows pgx.Rows
	var err error
	if q != nil {
		rows, err = q.Query(ctx, supportImportSelect+` WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 FOR UPDATE`, id, a.WorkspaceID, a.Brand)
	} else {
		rows, err = s.pool.Query(ctx, supportImportSelect+` WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`, id, a.WorkspaceID, a.Brand)
	}
	if err != nil {
		return supportImportView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanSupportImport)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

func (s *Server) supportImportError(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv errInvalid
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "support import not found")
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	default:
		s.internal(w, r, err)
	}
	return true
}

func (s *Server) getSupportImport(w http.ResponseWriter, r *http.Request) {
	v, err := s.supportImport(r.Context(), nil, accessFrom(r.Context()), r.PathValue("id"))
	if s.supportImportError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) createSupportImport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		IntegrationID string     `json:"integration_id"`
		HistoryFrom   *time.Time `json:"history_from"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !validUUID(req.IntegrationID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "integration_id is a connection's id")
		return
	}
	if req.HistoryFrom != nil && req.HistoryFrom.After(time.Now()) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "history_from is in the past, or null for all history")
		return
	}
	list, err := s.integrations.List(ctx, s.pool, a.WorkspaceID, "")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var conn *connectors.Integration
	for _, i := range connectors.ForBrand(list, a.Brand) {
		if i.ID == req.IntegrationID {
			conn = &i
			break
		}
	}
	if conn == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "no connection with that id serves this brand")
		return
	}
	if d, ok := s.integrations.Registry.Definition(conn.Connector); !ok || !d.Has(connectors.CapImportSupport) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", conn.Name+" can't copy support conversations")
		return
	}
	var by *string
	if a.UserID != "" {
		by = &a.UserID
	}
	var v supportImportView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		id, made, err := insertSupportImport(ctx, tx, a.WorkspaceID, a.Brand, conn.ID, conn.Connector, req.HistoryFrom, by)
		if err != nil {
			return err
		}
		if !made {
			return errConflict
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "support_import.created", "support_import", id,
			map[string]any{"source": conn.Connector, "integration_id": conn.ID, "brand_id": a.Brand, "all_history": req.HistoryFrom == nil}); err != nil {
			return err
		}
		v, err = s.supportImport(ctx, tx, a, id)
		return err
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "support_import_exists", "this connection already copies its conversations into a brand")
		return
	}
	if s.supportImportError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// updateSupportImport pauses or resumes copying.
func (s *Server) updateSupportImport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		Paused *bool `json:"paused"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Paused == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "paused is true or false")
		return
	}
	var v supportImportView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := s.supportImport(ctx, tx, a, r.PathValue("id"))
		if err != nil {
			return err
		}
		paused := cur.Status == "paused"
		if paused == *req.Paused {
			v = cur
			return nil
		}
		action := "support_import.paused"
		if *req.Paused {
			_, err = tx.Exec(ctx, `UPDATE support_imports SET status = 'paused', updated_at = now() WHERE id = $1`, cur.ID)
		} else {
			action = "support_import.resumed"
			// Carry on from where it stopped: the pass it was in, if any.
			_, err = tx.Exec(ctx, `UPDATE support_imports SET status = 'syncing', error = NULL, error_code = NULL, updated_at = now() WHERE id = $1`, cur.ID)
			if err == nil {
				err = queueSupportImport(ctx, tx, a.WorkspaceID, cur.ID, "resume:"+time.Now().UTC().Format(time.RFC3339Nano))
			}
		}
		if err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "support_import", cur.ID, map[string]any{"source": cur.Source}); err != nil {
			return err
		}
		v, err = s.supportImport(ctx, tx, a, cur.ID)
		return err
	})
	if s.supportImportError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// syncSupportImport looks for changes now instead of waiting for the next
// scheduled pass.
func (s *Server) syncSupportImport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var v supportImportView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := s.supportImport(ctx, tx, a, r.PathValue("id"))
		if err != nil {
			return err
		}
		if cur.Status == "paused" {
			return errInvalid("the import is paused: resume it to sync")
		}
		if cur.Status != "syncing" {
			if _, err := tx.Exec(ctx, `UPDATE support_imports SET status = 'syncing', error = NULL, error_code = NULL, updated_at = now() WHERE id = $1`, cur.ID); err != nil {
				return err
			}
			if err := queueSupportImport(ctx, tx, a.WorkspaceID, cur.ID, "now:"+time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "support_import.sync_requested", "support_import", cur.ID, map[string]any{"source": cur.Source}); err != nil {
			return err
		}
		v, err = s.supportImport(ctx, tx, a, cur.ID)
		return err
	})
	if s.supportImportError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}

// deleteSupportImport stops copying. The conversations copied so far stay
// in the inbox.
func (s *Server) deleteSupportImport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := s.supportImport(ctx, tx, a, r.PathValue("id"))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM support_imports WHERE id = $1`, cur.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support_import.deleted", "support_import", cur.ID,
			map[string]any{"source": cur.Source, "conversations": cur.Conversations})
	})
	if s.supportImportError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- The sync ----

// importRun is one import as a step works on it.
type importRun struct {
	id, ws, brand, integration, source, app string
	historyFrom, syncedThrough, passStarted *time.Time
	cursor                                  string
	// agents caches support_external_agents ids by kind:id, with the
	// Outflare user each one is.
	agents map[string]agentRef
}

type agentRef struct {
	id     string
	userID *string
}

func (s *Server) supportImportJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		ID string `json:"import_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return err
	}
	if job.WorkspaceID == nil {
		return errors.New("no workspace")
	}
	run := &importRun{id: p.ID, ws: *job.WorkspaceID, agents: map[string]agentRef{}}
	var status string
	var cursor *string
	err := s.pool.QueryRow(ctx, `SELECT brand_id::text, integration_id::text, source, external_app, status, history_from, synced_through, pass_started_at, cursor
		FROM support_imports WHERE id = $1 AND workspace_id = $2`, run.id, run.ws).
		Scan(&run.brand, &run.integration, &run.source, &run.app, &status, &run.historyFrom, &run.syncedThrough, &run.passStarted, &cursor)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && status == "paused" {
		return nil // removed or paused since it was queued
	}
	if err != nil {
		return err
	}
	if cursor != nil {
		run.cursor = *cursor
	}
	start := time.Now()
	outcome, err := s.supportImportStep(ctx, run)
	if err != nil {
		metrics.SupportImportStep(run.source, "failed", time.Since(start))
		return s.supportImportFailed(ctx, run, err, job.Attempt >= job.MaxAttempts)
	}
	metrics.SupportImportStep(run.source, outcome, time.Since(start))
	return nil
}

// supportImportFailed records why a step failed. A refused key or an
// invalid request won't fix itself: the import waits as failed, and the
// scheduler tries again later. Anything else is retried by the queue.
func (s *Server) supportImportFailed(ctx context.Context, run *importRun, cause error, last bool) error {
	code := "internal"
	var ce *connectors.Error
	if errors.As(cause, &ce) {
		code = ce.ErrorCode()
	}
	permanent := code == "connector_auth" || code == "connector_invalid"
	track.Fail(ctx, s.log, track.Event{Name: "support_import.sync_failed", WorkspaceID: run.ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"source": run.source, "permanent": permanent}}, code, cause)
	if !permanent && !last {
		return cause
	}
	msg := "The sync failed. It is tried again in a few minutes."
	if ce != nil {
		msg = ce.Message
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE support_imports SET status = 'failed', error = left($2, 1000), error_code = $3, last_synced_at = now(), updated_at = now()
			WHERE id = $1 AND status <> 'paused'`, run.id, msg, code)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return record(ctx, tx, nil, run.ws, "support_import.failed", "support_import", run.id,
			map[string]any{"source": run.source, "error_code": code})
	})
}

// supportImportStep reads one page of changed conversations and stores
// them. It returns page (more to read, the next step is queued) or synced.
func (s *Server) supportImportStep(ctx context.Context, run *importRun) (string, error) {
	o, err := s.integrations.Open(ctx, s.pool, run.ws, run.integration)
	if err != nil {
		return "", err
	}
	imp, ok := o.Connector.(connectors.SupportImporter)
	if !ok {
		return "", connectors.Errf(connectors.ErrInvalid, "%s can't copy support conversations", o.Name)
	}
	// A new pass: who the tool's account is, and its teammates and teams.
	if run.passStarted == nil || run.app == "" {
		acct, err := imp.Test(ctx, o.Client, o.Creds)
		if err != nil {
			return "", err
		}
		agents, err := imp.SupportAgents(ctx, o.Client, o.Creds)
		if err != nil {
			return "", err
		}
		now := time.Now().UTC()
		if run.passStarted == nil {
			run.passStarted, run.cursor = &now, ""
		}
		run.app = acct.ID
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			for _, ag := range agents {
				if _, err := run.agent(ctx, tx, ag.Kind, ag.ID, ag.Name, ag.Email); err != nil {
					return err
				}
			}
			_, err := tx.Exec(ctx, `UPDATE support_imports SET external_app = $2, pass_started_at = $3, cursor = NULLIF($4, ''), status = 'syncing', updated_at = now()
				WHERE id = $1`, run.id, run.app, run.passStarted, run.cursor)
			return err
		})
		if err != nil {
			return "", err
		}
	}
	since := time.Time{}
	switch {
	case run.syncedThrough != nil:
		since = *run.syncedThrough
	case run.historyFrom != nil:
		since = *run.historyFrom
	}
	refs, next, err := imp.SupportChanged(ctx, o.Client, o.Creds, since, run.cursor, supportImportPage)
	if err != nil {
		return "", err
	}
	added, messages := 0, 0
	for _, ref := range refs {
		result, n, err := s.importSupportConversation(ctx, run, imp, o, ref)
		if err != nil {
			metrics.SupportImportConversation(run.source, "error")
			return "", err
		}
		metrics.SupportImportConversation(run.source, result)
		if result != "unchanged" {
			added++
		}
		messages += n
	}
	metrics.SupportImportMessages(run.source, messages)
	outcome := "page"
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM support_imports WHERE id = $1 FOR UPDATE`, run.id).Scan(&status); err != nil {
			return err
		}
		if status == "paused" {
			// Keep its place; resuming carries on from here.
			outcome = "paused"
			_, err := tx.Exec(ctx, `UPDATE support_imports SET cursor = NULLIF($2, ''), updated_at = now() WHERE id = $1`, run.id, next)
			return err
		}
		if next != "" {
			if _, err := tx.Exec(ctx, `UPDATE support_imports SET cursor = $2, messages = messages + $3, status = 'syncing', updated_at = now() WHERE id = $1`,
				run.id, next, messages); err != nil {
				return err
			}
			return queueSupportImport(ctx, tx, run.ws, run.id, run.passStarted.Format(time.RFC3339Nano)+":"+next)
		}
		outcome = "synced"
		through := run.passStarted.Add(-supportImportOverlap)
		if _, err := tx.Exec(ctx, `UPDATE support_imports SET cursor = NULL, pass_started_at = NULL, synced_through = $2, status = 'idle',
			messages = messages + $3, error = NULL, error_code = NULL, last_synced_at = now(), updated_at = now(),
			conversations = (SELECT count(*) FROM support_conversations c WHERE c.workspace_id = support_imports.workspace_id
				AND c.source = support_imports.source AND c.external_app = support_imports.external_app)
			WHERE id = $1`, run.id, through, messages); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	// One event per page that changed something, never one per
	// conversation, so a big history doesn't flood webhooks and analytics.
	if added > 0 {
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			return recordWithProps(ctx, tx, nil, run.ws, "support_import.synced", "support_import", run.id,
				map[string]any{"source": run.source, "conversations": added, "messages": messages, "brand_id": run.brand},
				map[string]any{"source": run.source, "conversations": added, "messages": messages})
		})
	}
	return outcome, err
}

// importSupportConversation copies one conversation, unless Outflare has
// it as the tool last changed it. It returns created, updated or unchanged,
// and how many messages it added.
func (s *Server) importSupportConversation(ctx context.Context, run *importRun, imp connectors.SupportImporter, o *connectors.Opened, ref connectors.SupportRef) (string, int, error) {
	var have *time.Time
	err := s.pool.QueryRow(ctx, `SELECT external_updated_at FROM support_conversations
		WHERE workspace_id = $1 AND source = $2 AND external_app = $3 AND external_id = $4`, run.ws, run.source, run.app, ref.ID).Scan(&have)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", 0, err
	}
	if have != nil && !ref.UpdatedAt.IsZero() && !have.Before(ref.UpdatedAt) {
		return "unchanged", 0, nil
	}
	cv, err := imp.SupportConversation(ctx, o.Client, o.Creds, ref.ID)
	if err != nil {
		var ce *connectors.Error
		if errors.As(err, &ce) && ce.Kind == connectors.ErrInvalid {
			return "unchanged", 0, nil // deleted in the tool since the search
		}
		return "", 0, err
	}
	// The customer: found by their id in the tool, else read from the tool
	// and found or added by email.
	identity := ""
	var contact *connectors.SupportContact
	if cv.ContactID != "" {
		identity = run.app + ":" + cv.ContactID
		holders, err := customers.HoldersIn(ctx, s.pool, run.ws, run.brand, run.source, identity)
		if err != nil {
			return "", 0, err
		}
		if len(holders) == 0 {
			ct, ok, err := imp.SupportContact(ctx, o.Client, o.Creds, cv.ContactID)
			if err != nil {
				return "", 0, err
			}
			if ok {
				contact = &ct
			}
		}
	}
	var result string
	var added int
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		result, added, err = run.store(ctx, tx, cv, identity, contact)
		return err
	})
	return result, added, err
}

// person returns the customer's record: the one holding their id in the
// tool, else the one with their email (or a new one), else a new one known
// only by the tool's id.
func (run *importRun) person(ctx context.Context, tx pgx.Tx, identity string, ct *connectors.SupportContact) (*string, error) {
	if identity == "" {
		return nil, nil
	}
	holders, err := customers.HoldersIn(ctx, tx, run.ws, run.brand, run.source, identity)
	if err != nil {
		return nil, err
	}
	if len(holders) > 0 {
		return &holders[0], nil
	}
	if ct != nil && ct.Email != "" {
		ids, err := customers.EnsurePeopleIn(ctx, tx, run.ws, run.brand, "first_party", run.source, []customers.Person{{Email: ct.Email, Name: ct.Name}})
		if err != nil {
			return nil, err
		}
		if id, ok := ids[ct.Email]; ok {
			if _, err := tx.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, brand_id, namespace, value, verified, source)
				VALUES ($1, $2, $3, $4, $5, false, $4) ON CONFLICT DO NOTHING`, run.ws, id, run.brand, run.source, identity); err != nil {
				return nil, err
			}
			return &id, nil
		}
	}
	id, err := customers.EnsurePersonIn(ctx, tx, run.ws, run.brand, run.source, identity, false, "")
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// agent returns the support_external_agents row for a teammate, team or
// bot, adding it (and finding the Outflare user with its email) the first
// time.
func (run *importRun) agent(ctx context.Context, tx pgx.Tx, kind, id, name, email string) (agentRef, error) {
	if kind == "" || id == "" {
		return agentRef{}, nil
	}
	key := kind + ":" + id
	if a, ok := run.agents[key]; ok {
		return a, nil
	}
	var a agentRef
	err := tx.QueryRow(ctx, `
		INSERT INTO support_external_agents (workspace_id, brand_id, source, external_app, kind, external_id, name, user_id)
		VALUES ($1, $2, $3, $4, $5, $6, left($7, 300),
			(SELECT u.id FROM users u JOIN memberships m ON m.user_id = u.id WHERE m.workspace_id = $1 AND $8 <> '' AND lower(u.email) = lower($8) LIMIT 1))
		ON CONFLICT (workspace_id, source, external_app, kind, external_id) DO UPDATE SET
			name = CASE WHEN EXCLUDED.name <> '' THEN EXCLUDED.name ELSE support_external_agents.name END,
			user_id = COALESCE(EXCLUDED.user_id, support_external_agents.user_id), updated_at = now()
		RETURNING id::text, user_id::text`, run.ws, run.brand, run.source, run.app, kind, id, name, email).Scan(&a.id, &a.userID)
	if err != nil {
		return a, err
	}
	run.agents[key] = a
	return a, nil
}

// store writes one conversation and its messages, as the tool has them.
func (run *importRun) store(ctx context.Context, tx pgx.Tx, cv connectors.SupportConversation, identity string, ct *connectors.SupportContact) (string, int, error) {
	record, err := run.person(ctx, tx, identity, ct)
	if err != nil {
		return "", 0, err
	}
	assignee, err := run.agent(ctx, tx, "admin", cv.AssigneeID, "", "")
	if err != nil {
		return "", 0, err
	}
	team, err := run.agent(ctx, tx, "team", cv.TeamID, "", "")
	if err != nil {
		return "", 0, err
	}
	first, last := cv.CreatedAt, cv.CreatedAt
	lastFrom := "customer"
	for _, m := range cv.Messages {
		if m.Kind == "note" {
			continue
		}
		if first.IsZero() || m.At.Before(first) {
			first = m.At
		}
		if !m.At.Before(last) {
			last = m.At
			lastFrom = map[bool]string{true: "customer", false: "team"}[m.Kind == "in"]
		}
	}
	if first.IsZero() {
		first = time.Now()
	}
	if last.IsZero() {
		last = first
	}
	updated := cv.UpdatedAt
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	var rating *int
	var ratingRemark *string
	var ratedAt *time.Time
	if cv.Rating != nil {
		rating, ratingRemark, ratedAt = &cv.Rating.Score, &cv.Rating.Remark, &cv.Rating.At
		if cv.Rating.At.IsZero() {
			ratedAt = nil
		}
	}
	ai := connectors.SupportAI{Sources: []connectors.SupportAISource{}}
	if cv.AI != nil {
		ai = *cv.AI
		if ai.Sources == nil {
			ai.Sources = []connectors.SupportAISource{}
		}
	}
	var aiRating *int
	var aiRemark *string
	if ai.Rating > 0 {
		aiRating = &ai.Rating
	}
	if ai.RatingRemark != "" {
		aiRemark = &ai.RatingRemark
	}
	sources, err := json.Marshal(ai.Sources)
	if err != nil {
		return "", 0, err
	}
	tags := cv.Tags
	if tags == nil {
		tags = []string{}
	}
	var conv string
	var created bool
	err = tx.QueryRow(ctx, `
		INSERT INTO support_conversations (workspace_id, brand_id, record_id, channel, subject, status, assignee_id, tags,
			first_message_at, first_reply_at, closed_at, last_message_at, last_from, unread,
			source, integration_id, external_app, external_id, external_channel, external_assignee_id, external_team_id,
			priority, reopens, rating, rating_remark, rated_at,
			ai_agent, ai_resolution, ai_answer_type, ai_source_type, ai_source_title, ai_rating, ai_rating_remark, ai_sources, external_updated_at)
		VALUES ($1, $2, $3, $4, left($5, 1000), $6, $7, $8, $9, $10, $11, $12, $13, false,
			$14, $15, $16, $17, left($18, 60), $19, $20, $21, $22, $23, left($24, 4000), $25,
			$26, left($27, 60), left($28, 60), left($29, 60), left($30, 500), $31, left($32, 4000), $33, $34)
		ON CONFLICT (workspace_id, source, external_app, external_id) WHERE external_id IS NOT NULL DO UPDATE SET
			record_id = COALESCE(EXCLUDED.record_id, support_conversations.record_id), channel = EXCLUDED.channel, subject = EXCLUDED.subject,
			status = EXCLUDED.status, assignee_id = EXCLUDED.assignee_id, tags = EXCLUDED.tags,
			first_message_at = EXCLUDED.first_message_at, first_reply_at = EXCLUDED.first_reply_at, closed_at = EXCLUDED.closed_at,
			last_message_at = EXCLUDED.last_message_at, last_from = EXCLUDED.last_from, integration_id = EXCLUDED.integration_id,
			external_channel = EXCLUDED.external_channel, external_assignee_id = EXCLUDED.external_assignee_id, external_team_id = EXCLUDED.external_team_id,
			priority = EXCLUDED.priority, reopens = EXCLUDED.reopens, rating = EXCLUDED.rating, rating_remark = EXCLUDED.rating_remark, rated_at = EXCLUDED.rated_at,
			ai_agent = EXCLUDED.ai_agent, ai_resolution = EXCLUDED.ai_resolution, ai_answer_type = EXCLUDED.ai_answer_type,
			ai_source_type = EXCLUDED.ai_source_type, ai_source_title = EXCLUDED.ai_source_title, ai_rating = EXCLUDED.ai_rating,
			ai_rating_remark = EXCLUDED.ai_rating_remark, ai_sources = EXCLUDED.ai_sources, external_updated_at = EXCLUDED.external_updated_at,
			updated_at = now()
		RETURNING id::text, xmax = 0`,
		run.ws, run.brand, record, cv.Channel, cv.Subject, cv.Status, assignee.userID, tags,
		first, cv.FirstReplyAt, cv.ClosedAt, last, lastFrom,
		run.source, run.integration, run.app, cv.ID, cv.ExternalChannel, nilIfEmpty(assignee.id), nilIfEmpty(team.id),
		cv.Priority, cv.Reopens, rating, ratingRemark, ratedAt,
		map[bool]string{true: ai.Agent, false: ""}[cv.AI != nil], ai.Resolution, ai.AnswerType, ai.SourceType, ai.SourceTitle, aiRating, aiRemark, sources, updated,
	).Scan(&conv, &created)
	if err != nil {
		return "", 0, err
	}
	added := 0
	for _, m := range cv.Messages {
		if m.ID == "" {
			continue
		}
		author, err := run.agent(ctx, tx, m.AuthorKind, m.AuthorID, m.AuthorName, "")
		if err != nil {
			return "", 0, err
		}
		status := map[string]string{"in": "received", "out": "sent", "note": "saved"}[m.Kind]
		var sentAt *time.Time
		if m.Kind == "out" {
			sentAt = &m.At
		}
		var from *string
		if m.Kind == "in" {
			from = record
		}
		var inserted bool
		if err := tx.QueryRow(ctx, `
			INSERT INTO support_messages (workspace_id, conversation_id, kind, record_id, author_id, body, status, created_at, sent_at,
				external_id, author_type, external_author_id)
			VALUES ($1, $2, $3, $4, $5, left($6, 200000), $7, $8, $9, $10, $11, $12)
			ON CONFLICT (conversation_id, external_id) WHERE external_id IS NOT NULL DO UPDATE SET
				kind = EXCLUDED.kind, record_id = EXCLUDED.record_id, author_id = EXCLUDED.author_id, body = EXCLUDED.body,
				status = EXCLUDED.status, author_type = EXCLUDED.author_type, external_author_id = EXCLUDED.external_author_id
			RETURNING xmax = 0`,
			run.ws, conv, m.Kind, from, author.userID, m.Body, status, m.At, sentAt, m.ID, m.AuthorType, nilIfEmpty(author.id)).Scan(&inserted); err != nil {
			return "", 0, err
		}
		if inserted {
			added++
		}
	}
	if created {
		return "created", added, nil
	}
	return "updated", added, nil
}

// ---- The schedule ----

// RunSupportImports starts the next pass of each caught-up import every
// few minutes, retries failed ones, restarts stuck ones, and reports how
// far behind the imports are.
func (s *Server) RunSupportImports(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.QueueSupportImports(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "support_import.schedule_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// QueueSupportImports runs one tick of RunSupportImports and says how many
// passes it started. Servers ticking together start each once (the lock).
func (s *Server) QueueSupportImports(ctx context.Context) (int, error) {
	queued := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var got bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext('support.import.schedule'))`).Scan(&got); err != nil || !got {
			return err
		}
		rows, err := tx.Query(ctx, `
			UPDATE support_imports SET status = 'syncing', updated_at = now()
			WHERE id IN (SELECT id FROM support_imports
				WHERE (status IN ('idle', 'failed') AND (last_synced_at IS NULL OR last_synced_at < now() - $1::interval))
				   OR (status = 'syncing' AND updated_at < now() - $2::interval)
				ORDER BY last_synced_at NULLS FIRST LIMIT 500 FOR UPDATE SKIP LOCKED)
			RETURNING id::text, workspace_id::text`, supportImportEvery.String(), supportImportStuck.String())
		if err != nil {
			return err
		}
		type due struct{ id, ws string }
		var list []due
		var d due
		if _, err := pgx.ForEachRow(rows, []any{&d.id, &d.ws}, func() error {
			list = append(list, d)
			return nil
		}); err != nil {
			return err
		}
		key := "tick:" + time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339)
		for _, d := range list {
			if err := queueSupportImport(ctx, tx, d.ws, d.id, key); err != nil {
				return err
			}
			queued++
		}
		// How far behind: the oldest catch-up of an import that isn't
		// paused, and how many are failing.
		srcRows, err := tx.Query(ctx, `SELECT source, COALESCE(EXTRACT(EPOCH FROM now() - min(COALESCE(last_synced_at, created_at))), 0)::float8,
			count(*) FILTER (WHERE status = 'failed')::int FROM support_imports WHERE status <> 'paused' GROUP BY source`)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		var source string
		var lag float64
		var failing int
		if _, err := pgx.ForEachRow(srcRows, []any{&source, &lag, &failing}, func() error {
			seen[source] = true
			metrics.SupportImportHealth(source, time.Duration(lag*float64(time.Second)), failing)
			return nil
		}); err != nil {
			return err
		}
		for _, src := range []string{"intercom"} {
			if !seen[src] {
				metrics.SupportImportHealth(src, 0, 0)
			}
		}
		return nil
	})
	return queued, err
}
