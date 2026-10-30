package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Privacy export (plans/brands/README.md): everything a brand holds about
// one person or company, as a file. Asking for one makes a pending export
// and queues the privacy_export.build job, which gathers the record and
// every row that points at it (customers.Follows and customers.Owned, the
// lists merges already keep complete) in the record's brand only, and
// stores it as gzipped JSON. The file can be downloaded for 7 days; then
// the sweep deletes it. Erasing the record deletes its exports.

const (
	jobPrivacyExport = "privacy_export.build"
	// privacyExportDays is how long a ready export can be downloaded.
	privacyExportDays = 7
	// maxPrivacyExportBytes caps the uncompressed JSON: past it the export
	// fails with too_large rather than hold a worker and the database.
	maxPrivacyExportBytes = 100 << 20
	// privacyExportStuck is how long an export may stay pending before
	// the sweep fails it: its job is dead or lost.
	privacyExportStuck = 2 * time.Hour
)

// errExportTooLarge stops a build past maxPrivacyExportBytes.
var errExportTooLarge = errors.New("privacy export: over the size limit")

// exportLimit is the cap a build checks; tests lower it.
var exportLimit = func() *atomic.Int64 { var n atomic.Int64; n.Store(maxPrivacyExportBytes); return &n }()

// hiddenColumn matches the names of columns an export never includes:
// tokens, secrets, passwords, hashes, sealed values and credentials. Binary
// columns (sealed ciphertext, digests) and search vectors are left out by
// type as well.
var hiddenColumn = regexp.MustCompile(`(?i)(token|secret|password|passwd|hash|sealed|credential|api_?key|private_?key|signature)`)

// exportHidden reports whether a column of this name and type is left out
// of an export.
func exportHidden(name, dataType, udt string) bool {
	if hiddenColumn.MatchString(name) {
		return true
	}
	switch dataType {
	case "bytea", "tsvector":
		return true
	case "USER-DEFINED":
		// pgvector's vectors are derived from text already exported.
		return udt == "vector"
	}
	return false
}

func (s *Server) routesPrivacyExports(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("POST /v1/brands/{brand}/records/{record}/privacy-exports", s.onBrand(manage, s.requestPrivacyExport))
	mux.HandleFunc("GET /v1/brands/{brand}/privacy-exports", s.onBrand(read, s.listPrivacyExports))
	mux.HandleFunc("GET /v1/brands/{brand}/privacy-exports/{export}", s.onBrand(read, s.getPrivacyExport))
	// The file is personal data in bulk: downloading it takes the scope
	// that can change and erase people, not just read them.
	mux.HandleFunc("GET /v1/brands/{brand}/privacy-exports/{export}/download", s.onBrand(manage, s.downloadPrivacyExport))
}

// ---- Asking for one ----

