package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Moving a partner program over from another platform (Tolt), with the
// workspace's own connection. An import reads the platform's program and
// brings in what isn't here yet: partners with the codes of their links (so
// old links keep working), promo codes, the customers they referred, those
// customers' transactions, and commissions with their status, paid ones in
// paid payouts. It can run as often as wanted until switch-over day: every
// object is matched by its id on the platform, so a second run adds only
// what is new and brings over status changes (a commission approved, paid
// or rejected since).
//
// History comes in quietly: imported sales are not customer events (the
// workspace's own billing or backend already told attribution about them),
// and importing a reward sends no webhook, so the workspace's system never
// grants an old reward twice. Approved and paid commissions are spend on
// the day they were earned. A pending commission waits out the program's
// hold like any reward here; keep auto-approve off until switch-over day,
// so the platform's own decision comes over on the next run.

const (
	jobPartnerImport    = "partners.import"
	partnerImportIssues = 100 // issues kept in the summary; the rest are counted
)

// How long one step may read for. Tests make it small.
var partnerImportStep = 60 * time.Second

type partnerImportView struct {
	ID            string               `json:"id"`
	ProgramID     string               `json:"program_id"`
	IntegrationID *string              `json:"integration_id"`
	Connector     string               `json:"connector"`
	SourceProgram string               `json:"source_program"`
	Status        string               `json:"status"`
	Summary       partnerImportSummary `json:"summary"`
	Error         *string              `json:"error"`
	CreatedAt     time.Time            `json:"created_at"`
	StartedAt     *time.Time           `json:"started_at"`
	FinishedAt    *time.Time           `json:"finished_at"`
}

// errPaginationStuck stops an import whose platform answers the next page
// with the one before it.
var errPaginationStuck = errors.New("the partner platform sent the same page twice, so it isn't paging as expected; nothing more was read. Tell us which platform this was")

type partnerImportSummary struct {
	Kinds      []partnerImportCount `json:"kinds"`
	Issues     []partnerImportIssue `json:"issues"`
	MoreIssues int                  `json:"more_issues"`
}

// partnerImportCount is what happened to one kind of object: created here,
// updated (a status brought over), unchanged (already here) or skipped
// (see the issues).
type partnerImportCount struct {
	Kind      string `json:"kind"`
	Seen      int    `json:"seen"`
	Created   int    `json:"created"`
	Updated   int    `json:"updated"`
	Unchanged int    `json:"unchanged"`
	Skipped   int    `json:"skipped"`
	// ByStatus counts the partners read by their status there (active,
	// pending, rejected, suspended), to check against the platform.
	ByStatus map[string]int `json:"by_status,omitempty"`
	// Reported is how many the platform said its lists hold, when it says,
	// to reconcile with Seen.
	Reported int `json:"reported,omitempty"`
	// ReportedBy is Reported by part, when the platform lists the kind in
	// parts (Tolt's partners, by status).
	ReportedBy map[string]int `json:"reported_by,omitempty"`
}

// partnerImportIssue is an object that couldn't come over as it was. Only
// the platform's id and a reason code: never names or addresses.
type partnerImportIssue struct {
	Kind       string `json:"kind"`
	ExternalID string `json:"external_id"`
	Reason     string `json:"reason"`
	// Message says why a whole kind couldn't be read (reason unreadable):
	// the kind, the HTTP status and the answer's shape, never its values.
	Message string `json:"message,omitempty"`
}

type partnerImportProgress struct {
	Kind int `json:"kind"` // index into connectors.PartnerImportKinds
	// KindName is the kind being read; an import saved before it was
	// kept counted Kind in connectors.LegacyPartnerImportKinds.
	KindName string `json:"kind_name,omitempty"`
	After    string `json:"after"` // the page cursor
	Steps    int    `json:"steps"`
	// Codes is each partner's code, from their first link, until the
	// partners are read.
	Codes map[string]string `json:"codes,omitempty"`
	// Listed is each partner the platform's list gave, to reconcile with
	// how many it said there are.
	Listed map[string]bool `json:"listed,omitempty"`
}

func (v *partnerImportSummary) normalize() {
	have := map[string]bool{}
	for _, k := range v.Kinds {
		have[k.Kind] = true
	}
	for _, k := range connectors.PartnerImportKinds {
		if !have[k] {
			v.Kinds = append(v.Kinds, partnerImportCount{Kind: k})
		}
	}
	if v.Issues == nil {
		v.Issues = []partnerImportIssue{}
	}
}

func (v *partnerImportSummary) count(kind string) *partnerImportCount {
	for i := range v.Kinds {
		if v.Kinds[i].Kind == kind {
			return &v.Kinds[i]
		}
	}
	v.Kinds = append(v.Kinds, partnerImportCount{Kind: kind})
	return &v.Kinds[len(v.Kinds)-1]
}

const partnerImportSelect = `SELECT id::text, program_id::text, integration_id::text, connector, source_program, status, summary, error,
	created_at, started_at, finished_at FROM partner_imports`

func scanPartnerImport(row pgx.Row) (partnerImportView, error) {
	var v partnerImportView
	err := row.Scan(&v.ID, &v.ProgramID, &v.IntegrationID, &v.Connector, &v.SourceProgram, &v.Status, &v.Summary, &v.Error,
		&v.CreatedAt, &v.StartedAt, &v.FinishedAt)
	v.Summary.normalize()
	return v, err
}

func (s *Server) routesPartnerImports(mux *http.ServeMux) {
	read, manage := auth.ScopePartnersRead, auth.ScopePartnersManage
	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-programs/{id}/imports", s.inBrand(manage, s.startPartnerImport))
	mux.HandleFunc("GET /v1/workspaces/{ws}/partner-programs/{id}/imports", s.inBrand(read, s.listPartnerImports))
	mux.HandleFunc("GET /v1/workspaces/{ws}/partner-programs/{id}/imports/{import}", s.inBrand(read, s.getPartnerImport))
	mux.HandleFunc("GET /v1/workspaces/{ws}/partner-programs/{id}/import-sources", s.inBrand(manage, s.listPartnerImportSources))
}

// partnerImportIntegration finds the connected partner platform: the one
// asked for, or the first. errNotFound when there is none.
func (s *Server) partnerImportIntegration(ctx context.Context, ws, want string) (id, connector string, err error) {
	var ids []string
	for _, d := range s.integrations.Registry.All() {
		if slices.Contains(d.Capabilities, connectors.CapPartnersImport) {
			ids = append(ids, d.ID)
		}
	}
	err = s.pool.QueryRow(ctx, `SELECT id::text, connector FROM integrations WHERE workspace_id = $1 AND connector = ANY($2) AND status = 'connected'
		AND ($3 = '' OR id::text = $3) ORDER BY created_at LIMIT 1`, ws, ids, want).Scan(&id, &connector)
	return id, connector, err
}

