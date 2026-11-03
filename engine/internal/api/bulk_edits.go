package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/smartlists"
)

// Bulk edits (plans/customer-data-platform, step 6, and docs/bulk-edits.md):
// an owner changes many people or companies at once. The records come from
// a smart or static list, a saved query or a hand-picked list of ids; the
// changes set or clear fields (owner included), add or remove tags, put
// records in or take them out of a static list, and archive them.
//
// The owner's rules (2026-11-02):
//   - Only the workspace's owners run a bulk edit or undo one, signed in.
//     There is no size approval. Anyone who may change records can preview.
//   - Every change lands in each record's own history (attribute_changes,
//     and smart_list_changes for lists), carrying the bulk edit, so the
//     record says who changed what and through which bulk edit. Never only
//     an aggregate. An undo is a new bulk edit that adds history; nothing
//     is ever erased.
//
// The records are fixed when the bulk edit is made: exactly what the
// preview counted. It runs in a job, bulkEditChunk records per
// transaction, and a record changed by hand in between keeps that change
// for fields the bulk edit doesn't touch. Each changed record is a
// record.updated (or record.archived) like a hand edit, so rules,
// journeys and webhooks hear of it.

const (
	jobBulkEdit = "bulk_edits.run"

	// bulkEditMax is the most records one bulk edit changes.
	bulkEditMax = 50000
	// bulkEditChunk is how many records one transaction changes.
	bulkEditChunk = 200
	// bulkEditSample is how many records a preview shows changed.
	bulkEditSample = 20
	// bulkUndoWindow is how long a bulk edit can be undone.
	bulkUndoWindow = 7 * 24 * time.Hour
	// bulkEditsShown is the most GET .../bulk-edits lists at once.
	bulkEditsShown = 100
)

func (s *Server) routesBulkEdits(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("GET /v1/brands/{brand}/bulk-edits", s.onBrand(read, s.listBulkEdits))
	mux.HandleFunc("POST /v1/brands/{brand}/bulk-edits", s.onBrand(manage, s.createBulkEdit))
	mux.HandleFunc("POST /v1/brands/{brand}/bulk-edits/preview", s.onBrand(manage, s.previewBulkEdit))
	mux.HandleFunc("GET /v1/brands/{brand}/bulk-edits/{bulk_edit}", s.onBrand(read, s.getBulkEdit))
	mux.HandleFunc("POST /v1/brands/{brand}/bulk-edits/{bulk_edit}/undo", s.onBrand(manage, s.undoBulkEdit))
}

// ownerSignedIn reports whether the caller is one of the workspace's
// owners, signed in: only they run bulk edits.
func ownerSignedIn(a *access) bool {
	return a.Role == auth.RoleOwner && a.TokenID == "" && a.UserID != ""
}

func notOwner(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusForbidden, "owner_only", "only the workspace's owners, signed in, run bulk edits; preview it and ask an owner")
}

// bulkChanges is what a bulk edit does to each record.
type bulkChanges struct {
	// Set sets fields by key; null clears one. owner is a field.
	Set map[string]json.RawMessage `json:"set,omitempty"`
	// AddTags and RemoveTags change the tags field, keeping other tags.
	AddTags    []string `json:"add_tags,omitempty"`
	RemoveTags []string `json:"remove_tags,omitempty"`
	// AddToList and RemoveFromList are static lists (people only).
	AddToList      *string `json:"add_to_list,omitempty"`
	RemoveFromList *string `json:"remove_from_list,omitempty"`
	// Archive archives each record (Recently deleted is for deleting).
	Archive bool `json:"archive,omitempty"`
}

type bulkEditInput struct {
	// Exactly one of these says which records.
	SmartListID  *string     `json:"smart_list_id"`
	SavedQueryID *string     `json:"saved_query_id"`
	RecordIDs    []string    `json:"record_ids"`
	Changes      bulkChanges `json:"changes"`
}

type bulkEditView struct {
	ID         string          `json:"id"`
	BrandID    string          `json:"brand_id"`
	RecordType string          `json:"record_type"`
	Source     string          `json:"source"`
	SourceID   *string         `json:"source_id"`
	Changes    json.RawMessage `json:"changes"`
	Records    int             `json:"records"`
	Status     string          `json:"status"`
	// changed, unchanged, skipped (records); fields and lists (history
	// rows written); done (records gone through so far).
	Counts     map[string]int `json:"counts"`
	ActorID    *string        `json:"actor_id"`
	UserID     *string        `json:"user_id"`
	CreatedAt  time.Time      `json:"created_at"`
	FinishedAt *time.Time     `json:"finished_at"`
	Error      *string        `json:"error"`
	UndoneBy   *string        `json:"undone_by"`
	// Until when it can be undone, while it can.
	UndoUntil *time.Time `json:"undo_until"`
}

