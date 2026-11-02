package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Undo a batch (plans/brands/README.md, "Undo for bad data" and the
// owner's answer of 2026-10-30). Every write an import or ingest batch
// makes carries the batch (batch_id on records, identities, links, field
// history, product events, list members and leads; migration 0330). The
// writers mark their transaction with inBatch and the columns' defaults
// do the rest. A batch is a CSV import of leads, a CRM import, or one POST
// of events to a brand.
//
// The person chooses each time, after a preview that counts what the
// batch made and lists what changed since (someone's edits, emails sent,
// payments, activity, merges):
//   - untouched: what nobody changed since is undone; the changed records
//     stay, with what the batch put on them, listed so they can be removed
//     by hand.
//   - everything: the changed records go too.
//
// Either way it is one deletion of kind batch in Recently deleted,
// restorable for 7 days in one step. What can't be undone stays and is
// listed: a field someone changed since (the batch's value is already
// gone), a lead already in a sequence (emails sent can't be unsent; their
// history stays, and the timeline marks it as from an undone batch), and a
// record merged since (undo the merge first). Undoing fires nothing: no
// rule, journey, webhook or email for what it removes; batch.undone is
// the one event.

// undoInline is the most rows an undo does in the request; more run in a
// job (202) and the batch's undo.status says when it is done.
var undoInline = 2000

const (
	// changedListMax caps the changed items a preview lists.
	changedListMax = 200

	jobBatchUndo = "batches.undo"

	deletedBatch = "batch"
)

// Batch kinds.
const (
	batchCSVImport = "csv_import"
	batchCRMImport = "crm_import"
	batchEvents    = "events"
)

// What changed since a batch, by kind.
const (
	changedEdited   = "edited"           // someone else changed a field of a record the batch created
	changedEmailed  = "emailed"          // a record the batch created was sent email
	changedPaid     = "paid"             // a record the batch created has payments from elsewhere
	changedActivity = "activity"         // a record the batch created has other events since
	changedMerged   = "merged"           // a record the batch created was merged since: never undone here
	changedField    = "field_changed"    // a field the batch set was changed since: nothing of the batch is left there
	changedSequence = "lead_in_sequence" // a lead the batch created is in a sequence: never undone here
)

var undoModes = []string{"untouched", "everything"}

func (s *Server) routesBatches(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("GET /v1/brands/{brand}/batches", s.onBrand(read, s.listBatches))
	mux.HandleFunc("GET /v1/brands/{brand}/batches/{batch}", s.onBrand(read, s.getBatch))
	mux.HandleFunc("GET /v1/brands/{brand}/batches/{batch}/undo-preview", s.onBrand(read, s.previewBatchUndo))
	mux.HandleFunc("POST /v1/brands/{brand}/batches/{batch}/undo", s.onBrand(manage, s.undoBatch))
}

// ---- Marking writes ----

// inBatch marks every row the rest of tx inserts into a batch-carrying
// table as batch id's. An empty id clears the mark.
func inBatch(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `SELECT set_config('outflare.batch_id', $1, true)`, id)
	return err
}

// startBatch records a new batch of the brand (or, with brand empty, the
// workspace's first brand) and marks the rest of tx as it. id is the
// batch's id when the import already has one (a CRM import's), else new.
func startBatch(ctx context.Context, tx pgx.Tx, p *principal, ws, brand, kind, source, label, id string) (string, error) {
	err := tx.QueryRow(ctx, `INSERT INTO batches (id, workspace_id, brand_id, kind, source, label, actor_id, actor_type, user_id, token_id, request_id)
		VALUES (COALESCE($1::uuid, gen_random_uuid()), $2, COALESCE($3::uuid, outflare_first_brand($2)), $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id::text`,
		nullIfEmpty(id), ws, nullIfEmpty(brand), kind, source[:min(len(source), 200)], label[:min(len(label), 200)],
		nullIfEmpty(p.ActorID), p.ActorType, nullIfEmpty(p.personID()), nullIfEmpty(p.TokenID), nullIfEmpty(RequestID(ctx))).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, inBatch(ctx, tx, id)
}

// ---- Views ----

type batchUndoView struct {
	// null (never undone), queued, running, done or failed.
	Status     *string        `json:"status"`
	Mode       *string        `json:"mode"`
	UndoneAt   *time.Time     `json:"undone_at"`
	DeletionID *string        `json:"deletion_id"`
	Counts     map[string]int `json:"counts"`
	Error      *string        `json:"error"`
}

type batchView struct {
	ID      string `json:"id"`
	BrandID string `json:"brand_id"`
	// csv_import, crm_import or events.
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Label  string `json:"label"`
	// What the batch made that is still here, by kind: records,
	// field_values, identities, relationships, events, list_memberships
	// and leads.
	Counts    map[string]int `json:"counts"`
	CreatedBy deletedBy      `json:"created_by"`
	CreatedAt time.Time      `json:"created_at"`
	Undo      batchUndoView  `json:"undo"`
}