// listPartnerImportSources lists the programs in the connected partner
// platform account, so the person importing picks one instead of typing
// its id. It only reads.
func (s *Server) listPartnerImportSources(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if _, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id")); !ok {
		return
	}
	want := r.URL.Query().Get("integration_id")
	if want != "" && !validUUID(want) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "integration_id is a connection's id")
		return
	}
	ctx := r.Context()
	integration, _, err := s.partnerImportIntegration(ctx, a.WorkspaceID, want)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusConflict, "connector_needed", "Connect Tolt first (Connectors, Partner programs), with your own Tolt API key.")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	o, err := s.integrations.Open(ctx, s.pool, a.WorkspaceID, integration)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	imp, ok := o.Connector.(connectors.PartnerImporter)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", o.Connector.Definition().Name+" can't import a partner program")
		return
	}
	progs, err := imp.PartnerPrograms(ctx, o.Client, o.Creds)
	if err != nil {
		connectorError(w, r, err)
		return
	}
	pick := ""
	if p, ok := pickPartnerSource(progs); ok {
		pick = p.ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": progs, "integration_id": integration, "suggested": pick})
}

// pickPartnerSource is the program an import takes when none was named:
// the only one, or else the only one that is active.
func pickPartnerSource(progs []connectors.PartnerProgram) (connectors.PartnerProgram, bool) {
	if len(progs) == 1 {
		return progs[0], true
	}
	var live []connectors.PartnerProgram
	for _, p := range progs {
		if p.Status == "active" {
			live = append(live, p)
		}
	}
	if len(live) == 1 {
		return live[0], true
	}
	return connectors.PartnerProgram{}, false
}