const bulkEditColumns = `b.id::text, b.brand_id::text, b.record_type, b.source, b.source_id::text, b.changes, cardinality(b.record_ids),
	b.status, b.counts, b.actor_id::text, b.user_id::text, b.created_at, b.finished_at, b.error, b.undone_by::text`

func scanBulkEdit(row pgx.Row) (bulkEditView, error) {
	var v bulkEditView
	err := row.Scan(&v.ID, &v.BrandID, &v.RecordType, &v.Source, &v.SourceID, &v.Changes, &v.Records,
		&v.Status, &v.Counts, &v.ActorID, &v.UserID, &v.CreatedAt, &v.FinishedAt, &v.Error, &v.UndoneBy)
	if err == nil && v.Source != "undo" && (v.Status == "done" || v.Status == "failed") && v.UndoneBy == nil && time.Since(v.CreatedAt) < bulkUndoWindow {
		until := v.CreatedAt.Add(bulkUndoWindow)
		v.UndoUntil = &until
	}
	return v, err
}

func getBulkEditRow(ctx context.Context, q querier, a *access, id string) (bulkEditView, error) {
	if !validUUID(id) {
		return bulkEditView{}, errNotFound
	}
	v, err := scanBulkEdit(q.QueryRow(ctx, `SELECT `+bulkEditColumns+` FROM bulk_edits b WHERE b.id = $1 AND b.workspace_id = $2 AND b.brand_id = $3`,
		id, a.WorkspaceID, a.Brand))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

// ---- Choosing the records ----

// bulkSelection is the records a bulk edit changes, in id order (the
// order the job locks them in).
type bulkSelection struct {
	recordType string
	source     string
	sourceID   *string
	ids        []string
}

// bulkRecords works out which records a bulk edit changes: the brand's
// live people or companies of one type, merged and deleted ones left out.
func (s *Server) bulkRecords(w http.ResponseWriter, r *http.Request, a *access, in *bulkEditInput) (bulkSelection, bool) {
	ctx := r.Context()
	n := 0
	for _, set := range []bool{in.SmartListID != nil, in.SavedQueryID != nil, in.RecordIDs != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "say which records with exactly one of smart_list_id, saved_query_id or record_ids")
		return bulkSelection{}, false
	}
	var sel bulkSelection
	var candidates []string
	switch {
	case in.SmartListID != nil:
		sel.source, sel.sourceID = "smart_list", in.SmartListID
		var kind, recordType string
		err := s.pool.QueryRow(ctx, `SELECT kind, record_type FROM smart_lists WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`,
			validOrNil(*in.SmartListID), a.WorkspaceID, a.Brand).Scan(&kind, &recordType)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "smart_list_id is not a list of this brand")
			return sel, false
		}
		if err != nil {
			s.internal(w, r, err)
			return sel, false
		}
		table := "smart_list_members"
		if kind == smartlists.KindStatic {
			table = "static_list_members"
		}
		rows, err := s.pool.Query(ctx, `SELECT record_id::text FROM `+table+` WHERE list_id = $1`, *in.SmartListID)
		if err == nil {
			candidates, err = pgx.CollectRows(rows, pgx.RowTo[string])
		}
		if err != nil {
			s.internal(w, r, err)
			return sel, false
		}
		sel.recordType = recordType
	case in.SavedQueryID != nil:
		sel.source, sel.sourceID = "query", in.SavedQueryID
		r.SetPathValue("saved_query", *in.SavedQueryID)
		_, v, ok := s.savedQuerySQL(w, r, a, savedRunInput{})
		if !ok {
			return sel, false
		}
		res, _, err := s.execQuery(ctx, a, v.SQL, queryExportMaxRows, queryExportMaxBytes)
		if s.queryFailed(w, r, err) {
			return sel, false
		}
		col := slices.IndexFunc(res.Columns, func(c queryColumn) bool { return c.Name == "record_id" })
		if col < 0 {
			col = slices.IndexFunc(res.Columns, func(c queryColumn) bool { return c.Name == "id" })
		}
		if col < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "the saved query needs a record_id (or id) column of people or companies")
			return sel, false
		}
		if res.Truncated {
			writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("the saved query returns more than %d rows: narrow it, or use a smart list", queryExportMaxRows))
			return sel, false
		}
		for _, row := range res.Rows {
			if id, ok := row[col].(string); ok && validUUID(id) {
				candidates = append(candidates, strings.ToLower(id))
			}
		}
	default:
		sel.source = "records"
		if len(in.RecordIDs) == 0 || len(in.RecordIDs) > queryExportMaxRows {
			writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("record_ids holds 1 to %d ids; use a smart list for more", queryExportMaxRows))
			return sel, false
		}
		for _, id := range in.RecordIDs {
			if !validUUID(id) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "record_ids holds record ids")
				return sel, false
			}
			candidates = append(candidates, strings.ToLower(id))
		}
	}
	type found struct {
		id, recordType string
	}
	rows, err := s.pool.Query(ctx, `SELECT r.id::text, r.record_type FROM records r
		WHERE r.workspace_id = $1 AND r.brand_id = $2 AND r.id = ANY ($3::uuid[]) AND r.merged_into IS NULL AND r.deleted_at IS NULL
		  AND r.record_type IN ('person', 'account') AND ($4 = '' OR r.record_type = $4)
		ORDER BY r.id LIMIT $5`, a.WorkspaceID, a.Brand, candidates, sel.recordType, bulkEditMax+1)
	var list []found
	if err == nil {
		list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (found, error) {
			var f found
			return f, row.Scan(&f.id, &f.recordType)
		})
	}
	if err != nil {
		s.internal(w, r, err)
		return sel, false
	}
	if len(list) > bulkEditMax {
		writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("a bulk edit changes up to %d records: narrow the list", bulkEditMax))
		return sel, false
	}
	for _, f := range list {
		if sel.recordType == "" {
			sel.recordType = f.recordType
		}
		if f.recordType != sel.recordType {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a bulk edit changes people or companies, not both: pick one kind")
			return sel, false
		}
		sel.ids = append(sel.ids, f.id)
	}
	if len(sel.ids) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "no people or companies of this brand to change: the list or query is empty")
		return sel, false
	}
	return sel, true
}