const batchColumns = `b.id::text, b.brand_id::text, b.kind, b.source, b.label,
	jsonb_build_object(
		'records', (SELECT count(*) FROM records x WHERE x.workspace_id = b.workspace_id AND x.batch_id = b.id AND x.brand_id = b.brand_id AND x.deleted_at IS NULL),
		'field_values', (SELECT count(DISTINCT (x.record_id, x.field_key)) FROM attribute_changes x JOIN records r ON r.id = x.record_id
			WHERE x.workspace_id = b.workspace_id AND x.batch_id = b.id AND r.brand_id = b.brand_id AND r.batch_id IS DISTINCT FROM b.id),
		'identities', (SELECT count(*) FROM record_identities x WHERE x.workspace_id = b.workspace_id AND x.batch_id = b.id AND x.brand_id = b.brand_id),
		'relationships', (SELECT count(*) FROM record_relationships x WHERE x.workspace_id = b.workspace_id AND x.batch_id = b.id AND x.brand_id = b.brand_id),
		'events', (SELECT count(*) FROM customer_events x WHERE x.workspace_id = b.workspace_id AND x.batch_id = b.id AND x.brand_id = b.brand_id),
		'list_memberships', (SELECT count(*) FROM static_list_members x WHERE x.workspace_id = b.workspace_id AND x.batch_id = b.id AND x.brand_id = b.brand_id)
			+ (SELECT count(*) FROM lead_list_members x JOIN lead_lists l ON l.id = x.list_id WHERE l.workspace_id = b.workspace_id AND x.batch_id = b.id),
		'leads', (SELECT count(*) FROM leads x WHERE x.workspace_id = b.workspace_id AND x.batch_id = b.id
			AND (x.record_id IS NULL OR EXISTS (SELECT 1 FROM records r WHERE r.id = x.record_id AND r.brand_id = b.brand_id)))),
	b.actor_type, b.user_id::text, b.token_id::text, u.name, b.created_at,
	b.undo_status, b.undo_mode, b.undone_at, b.deletion_id::text, b.undo_counts, b.undo_error`

const batchFrom = ` FROM batches b LEFT JOIN users u ON u.id = b.user_id`

func scanBatch(row pgx.CollectableRow) (batchView, error) {
	var v batchView
	err := row.Scan(&v.ID, &v.BrandID, &v.Kind, &v.Source, &v.Label, &v.Counts, &v.CreatedBy.ActorType, &v.CreatedBy.UserID, &v.CreatedBy.TokenID,
		&v.CreatedBy.Name, &v.CreatedAt, &v.Undo.Status, &v.Undo.Mode, &v.Undo.UndoneAt, &v.Undo.DeletionID, &v.Undo.Counts, &v.Undo.Error)
	if v.Counts == nil {
		v.Counts = map[string]int{}
	}
	if v.Undo.Counts == nil {
		v.Undo.Counts = map[string]int{}
	}
	return v, err
}

func loadBatch(ctx context.Context, q pgx.Tx, a *access, id string) (batchView, error) {
	if !validUUID(id) {
		return batchView{}, errNotFound
	}
	rows, err := q.Query(ctx, `SELECT `+batchColumns+batchFrom+` WHERE b.id = $1 AND b.brand_id = $2 AND b.workspace_id = $3`, id, a.Brand, a.WorkspaceID)
	if err != nil {
		return batchView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanBatch)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

func (s *Server) listBatches(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	limit, err := limitParam(r, 25, 100)
	if s.allocError(w, r, err) {
		return
	}
	beforeAt, beforeID, ok := timeCursor(w, r)
	if !ok {
		return
	}
	where := []string{"b.brand_id = $1", "b.workspace_id = $2", "(b.created_at, b.id::text) < ($3, $4)"}
	args := []any{a.Brand, a.WorkspaceID, beforeAt, beforeID}
	if v := q.Get("kind"); v != "" {
		if !slices.Contains([]string{batchCSVImport, batchCRMImport, batchEvents}, v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is csv_import, crm_import or events")
			return
		}
		args = append(args, v)
		where = append(where, "b.kind = $"+strconv.Itoa(len(args)))
	}
	args = append(args, limit)
	var list []batchView
	err = s.readTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+batchColumns+batchFrom+` WHERE `+strings.Join(where, " AND ")+`
			ORDER BY b.created_at DESC, b.id::text DESC LIMIT $`+strconv.Itoa(len(args)), args...)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, scanBatch)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	if list == nil {
		list = []batchView{}
	}
	var next *string
	if len(list) == limit {
		last := list[len(list)-1]
		v := last.CreatedAt.Format(time.RFC3339Nano) + "|" + last.ID
		next = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}