// startPartnerImport queues an import into the program from the
// workspace's partner platform connection.
func (s *Server) startPartnerImport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	prog, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in struct {
		IntegrationID string `json:"integration_id"`
		SourceProgram string `json:"source_program"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.SourceProgram = strings.TrimSpace(in.SourceProgram)
	if len(in.SourceProgram) > 200 || in.IntegrationID != "" && !validUUID(in.IntegrationID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "integration_id is a connection's id; source_program is the program's id on the platform")
		return
	}
	if prog.Status == "archived" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the program is archived")
		return
	}
	ctx := r.Context()
	integration, connector, err := s.partnerImportIntegration(ctx, a.WorkspaceID, in.IntegrationID)
	if errors.Is(err, pgx.ErrNoRows) {
		if in.IntegrationID != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "integration_id: no connected partner platform with that id")
			return
		}
		writeError(w, r, http.StatusConflict, "connector_needed", "Connect Tolt first (Connectors, Partner programs), with your own Tolt API key.")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO partner_imports (workspace_id, program_id, integration_id, connector, source_program, created_by, created_by_type)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`, a.WorkspaceID, prog.ID, integration, connector, in.SourceProgram,
			nullIfEmpty(a.ActorID), a.ActorType).Scan(&id)
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return errConflict
		}
		if err != nil {
			return err
		}
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnerImport, WorkspaceID: &a.WorkspaceID, Payload: map[string]string{"import_id": id},
			MaxAttempts: 5, IdempotencyKey: id + ":0"}); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "partner_import.started", "partner_import", id,
			map[string]any{"program_id": prog.ID, "program_kind": prog.Kind, "connector": connector})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "an import into this program is already running")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := scanPartnerImport(s.pool.QueryRow(ctx, partnerImportSelect+` WHERE id = $1`, id))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) listPartnerImports(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	prog, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), partnerImportSelect+` WHERE workspace_id = $1 AND program_id = $2 ORDER BY created_at DESC LIMIT 50`,
		a.WorkspaceID, prog.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (partnerImportView, error) { return scanPartnerImport(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getPartnerImport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	prog, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	id := r.PathValue("import")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "import not found")
		return
	}
	v, err := scanPartnerImport(s.pool.QueryRow(r.Context(), partnerImportSelect+` WHERE workspace_id = $1 AND program_id = $2 AND id = $3`,
		a.WorkspaceID, prog.ID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "import not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- Running ----

func (s *Server) partnerImportJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		ImportID string `json:"import_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.ImportID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	return s.runPartnerImportStep(ctx, *job.WorkspaceID, p.ImportID, job.Attempt >= job.MaxAttempts)
}

type partnerImportRun struct {
	s         *Server
	ws, id    string
	by        principal
	prog      *partnerProgramView
	connector string
	source    string
	o         *connectors.Opened
	imp       connectors.PartnerImporter
	progress  partnerImportProgress
	sum       partnerImportSummary
	// asked holds the partners fetched by id in this step, or found
	// missing there, so each is asked for once.
	asked map[string]bool
}

// runPartnerImportStep reads pages until the step's time is up, then
// queues the next step. A failure the platform may get over (a rate limit,
// an outage) is retried; on the last attempt, or when the connection is
// refused, the import ends as failed with the reason.
func (s *Server) runPartnerImportStep(ctx context.Context, ws, id string, last bool) error {
	run := &partnerImportRun{s: s, ws: ws, id: id}
	var program, actorType string
	var integration, actorID *string
	var progress []byte
	err := s.pool.QueryRow(ctx, `UPDATE partner_imports SET status = 'running', started_at = coalesce(started_at, now())
		WHERE workspace_id = $1 AND id = $2 AND status IN ('queued', 'running')
		RETURNING program_id::text, integration_id::text, connector, source_program, progress, summary, created_by::text, created_by_type`, ws, id).
		Scan(&program, &integration, &run.connector, &run.source, &progress, &run.sum, &actorID, &actorType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // finished or gone
	}
	if err != nil {
		return err
	}
	_ = json.Unmarshal(progress, &run.progress)
	run.sum.normalize()
	run.by = principal{ActorID: deref(actorID), ActorType: actorType}

	done, err := run.step(ctx, program, integration, last)
	if err != nil {
		permanent := errors.Is(err, connectors.ErrNotFound) || connectors.KindOf(err) == connectors.ErrAuth ||
			connectors.KindOf(err) == connectors.ErrInvalid || errors.As(err, new(errInvalid)) || errors.Is(err, errPaginationStuck)
		if !last && !permanent {
			// Keep the pages this step stored, so the retry carries on
			// after them and the summary still counts them as new.
			if serr := run.saveProgress(ctx); serr != nil {
				return errors.Join(err, serr)
			}
			return err
		}
		return run.finish(ctx, err)
	}
	if done {
		return run.finish(ctx, run.nothingRead())
	}
	return run.save(ctx)
}

func (r *partnerImportRun) step(ctx context.Context, program string, integration *string, last bool) (bool, error) {
	prog, err := scanPartnerProgram(r.s.pool.QueryRow(ctx, partnerProgramSelect+` AND p.id = $2`, r.ws, program))
	if err != nil {
		return false, err
	}
	r.prog = &prog
	if integration == nil {
		return false, errInvalid("the partner platform connection was removed")
	}
	if r.o, err = r.s.integrations.Open(ctx, r.s.pool, r.ws, *integration); err != nil {
		return false, err
	}
	imp, ok := r.o.Connector.(connectors.PartnerImporter)
	if !ok {
		return false, errInvalid(r.o.Connector.Definition().Name + " can't import a partner program")
	}
	r.imp = imp
	if r.source == "" {
		if err := r.pickProgram(ctx); err != nil {
			return false, err
		}
	}
	deadline := time.Now().Add(partnerImportStep)
	kinds := connectors.PartnerImportKinds
	if r.progress.KindName == "" && r.progress.Kind > 0 && r.progress.Kind < len(connectors.LegacyPartnerImportKinds) {
		r.progress.KindName = connectors.LegacyPartnerImportKinds[r.progress.Kind]
	}
	if i := slices.Index(kinds, r.progress.KindName); i >= 0 {
		r.progress.Kind = i
	}
	for r.progress.Kind < len(kinds) {
		if time.Now().After(deadline) {
			return false, nil
		}
		kind := kinds[r.progress.Kind]
		page, next, err := r.imp.PartnerPage(ctx, r.o.Client, r.o.Creds, r.source, kind, r.progress.After)
		if errors.Is(err, connectors.ErrPartnerKindUnavailable) {
			r.warn(kind, "", kind+"_unavailable")
			page, next, err = connectors.PartnerObjects{}, "", nil
		}
		if err != nil {
			if !r.kindFailed(ctx, kind, err, last) {
				return false, err
			}
			r.progress.Kind++
			r.progress.After = ""
			if r.progress.Kind < len(kinds) {
				r.progress.KindName = kinds[r.progress.Kind]
			}
			continue
		}
		if err := r.page(ctx, kind, page); err != nil {
			return false, err
		}
		if next != "" && next == r.progress.After {
			// The platform sent the same page again: it ignored the
			// cursor, and reading on would never end.
			return false, errPaginationStuck
		}
		r.progress.After = next
		if next == "" {
			r.progress.Kind++
			if r.progress.Kind < len(kinds) {
				r.progress.KindName = kinds[r.progress.Kind]
			}
		}
	}
	return true, nil
}

// pickProgram takes the account's only program, or says which to choose.
func (r *partnerImportRun) pickProgram(ctx context.Context) error {
	progs, err := r.imp.PartnerPrograms(ctx, r.o.Client, r.o.Creds)
	if err != nil {
		return err
	}
	if len(progs) == 0 {
		return errInvalid("the " + r.o.Connector.Definition().Name + " account has no program")
	}
	if p, ok := pickPartnerSource(progs); ok {
		r.source = p.ID
		_, err := r.s.pool.Exec(ctx, `UPDATE partner_imports SET source_program = $2 WHERE id = $1`, r.id, r.source)
		return err
	}
	var ids []string
	for _, p := range progs {
		ids = append(ids, p.ID)
	}
	return errInvalid("the account has " + fmt.Sprint(len(progs)) + " programs: start the import again with source_program, one of " + strings.Join(ids, ", "))
}

// kindFailed decides whether a kind the platform wouldn't give can be left
// out so the import reads the others: an answer that can't be read or a
// request it refuses (asking again gets the same), or any failure but a
// refused key on the last attempt. It notes the kind as unreadable.
func (r *partnerImportRun) kindFailed(ctx context.Context, kind string, err error, last bool) bool {
	unreadable := errors.As(err, new(*connectors.UnreadableError))
	if !unreadable {
		switch k := connectors.KindOf(err); {
		case k == connectors.ErrAuth, k != connectors.ErrInvalid && !last:
			return false
		}
	}
	code := "unreadable"
	if !unreadable {
		code = crmImportErrorCode(err)
	}
	track.Fail(ctx, r.s.log, track.Event{Name: "partner_import.kind_skipped", WorkspaceID: r.ws, Surface: "job",
		Props: map[string]any{"import_id": r.id, "connector": r.connector, "kind": kind}}, code, err)
	metrics.PartnerImportObjects(kind, "unreadable", 1)
	// Kept whatever the cap: one per kind at most, and it matters most.
	r.sum.Issues = append(r.sum.Issues, partnerImportIssue{Kind: kind, Reason: "unreadable", Message: cutUTF8(err.Error(), 500)})
	return true
}

// nothingRead fails an import that couldn't read a single kind: that is
// the connection or the program, not one list.
func (r *partnerImportRun) nothingRead() error {
	var first string
	n := 0
	for _, i := range r.sum.Issues {
		if i.Reason == "unreadable" {
			if n == 0 {
				first = i.Message
			}
			n++
		}
	}
	if n < len(connectors.PartnerImportKinds) {
		return nil
	}
	return &connectors.UnreadableError{Message: "nothing could be read: " + first}
}

// saveProgress keeps where the import got to and its summary.
func (r *partnerImportRun) saveProgress(ctx context.Context) error {
	_, err := r.s.pool.Exec(ctx, `UPDATE partner_imports SET progress = $3, summary = $4 WHERE workspace_id = $1 AND id = $2`,
		r.ws, r.id, r.progress, r.sum)
	return err
}

func (r *partnerImportRun) save(ctx context.Context) error {
	return pgx.BeginFunc(ctx, r.s.pool, func(tx pgx.Tx) error {
		r.progress.Steps++
		if _, err := tx.Exec(ctx, `UPDATE partner_imports SET progress = $3, summary = $4 WHERE workspace_id = $1 AND id = $2`,
			r.ws, r.id, r.progress, r.sum); err != nil {
			return err
		}
		_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnerImport, WorkspaceID: &r.ws, Payload: map[string]string{"import_id": r.id},
			MaxAttempts: 5, IdempotencyKey: fmt.Sprintf("%s:%d", r.id, r.progress.Steps)})
		return err
	})
}

func (r *partnerImportRun) finish(ctx context.Context, runErr error) error {
	status, msg := "done", (*string)(nil)
	code := ""
	if runErr != nil {
		status = "failed"
		m := runErr.Error()
		msg = &m
		code = crmImportErrorCode(runErr)
		if errors.Is(runErr, errPaginationStuck) {
			code = "pagination_stuck"
		}
		if errors.As(runErr, new(*connectors.UnreadableError)) {
			code = "unreadable"
		}
		track.Fail(ctx, r.s.log, track.Event{Name: "partner_import.failed", WorkspaceID: r.ws, Surface: "job",
			Props: map[string]any{"import_id": r.id, "connector": r.connector}}, code, runErr)
	}
	metrics.PartnerImport(r.connector, status)
	if runErr == nil {
		r.reconcilePartners()
	}
	r.progress.Codes, r.progress.Listed = nil, nil
	data := map[string]any{"connector": r.connector, "status": status, "issues": len(r.sum.Issues) + r.sum.MoreIssues}
	if r.prog != nil {
		data["program_id"], data["program_kind"] = r.prog.ID, r.prog.Kind
	}
	for _, k := range r.sum.Kinds {
		data[k.Kind+"_created"] = k.Created
	}
	if code != "" {
		data["error_code"] = code
	}
	return pgx.BeginFunc(ctx, r.s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE partner_imports SET status = $3, progress = $4, summary = $5, error = $6, finished_at = now()
			WHERE workspace_id = $1 AND id = $2`, r.ws, r.id, status, r.progress, r.sum, msg); err != nil {
			return err
		}
		return record(ctx, tx, &r.by, r.ws, "partner_import."+map[string]string{"done": "finished", "failed": "stopped"}[status],
			"partner_import", r.id, data)
	})
}