type privacyExportView struct {
	ID          string         `json:"id"`
	BrandID     string         `json:"brand_id"`
	RecordID    *string        `json:"record_id"`
	Status      string         `json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
	FinishedAt  *time.Time     `json:"finished_at"`
	ExpiresAt   *time.Time     `json:"expires_at"`
	SizeBytes   *int64         `json:"size_bytes"`
	Counts      map[string]int `json:"counts"`
	ErrorCode   *string        `json:"error_code"`
	Downloads   int            `json:"downloads"`
	DownloadURL *string        `json:"download_url"`
}

const privacyExportColumns = `x.id::text, x.brand_id::text, x.record_id::text, x.status, x.created_at, x.finished_at, x.expires_at,
	x.size_bytes, x.counts, x.error_code, x.downloads`

func scanPrivacyExport(row pgx.CollectableRow) (privacyExportView, error) {
	var v privacyExportView
	err := row.Scan(&v.ID, &v.BrandID, &v.RecordID, &v.Status, &v.CreatedAt, &v.FinishedAt, &v.ExpiresAt, &v.SizeBytes, &v.Counts, &v.ErrorCode, &v.Downloads)
	return v, err
}

// withLink adds the download link to a ready export.
func (s *Server) withLink(v privacyExportView) privacyExportView {
	if v.Counts == nil {
		v.Counts = map[string]int{}
	}
	if v.Status == "ready" {
		u := s.publicURL + "/v1/brands/" + v.BrandID + "/privacy-exports/" + v.ID + "/download"
		v.DownloadURL = &u
	}
	return v
}

func (s *Server) requestPrivacyExport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	rec := r.PathValue("record")
	if !validUUID(rec) {
		writeError(w, r, http.StatusNotFound, "not_found", "record not found")
		return
	}
	var out privacyExportView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Only a record of this brand: another brand's person is not found.
		var recType string
		var merged *string
		err := tx.QueryRow(ctx, `SELECT record_type, merged_into::text FROM records WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`,
			rec, a.WorkspaceID, a.Brand).Scan(&recType, &merged)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if merged != nil {
			return errMergedRecord(*merged)
		}
		row, err := tx.Query(ctx, `INSERT INTO privacy_exports (workspace_id, brand_id, record_id, actor_id, actor_type, user_id, token_id, request_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+strings.ReplaceAll(privacyExportColumns, "x.", ""),
			a.WorkspaceID, a.Brand, rec, nullIfEmpty(a.ActorID), a.ActorType, nullIfEmpty(a.personID()), nullIfEmpty(a.TokenID), nullIfEmpty(RequestID(ctx)))
		if err != nil {
			return err
		}
		if out, err = pgx.CollectExactlyOneRow(row, scanPrivacyExport); err != nil {
			return err
		}
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPrivacyExport, WorkspaceID: &a.WorkspaceID, MaxAttempts: 5,
			Payload: map[string]string{"export_id": out.ID}, IdempotencyKey: "privacy_export:" + out.ID}); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "privacy_export.requested", "privacy_export", out.ID,
			map[string]any{"record_id": rec, "brand_id": a.Brand, "record_type": recType}, map[string]any{"record_id": rec})
	})
	var m errMergedRecord
	if errors.As(err, &m) {
		writeError(w, r, http.StatusConflict, "conflict", "this record was merged into "+string(m)+"; export that record, which now holds what was this one's")
		return
	}
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "record not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, s.withLink(out))
}

// errMergedRecord is a record merged into another, whose id it holds.
type errMergedRecord string

func (e errMergedRecord) Error() string { return "record merged into " + string(e) }

// ---- Reading ----

func (s *Server) listPrivacyExports(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	beforeAt, beforeID, err := timeCursor(q.Get("before"))
	if s.allocError(w, r, err) {
		return
	}
	where := []string{"x.brand_id = $1", "x.workspace_id = $2", "(x.created_at, x.id::text) < ($3, $4)"}
	args := []any{a.Brand, a.WorkspaceID, beforeAt, beforeID}
	if v := q.Get("record"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "record is a record's id")
			return
		}
		args = append(args, v)
		where = append(where, "x.record_id = $"+strconv.Itoa(len(args)))
	}
	if v := q.Get("status"); v != "" {
		if !slices.Contains([]string{"pending", "ready", "failed", "expired"}, v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is pending, ready, failed or expired")
			return
		}
		args = append(args, v)
		where = append(where, "x.status = $"+strconv.Itoa(len(args)))
	}
	args = append(args, limit)
	var list []privacyExportView
	err = s.readTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+privacyExportColumns+` FROM privacy_exports x WHERE `+strings.Join(where, " AND ")+`
			ORDER BY x.created_at DESC, x.id::text DESC LIMIT $`+strconv.Itoa(len(args)), args...)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, scanPrivacyExport)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	out := make([]privacyExportView, 0, len(list))
	for _, v := range list {
		out = append(out, s.withLink(v))
	}
	var next *string
	if len(list) == limit {
		last := list[len(list)-1]
		v := last.CreatedAt.Format(time.RFC3339Nano) + "|" + last.ID
		next = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_before": next})
}