func (s *Server) getBatch(w http.ResponseWriter, r *http.Request) {
	var v batchView
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		v, err = loadBatch(r.Context(), tx, accessFrom(r.Context()), r.PathValue("batch"))
		return err
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "batch not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- Planning ----

type changedItem struct {
	// edited, emailed, paid, activity, merged, field_changed or
	// lead_in_sequence.
	Kind     string  `json:"kind"`
	RecordID *string `json:"record_id"`
	LeadID   *string `json:"lead_id"`
	// The field, for field_changed.
	Field *string `json:"field"`
	// Whether "undo everything" removes it: false for what is never undone
	// here (a field changed since, a lead in a sequence, a merged record).
	RemovedByEverything bool `json:"removed_by_everything"`
}

// createdRecord is a record the batch created, with what changed since.
type createdRecord struct {
	id                                      string
	merged, edited, emailed, paid, activity bool
}

func (c createdRecord) changed() bool {
	return c.merged || c.edited || c.emailed || c.paid || c.activity
}

type batchField struct {
	record, key string
	old, new    json.RawMessage
	revertible  bool
	merged      bool
}

type batchLead struct {
	id         string
	record     *string
	inSequence bool
}

// undoPlan is what a batch made that is still here, and what changed.
// Only the batch's brand's rows are in it: an undo never reaches another
// brand's data.
type undoPlan struct {
	batch   string
	brand   string
	records []createdRecord
	fields  []batchField
	leads   []batchLead
}

func planUndo(ctx context.Context, tx pgx.Tx, ws, brand, batch string) (*undoPlan, error) {
	p := &undoPlan{batch: batch, brand: brand}
	rows, err := tx.Query(ctx, `SELECT r.id::text,
			r.merged_into IS NOT NULL OR EXISTS (SELECT 1 FROM record_merges m WHERE (m.into_id = r.id OR m.from_id = r.id) AND m.undone_at IS NULL),
			EXISTS (SELECT 1 FROM attribute_changes c WHERE c.record_id = r.id AND c.batch_id IS DISTINCT FROM $2 AND c.actor_id IS NOT NULL),
			EXISTS (SELECT 1 FROM emails e WHERE e.record_id = r.id AND NOT e.test) OR EXISTS (SELECT 1 FROM messages m WHERE m.record_id = r.id),
			EXISTS (SELECT 1 FROM customer_events e WHERE e.record_id = r.id AND e.batch_id IS DISTINCT FROM $2 AND e.name = ANY($3))
				OR EXISTS (SELECT 1 FROM payment_subscriptions s WHERE s.record_id = r.id),
			EXISTS (SELECT 1 FROM customer_events e WHERE e.record_id = r.id AND e.batch_id IS DISTINCT FROM $2 AND NOT (e.name = ANY($3)))
		FROM records r WHERE r.workspace_id = $1 AND r.batch_id = $2 AND r.brand_id = $4 AND r.deleted_at IS NULL ORDER BY r.created_at, r.id`, ws, batch, paymentEvents, brand)
	if err != nil {
		return nil, err
	}
	if p.records, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (createdRecord, error) {
		var c createdRecord
		return c, row.Scan(&c.id, &c.merged, &c.edited, &c.emailed, &c.paid, &c.activity)
	}); err != nil {
		return nil, err
	}
	// Field values the batch set on records it didn't create: the value
	// before its first change, its last value, and whether the field
	// still holds that.
	rows, err = tx.Query(ctx, `WITH b AS (
			SELECT c.record_id, c.field_key, (array_agg(c.old_value ORDER BY c.id))[1] AS old, (array_agg(c.new_value ORDER BY c.id DESC))[1] AS new
			FROM attribute_changes c WHERE c.workspace_id = $1 AND c.batch_id = $2 GROUP BY 1, 2)
		SELECT b.record_id::text, b.field_key, b.old, b.new, cur IS NOT DISTINCT FROM b.new, r.merged_into IS NOT NULL
		FROM b JOIN records r ON r.id = b.record_id,
			LATERAL (SELECT CASE WHEN b.field_key = 'name' THEN to_jsonb(r.name) ELSE r.attributes -> b.field_key END AS cur) x
		WHERE r.batch_id IS DISTINCT FROM $2 AND r.brand_id = $3 AND r.deleted_at IS NULL ORDER BY 1, 2`, ws, batch, brand)
	if err != nil {
		return nil, err
	}
	if p.fields, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (batchField, error) {
		var f batchField
		return f, row.Scan(&f.record, &f.key, &f.old, &f.new, &f.revertible, &f.merged)
	}); err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT l.id::text, l.record_id::text,
			EXISTS (SELECT 1 FROM enrollments e WHERE e.lead_id = l.id) OR EXISTS (SELECT 1 FROM messages m WHERE m.lead_id = l.id)
			OR EXISTS (SELECT 1 FROM inbound_messages m WHERE m.lead_id = l.id) OR EXISTS (SELECT 1 FROM meetings m WHERE m.lead_id = l.id)
			OR EXISTS (SELECT 1 FROM deals d WHERE d.lead_id = l.id) OR EXISTS (SELECT 1 FROM link_pitches x WHERE x.lead_id = l.id)
		FROM leads l WHERE l.workspace_id = $1 AND l.batch_id = $2
			AND (l.record_id IS NULL OR EXISTS (SELECT 1 FROM records r WHERE r.id = l.record_id AND r.brand_id = $3)) ORDER BY l.id`, ws, batch, brand)
	if err != nil {
		return nil, err
	}
	p.leads, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (batchLead, error) {
		var l batchLead
		return l, row.Scan(&l.id, &l.record, &l.inSequence)
	})
	return p, err
}

// split is the records a mode removes and the ones it keeps.
func (p *undoPlan) split(mode string) (remove, keep []string) {
	for _, r := range p.records {
		if r.merged || (mode == "untouched" && r.changed()) {
			keep = append(keep, r.id)
		} else {
			remove = append(remove, r.id)
		}
	}
	return remove, keep
}

// changes lists what changed since the batch, up to max, and counts it
// all by kind.
func (p *undoPlan) changes(max int) ([]changedItem, map[string]int) {
	list := []changedItem{}
	counts := map[string]int{}
	add := func(it changedItem) {
		counts[it.Kind]++
		if len(list) < max {
			list = append(list, it)
		}
	}
	for _, r := range p.records {
		id := r.id
		for _, k := range []struct {
			on   bool
			kind string
		}{{r.merged, changedMerged}, {r.edited, changedEdited}, {r.emailed, changedEmailed}, {r.paid, changedPaid}, {r.activity, changedActivity}} {
			if k.on {
				add(changedItem{Kind: k.kind, RecordID: &id, RemovedByEverything: !r.merged})
			}
		}
	}
	for _, f := range p.fields {
		if !f.revertible || f.merged {
			rec, key := f.record, f.key
			add(changedItem{Kind: changedField, RecordID: &rec, Field: &key})
		}
	}
	for _, l := range p.leads {
		if l.inSequence {
			id := l.id
			add(changedItem{Kind: changedSequence, LeadID: &id, RecordID: l.record})
		}
	}
	return list, counts
}

// size is how many rows an undo of the plan touches, about.
func (p *undoPlan) size(ctx context.Context, tx pgx.Tx, ws string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM record_identities WHERE workspace_id = $1 AND batch_id = $2 AND brand_id = $3)
		+ (SELECT count(*) FROM record_relationships WHERE workspace_id = $1 AND batch_id = $2 AND brand_id = $3)
		+ (SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND batch_id = $2 AND brand_id = $3)
		+ (SELECT count(*) FROM static_list_members WHERE workspace_id = $1 AND batch_id = $2 AND brand_id = $3)`, ws, p.batch, p.brand).Scan(&n)
	return n + len(p.records) + len(p.fields) + len(p.leads), err
}