// reportedTotal adds up what the platform reported for each part of a
// list. When every part reported the same number, that number is the
// whole list's count, which the platform gave whatever the filter.
func reportedTotal(by map[string]int) int {
	sum, same, first := 0, true, -1
	for _, n := range by {
		sum += n
		if first >= 0 && n != first {
			same = false
		}
		first = n
	}
	if same && len(by) > 1 && first > 0 {
		return first
	}
	return sum
}

// reconcilePartners notes when the platform reported more partners than
// its list gave (Tolt's list has left out partners who haven't finished
// onboarding), and whether any of those has referrals here: a customer,
// commission or promo code that names a partner who didn't come over.
func (r *partnerImportRun) reconcilePartners() {
	reported := r.sum.count(connectors.PartnerKindPartners).Reported
	if reported == 0 {
		return
	}
	listed := len(r.progress.Listed)
	if listed >= reported {
		return
	}
	named := 0
	for _, i := range r.sum.Issues {
		if i.Kind != connectors.PartnerKindPartners && (i.Reason == "partner_missing" || i.Reason == "partner_id_missing") ||
			i.Reason == "partner_unavailable" {
			named++
		}
	}
	msg := fmt.Sprintf("%s reports %d partners and its partner list gave %d, so it left out %d.",
		r.o.Connector.Definition().Name, reported, listed, reported-listed)
	if named == 0 {
		msg += " Every customer, commission and promo code names a partner who came over, so none of the others has referrals."
	} else {
		msg += fmt.Sprintf(" %d customers, commissions or promo codes name a partner who didn't come over: see their issues.", named)
	}
	// Kept whatever the cap, like an unreadable kind: it is what to look at first.
	r.sum.Issues = append(r.sum.Issues, partnerImportIssue{Kind: connectors.PartnerKindPartners, Reason: "partners_unlisted", Message: msg})
}

// issue notes an object that couldn't come over.
func (r *partnerImportRun) issue(kind, external, reason string) {
	r.sum.count(kind).Skipped++
	metrics.PartnerImportObjects(kind, "skipped", 1)
	r.warn(kind, external, reason)
}

// warn notes something about an object that came over all the same (a
// code that had to change, a customer another partner already had).
func (r *partnerImportRun) warn(kind, external, reason string) {
	r.warnWhy(kind, external, reason, "")
}

// warnWhy is warn with a message: ids, counts, statuses and field names,
// never values from the platform's answer.
func (r *partnerImportRun) warnWhy(kind, external, reason, message string) {
	if len(r.sum.Issues) >= partnerImportIssues {
		r.sum.MoreIssues++
		return
	}
	r.sum.Issues = append(r.sum.Issues, partnerImportIssue{Kind: kind, ExternalID: cutUTF8(external, 200), Reason: reason, Message: cutUTF8(message, 500)})
}

// note counts one object's result: created, updated or unchanged.
func (r *partnerImportRun) note(kind, result string) {
	c := r.sum.count(kind)
	switch result {
	case "created":
		c.Created++
	case "updated":
		c.Updated++
	default:
		c.Unchanged++
	}
	metrics.PartnerImportObjects(kind, result, 1)
}