// privacyExport reads one of the brand's exports.
func (s *Server) privacyExport(ctx context.Context, a *access, id string) (privacyExportView, error) {
	if !validUUID(id) {
		return privacyExportView{}, errNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT `+privacyExportColumns+` FROM privacy_exports x WHERE x.id = $1 AND x.brand_id = $2 AND x.workspace_id = $3`,
		id, a.Brand, a.WorkspaceID)
	if err != nil {
		return privacyExportView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanPrivacyExport)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return s.withLink(v), err
}

func (s *Server) getPrivacyExport(w http.ResponseWriter, r *http.Request) {
	v, err := s.privacyExport(r.Context(), accessFrom(r.Context()), r.PathValue("export"))
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "privacy export not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) downloadPrivacyExport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	id := r.PathValue("export")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "privacy export not found")
		return
	}
	var (
		status  string
		content []byte
		expires *time.Time
		record  *string
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT status, content, expires_at, record_id::text FROM privacy_exports
			WHERE id = $1 AND brand_id = $2 AND workspace_id = $3 FOR UPDATE`, id, a.Brand, a.WorkspaceID).Scan(&status, &content, &expires, &record)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if status != "ready" || content == nil || (expires != nil && !expires.After(time.Now())) {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE privacy_exports SET downloads = downloads + 1 WHERE id = $1`, id); err != nil {
			return err
		}
		data := map[string]any{"brand_id": a.Brand, "size_bytes": len(content)}
		if record != nil {
			data["record_id"] = *record
		}
		return recordExport(ctx, tx, &a.principal, a.WorkspaceID, "privacy_export.downloaded", id, data)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "privacy export not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	switch {
	case status == "expired" || (status == "ready" && (content == nil || (expires != nil && !expires.After(time.Now())))):
		writeError(w, r, http.StatusGone, "export_expired", "this privacy export expired and its file was deleted; ask for a new one")
		return
	case status != "ready":
		writeError(w, r, http.StatusConflict, "export_not_ready", "this privacy export is "+status+"; download it once its status is ready")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="privacy-export-`+id+`.json.gz"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
	w.Write(content) //nolint:errcheck // the client went away
}

// recordExport records an action on an export.
func recordExport(ctx context.Context, tx pgx.Tx, p *principal, ws, action, id string, data map[string]any) error {
	props := map[string]any{}
	for _, k := range []string{"record_id", "size_bytes", "rows", "error_code"} {
		if v, ok := data[k]; ok {
			props[k] = v
		}
	}
	return recordWithProps(ctx, tx, p, ws, action, "privacy_export", id, data, props)
}

// ---- Building ----

func (s *Server) privacyExportJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		ExportID string `json:"export_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.ExportID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	err := s.buildPrivacyExport(ctx, *job.WorkspaceID, p.ExportID)
	if err != nil && ctx.Err() == nil && (errors.Is(err, jobs.ErrPermanent) || job.Attempt >= job.MaxAttempts) {
		// The last try: the export fails rather than wait for ever.
		if ferr := s.failPrivacyExport(ctx, *job.WorkspaceID, p.ExportID, "internal", 0); ferr != nil {
			return errors.Join(err, ferr)
		}
	}
	return err
}

// exportRow is who asked for an export, so its result is recorded as theirs.
type exportRow struct {
	brand, brandName, status string
	record                   *string
	requestID                *string
	tokenID, actorID         *string
	actorType                string
	userID                   *string
}

func (x exportRow) principal() principal {
	return inboxRow{requestID: x.requestID, tokenID: x.tokenID, actorID: x.actorID, actorType: x.actorType, userID: x.userID}.principal()
}

func (x exportRow) ctx(ctx context.Context) context.Context {
	if x.requestID != nil {
		return context.WithValue(ctx, requestIDKey, *x.requestID)
	}
	return ctx
}

func loadExport(ctx context.Context, q pgx.Tx, ws, id string) (exportRow, error) {
	var x exportRow
	err := q.QueryRow(ctx, `SELECT x.brand_id::text, b.name, x.status, x.record_id::text, x.request_id, x.token_id::text, x.actor_id::text, x.actor_type, x.user_id::text
		FROM privacy_exports x JOIN brands b ON b.id = x.brand_id WHERE x.id = $1 AND x.workspace_id = $2 FOR UPDATE OF x`, id, ws).
		Scan(&x.brand, &x.brandName, &x.status, &x.record, &x.requestID, &x.tokenID, &x.actorID, &x.actorType, &x.userID)
	return x, err
}

// buildPrivacyExport gathers the export's record and writes the file.
func (s *Server) buildPrivacyExport(ctx context.Context, ws, id string) error {
	start := time.Now()
	var (
		code       string
		rawBytes   int64
		size       int
		counts     map[string]int
		builtReady bool
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		code, builtReady = "", false
		x, err := loadExport(ctx, tx, ws, id)
		if errors.Is(err, pgx.ErrNoRows) {
			// Erased with its record before it was built.
			return nil
		}
		if err != nil || x.status != "pending" {
			return err
		}
		if x.record == nil {
			code = "record_gone"
			return s.failExportTx(ctx, tx, ws, id, x, code, 0)
		}
		file, n, c, err := gatherPrivacyExport(ctx, tx, ws, x.brand, x.brandName, *x.record)
		switch {
		case errors.Is(err, errExportTooLarge):
			code = "too_large"
			return s.failExportTx(ctx, tx, ws, id, x, code, n)
		case errors.Is(err, errNotFound):
			code = "record_gone"
			return s.failExportTx(ctx, tx, ws, id, x, code, 0)
		case err != nil:
			return err
		}
		rawBytes, size, counts = n, len(file), c
		var expires time.Time
		if err := tx.QueryRow(ctx, `UPDATE privacy_exports SET status = 'ready', content = $2, size_bytes = $3, raw_bytes = $4, counts = $5,
				finished_at = now(), expires_at = now() + make_interval(days => $6) WHERE id = $1 RETURNING expires_at`,
			id, file, size, rawBytes, counts, privacyExportDays).Scan(&expires); err != nil {
			return err
		}
		rows := 0
		for _, n := range counts {
			rows += n
		}
		p := x.principal()
		builtReady = true
		return recordExport(x.ctx(ctx), tx, &p, ws, "privacy_export.ready", id, map[string]any{"record_id": *x.record, "brand_id": x.brand,
			"size_bytes": size, "rows": rows, "counts": counts, "expires_at": expires})
	})
	took := time.Since(start)
	switch {
	case err != nil:
		metrics.PrivacyExportBuilt("error", took, 0)
	case builtReady:
		metrics.PrivacyExportBuilt("ready", took, rawBytes)
	case code != "":
		metrics.PrivacyExportBuilt(code, took, rawBytes)
		s.log.Warn("privacy export failed", "export_id", id, "error_code", code, "bytes", rawBytes)
	}
	return err
}