// would counts what undoing in mode would remove or put back.
func (p *undoPlan) would(mode string) map[string]int {
	remove, keep := p.split(mode)
	out := map[string]int{"records": len(remove), "records_kept": len(keep)}
	for _, f := range p.fields {
		if f.revertible && !f.merged {
			out["field_values"]++
		}
	}
	for _, l := range p.leads {
		if !l.inSequence && (l.record == nil || !slices.Contains(keep, *l.record)) {
			out["leads"]++
		}
	}
	return out
}

// ---- Preview ----

type batchPreview struct {
	Batch batchView `json:"batch"`
	// What the batch made that is still here, by kind.
	Made map[string]int `json:"made"`
	// What changed since, by kind, and the items (up to 200).
	ChangedCounts map[string]int `json:"changed_counts"`
	Changed       []changedItem  `json:"changed"`
	// The changed list stops at 200 items.
	ChangedTruncated bool `json:"changed_truncated"`
	// What each mode would undo: records removed and kept, field values
	// put back, leads removed. Identities, links, events and list
	// memberships of the batch go with them.
	Untouched  map[string]int `json:"untouched"`
	Everything map[string]int `json:"everything"`
	// Whether POST .../undo would run in a job (202) rather than at once.
	RunsInJob bool `json:"runs_in_job"`
}