// missingPartners brings in, one by one, the partners a page's promo
// codes, customers and commissions name that the partner list didn't give
// (Tolt lists only onboarded partners), when the platform can read a
// partner by id. They count as partners read.
func (r *partnerImportRun) missingPartners(ctx context.Context, page connectors.PartnerObjects,
	oneOf func(string, func(pgx.Tx) (string, error), string) error) error {
	f, ok := r.imp.(connectors.PartnerFetcher)
	if !ok {
		return nil
	}
	ids := page.PartnerIDs()
	if len(page.PromoCodes) > 0 || len(page.Customers) > 0 || len(page.Commissions) > 0 {
		// The partners links name too, once the list has been read: a
		// partner with a link but nothing else is still a partner.
		for id := range r.progress.Codes {
			ids = append(ids, id)
		}
		slices.Sort(ids[len(page.PartnerIDs()):])
	}
	for _, id := range ids {
		if r.asked[id] {
			continue
		}
		if r.asked == nil {
			r.asked = map[string]bool{}
		}
		r.asked[id] = true
		var known bool
		if err := r.s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM partner_import_refs WHERE program_id = $1 AND connector = $2
			AND kind = 'partner' AND external_id = $3)`, r.prog.ID, r.connector, id).Scan(&known); err != nil {
			return err
		}
		if known {
			continue
		}
		p, err := f.PartnerByID(ctx, r.o.Client, r.o.Creds, r.source, id)
		switch {
		case errors.As(err, new(*connectors.UnreadableError)) || connectors.KindOf(err) == connectors.ErrInvalid:
			// Gone there, or not readable: what names it is skipped.
			why := "the platform refused the request"
			var u *connectors.UnreadableError
			if errors.As(err, &u) {
				why = u.Message
			}
			r.warnWhy(connectors.PartnerKindPartners, id, "partner_unavailable", why)
			continue
		case err != nil:
			return err
		}
		r.sum.count(connectors.PartnerKindPartners).Seen++
		if err := oneOf(connectors.PartnerKindPartners, func(tx pgx.Tx) (string, error) { return r.partner(ctx, tx, p) }, id); err != nil {
			return err
		}
	}
	return nil
}

// page stores one page, each object in its own transaction, so one that
// can't come over never holds up the rest.
func (r *partnerImportRun) page(ctx context.Context, kind string, page connectors.PartnerObjects) error {
	c := r.sum.count(kind)
	c.Seen += page.Len()
	for _, p := range page.Partners {
		if c.ByStatus == nil {
			c.ByStatus = map[string]int{}
		}
		c.ByStatus[p.Status]++
		if r.progress.Listed == nil {
			r.progress.Listed = map[string]bool{}
		}
		r.progress.Listed[p.ID] = true
	}
	if page.Reported != nil {
		if page.ReportedFor == "" {
			c.Reported += *page.Reported
		} else {
			if c.ReportedBy == nil {
				c.ReportedBy = map[string]int{}
			}
			c.ReportedBy[page.ReportedFor] = *page.Reported
			c.Reported = reportedTotal(c.ReportedBy)
		}
	}
	oneOf := func(kind string, f func(tx pgx.Tx) (string, error), external string) error {
		var result string
		err := pgx.BeginFunc(ctx, r.s.pool, func(tx pgx.Tx) error {
			var err error
			result, err = f(tx)
			return err
		})
		var reason importSkip
		switch {
		case errors.As(err, &reason):
			r.issue(kind, external, string(reason))
			return nil
		case err != nil:
			return err
		}
		r.note(kind, result)
		return nil
	}
	one := func(f func(tx pgx.Tx) (string, error), external string) error { return oneOf(kind, f, external) }
	if err := r.missingPartners(ctx, page, oneOf); err != nil {
		return err
	}
	switch kind {
	case connectors.PartnerKindGroups:
		for _, g := range page.Groups {
			if err := one(func(tx pgx.Tx) (string, error) { return r.group(ctx, tx, g) }, g.ID); err != nil {
				return err
			}
		}
	case connectors.PartnerKindFlows:
		for _, f := range page.Flows {
			if err := one(func(tx pgx.Tx) (string, error) { return r.flow(ctx, tx, f) }, f.ID); err != nil {
				return err
			}
		}
	case connectors.PartnerKindLinks:
		for _, l := range page.Links {
			r.link(l)
		}
	case connectors.PartnerKindPartners:
		for _, p := range page.Partners {
			if err := one(func(tx pgx.Tx) (string, error) { return r.partner(ctx, tx, p) }, p.ID); err != nil {
				return err
			}
		}
	case connectors.PartnerKindPromoCodes:
		for _, c := range page.PromoCodes {
			if c.PartnerID == "" && c.ID != "" {
				// Its partner is under a field the import doesn't read yet:
				// the message names the fields there are.
				r.sum.count(kind).Skipped++
				metrics.PartnerImportObjects(kind, "skipped", 1)
				r.warnWhy(kind, c.ID, "partner_id_missing", "no partner id in the promo code; its fields: "+c.Keys)
				continue
			}
			if err := one(func(tx pgx.Tx) (string, error) { return r.promoCode(ctx, tx, c) }, c.ID); err != nil {
				return err
			}
		}
	case connectors.PartnerKindCustomers:
		for _, c := range page.Customers {
			if err := one(func(tx pgx.Tx) (string, error) { return r.customer(ctx, tx, c) }, c.ID); err != nil {
				return err
			}
		}
	case connectors.PartnerKindTransactions:
		for _, t := range page.Transactions {
			if err := one(func(tx pgx.Tx) (string, error) { return r.transaction(ctx, tx, t) }, t.ID); err != nil {
				return err
			}
		}
	case connectors.PartnerKindCommissions:
		for _, c := range page.Commissions {
			if err := one(func(tx pgx.Tx) (string, error) { return r.commission(ctx, tx, c) }, c.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// importSkip is why one object couldn't come over.
type importSkip string

func (e importSkip) Error() string { return string(e) }

// linkParams are the link parameters the website script reads as a ref.
var linkParams = []string{"ref", "via"}

// link keeps each partner's first link value as their code. A link with
// a parameter the website script doesn't read, or a second link, is noted.
func (r *partnerImportRun) link(l connectors.ImportLink) {
	if l.PartnerID == "" || l.Value == "" {
		r.issue("links", l.ID, "incomplete")
		return
	}
	if l.Param != "" && !slices.Contains(linkParams, strings.ToLower(l.Param)) {
		r.issue("links", l.ID, "link_param")
		return
	}
	if r.progress.Codes == nil {
		r.progress.Codes = map[string]string{}
	}
	if _, ok := r.progress.Codes[l.PartnerID]; ok {
		r.issue("links", l.ID, "extra_link")
		return
	}
	r.progress.Codes[l.PartnerID] = l.Value
	r.note("links", "created")
}

// ref finds what an object on the platform became here.
func (r *partnerImportRun) ref(ctx context.Context, tx pgx.Tx, kind, external string) (string, bool, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT local_id::text FROM partner_import_refs WHERE program_id = $1 AND connector = $2 AND kind = $3 AND external_id = $4`,
		r.prog.ID, r.connector, kind, external).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

func (r *partnerImportRun) keep(ctx context.Context, tx pgx.Tx, kind, external, local string) error {
	_, err := tx.Exec(ctx, `INSERT INTO partner_import_refs (workspace_id, program_id, connector, kind, external_id, local_id)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (program_id, connector, kind, external_id) DO UPDATE SET local_id = EXCLUDED.local_id`,
		r.ws, r.prog.ID, r.connector, kind, cutUTF8(external, 200), local)
	return err
}