func validOrNil(id string) *string {
	if validUUID(id) {
		return &id
	}
	return nil
}

// ---- Checking the changes ----

// checkBulkChanges checks the changes against the brand's catalog before
// anything runs, so a bulk edit fails at once, not record by record.
func checkBulkChanges(ctx context.Context, tx pgx.Tx, a *access, recordType string, ch *bulkChanges) error {
	if len(ch.Set) == 0 && len(ch.AddTags) == 0 && len(ch.RemoveTags) == 0 && ch.AddToList == nil && ch.RemoveFromList == nil && !ch.Archive {
		return errInvalid("changes is empty: set fields, add_tags, remove_tags, add_to_list, remove_from_list or archive")
	}
	c, err := loadCatalog(ctx, tx, a.WorkspaceID, a.Brand, false)
	if err != nil {
		return err
	}
	t := c.recordType(recordType)
	if t == nil {
		return errNotFound
	}
	for key, raw := range ch.Set {
		f := t.field(key)
		switch {
		case f == nil:
			return errInvalid(fmt.Sprintf("%s has no field %s", t.Name, key))
		case f.Archived:
			return errInvalid(fmt.Sprintf("field %s is archived; restore it in the catalog to write it", key))
		case f.WritePolicy == "computed":
			return errInvalid(fmt.Sprintf("field %s is computed by Outflare and cannot be written", key))
		case f.WritePolicy == "source_owned":
			return errInvalid(fmt.Sprintf("field %s is owned by its source system", key))
		case key == "tags" && (len(ch.AddTags) > 0 || len(ch.RemoveTags) > 0):
			return errInvalid("set tags, or add_tags and remove_tags, not both")
		}
		if string(raw) == "null" {
			continue
		}
		v, err := normalizeValue(ctx, tx, c, a.WorkspaceID, f, raw)
		if err != nil {
			return err
		}
		if err := checkReferences(ctx, tx, c, a.WorkspaceID, f, asList(v)); err != nil {
			return err
		}
	}
	if len(ch.AddTags) > 0 || len(ch.RemoveTags) > 0 {
		f := t.field("tags")
		if f == nil {
			return errInvalid(t.Name + " has no tags field")
		}
		raw, _ := json.Marshal(append(slices.Clone(ch.AddTags), ch.RemoveTags...))
		if _, err := normalizeValue(ctx, tx, c, a.WorkspaceID, f, raw); err != nil {
			return err
		}
	}
	for _, list := range []*string{ch.AddToList, ch.RemoveFromList} {
		if list == nil {
			continue
		}
		if recordType != "person" {
			return errInvalid("static lists hold people: add_to_list and remove_from_list work on people only")
		}
		var kind string
		err := tx.QueryRow(ctx, `SELECT kind FROM smart_lists WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`,
			validOrNil(*list), a.WorkspaceID, a.Brand).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind != smartlists.KindStatic) {
			return errInvalid("add_to_list and remove_from_list are static lists of this brand")
		}
		if err != nil {
			return err
		}
	}
	if ch.AddToList != nil && ch.RemoveFromList != nil && *ch.AddToList == *ch.RemoveFromList {
		return errInvalid("add_to_list and remove_from_list are the same list")
	}
	return nil
}