func (s *Server) previewBatchUndo(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var out batchPreview
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		var err error
		if out.Batch, err = loadBatch(ctx, tx, a, r.PathValue("batch")); err != nil {
			return err
		}
		out.Made = out.Batch.Counts
		p, err := planUndo(ctx, tx, a.WorkspaceID, a.Brand, out.Batch.ID)
		if err != nil {
			return err
		}
		var total int
		out.Changed, out.ChangedCounts = p.changes(changedListMax)
		for _, n := range out.ChangedCounts {
			total += n
		}
		out.ChangedTruncated = total > len(out.Changed)
		out.Untouched, out.Everything = p.would("untouched"), p.would("everything")
		n, err := p.size(ctx, tx, a.WorkspaceID)
		out.RunsInJob = n > undoInline
		return err
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "batch not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- Undoing ----

var (
	errBatchUndone  = recordConflict{"batch_already_undone", "this batch was already undone; restore it from Recently deleted to undo it again"}
	errBatchRunning = recordConflict{"batch_undo_running", "this batch is being undone; GET the batch to see when it is done"}
)

func (s *Server) undoBatch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var body struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !slices.Contains(undoModes, body.Mode) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `mode is "untouched" (undo only what nobody changed since) or "everything" (changed items go too, restorable for 7 days)`)
		return
	}
	id := r.PathValue("batch")
	queued := false
	start := time.Now()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockBatch(ctx, tx, a, id); err != nil {
			return err
		}
		p, err := planUndo(ctx, tx, a.WorkspaceID, a.Brand, id)
		if err != nil {
			return err
		}
		n, err := p.size(ctx, tx, a.WorkspaceID)
		if err != nil {
			return err
		}
		if n > undoInline {
			queued = true
			if _, err := tx.Exec(ctx, `UPDATE batches SET undo_status = 'queued', undo_mode = $2, undo_error = NULL WHERE id = $1`, id, body.Mode); err != nil {
				return err
			}
			_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobBatchUndo, WorkspaceID: &a.WorkspaceID, MaxAttempts: 1, IdempotencyKey: "undo:" + id,
				Payload: batchUndoJob{Batch: id, Brand: a.Brand, Mode: body.Mode, ActorID: a.ActorID, ActorType: a.ActorType,
					UserID: a.personID(), TokenID: a.TokenID, RequestID: RequestID(ctx)}})
			return err
		}
		return runBatchUndo(ctx, tx, a, p, body.Mode)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "batch not found")
		return
	}
	if s.recordError(w, r, err) {
		return
	}
	var v batchView
	if err := s.readTx(ctx, func(tx pgx.Tx) error {
		var err error
		v, err = loadBatch(ctx, tx, a, id)
		return err
	}); s.allocError(w, r, err) {
		return
	}
	if queued {
		metrics.BatchUndone(body.Mode, "queued", 0)
		writeJSON(w, http.StatusAccepted, v)
		return
	}
	metrics.BatchUndone(body.Mode, "undone", time.Since(start))
	writeJSON(w, http.StatusOK, v)
}

// lockBatch locks a batch of the route's brand that can be undone.
func lockBatch(ctx context.Context, tx pgx.Tx, a *access, id string) error {
	if !validUUID(id) {
		return errNotFound
	}
	var status *string
	var undone *time.Time
	err := tx.QueryRow(ctx, `SELECT undo_status, undone_at FROM batches WHERE id = $1 AND brand_id = $2 AND workspace_id = $3 FOR UPDATE`,
		id, a.Brand, a.WorkspaceID).Scan(&status, &undone)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return errNotFound
	case err != nil:
		return err
	case undone != nil:
		return errBatchUndone
	case status != nil && (*status == "queued" || *status == "running"):
		return errBatchRunning
	}
	return nil
}

type batchUndoJob struct {
	Batch     string `json:"batch_id"`
	Brand     string `json:"brand_id"`
	Mode      string `json:"mode"`
	ActorID   string `json:"actor_id"`
	ActorType string `json:"actor_type"`
	UserID    string `json:"user_id"`
	TokenID   string `json:"token_id"`
	RequestID string `json:"request_id"`
}