func (r *partnerImportRun) partner(ctx context.Context, tx pgx.Tx, p connectors.ImportPartner) (string, error) {
	if p.ID == "" {
		return "", importSkip("incomplete")
	}
	id, ok, err := r.ref(ctx, tx, "partner", p.ID)
	if err != nil {
		return "", err
	}
	if ok {
		// Bring over a status or group change, unless someone changed it here.
		tag, err := tx.Exec(ctx, `UPDATE partner_memberships SET status = $2, updated_at = now() WHERE id = $1 AND status <> $2 AND source = 'import'`, id, p.Status)
		if err != nil {
			return "", err
		}
		moved, err := r.partnerGroup(ctx, tx, id, p.GroupID, true)
		if err != nil || (tag.RowsAffected() == 0 && !moved) {
			return "unchanged", err
		}
		return "updated", nil
	}
	email, err := normalizeEmail(p.Email)
	if err != nil || email == "" {
		return "", importSkip("no_email")
	}
	rec, err := customers.EnsurePerson(ctx, tx, r.ws, "email", email, false, RequestID(ctx))
	if err != nil {
		return "", err
	}
	if p.Name != "" {
		if _, err := tx.Exec(ctx, `UPDATE records SET name = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2 AND name = ''`,
			rec, r.ws, cutUTF8(p.Name, 200)); err != nil {
			return "", err
		}
	}
	// Someone already a partner in the program is the same partner.
	err = tx.QueryRow(ctx, `SELECT id::text FROM partner_memberships WHERE program_id = $1 AND record_id = $2`, r.prog.ID, rec).Scan(&id)
	if err == nil {
		return "unchanged", r.keep(ctx, tx, "partner", p.ID, id)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	code := r.progress.Codes[p.ID]
	if code != "" && !partnerCodeRE.MatchString(code) {
		r.warn("partners", p.ID, "code_changed")
		code = ""
	}
	join := partnerJoin{rec: rec, code: code, status: p.Status, source: "import", payout: p.PayoutMethod, note: "Imported from " + r.o.Connector.Definition().Name}
	id, err = r.s.joinPartner(ctx, tx, r.ws, &r.by, r.prog, join)
	if errors.Is(err, errCodeTaken) {
		r.warn("partners", p.ID, "code_changed")
		join.code = ""
		id, err = r.s.joinPartner(ctx, tx, r.ws, &r.by, r.prog, join)
	}
	if err != nil {
		return "", err
	}
	if !p.Created.IsZero() {
		if _, err := tx.Exec(ctx, `UPDATE partner_memberships SET created_at = $2 WHERE id = $1`, id, p.Created); err != nil {
			return "", err
		}
	}
	if _, err := r.partnerGroup(ctx, tx, id, p.GroupID, false); err != nil {
		return "", err
	}
	return "created", r.keep(ctx, tx, "partner", p.ID, id)
}

func (r *partnerImportRun) promoCode(ctx context.Context, tx pgx.Tx, c connectors.ImportPromoCode) (string, error) {
	if c.ID == "" {
		return "", importSkip("incomplete")
	}
	if _, ok, err := r.ref(ctx, tx, "promo_code", c.ID); err != nil || ok {
		return "unchanged", err
	}
	member, ok, err := r.ref(ctx, tx, "partner", c.PartnerID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", importSkip("partner_missing")
	}
	if !partnerCodeRE.MatchString(c.Code) {
		return "", importSkip("promo_code_invalid")
	}
	// The code exists in the billing account already: it is kept here as is.
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO partner_promo_codes (workspace_id, membership_id, code) VALUES ($1, $2, $3)
		ON CONFLICT (workspace_id, lower(code)) DO NOTHING RETURNING id::text`, r.ws, member, c.Code).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", importSkip("promo_code_taken")
	}
	if err != nil {
		return "", err
	}
	return "created", r.keep(ctx, tx, "promo_code", c.ID, id)
}

func (r *partnerImportRun) customer(ctx context.Context, tx pgx.Tx, c connectors.ImportCustomer) (string, error) {
	if c.ID == "" {
		return "", importSkip("incomplete")
	}
	if local, ok, err := r.ref(ctx, tx, "customer", c.ID); err != nil || ok {
		if err != nil {
			return "", err
		}
		// Imported before: still add ids an earlier run didn't keep.
		var rec string
		err := tx.QueryRow(ctx, `SELECT record_id::text FROM partner_referrals WHERE workspace_id = $1 AND id::text = $2`, r.ws, local).Scan(&rec)
		if errors.Is(err, pgx.ErrNoRows) {
			return "unchanged", nil
		}
		if err == nil {
			err = r.aliases(ctx, tx, rec, c)
		}
		return "unchanged", err
	}
	member, ok, err := r.ref(ctx, tx, "partner", c.PartnerID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", importSkip("partner_missing")
	}
	if c.CustomerID == "" && c.Email == "" {
		return "", importSkip("no_email")
	}
	run := &conversionRun{s: r.s, tx: tx, ws: r.ws, in: conversionInput{CustomerID: cutUTF8(c.CustomerID, 200), Email: c.Email}}
	rec, err := run.customer(ctx)
	if errors.As(err, new(errInvalid)) {
		return "", importSkip("no_email")
	}
	if err != nil {
		return "", err
	}
	if err := r.aliases(ctx, tx, rec, c); err != nil {
		return "", err
	}
	at := c.Created
	if at.IsZero() {
		at = time.Now()
	}
	var id, boundTo string
	err = tx.QueryRow(ctx, `INSERT INTO partner_referrals (workspace_id, program_id, membership_id, record_id, via, created_at)
		VALUES ($1, $2, $3, $4, 'import', $5) ON CONFLICT (program_id, record_id) DO NOTHING RETURNING id::text`,
		r.ws, r.prog.ID, member, rec, at).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// Bound here already: the first partner keeps the customer.
		if err := tx.QueryRow(ctx, `SELECT id::text, membership_id::text FROM partner_referrals WHERE program_id = $1 AND record_id = $2`,
			r.prog.ID, rec).Scan(&id, &boundTo); err != nil {
			return "", err
		}
		if boundTo != member {
			r.warn("customers", c.ID, "already_referred")
		}
		return "unchanged", r.keep(ctx, tx, "customer", c.ID, id)
	}
	if err != nil {
		return "", err
	}
	return "created", r.keep(ctx, tx, "customer", c.ID, id)
}

// aliases keeps the other ids the backend may report the customer by
// (Tolt's cus_... id) as user ids of their record, so conversions sent
// with them after the switch credit the partner. An id another record
// already holds stays there, and the import lists it.
func (r *partnerImportRun) aliases(ctx context.Context, tx pgx.Tx, rec string, c connectors.ImportCustomer) error {
	for _, alias := range c.Aliases {
		alias = cutUTF8(alias, 200)
		if alias == "" || alias == c.CustomerID {
			continue
		}
		owner, ok, err := customers.OwnerOf(ctx, tx, r.ws, "user_id", alias)
		if err != nil {
			return err
		}
		if ok {
			if owner != rec {
				r.warn("customers", c.ID, "customer_id_taken")
			}
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, verified_at, source)
			VALUES ($1, $2, 'user_id', $3, true, now(), 'partners') ON CONFLICT DO NOTHING`, r.ws, rec, alias); err != nil {
			return err
		}
	}
	return nil
}

func (r *partnerImportRun) currency(c string) string {
	if currencyRE.MatchString(c) {
		return c
	}
	return r.prog.Currency
}

// externalID is how an imported transaction is stored, so it never meets
// an id the workspace's backend reports.
func (r *partnerImportRun) externalID(id string) string { return cutUTF8(r.connector+":"+id, 200) }