// tagsAfter is a record's tags after adding and removing some, in order,
// and whether that differs from before.
func tagsAfter(cur any, add, remove []string) ([]any, bool) {
	var out []any
	seen := map[string]bool{}
	for _, t := range asList(cur) {
		if s, ok := t.(string); ok && !seen[s] && !slices.Contains(remove, s) {
			out, seen[s] = append(out, s), true
		}
	}
	for _, t := range add {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !seen[t] && !slices.Contains(remove, t) {
			out, seen[t] = append(out, t), true
		}
	}
	if out == nil {
		out = []any{}
	}
	return out, !sameJSON(out, cur) && !(len(out) == 0 && cur == nil)
}

// ---- Preview ----

type bulkFieldDiff struct {
	Field string `json:"field"`
	From  any    `json:"from"`
	To    any    `json:"to"`
}

type bulkPreviewRecord struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Changes []bulkFieldDiff `json:"changes"`
}

func (s *Server) previewBulkEdit(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in bulkEditInput
	if !decode(w, r, &in) {
		return
	}
	sel, ok := s.bulkRecords(w, r, a, &in)
	if !ok {
		return
	}
	var sample []bulkPreviewRecord
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		if err := checkBulkChanges(ctx, tx, a, sel.recordType, &in.Changes); err != nil {
			return err
		}
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, a.Brand, false)
		if err != nil {
			return err
		}
		t := c.recordType(sel.recordType)
		for _, id := range sel.ids[:min(len(sel.ids), bulkEditSample)] {
			rec, err := loadRecord(ctx, tx, a, id)
			if err != nil {
				return err
			}
			p := bulkPreviewRecord{ID: rec.ID, Name: rec.Name, Changes: []bulkFieldDiff{}}
			keys := make([]string, 0, len(in.Changes.Set))
			for k := range in.Changes.Set {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				var next any
				if string(in.Changes.Set[k]) != "null" {
					if next, err = normalizeValue(ctx, tx, c, a.WorkspaceID, t.field(k), in.Changes.Set[k]); err != nil {
						return err
					}
				}
				if prev, had := rec.Attributes[k]; !sameJSON(prev, next) && (had || next != nil) {
					p.Changes = append(p.Changes, bulkFieldDiff{k, prev, next})
				}
			}
			if len(in.Changes.AddTags) > 0 || len(in.Changes.RemoveTags) > 0 {
				if next, diff := tagsAfter(rec.Attributes["tags"], in.Changes.AddTags, in.Changes.RemoveTags); diff {
					p.Changes = append(p.Changes, bulkFieldDiff{"tags", rec.Attributes["tags"], next})
				}
			}
			for _, l := range []struct {
				id   *string
				join bool
			}{{in.Changes.AddToList, true}, {in.Changes.RemoveFromList, false}} {
				if l.id == nil {
					continue
				}
				member, err := smartlists.InStatic(ctx, tx, *l.id, rec.ID)
				if err != nil {
					return err
				}
				if member != l.join {
					p.Changes = append(p.Changes, bulkFieldDiff{"list:" + *l.id, member, l.join})
				}
			}
			if in.Changes.Archive && !rec.Archived {
				p.Changes = append(p.Changes, bulkFieldDiff{"archived", false, true})
			}
			sample = append(sample, p)
		}
		return nil
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record_type": sel.recordType, "records": len(sel.ids), "sample": sample,
		"can_run": ownerSignedIn(a)})
}

// ---- Running ----

type bulkEditJob struct {
	ID string `json:"id"`
}