// failPrivacyExport marks a pending export failed with code, in its own
// transaction.
func (s *Server) failPrivacyExport(ctx context.Context, ws, id, code string, n int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		x, err := loadExport(ctx, tx, ws, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil || x.status != "pending" {
			return err
		}
		return s.failExportTx(ctx, tx, ws, id, x, code, n)
	})
}

func (s *Server) failExportTx(ctx context.Context, tx pgx.Tx, ws, id string, x exportRow, code string, n int64) error {
	if _, err := tx.Exec(ctx, `UPDATE privacy_exports SET status = 'failed', error_code = $2, raw_bytes = NULLIF($3, 0), finished_at = now() WHERE id = $1`,
		id, code, n); err != nil {
		return err
	}
	ev := track.Event{Name: "privacy_export.build_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"export_id": id, "brand_id": x.brand}}
	track.Fail(ctx, s.log, ev, code, fmt.Errorf("privacy export %s failed: %s", id, code))
	data := map[string]any{"brand_id": x.brand, "error_code": code}
	if x.record != nil {
		data["record_id"] = *x.record
	}
	p := x.principal()
	return recordExport(x.ctx(ctx), tx, &p, ws, "privacy_export.failed", id, data)
}

// exportSource is a table that points at records, with the columns that do.
type exportSource struct {
	table string
	cols  []string
}

// privacyExportSources are the tables an export reads: every column that
// points at a record (customers.Follows and customers.Owned, which a test
// keeps complete), a table's columns together so a row is in the file once.
// The exports themselves are left out.
func privacyExportSources() []exportSource {
	var out []exportSource
	for _, ref := range slices.Concat(customers.Follows, customers.Owned) {
		if ref.Table == "privacy_exports" {
			continue
		}
		i := slices.IndexFunc(out, func(s exportSource) bool { return s.table == ref.Table })
		if i < 0 {
			out = append(out, exportSource{table: ref.Table})
			i = len(out) - 1
		}
		if !slices.Contains(out[i].cols, ref.Column) {
			out[i].cols = append(out[i].cols, ref.Column)
		}
	}
	return out
}

// tableColumns returns a table's columns by name, with their types; none
// when the table doesn't exist.
func tableColumns(ctx context.Context, tx pgx.Tx, table string) (map[string][2]string, error) {
	rows, err := tx.Query(ctx, `SELECT column_name, data_type, udt_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1`, table)
	if err != nil {
		return nil, err
	}
	out := map[string][2]string{}
	for rows.Next() {
		var name, typ, udt string
		if err := rows.Scan(&name, &typ, &udt); err != nil {
			return nil, err
		}
		out[name] = [2]string{typ, udt}
	}
	return out, rows.Err()
}

// exportWriter writes the file's JSON, gzipped, counting the uncompressed
// bytes against the limit.
type exportWriter struct {
	buf bytes.Buffer
	gz  *gzip.Writer
	n   int64
}

func (e *exportWriter) write(s string) error {
	e.n += int64(len(s))
	if e.n > exportLimit.Load() {
		return errExportTooLarge
	}
	_, err := e.gz.Write([]byte(s))
	return err
}

// selectRows streams the rows of one query, as JSON objects without the
// hidden columns, into the file as a JSON array, and returns how many.
func selectRows(ctx context.Context, tx pgx.Tx, out *exportWriter, table string, cols map[string][2]string, where string, args ...any) (int, error) {
	hidden := []string{}
	for name, t := range cols {
		if exportHidden(name, t[0], t[1]) {
			hidden = append(hidden, name)
		}
	}
	slices.Sort(hidden)
	order := ""
	switch {
	case cols["created_at"] != [2]string{}:
		order = " ORDER BY t.created_at"
	case cols["occurred_at"] != [2]string{}:
		order = " ORDER BY t.occurred_at"
	}
	args = append(args, hidden)
	rows, err := tx.Query(ctx, `SELECT (to_jsonb(t) - $`+strconv.Itoa(len(args))+`::text[])::text FROM `+pgx.Identifier{table}.Sanitize()+` t WHERE `+where+order, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if err := out.write("["); err != nil {
		return 0, err
	}
	n := 0
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			return n, err
		}
		if n > 0 {
			if err := out.write(","); err != nil {
				return n, err
			}
		}
		if err := out.write(row); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	return n, out.write("]")
}

// gatherPrivacyExport builds the file for one record of a brand: the
// record and, by table, every row that points at it in that brand. It
// returns the gzipped JSON, its uncompressed size and the rows by table.
func gatherPrivacyExport(ctx context.Context, tx pgx.Tx, ws, brand, brandName, rec string) ([]byte, int64, map[string]int, error) {
	out := &exportWriter{}
	out.gz = gzip.NewWriter(&out.buf)
	head, err := json.Marshal(map[string]any{"exported_at": time.Now().UTC(), "brand": map[string]string{"id": brand, "name": brandName}})
	if err != nil {
		return nil, 0, nil, err
	}
	// The head without its closing brace, so record and tables follow.
	if err := out.write(string(head[:len(head)-1]) + `,"record":`); err != nil {
		return nil, out.n, nil, err
	}
	cols, err := tableColumns(ctx, tx, "records")
	if err != nil {
		return nil, 0, nil, err
	}
	// The record itself: an object, not a list.
	hidden := []string{}
	for name, t := range cols {
		if exportHidden(name, t[0], t[1]) {
			hidden = append(hidden, name)
		}
	}
	var self string
	err = tx.QueryRow(ctx, `SELECT (to_jsonb(t) - $4::text[])::text FROM records t WHERE t.id = $1 AND t.workspace_id = $2 AND t.brand_id = $3`,
		rec, ws, brand, hidden).Scan(&self)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, nil, errNotFound
	}
	if err != nil {
		return nil, 0, nil, err
	}
	if err := out.write(self + `,"tables":{`); err != nil {
		return nil, out.n, nil, err
	}
	counts := map[string]int{}
	first := true
	for _, src := range privacyExportSources() {
		cols, err := tableColumns(ctx, tx, src.table)
		if err != nil {
			return nil, out.n, nil, err
		}
		if len(cols) == 0 {
			continue
		}
		args := []any{rec}
		var match []string
		for _, c := range src.cols {
			if _, ok := cols[c]; ok {
				match = append(match, "t."+pgx.Identifier{c}.Sanitize()+" = $1")
			}
		}
		if len(match) == 0 {
			continue
		}
		where := "(" + strings.Join(match, " OR ") + ")"
		if _, ok := cols["workspace_id"]; ok {
			args = append(args, ws)
			where += " AND t.workspace_id = $" + strconv.Itoa(len(args))
		}
		// A row linked to the record with no brand (shared by every brand,
		// until the brand split's contract step) is still about this
		// person, so it is theirs too; another brand's row is not.
		if _, ok := cols["brand_id"]; ok {
			args = append(args, brand)
			where += " AND (t.brand_id = $" + strconv.Itoa(len(args)) + " OR t.brand_id IS NULL)"
		}
		key, _ := json.Marshal(src.table)
		sep := ","
		if first {
			sep = ""
		}
		first = false
		if err := out.write(sep + string(key) + ":"); err != nil {
			return nil, out.n, nil, err
		}
		n, err := selectRows(ctx, tx, out, src.table, cols, where, args...)
		if err != nil {
			return nil, out.n, nil, err
		}
		counts[src.table] = n
	}
	if err := out.write("}}"); err != nil {
		return nil, out.n, nil, err
	}
	if err := out.gz.Close(); err != nil {
		return nil, out.n, nil, err
	}
	return out.buf.Bytes(), out.n, counts, nil
}