func (r *partnerImportRun) transaction(ctx context.Context, tx pgx.Tx, t connectors.ImportTransaction) (string, error) {
	if t.ID == "" {
		return "", importSkip("incomplete")
	}
	referral, ok, err := r.ref(ctx, tx, "customer", t.CustomerID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", importSkip("customer_missing")
	}
	at := t.Created
	if at.IsZero() {
		at = time.Now()
	}
	ext, cur := r.externalID(t.ID), r.currency(t.Currency)
	result := "unchanged"
	var sale string
	err = tx.QueryRow(ctx, `INSERT INTO partner_conversions (workspace_id, program_id, referral_id, record_id, type, recurring, external_id,
			amount_cents, currency, source, occurred_at, payment_source, billing_interval)
		SELECT $1, $2, f.id, f.record_id, 'sale', $4, $5, $6, $7, 'import', $8, $9, $10 FROM partner_referrals f WHERE f.id = $3
		ON CONFLICT (program_id, type, external_id) WHERE external_id <> '' DO NOTHING RETURNING id::text`,
		r.ws, r.prog.ID, referral, t.Recurring, ext, max(t.AmountCents, 0), cur, at, t.PaymentSource, t.Interval).Scan(&sale)
	switch {
	case err == nil:
		result = "created"
		if _, err := tx.Exec(ctx, `UPDATE partner_referrals SET status = CASE WHEN status = 'lead' THEN 'customer' ELSE status END,
			first_sale_at = LEAST(coalesce(first_sale_at, $2), $2) WHERE id = $1`, referral, at); err != nil {
			return "", err
		}
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(ctx, `SELECT id::text FROM partner_conversions WHERE program_id = $1 AND type = 'sale' AND external_id = $2`,
			r.prog.ID, ext).Scan(&sale); err != nil {
			return "", err
		}
	default:
		return "", err
	}
	if t.RefundedCents > 0 {
		refundAt := t.RefundedAt
		if refundAt.IsZero() {
			refundAt = time.Now()
		}
		tag, err := tx.Exec(ctx, `INSERT INTO partner_conversions (workspace_id, program_id, referral_id, record_id, type, external_id, refund_of,
				amount_cents, currency, source, occurred_at)
			SELECT $1, $2, s.referral_id, s.record_id, 'refund', s.external_id, s.id, $4, s.currency, 'import', $5 FROM partner_conversions s WHERE s.id = $3
			ON CONFLICT (program_id, type, external_id) WHERE external_id <> '' DO NOTHING`, r.ws, r.prog.ID, sale, min(t.RefundedCents, max(t.AmountCents, 0)), refundAt)
		if err != nil {
			return "", err
		}
		if tag.RowsAffected() > 0 && result == "unchanged" {
			result = "updated"
		}
	}
	return result, nil
}