func (s *Server) createBulkEdit(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if !ownerSignedIn(a) {
		notOwner(w, r)
		return
	}
	var in bulkEditInput
	if !decode(w, r, &in) {
		return
	}
	sel, ok := s.bulkRecords(w, r, a, &in)
	if !ok {
		return
	}
	changes, _ := json.Marshal(in.Changes)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := checkBulkChanges(ctx, tx, a, sel.recordType, &in.Changes); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO bulk_edits (workspace_id, brand_id, record_type, source, source_id, changes, record_ids, actor_id, user_id, request_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id::text`,
			a.WorkspaceID, a.Brand, sel.recordType, sel.source, sel.sourceID, changes, sel.ids,
			nullIfEmpty(a.ActorID), nullIfEmpty(a.personID()), nullIfEmpty(RequestID(ctx))).Scan(&id); err != nil {
			return err
		}
		if err := s.enqueueBulkEdit(ctx, tx, a, id); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "bulk_edit.started", "bulk_edit", id,
			map[string]any{"brand_id": a.Brand, "source": sel.source, "record_type": sel.recordType, "records": len(sel.ids)},
			map[string]any{"source": sel.source, "record_type": sel.recordType, "records": len(sel.ids), "kinds": bulkKinds(&in.Changes)})
	})
	if s.recordError(w, r, err) {
		return
	}
	s.writeNewestBulkEdit(w, r, a)
}

// writeNewestBulkEdit answers 202 with the bulk edit this request made.
func (s *Server) writeNewestBulkEdit(w http.ResponseWriter, r *http.Request, a *access) {
	v, err := scanBulkEdit(s.pool.QueryRow(r.Context(), `SELECT `+bulkEditColumns+` FROM bulk_edits b
		WHERE b.workspace_id = $1 AND b.brand_id = $2 AND b.request_id = $3 ORDER BY b.created_at DESC LIMIT 1`,
		a.WorkspaceID, a.Brand, RequestID(r.Context())))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}

// bulkKinds names the kinds of change, for product events.
func bulkKinds(ch *bulkChanges) []string {
	var k []string
	if len(ch.Set) > 0 {
		k = append(k, "set")
	}
	if len(ch.AddTags) > 0 || len(ch.RemoveTags) > 0 {
		k = append(k, "tags")
	}
	if ch.AddToList != nil || ch.RemoveFromList != nil {
		k = append(k, "list")
	}
	if ch.Archive {
		k = append(k, "archive")
	}
	return k
}

func (s *Server) enqueueBulkEdit(ctx context.Context, tx pgx.Tx, a *access, id string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobBulkEdit, WorkspaceID: &a.WorkspaceID, IdempotencyKey: "bulk:" + id,
		Payload: bulkEditJob{ID: id}})
	return err
}

func (s *Server) undoBulkEdit(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if !ownerSignedIn(a) {
		notOwner(w, r)
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		orig, err := getBulkEditRow(ctx, tx, a, r.PathValue("bulk_edit"))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM bulk_edits WHERE id = $1 FOR UPDATE`, orig.ID); err != nil {
			return err
		}
		if orig, err = getBulkEditRow(ctx, tx, a, orig.ID); err != nil {
			return err
		}
		switch {
		case orig.Source == "undo":
			return recordConflict{"conflict", "an undo can't be undone: make a new bulk edit"}
		case orig.UndoneBy != nil:
			return recordConflict{"conflict", "this bulk edit was already undone by " + *orig.UndoneBy}
		case orig.Status != "done" && orig.Status != "failed":
			return recordConflict{"conflict", "this bulk edit hasn't finished: undo it once it is done"}
		case orig.UndoUntil == nil:
			return recordConflict{"undo_expired", "a bulk edit can be undone for 7 days; this one is older: make a new bulk edit to change the records back"}
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO bulk_edits (workspace_id, brand_id, record_type, source, source_id, changes, record_ids, actor_id, user_id, request_id)
			SELECT workspace_id, brand_id, record_type, 'undo', id, jsonb_build_object('undo', id), record_ids, $2, $3, $4 FROM bulk_edits WHERE id = $1
			RETURNING id::text`, orig.ID, nullIfEmpty(a.ActorID), nullIfEmpty(a.personID()), nullIfEmpty(RequestID(ctx))).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE bulk_edits SET undone_by = $2 WHERE id = $1`, orig.ID, id); err != nil {
			return err
		}
		if err := s.enqueueBulkEdit(ctx, tx, a, id); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "bulk_edit.undone", "bulk_edit", orig.ID,
			map[string]any{"brand_id": a.Brand, "undo_id": id, "records": orig.Records},
			map[string]any{"records": orig.Records})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "bulk edit not found")
		return
	}
	if s.recordError(w, r, err) {
		return
	}
	s.writeNewestBulkEdit(w, r, a)
}