func (s *Server) batchUndoJob(ctx context.Context, job jobs.Job) error {
	var p batchUndoJob
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.Batch) || !slices.Contains(undoModes, p.Mode) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	a := &access{principal: principal{ActorID: p.ActorID, ActorType: p.ActorType, UserID: p.UserID, TokenID: p.TokenID},
		WorkspaceID: *job.WorkspaceID, Brand: p.Brand}
	if p.RequestID != "" {
		ctx = context.WithValue(ctx, requestIDKey, p.RequestID)
	}
	start := time.Now()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status *string
		if err := tx.QueryRow(ctx, `SELECT undo_status FROM batches WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, p.Batch, a.WorkspaceID).Scan(&status); err != nil {
			return err
		}
		if status == nil || *status != "queued" {
			return nil
		}
		plan, err := planUndo(ctx, tx, a.WorkspaceID, a.Brand, p.Batch)
		if err != nil {
			return err
		}
		return runBatchUndo(ctx, tx, a, plan, p.Mode)
	})
	if err != nil {
		metrics.BatchUndone(p.Mode, "failed", 0)
		if _, uerr := s.pool.Exec(context.WithoutCancel(ctx), `UPDATE batches SET undo_status = 'failed', undo_error = $2 WHERE id = $1`,
			p.Batch, "the undo failed and changed nothing; try again, and if it fails again tell support with this batch's id"); uerr != nil {
			return errors.Join(err, uerr)
		}
		return fmt.Errorf("%w: undo batch %s: %w", jobs.ErrPermanent, p.Batch, err)
	}
	metrics.BatchUndone(p.Mode, "undone", time.Since(start))
	return nil
}

// runBatchUndo undoes a planned batch in mode, as one deletion.
func runBatchUndo(ctx context.Context, tx pgx.Tx, a *access, p *undoPlan, mode string) error {
	ws, batch, brand := a.WorkspaceID, p.batch, p.brand
	qctx := quietly(ctx)
	remove, keep := p.split(mode)
	if keep == nil {
		keep = []string{}
	}
	if remove == nil {
		remove = []string{}
	}
	d, err := startDeletion(ctx, tx, a, deletedBatch, nil, batch)
	if err != nil {
		return err
	}
	payments := map[string]bool{}

	// The records it created: deleted with what hangs off them, as a
	// record delete does.
	if len(remove) > 0 {
		if _, err := d.move(ctx, tx, "record_identities", "gone.record_id", `t.workspace_id = $1 AND t.record_id = ANY($2::uuid[])`, ws, remove); err != nil {
			return err
		}
		if _, err := d.move(ctx, tx, "record_relationships", `CASE WHEN gone.from_id = ANY($2::uuid[]) THEN gone.from_id ELSE gone.to_id END`,
			`t.workspace_id = $1 AND (t.from_id = ANY($2::uuid[]) OR t.to_id = ANY($2::uuid[]))`, ws, remove); err != nil {
			return err
		}
		if _, err := d.move(ctx, tx, "static_list_members", "gone.record_id", `t.workspace_id = $1 AND t.record_id = ANY($2::uuid[])`, ws, remove); err != nil {
			return err
		}
		events, err := eventIDs(ctx, tx, `workspace_id = $1 AND record_id = ANY($2::uuid[])`, ws, remove)
		if err != nil {
			return err
		}
		if _, err := d.moveEvents(ctx, tx, events); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE journey_runs SET status = 'exited', exit_reason = 'deleted', finished_at = now(), next_at = NULL, updated_at = now()
			WHERE workspace_id = $1 AND record_id = ANY($2::uuid[]) AND status = 'active'`, ws, remove)
		if err != nil {
			return err
		}
		if n := int(tag.RowsAffected()); n > 0 {
			d.counts["journey_runs_ended"] = n
		}
		tag, err = tx.Exec(ctx, `UPDATE records SET deleted_at = now(), deletion_id = $2, archived_at = COALESCE(archived_at, now()),
			version = version + 1, updated_at = now() WHERE id = ANY($1::uuid[]) AND deleted_at IS NULL`, remove, d.id)
		if err != nil {
			return err
		}
		d.counts["records"] = int(tag.RowsAffected())
	}

	// What it added to records it didn't create, except on the ones kept.
	if _, err := d.move(ctx, tx, "record_identities", "gone.record_id", `t.workspace_id = $1 AND t.batch_id = $2 AND t.brand_id = $4
		AND NOT (t.record_id = ANY($3::uuid[]))`, ws, batch, keep, brand); err != nil {
		return err
	}
	if _, err := d.move(ctx, tx, "record_relationships", "gone.from_id", `t.workspace_id = $1 AND t.batch_id = $2 AND t.field_key IS NULL AND t.brand_id = $4
		AND NOT (t.from_id = ANY($3::uuid[])) AND NOT (t.to_id = ANY($3::uuid[]))`, ws, batch, keep, brand); err != nil {
		return err
	}
	if _, err := d.move(ctx, tx, "static_list_members", "gone.record_id", `t.workspace_id = $1 AND t.batch_id = $2 AND t.brand_id = $4
		AND NOT (t.record_id = ANY($3::uuid[]))`, ws, batch, keep, brand); err != nil {
		return err
	}
	events, err := eventIDs(ctx, tx, `workspace_id = $1 AND batch_id = $2 AND brand_id = $4 AND (record_id IS NULL OR NOT (record_id = ANY($3::uuid[])))`, ws, batch, keep, brand)
	if err != nil {
		return err
	}
	if len(events) > 0 {
		rows, err := tx.Query(ctx, `SELECT DISTINCT record_id::text FROM customer_events WHERE id = ANY($1::uuid[]) AND record_id IS NOT NULL AND name = ANY($2)`, events, paymentEvents)
		if err != nil {
			return err
		}
		recs, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, r := range recs {
			payments[r] = true
		}
		if _, err := d.moveEvents(ctx, tx, events); err != nil {
			return err
		}
	}

	// Field values it set, back to what they were, while they still hold
	// what it put there.
	c, err := loadCatalog(ctx, tx, ws, brand, false)
	if err != nil {
		return err
	}
	reverted := 0
	for _, f := range p.fields {
		if !f.revertible || f.merged {
			continue
		}
		var want, prev any
		if err := unmarshalNullable(f.new, &want); err != nil {
			return err
		}
		if err := unmarshalNullable(f.old, &prev); err != nil {
			return err
		}
		var ok bool
		if f.key == "name" {
			ok, err = revertName(ctx, tx, f.record, want, prev)
		} else {
			ok, err = revertField(qctx, tx, a, c, f.record, f.key, want, prev, f.old != nil, "")
		}
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deleted_rows (deletion_id, workspace_id, brand_id, record_id, source_table, row_id, data)
			VALUES ($1, $2, $3, $4, $5, $6, jsonb_build_object('key', $7::text, 'value', $8::jsonb, 'previous', $9::jsonb, 'had', $10::boolean))`,
			d.id, ws, a.Brand, f.record, fieldValueRows, f.record+":"+f.key, f.key, jsonOrNull(f.new), jsonOrNull(f.old), f.old != nil); err != nil {
			return err
		}
		reverted++
	}
	if reverted > 0 {
		d.counts[fieldValueRows] = reverted
	}

	// Leads it created that no sequence has used, with their list places;
	// and its list places on other leads.
	var leads []string
	for _, l := range p.leads {
		if !l.inSequence && (l.record == nil || !slices.Contains(keep, *l.record)) {
			leads = append(leads, l.id)
		}
	}
	if leads == nil {
		leads = []string{}
	}
	if _, err := d.move(ctx, tx, "lead_list_members", "(SELECT l.record_id FROM leads l WHERE l.id = gone.lead_id)",
		`(t.lead_id = ANY($2::uuid[]) OR t.batch_id = $3) AND EXISTS (SELECT 1 FROM lead_lists l WHERE l.id = t.list_id AND l.workspace_id = $1)
			AND NOT EXISTS (SELECT 1 FROM leads x WHERE x.id = t.lead_id AND x.record_id = ANY($4::uuid[]))
			AND NOT EXISTS (SELECT 1 FROM leads x JOIN records r ON r.id = x.record_id WHERE x.id = t.lead_id AND r.brand_id IS DISTINCT FROM $5)`,
		ws, leads, batch, keep, brand); err != nil {
		return err
	}
	if len(leads) > 0 {
		if _, err := d.move(ctx, tx, "lead_sources", "(SELECT l.record_id FROM leads l WHERE l.id = gone.lead_id)",
			`t.lead_id = ANY($2::uuid[]) AND EXISTS (SELECT 1 FROM leads l WHERE l.id = t.lead_id AND l.workspace_id = $1)`, ws, leads); err != nil {
			return err
		}
		if _, err := d.move(ctx, tx, "leads", "gone.record_id", `t.workspace_id = $1 AND t.id = ANY($2::uuid[])`, ws, leads); err != nil {
			return err
		}
	}
	for r := range payments {
		if err := quietBilling(ctx, tx, ws, r); err != nil {
			return err
		}
	}
	if err := d.finish(ctx, tx); err != nil {
		return err
	}
	_, changed := p.changes(0)
	counts := map[string]int{}
	total := 0
	for k, n := range d.counts {
		counts[k] = n
		total += n
	}
	counts["records_kept"] = len(keep)
	for k, n := range changed {
		counts["changed_"+k] = n
	}
	if _, err := tx.Exec(ctx, `UPDATE batches SET undo_status = 'done', undo_mode = $2, undone_at = now(), undone_by = $3, undo_counts = $4,
		deletion_id = $5, undo_error = NULL WHERE id = $1`, batch, mode, nullIfEmpty(a.ActorID), counts, d.id); err != nil {
		return err
	}
	metrics.UndoRowsReverted("batch", total)
	metrics.RecoveryDeleted(deletedBatch)
	var kind string
	if err := tx.QueryRow(ctx, `SELECT kind FROM batches WHERE id = $1`, batch).Scan(&kind); err != nil {
		return err
	}
	return recordWithProps(ctx, tx, &a.principal, ws, "batch.undone", "batch", batch,
		map[string]any{"kind": kind, "mode": mode, "brand_id": a.Brand, "deletion_id": d.id, "counts": counts},
		map[string]any{"kind": kind, "mode": mode, "deletion_id": d.id, "records": d.counts["records"], "records_kept": len(keep), "rows": total})
}

func eventIDs(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM customer_events WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func unmarshalNullable(raw json.RawMessage, v *any) error {
	if raw == nil {
		*v = nil
		return nil
	}
	return json.Unmarshal(raw, v)
}

func jsonOrNull(raw json.RawMessage) *string {
	if raw == nil {
		return nil
	}
	s := string(raw)
	return &s
}

// revertName puts a record's name back to prev while it is still want.
func revertName(ctx context.Context, tx pgx.Tx, recordID string, want, prev any) (bool, error) {
	w, _ := want.(string)
	p, _ := prev.(string)
	tag, err := tx.Exec(ctx, `UPDATE records SET name = $3, version = version + 1, updated_at = now() WHERE id = $1 AND name = $2`, recordID, w, p)
	return tag.RowsAffected() > 0, err
}

// ---- Restoring and purging a batch's deletion ----

// restoreBatchRows puts back what an undone batch's deletion holds: its
// records, the rows that hang off them and its other rows, and its field
// values onto fields that still hold what the undo put back.
func restoreBatchRows(ctx context.Context, tx pgx.Tx, a *access, id string) (map[string]int, []notRestored, error) {
	res := map[string]int{}
	tag, err := tx.Exec(ctx, `UPDATE records SET deleted_at = NULL, deletion_id = NULL,
		archived_at = CASE WHEN archived_at = deleted_at THEN NULL ELSE archived_at END, version = version + 1, updated_at = now()
		WHERE workspace_id = $1 AND deletion_id = $2`, a.WorkspaceID, id)
	if err != nil {
		return nil, nil, err
	}
	if n := int(tag.RowsAffected()); n > 0 {
		res["records"] = n
	}
	var failed []notRestored
	payments := map[string]bool{}
	for _, table := range deletedTables {
		ids, left, err := restoreTable(ctx, tx, a.WorkspaceID, id, table)
		if err != nil {
			return nil, nil, err
		}
		if len(ids) > 0 {
			res[table] = len(ids)
		}
		failed = append(failed, left...)
		if table == "customer_events" && len(ids) > 0 {
			rows, err := tx.Query(ctx, `SELECT DISTINCT record_id::text FROM customer_events WHERE id = ANY($1::uuid[]) AND record_id IS NOT NULL AND name = ANY($2)`,
				ids, paymentEvents)
			if err != nil {
				return nil, nil, err
			}
			recs, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return nil, nil, err
			}
			for _, r := range recs {
				payments[r] = true
			}
		}
	}
	if err := relinkInbox(ctx, tx, a.WorkspaceID, id); err != nil {
		return nil, nil, err
	}
	// Field values the undo put back go to the batch's value again, while
	// nobody changed them since.
	rows, err := tx.Query(ctx, `SELECT x.id, x.record_id::text, x.data FROM deleted_rows x WHERE x.deletion_id = $1 AND x.source_table = $2 ORDER BY x.id`, id, fieldValueRows)
	if err != nil {
		return nil, nil, err
	}
	type fv struct {
		seq    int64
		record string
		data   struct {
			Key      string          `json:"key"`
			Value    json.RawMessage `json:"value"`
			Previous json.RawMessage `json:"previous"`
			Had      bool            `json:"had"`
		}
	}
	values, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (fv, error) {
		var v fv
		var raw []byte
		if err := row.Scan(&v.seq, &v.record, &raw); err != nil {
			return v, err
		}
		return v, json.Unmarshal(raw, &v.data)
	})
	if err != nil {
		return nil, nil, err
	}
	c, err := loadCatalog(ctx, tx, a.WorkspaceID, a.Brand, false)
	if err != nil {
		return nil, nil, err
	}
	for _, v := range values {
		var want, next any
		prevRaw := v.data.Previous
		if !v.data.Had || string(prevRaw) == "null" {
			prevRaw = nil
		}
		if err := unmarshalNullable(prevRaw, &want); err != nil {
			return nil, nil, err
		}
		if err := unmarshalNullable(v.data.Value, &next); err != nil {
			return nil, nil, err
		}
		if string(v.data.Value) == "null" {
			v.data.Value = nil
		}
		var ok bool
		if v.data.Key == "name" {
			ok, err = revertName(ctx, tx, v.record, want, next)
		} else {
			ok, err = revertField(quietly(ctx), tx, a, c, v.record, v.data.Key, want, next, v.data.Value != nil, "")
		}
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			failed = append(failed, notRestored{Table: fieldValueRows, ID: v.record + ":" + v.data.Key, Reason: reasonChanged})
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM deleted_rows WHERE id = $1`, v.seq); err != nil {
			return nil, nil, err
		}
		res[fieldValueRows]++
	}
	for r := range payments {
		if err := quietBilling(ctx, tx, a.WorkspaceID, r); err != nil {
			return nil, nil, err
		}
	}
	// Once all of it is back, the batch can be undone again.
	if len(failed) == 0 {
		if _, err := tx.Exec(ctx, `UPDATE batches SET undo_status = NULL, undo_mode = NULL, undone_at = NULL, undone_by = NULL, undo_counts = '{}'
			WHERE deletion_id = $1`, id); err != nil {
			return nil, nil, err
		}
	}
	return res, failed, nil
}

// purgeBatchRecords erases the records an undone batch's deletion still holds,
// with the records merged into them.
func purgeBatchRecords(ctx context.Context, tx pgx.Tx, ws, deletionID string) error {
	rows, err := tx.Query(ctx, `WITH RECURSIVE m AS (SELECT id FROM records WHERE workspace_id = $1 AND deletion_id = $2 AND deleted_at IS NOT NULL
			UNION SELECT r.id FROM records r JOIN m ON r.merged_into = m.id WHERE r.workspace_id = $1)
		SELECT id::text FROM m`, ws, deletionID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) == 0 {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT DISTINCT x.data->>'value' FROM deleted_rows x JOIN records r ON r.id = x.record_id AND r.record_type = 'person'
		WHERE x.deletion_id = $1 AND x.source_table = 'record_identities' AND x.data->>'namespace' = 'email' AND x.record_id = ANY($2::uuid[])`, deletionID, ids)
	if err != nil {
		return err
	}
	emails, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	_, err = customers.Erase(ctx, tx, ws, ids, emails)
	return err
}