// commission brings over one commission, or the change in its status
// since the last run. Nothing here sends a webhook.
func (r *partnerImportRun) commission(ctx context.Context, tx pgx.Tx, c connectors.ImportCommission) (string, error) {
	if c.ID == "" {
		return "", importSkip("incomplete")
	}
	member, ok, err := r.ref(ctx, tx, "partner", c.PartnerID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", importSkip("partner_missing")
	}
	earned := c.Created
	if earned.IsZero() {
		earned = time.Now()
	}
	id, ok, err := r.ref(ctx, tx, "commission", c.ID)
	if err != nil {
		return "", err
	}
	if ok {
		return r.commissionStatus(ctx, tx, c, id, member, earned)
	}
	var conversion *string
	if c.TransactionID != "" {
		var cv string
		err := tx.QueryRow(ctx, `SELECT id::text FROM partner_conversions WHERE program_id = $1 AND type = 'sale' AND external_id = $2`,
			r.prog.ID, r.externalID(c.TransactionID)).Scan(&cv)
		if err == nil {
			conversion = &cv
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	kind := "cash"
	if c.AmountCents < 0 {
		kind = "adjustment"
	}
	hold := earned.AddDate(0, 0, r.prog.HoldDays)
	if c.Status != "pending" {
		hold = earned
	}
	err = tx.QueryRow(ctx, `INSERT INTO partner_rewards (workspace_id, program_id, membership_id, side, recipient_id, kind, conversion_id,
			amount_cents, currency, status, hold_until, note, approved_at, created_at)
		SELECT $1, $2, m.id, 'partner', m.record_id, $4, $5, $6, $7, 'pending', $8, $9, NULL, $10 FROM partner_memberships m WHERE m.id = $3
		RETURNING id::text`, r.ws, r.prog.ID, member, kind, conversion, c.AmountCents, r.currency(c.Currency), hold,
		"Imported from "+r.o.Connector.Definition().Name, earned).Scan(&id)
	if err != nil {
		return "", err
	}
	if err := r.keep(ctx, tx, "commission", c.ID, id); err != nil {
		return "", err
	}
	if _, err := r.commissionStatus(ctx, tx, c, id, member, earned); err != nil {
		return "", err
	}
	return "created", nil
}

// commissionStatus moves an imported reward to the commission's status on
// the platform: pending ones approve, are paid or are rejected there until
// switch-over day. A reward someone already moved on here stays, and a
// rejection after an approval here is noted.
func (r *partnerImportRun) commissionStatus(ctx context.Context, tx pgx.Tx, c connectors.ImportCommission, id, member string, earned time.Time) (string, error) {
	var status, currency string
	var cents int64
	var payout *string
	if err := tx.QueryRow(ctx, `SELECT status, amount_cents, currency, payout_id::text FROM partner_rewards WHERE id = $1`, id).
		Scan(&status, &cents, &currency, &payout); err != nil {
		return "", err
	}
	want := c.Status
	switch {
	case status == want || status == "paid" || status == "fulfilled":
		return "unchanged", nil
	case want == "pending":
		return "unchanged", nil
	case want == "rejected":
		if status != "pending" {
			r.warn("commissions", c.ID, "status_conflict")
			return "unchanged", nil
		}
		_, err := tx.Exec(ctx, `UPDATE partner_rewards SET status = 'rejected', note = note || ' (rejected there)', updated_at = now() WHERE id = $1`, id)
		return "updated", err
	}
	// approved or paid
	if status == "pending" {
		if _, err := tx.Exec(ctx, `UPDATE partner_rewards SET status = 'approved', approved_at = $2, updated_at = now() WHERE id = $1`, id, earned); err != nil {
			return "", err
		}
		if cents > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO spend_entries (workspace_id, category, provider, amount_cents, currency, occurred_at, source, ref, note, channel, brand_id)
				SELECT $1, 'people', 'partners', $2, $3, $4, 'auto', $5, 'Partner reward (' || p.name || ')', 'affiliates', p.brand_id
				FROM partner_programs p WHERE p.id = $6`, r.ws, cents, currency, earned, "partner_reward:"+id, r.prog.ID); err != nil {
				return "", err
			}
		}
	}
	if want == "paid" && payout == nil {
		if err := r.payInPayout(ctx, tx, c, id, member, cents, currency, earned); err != nil {
			return "", err
		}
	}
	return "updated", nil
}

// payInPayout puts a paid commission in the paid payout it was part of on
// the platform (or, when the platform doesn't say, the partner's payout on
// the day it was paid).
func (r *partnerImportRun) payInPayout(ctx context.Context, tx pgx.Tx, c connectors.ImportCommission, id, member string, cents int64, currency string, earned time.Time) error {
	paid := c.PaidAt
	if paid.IsZero() {
		paid = earned
	}
	key := c.PayoutID
	if key == "" {
		key = c.PartnerID + ":" + paid.UTC().Format("2006-01-02") + ":" + currency
	}
	payout, ok, err := r.ref(ctx, tx, "payout", key)
	if err != nil {
		return err
	}
	if ok {
		tag, err := tx.Exec(ctx, `UPDATE partner_payouts SET amount_cents = amount_cents + $2, updated_at = now() WHERE id = $1 AND amount_cents + $2 > 0`, payout, cents)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // a clawback larger than the payout: it stays approved
		}
	} else {
		if cents <= 0 {
			return nil
		}
		if err := tx.QueryRow(ctx, `INSERT INTO partner_payouts (workspace_id, membership_id, amount_cents, currency, status, note, reference, paid_at, created_at)
			VALUES ($1, $2, $3, $4, 'paid', $5, $6, $7, $7) RETURNING id::text`, r.ws, member, cents, currency,
			"Paid in "+r.o.Connector.Definition().Name, cutUTF8(c.PayoutID, 200), paid).Scan(&payout); err != nil {
			return err
		}
		if err := r.keep(ctx, tx, "payout", key, payout); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE partner_rewards SET status = 'paid', payout_id = $2, updated_at = now() WHERE id = $1`, id, payout)
	return err
}

// group brings over a platform group: one of the program's groups with the
// same name, else a new one. The platform's default group becomes the
// program's default when it has none.
func (r *partnerImportRun) group(ctx context.Context, tx pgx.Tx, g connectors.ImportGroup) (string, error) {
	name := strings.TrimSpace(cutUTF8(g.Name, 80))
	if g.ID == "" || name == "" {
		return "", importSkip("incomplete")
	}
	if _, ok, err := r.ref(ctx, tx, "group", g.ID); err != nil || ok {
		return "unchanged", err
	}
	var id string
	result := "unchanged"
	err := tx.QueryRow(ctx, `SELECT id::text FROM partner_groups WHERE program_id = $1 AND lower(name) = lower($2)`, r.prog.ID, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM partner_groups WHERE program_id = $1`, r.prog.ID).Scan(&n); err != nil {
			return "", err
		}
		if n >= maxPartnerGroups {
			return "", importSkip("too_many_groups")
		}
		err = tx.QueryRow(ctx, `INSERT INTO partner_groups (workspace_id, program_id, name) VALUES ($1, $2, $3) RETURNING id::text`, r.ws, r.prog.ID, name).Scan(&id)
		result = "created"
	}
	if err != nil {
		return "", err
	}
	if g.Default {
		if _, err := tx.Exec(ctx, `UPDATE partner_programs SET default_group_id = $2 WHERE id = $1 AND default_group_id IS NULL`, r.prog.ID, id); err != nil {
			return "", err
		}
	}
	return result, r.keep(ctx, tx, "group", g.ID, id)
}

// partnerGroup puts an imported partner in their platform group's group
// here. On a later run (again) it follows the platform only for partners
// the import made.
func (r *partnerImportRun) partnerGroup(ctx context.Context, tx pgx.Tx, member, platformGroup string, again bool) (bool, error) {
	if platformGroup == "" {
		return false, nil
	}
	group, ok, err := r.ref(ctx, tx, "group", platformGroup)
	if err != nil || !ok {
		return false, err
	}
	q := `UPDATE partner_memberships SET group_id = $2, updated_at = now() WHERE id = $1 AND group_id IS DISTINCT FROM $2::uuid`
	if again {
		q += ` AND source = 'import'`
	}
	tag, err := tx.Exec(ctx, q, member, group)
	return tag.RowsAffected() > 0, err
}

// flow brings over a platform flow as a flow here, after the program's
// own. One with anything Outflare can't map isn't imported at all, so
// nothing pays that shouldn't; the issue says what to add by hand.
func (r *partnerImportRun) flow(ctx context.Context, tx pgx.Tx, f connectors.ImportFlow) (string, error) {
	if f.ID == "" {
		return "", importSkip("incomplete")
	}
	if _, ok, err := r.ref(ctx, tx, "flow", f.ID); err != nil || ok {
		return "unchanged", err
	}
	if len(f.Unsupported) > 0 {
		slices.Sort(f.Unsupported)
		return "", importSkip("flow_" + slices.Compact(f.Unsupported)[0] + "_unsupported")
	}
	in := partnerRuleIn{Trigger: f.Trigger, Side: "partner", Kind: f.Kind, PercentBP: f.PercentBP, AmountCents: f.AmountCents,
		Recurrence: f.Recurrence, Months: f.Months, Name: cutUTF8(f.Name, 120), Conditions: f.Conditions, Approval: f.Approval,
		ApprovalDays: f.ApprovalDays}
	if f.GroupID != "" {
		g, ok, err := r.ref(ctx, tx, "group", f.GroupID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", importSkip("group_missing")
		}
		in.Group = &g
	}
	if f.Kind == "group" {
		g, ok, err := r.ref(ctx, tx, "group", f.TargetGroupID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", importSkip("group_missing")
		}
		in.TargetGroupID = &g
	}
	for _, o := range f.Overrides {
		if o.PartnerID != nil {
			m, ok, err := r.ref(ctx, tx, "partner", *o.PartnerID)
			if err != nil {
				return "", err
			}
			if !ok {
				r.warn("flows", f.ID, "custom_rate_partner_missing")
				continue
			}
			o.PartnerID = &m
		}
		in.Overrides = append(in.Overrides, o)
	}
	groups, members := []string{}, []string{}
	rows, err := tx.Query(ctx, `SELECT id::text FROM partner_groups WHERE program_id = $1`, r.prog.ID)
	if err != nil {
		return "", err
	}
	if groups, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return "", err
	}
	for _, o := range in.Overrides {
		if o.PartnerID != nil {
			members = append(members, *o.PartnerID)
		}
	}
	rules, err := checkPartnerRules([]partnerRuleIn{in}, groups, members)
	if errors.As(err, new(errInvalid)) {
		return "", importSkip("flow_invalid")
	}
	if err != nil {
		return "", err
	}
	ids, err := insertPartnerRules(ctx, tx, r.ws, r.prog.ID, rules)
	if err != nil {
		return "", err
	}
	return "created", r.keep(ctx, tx, "flow", f.ID, ids[0])
}