// bulkEditJob runs a bulk edit or an undo, chunk by chunk. A retry carries
// on from the last finished chunk (counts.done).
func (s *Server) bulkEditJob(ctx context.Context, job jobs.Job) error {
	var p bulkEditJob
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.ID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	var (
		be      bulkEditView
		ids     []string
		reqID   *string
		changes bulkChanges
	)
	row := s.pool.QueryRow(ctx, `SELECT `+bulkEditColumns+`, b.record_ids::text[], b.request_id FROM bulk_edits b WHERE b.id = $1 AND b.workspace_id = $2`,
		p.ID, *job.WorkspaceID)
	if err := row.Scan(&be.ID, &be.BrandID, &be.RecordType, &be.Source, &be.SourceID, &be.Changes, &be.Records,
		&be.Status, &be.Counts, &be.ActorID, &be.UserID, &be.CreatedAt, &be.FinishedAt, &be.Error, &be.UndoneBy, &ids, &reqID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if be.Status == "done" || be.Status == "failed" {
		return nil
	}
	kind := "edit"
	if be.Source == "undo" {
		kind = "undo"
	} else if err := json.Unmarshal(be.Changes, &changes); err != nil {
		return fmt.Errorf("%w: bulk edit %s changes: %w", jobs.ErrPermanent, be.ID, err)
	}
	if reqID != nil {
		ctx = context.WithValue(ctx, requestIDKey, *reqID)
	}
	a := &access{principal: principal{ActorID: deref(be.ActorID), ActorType: "human", UserID: deref(be.UserID)},
		WorkspaceID: *job.WorkspaceID, Brand: be.BrandID, Role: auth.RoleOwner}
	if _, err := s.pool.Exec(ctx, `UPDATE bulk_edits SET status = 'running' WHERE id = $1 AND status = 'queued'`, be.ID); err != nil {
		return err
	}
	counts := be.Counts
	if counts == nil {
		counts = map[string]int{}
	}
	// A failure is retried from the last finished chunk; after the last
	// attempt the bulk edit is failed, with what it did kept (and undoable).
	fail := func(err error) error {
		if job.Attempt < job.MaxAttempts {
			return fmt.Errorf("bulk edit %s: %w", be.ID, err)
		}
		metrics.BulkEditFinished(kind, "failed", counts, time.Since(be.CreatedAt))
		msg := "the bulk edit stopped part way: the first counts.done records are changed; undo it, or make a new bulk edit for the rest"
		if _, uerr := s.pool.Exec(context.WithoutCancel(ctx), `UPDATE bulk_edits SET status = 'failed', error = $2, finished_at = now() WHERE id = $1`, be.ID, msg); uerr != nil {
			return errors.Join(err, uerr)
		}
		return fmt.Errorf("%w: bulk edit %s: %w", jobs.ErrPermanent, be.ID, err)
	}
	for start := counts["done"]; start < len(ids); start += bulkEditChunk {
		chunk := ids[start:min(start+bulkEditChunk, len(ids))]
		next := maps.Clone(counts)
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('outflare.bulk_edit_id', $1, true)`, be.ID); err != nil {
				return err
			}
			var res map[string]int
			var err error
			if kind == "undo" {
				res, err = undoBulkChunk(ctx, tx, a, be.ID, deref(be.SourceID), chunk)
			} else {
				res, err = applyBulkChunk(ctx, tx, a, be.ID, be.RecordType, &changes, chunk)
			}
			if err != nil {
				return err
			}
			for k, n := range res {
				next[k] += n
			}
			next["done"] = start + len(chunk)
			_, err = tx.Exec(ctx, `UPDATE bulk_edits SET counts = $2 WHERE id = $1`, be.ID, next)
			return err
		})
		if err != nil {
			return fail(err)
		}
		counts = next
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The history rows are the record of what changed: count them.
		var fields, lists int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM attribute_changes WHERE bulk_edit_id = $1),
			(SELECT count(*) FROM smart_list_changes WHERE bulk_edit_id = $1)`, be.ID).Scan(&fields, &lists); err != nil {
			return err
		}
		counts["fields"], counts["lists"] = fields, lists
		if _, err := tx.Exec(ctx, `UPDATE bulk_edits SET status = 'done', counts = $2, finished_at = now(), error = NULL WHERE id = $1`, be.ID, counts); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "bulk_edit.finished", "bulk_edit", be.ID,
			map[string]any{"brand_id": be.BrandID, "kind": kind, "counts": counts},
			map[string]any{"kind": kind, "changed": counts["changed"], "unchanged": counts["unchanged"], "skipped": counts["skipped"]})
	})
	if err != nil {
		return fail(err)
	}
	metrics.BulkEditFinished(kind, "done", counts, time.Since(be.CreatedAt))
	return nil
}

// lockBulkRecord locks one record of the chunk, or says to skip it: gone,
// merged or moved to another brand since the bulk edit was made.
func lockBulkRecord(ctx context.Context, tx pgx.Tx, a *access, id string) (recordView, bool, error) {
	rec, err := lockRecord(ctx, tx, a, id)
	var rc recordConflict
	if errors.Is(err, errNotFound) || errors.Is(err, errSharedRecord) || errors.As(err, &rc) {
		return rec, false, nil
	}
	if err != nil {
		return rec, false, err
	}
	if rec.Attributes == nil {
		rec.Attributes = map[string]any{}
	}
	if rec.AttributeSources == nil {
		rec.AttributeSources = map[string]string{}
	}
	return rec, true, nil
}