// ---- Expiry ----

// RunPrivacyExports expires privacy exports until ctx ends: a ready export
// past its 7 days loses its file, and one pending far too long (its job is
// dead) fails.
func (s *Server) RunPrivacyExports(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		if _, err := s.ExpirePrivacyExports(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "privacy_export.sweep_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ExpirePrivacyExports deletes the files of exports past their expiry and
// fails exports stuck pending, and returns how many expired.
func (s *Server) ExpirePrivacyExports(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE privacy_exports SET status = 'expired', content = NULL
		WHERE id IN (SELECT id FROM privacy_exports WHERE status = 'ready' AND expires_at <= now() ORDER BY expires_at LIMIT 1000)`)
	if err != nil {
		return 0, err
	}
	n := int(tag.RowsAffected())
	metrics.PrivacyExportsExpired(n)
	rows, err := s.pool.Query(ctx, `SELECT workspace_id::text, id::text FROM privacy_exports
		WHERE status = 'pending' AND created_at < now() - make_interval(secs => $1) LIMIT 100`, privacyExportStuck.Seconds())
	if err != nil {
		return n, err
	}
	type stuck struct{ ws, id string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (stuck, error) {
		var x stuck
		return x, r.Scan(&x.ws, &x.id)
	})
	if err != nil {
		return n, err
	}
	for _, x := range list {
		if err := s.failPrivacyExport(ctx, x.ws, x.id, "internal", 0); err != nil {
			return n, err
		}
		metrics.PrivacyExportBuilt("internal", 0, 0)
	}
	return n, nil
}