// saveBulkRecord writes a record's new attributes and its audit action,
// as a hand edit does.
func saveBulkRecord(ctx context.Context, tx pgx.Tx, a *access, rec *recordView, bulkID string) error {
	if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now() WHERE id = $1`,
		rec.ID, rec.Attributes, rec.AttributeSources); err != nil {
		return err
	}
	return record(ctx, tx, &a.principal, a.WorkspaceID, "record.updated", "record", rec.ID, map[string]any{"type": rec.Type, "bulk_edit_id": bulkID})
}

func setArchived(ctx context.Context, tx pgx.Tx, a *access, rec *recordView, archived bool, bulkID string) error {
	at, action := "now()", "record.archived"
	if !archived {
		at, action = "NULL", "record.restored"
	}
	if _, err := tx.Exec(ctx, `UPDATE records SET archived_at = `+at+`, version = version + 1, updated_at = now() WHERE id = $1`, rec.ID); err != nil {
		return err
	}
	if err := archiveChanged(ctx, tx, a, rec.ID, archived, "manual"); err != nil {
		return err
	}
	return record(ctx, tx, &a.principal, a.WorkspaceID, action, "record", rec.ID, map[string]any{"type": rec.Type, "bulk_edit_id": bulkID})
}

// applyBulkChunk makes a bulk edit's changes to some records: fields and
// tags, then lists, then archiving (static lists take only live people).
func applyBulkChunk(ctx context.Context, tx pgx.Tx, a *access, bulkID, recordType string, ch *bulkChanges, ids []string) (map[string]int, error) {
	c, err := loadCatalog(ctx, tx, a.WorkspaceID, a.Brand, false)
	if err != nil {
		return nil, err
	}
	t := c.recordType(recordType)
	if t == nil {
		return nil, errNotFound
	}
	res := map[string]int{}
	recs := map[string]*recordView{}
	var live []string
	for _, id := range ids {
		rec, ok, err := lockBulkRecord(ctx, tx, a, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			res["skipped"]++
			continue
		}
		live, recs[id] = append(live, id), &rec
		in := map[string]json.RawMessage{}
		for k, v := range ch.Set {
			in[k] = v
		}
		if len(ch.AddTags) > 0 || len(ch.RemoveTags) > 0 {
			if next, diff := tagsAfter(rec.Attributes["tags"], ch.AddTags, ch.RemoveTags); diff {
				in["tags"], _ = json.Marshal(next)
			}
		}
		did, err := applyAttributes(ctx, tx, c, t, a, &rec, in, "manual")
		if err != nil {
			return nil, err
		}
		if did {
			if err := saveBulkRecord(ctx, tx, a, &rec, bulkID); err != nil {
				return nil, err
			}
		}
	}
	if len(live) > 0 {
		var err error
		if ch.AddToList != nil {
			_, err = smartlists.AddStatic(ctx, tx, a.WorkspaceID, *ch.AddToList, live, "bulk_edit", bulkID)
		}
		if err == nil && ch.RemoveFromList != nil {
			_, err = smartlists.RemoveStatic(ctx, tx, a.WorkspaceID, *ch.RemoveFromList, live)
		}
		if errors.Is(err, smartlists.ErrGone) || errors.Is(err, smartlists.ErrNotStatic) {
			return nil, errInvalid("the list was deleted since the bulk edit was made")
		}
		if err != nil {
			return nil, err
		}
	}
	if ch.Archive {
		for _, id := range live {
			if rec := recs[id]; !rec.Archived {
				if err := setArchived(ctx, tx, a, rec, true, bulkID); err != nil {
					return nil, err
				}
			}
		}
	}
	return countTouched(ctx, tx, live, res)
}

// countTouched counts the records of a chunk that got history from this
// bulk edit (changed) and those that didn't (unchanged): the history is
// what changed.
func countTouched(ctx context.Context, tx pgx.Tx, live []string, res map[string]int) (map[string]int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM unnest($1::uuid[]) x(id) WHERE
		EXISTS (SELECT 1 FROM attribute_changes c WHERE c.record_id = x.id AND c.bulk_edit_id = current_setting('outflare.bulk_edit_id')::uuid)
		OR EXISTS (SELECT 1 FROM smart_list_changes c WHERE c.record_id = x.id AND c.bulk_edit_id = current_setting('outflare.bulk_edit_id')::uuid)`,
		live).Scan(&n)
	res["changed"], res["unchanged"] = n, len(live)-n
	return res, err
}

// undoBulkChunk puts some records back as they were before bulk edit
// orig: each field it set goes back unless someone changed it since, a
// record it archived is restored unless restored since, and list moves
// are reversed unless moved back since. What it can't put back stays and
// is counted (kept). It writes new history, carrying the undo; the old
// history stays.
func undoBulkChunk(ctx context.Context, tx pgx.Tx, a *access, self, orig string, ids []string) (map[string]int, error) {
	c, err := loadCatalog(ctx, tx, a.WorkspaceID, a.Brand, false)
	if err != nil {
		return nil, err
	}
	type fieldChange struct {
		key      string
		old, now *json.RawMessage
	}
	rows, err := tx.Query(ctx, `SELECT record_id::text, field_key, old_value, new_value FROM attribute_changes
		WHERE bulk_edit_id = $1 AND record_id = ANY ($2::uuid[]) ORDER BY id`, orig, ids)
	if err != nil {
		return nil, err
	}
	byRecord := map[string][]fieldChange{}
	for rows.Next() {
		var id string
		var f fieldChange
		if err := rows.Scan(&id, &f.key, &f.old, &f.now); err != nil {
			rows.Close()
			return nil, err
		}
		byRecord[id] = append(byRecord[id], f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res := map[string]int{}
	var live []string
	for _, id := range ids {
		rec, ok, err := lockBulkRecord(ctx, tx, a, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			res["skipped"]++
			continue
		}
		live = append(live, id)
		t := c.recordType(rec.Type)
		in := map[string]json.RawMessage{}
		for _, f := range byRecord[id] {
			if f.key == "archived" {
				if !rec.Archived {
					res["kept"]++
				} else if err := setArchived(ctx, tx, a, &rec, false, self); err != nil {
					return nil, err
				}
				continue
			}
			var want any
			if f.now != nil {
				_ = json.Unmarshal(*f.now, &want)
			}
			cur, had := rec.Attributes[f.key]
			if t == nil || t.field(f.key) == nil || had != (f.now != nil) || !sameJSON(cur, want) {
				res["kept"]++
				continue
			}
			in[f.key] = json.RawMessage("null")
			if f.old != nil {
				in[f.key] = *f.old
			}
		}
		if len(in) == 0 {
			continue
		}
		did, err := applyAttributes(ctx, tx, c, t, a, &rec, in, "manual")
		if err != nil {
			return nil, err
		}
		if did {
			if err := saveBulkRecord(ctx, tx, a, &rec, self); err != nil {
				return nil, err
			}
		}
	}
	// Lists: reverse each move that still stands.
	lrows, err := tx.Query(ctx, `SELECT c.list_id::text, c.change, array_agg(c.record_id::text) FROM smart_list_changes c
		WHERE c.bulk_edit_id = $1 AND c.record_id = ANY ($2::uuid[]) GROUP BY 1, 2`, orig, live)
	if err != nil {
		return nil, err
	}
	type move struct {
		list, change string
		ids          []string
	}
	moves, err := pgx.CollectRows(lrows, func(row pgx.CollectableRow) (move, error) {
		var m move
		return m, row.Scan(&m.list, &m.change, &m.ids)
	})
	if err != nil {
		return nil, err
	}
	for _, m := range moves {
		var ch smartlists.Change
		if m.change == "enter" {
			ch, err = smartlists.RemoveStatic(ctx, tx, a.WorkspaceID, m.list, m.ids)
		} else {
			ch, err = smartlists.AddStatic(ctx, tx, a.WorkspaceID, m.list, m.ids, "bulk_edit", self)
		}
		if errors.Is(err, smartlists.ErrGone) {
			res["kept"] += len(m.ids)
			continue
		}
		if err != nil {
			return nil, err
		}
		res["kept"] += len(m.ids) - ch.Changed
	}
	return countTouched(ctx, tx, live, res)
}

// ---- Reading ----

func (s *Server) listBulkEdits(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	limit, err := limitParam(r, 50, bulkEditsShown)
	if s.allocError(w, r, err) {
		return
	}
	var before *time.Time
	if v, ok := pageCursor(w, r, pageBefore); !ok {
		return
	} else if v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			invalidCursor(w, r)
			return
		}
		before = &t
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+bulkEditColumns+` FROM bulk_edits b WHERE b.workspace_id = $1 AND b.brand_id = $2
		AND ($3::timestamptz IS NULL OR b.created_at < $3) ORDER BY b.created_at DESC, b.id DESC LIMIT $4`, a.WorkspaceID, a.Brand, before, limit)
	var list []bulkEditView
	if err == nil {
		list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (bulkEditView, error) { return scanBulkEdit(row) })
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []bulkEditView{}
	}
	var next *string
	if len(list) == limit {
		c := list[len(list)-1].CreatedAt.Format(time.RFC3339Nano)
		next = &c
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}

func (s *Server) getBulkEdit(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := getBulkEditRow(r.Context(), s.pool, a, r.PathValue("bulk_edit"))
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "bulk edit not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
