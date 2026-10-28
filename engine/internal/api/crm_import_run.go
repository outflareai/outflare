package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/indexability"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/usagelimits"
)

// Moving from another CRM, once: the import copies the CRM's objects into
// customer records. Each object becomes a record type (People are person,
// Companies are account, Deals and custom objects get a type of their own),
// each field becomes a catalog field (an existing one when it already holds
// the same kind of value), select and status choices become option lists,
// and emails, phones and domains become identities. Values come in as CRM
// data (source crm_sync), so they never overwrite what someone typed in
// Outflare or what the workspace's own product sent.
//
// Every record keeps its CRM id as an identity (attio.record), so running
// the import again, such as on switch-over day, updates the same records.
// A person whose email, or a company whose domain, an Outflare record
// already holds verified is matched to that record instead of duplicated.
//
// It runs in steps, one page of records each and at most crmImportStep of
// work, so a large CRM never holds a worker past its lease.

const (
	jobCRMImport     = "crm.import"
	crmImportWarning = 100 // warnings kept in the summary; the rest are counted
)

// Page and batch sizes, and how long one step may run. Tests make them
// small.
var (
	crmImportPage  = 500
	crmImportBatch = 100
	crmImportStep  = 90 * time.Second
)

type crmImportView struct {
	ID            string           `json:"id"`
	IntegrationID string           `json:"integration_id"`
	Connector     string           `json:"connector"`
	Status        string           `json:"status"`
	Objects       []string         `json:"objects"`
	Summary       crmImportSummary `json:"summary"`
	Error         *string          `json:"error"`
	CreatedAt     time.Time        `json:"created_at"`
	StartedAt     *time.Time       `json:"started_at"`
	FinishedAt    *time.Time       `json:"finished_at"`
}

type crmImportSummary struct {
	Objects      []crmObjectResult `json:"objects"`
	Catalog      crmCatalogResult  `json:"catalog"`
	Members      crmMemberResult   `json:"members"`
	Placeholders int               `json:"placeholders"`
	Warnings     []string          `json:"warnings"`
	MoreWarnings int               `json:"more_warnings"`
}

type crmObjectResult struct {
	Object     string `json:"object"`
	Name       string `json:"name"`
	RecordType string `json:"record_type"`
	Seen       int    `json:"seen"`
	Created    int    `json:"created"`
	Matched    int    `json:"matched"`
	Updated    int    `json:"updated"`
	Unchanged  int    `json:"unchanged"`
}

type crmCatalogResult struct {
	TypesCreated      []string `json:"types_created"`
	FieldsCreated     []string `json:"fields_created"`
	FieldsReused      []string `json:"fields_reused"`
	OptionSetsCreated []string `json:"option_sets_created"`
	FieldsSkipped     []string `json:"fields_skipped"`
}

type crmMemberResult struct {
	Total   int `json:"total"`
	Matched int `json:"matched"`
}

func (sum *crmImportSummary) warn(format string, args ...any) {
	if len(sum.Warnings) < crmImportWarning {
		sum.Warnings = append(sum.Warnings, fmt.Sprintf(format, args...))
	} else {
		sum.MoreWarnings++
	}
}

func (sum *crmImportSummary) normalize() {
	if sum.Objects == nil {
		sum.Objects = []crmObjectResult{}
	}
	if sum.Warnings == nil {
		sum.Warnings = []string{}
	}
	for _, l := range []*[]string{&sum.Catalog.TypesCreated, &sum.Catalog.FieldsCreated, &sum.Catalog.FieldsReused,
		&sum.Catalog.OptionSetsCreated, &sum.Catalog.FieldsSkipped} {
		if *l == nil {
			*l = []string{}
		}
	}
}

// crmProgress is where an import got to: its plan, then the object and
// offset the next step starts at.
type crmProgress struct {
	Plan   *crmPlan `json:"plan,omitempty"`
	Object int      `json:"object"`
	Offset int      `json:"offset"`
	// Steps queued so far, which keeps each step's job distinct.
	Steps int `json:"steps"`
}

type crmPlan struct {
	Objects []crmObjectPlan `json:"objects"`
	// CRM member id to Outflare user id, matched by email.
	Members map[string]string `json:"members"`
}

type crmObjectPlan struct {
	Slug   string                  `json:"slug"`
	Name   string                  `json:"name"`
	Type   string                  `json:"type"`
	Fields map[string]crmFieldPlan `json:"fields"`
}

// Where a CRM field's values go.
const (
	crmToField    = "field"    // the Outflare field Key
	crmToName     = "name"     // the record's name (and first and last name for a person)
	crmToEmail    = "email"    // email identities
	crmToPhone    = "phone"    // phone identities
	crmToDomain   = "domain"   // domain identities, and the website when it is empty
	crmToLinkedIn = "linkedin" // the built-in LinkedIn field, when the value is a LinkedIn URL
)

type crmFieldPlan struct {
	Kind      string            `json:"kind"`
	To        string            `json:"to"`
	Key       string            `json:"key,omitempty"`
	ValueType string            `json:"value_type,omitempty"`
	Multiple  bool              `json:"multiple,omitempty"`
	Target    string            `json:"target,omitempty"` // record type a reference points at
	Options   map[string]string `json:"options,omitempty"`
	// A location also fills the built-in city and country.
	Place bool `json:"place,omitempty"`
}

// crmValueTypes is the Outflare value type each CRM field kind becomes.
var crmValueTypes = map[string]string{
	connectors.CRMText: "text", connectors.CRMNumber: "number", connectors.CRMCheckbox: "checkbox", connectors.CRMCurrency: "currency",
	connectors.CRMDate: "date", connectors.CRMTimestamp: "timestamp", connectors.CRMRating: "rating", connectors.CRMSelect: "select",
	connectors.CRMStatus: "status", connectors.CRMEmail: "email", connectors.CRMPhone: "phone", connectors.CRMDomain: "url",
	connectors.CRMReference: "record_reference", connectors.CRMMemberRef: "actor_reference", connectors.CRMLocationVal: "location",
	connectors.CRMPersonName: "text",
}

// crmSkippedFields are CRM system fields that hold nothing worth keeping:
// they are worked out by the CRM, or are the other side of a link already
// imported (a company's team is its people's companies).
var crmSkippedFields = map[string]map[string]string{
	"*": {"created_by": "who added the record in the CRM", "strongest_connection_user": "worked out by the CRM",
		"strongest_connection_strength": "worked out by the CRM", "strongest_connection_strength_legacy": "worked out by the CRM"},
	"companies": {"team": "the other side of each person's companies"},
}

// crmKey turns a CRM name or slug into a catalog key of at most n
// characters, or "" when nothing usable is left.
func crmKey(s string, n int) string {
	k := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "_"), "_")
	if k != "" && (k[0] < 'a' || k[0] > 'z') {
		k = "f_" + k
	}
	if len(k) > n {
		k = strings.TrimRight(k[:n], "_")
	}
	if len(k) < 2 {
		return ""
	}
	return k
}

// ---- Starting and reading ----

const crmImportCols = `id::text, integration_id::text, connector, status, objects, summary, error, created_at, started_at, finished_at`

func scanCRMImport(row pgx.Row) (crmImportView, error) {
	var v crmImportView
	err := row.Scan(&v.ID, &v.IntegrationID, &v.Connector, &v.Status, &v.Objects, &v.Summary, &v.Error, &v.CreatedAt, &v.StartedAt, &v.FinishedAt)
	v.Summary.normalize()
	if v.Objects == nil {
		v.Objects = []string{}
	}
	return v, err
}

type crmQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func getCRMImport(ctx context.Context, q crmQuerier, ws, id string) (crmImportView, error) {
	return scanCRMImport(q.QueryRow(ctx, `SELECT `+crmImportCols+` FROM crm_imports WHERE workspace_id = $1 AND id = $2`, ws, id))
}

// startCRMImport records an import and queues its first step, in the
// caller's transaction.
func startCRMImport(ctx context.Context, tx pgx.Tx, a *access, integrationID, connector string, objects []string) (crmImportView, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO crm_imports (workspace_id, integration_id, connector, objects, created_by, created_by_type)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, a.WorkspaceID, integrationID, connector, objects, nullIfEmpty(a.ActorID), a.ActorType).Scan(&id)
	if isUnique(err) {
		return crmImportView{}, recordConflict{"conflict", "an import from this connection is already running; wait for it to finish (GET .../crm-imports)"}
	}
	if err != nil {
		return crmImportView{}, err
	}
	if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobCRMImport, WorkspaceID: &a.WorkspaceID, Payload: map[string]string{"import_id": id},
		MaxAttempts: 5, IdempotencyKey: id + ":0"}); err != nil {
		return crmImportView{}, err
	}
	if err := record(ctx, tx, &a.principal, a.WorkspaceID, "crm_import.started", "crm_import", id,
		map[string]any{"integration_id": integrationID, "connector": connector, "objects": len(objects)}); err != nil {
		return crmImportView{}, err
	}
	return getCRMImport(ctx, tx, a.WorkspaceID, id)
}

// RegisterJobs adds the server's own background work to the queue.
func (s *Server) RegisterJobs(q *jobs.Queue) {
	q.Handle(jobCRMImport, s.crmImportJob)
	q.Handle(jobPaymentsSync, s.paymentsSyncJob)
	q.Handle(jobSetupInstall, s.setupInstallJob)
	q.Handle(jobDNSAdd, s.dnsAddJob)
	q.Handle(jobTrackingCheck, s.trackingCheckJob)
	q.Handle(jobTrackingHTTPS, s.trackingHTTPSJob)
	q.Handle(jobTrackingHTTPSRemove, s.trackingHTTPSRemoveJob)
	q.Handle(jobVisitorReveal, s.visitorRevealJob)
	q.Handle(jobAIBudgetAlert, s.aiBudgetAlertJob)
	q.Handle(usagelimits.JobAlert, s.connectionLimitAlertJob)
	q.Handle(inbox.JobDeal, s.replyDealJob)
	(&alerts.Service{Pool: s.pool, Log: s.log, Send: s.sendAccountEmail, PublicURL: s.publicURL, Store: s.integrations}).Register(q)
	q.Handle(jobFixTicketWrite, s.fixTicketWriteJob)
	q.Handle(indexability.JobSnapshot, indexability.Handler(s.pool))
	s.registerSocialJobs(q)
	s.registerSemanticJobs(q)
}

func (s *Server) crmImportJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		ImportID string `json:"import_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.ImportID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	return s.runCRMImportStep(ctx, *job.WorkspaceID, p.ImportID, job.Attempt >= job.MaxAttempts)
}

// ---- Running ----

type crmRun struct {
	s        *Server
	a        *access
	id       string
	o        *connectors.Opened
	imp      connectors.CRMImporter
	progress crmProgress
	sum      crmImportSummary
	objects  []string
}

// runCRMImportStep runs one step of an import: the plan first, then pages
// of records until the step's time is up, then queues the next step. A
// failure the CRM may get over (a rate limit, an outage) is retried; on
// the last attempt, or when the connection itself is refused, the import
// ends as failed with the reason.
func (s *Server) runCRMImportStep(ctx context.Context, ws, id string, last bool) error {
	run := &crmRun{s: s, id: id}
	var integrationID, actorType string
	var actorID *string
	var progress []byte
	err := s.pool.QueryRow(ctx, `UPDATE crm_imports SET status = 'running', started_at = coalesce(started_at, now())
		WHERE workspace_id = $1 AND id = $2 AND status IN ('queued', 'running')
		RETURNING integration_id::text, objects, progress, summary, created_by::text, created_by_type`, ws, id).
		Scan(&integrationID, &run.objects, &progress, &run.sum, &actorID, &actorType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // finished or gone
	}
	if err != nil {
		return err
	}
	_ = json.Unmarshal(progress, &run.progress)
	run.sum.normalize()
	run.a = &access{principal: principal{ActorID: deref(actorID), ActorType: actorType}, WorkspaceID: ws}
	if actorID != nil && actorType == "human" {
		_ = s.pool.QueryRow(ctx, `SELECT id::text FROM users WHERE actor_id = $1`, *actorID).Scan(&run.a.UserID)
	}

	done, err := run.step(ctx, integrationID)
	if err != nil {
		permanent := errors.Is(err, connectors.ErrNotFound) || connectors.KindOf(err) == connectors.ErrAuth || errors.As(err, new(errInvalid))
		if !last && !permanent {
			return err
		}
		return run.finish(ctx, err)
	}
	if done {
		return run.finish(ctx, nil)
	}
	return run.save(ctx, true)
}

// save stores where the import got to and, when more remains, queues the
// next step in the same transaction.
func (r *crmRun) save(ctx context.Context, next bool) error {
	return pgx.BeginFunc(ctx, r.s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE crm_imports SET progress = $3, summary = $4 WHERE workspace_id = $1 AND id = $2`,
			r.a.WorkspaceID, r.id, r.progress, r.sum); err != nil {
			return err
		}
		if !next {
			return nil
		}
		r.progress.Steps++
		if _, err := tx.Exec(ctx, `UPDATE crm_imports SET progress = $3 WHERE workspace_id = $1 AND id = $2`, r.a.WorkspaceID, r.id, r.progress); err != nil {
			return err
		}
		_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobCRMImport, WorkspaceID: &r.a.WorkspaceID, Payload: map[string]string{"import_id": r.id},
			MaxAttempts: 5, IdempotencyKey: fmt.Sprintf("%s:%d", r.id, r.progress.Steps)})
		return err
	})
}

func (r *crmRun) finish(ctx context.Context, runErr error) error {
	status, msg := "done", (*string)(nil)
	if runErr != nil {
		status = "failed"
		m := runErr.Error()
		msg = &m
		r.s.log.Warn("crm import failed", "workspace_id", r.a.WorkspaceID, "import_id", r.id, "err", runErr)
	}
	created, updated := 0, 0
	for _, o := range r.sum.Objects {
		created += o.Created
		updated += o.Updated + o.Matched
	}
	return pgx.BeginFunc(ctx, r.s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE crm_imports SET status = $3, progress = $4, summary = $5, error = $6, finished_at = now()
			WHERE workspace_id = $1 AND id = $2`, r.a.WorkspaceID, r.id, status, r.progress, r.sum, msg); err != nil {
			return err
		}
		data := map[string]any{"objects": len(r.sum.Objects), "created": created, "updated": updated, "placeholders": r.sum.Placeholders,
			"types_created": len(r.sum.Catalog.TypesCreated), "fields_created": len(r.sum.Catalog.FieldsCreated),
			"warnings": len(r.sum.Warnings) + r.sum.MoreWarnings}
		if runErr != nil {
			data["error_code"] = crmImportErrorCode(runErr)
		}
		return record(ctx, tx, &r.a.principal, r.a.WorkspaceID, "crm_import."+map[string]string{"done": "finished", "failed": "failed"}[status],
			"crm_import", r.id, data)
	})
}

func crmImportErrorCode(err error) string {
	switch {
	case errors.Is(err, connectors.ErrNotFound):
		return "not_found"
	case connectors.KindOf(err) != "":
		return "provider_" + string(connectors.KindOf(err))
	case errors.As(err, new(errInvalid)):
		return "invalid_request"
	}
	return "internal"
}

func (r *crmRun) step(ctx context.Context, integrationID string) (bool, error) {
	o, err := r.s.integrations.Open(ctx, r.s.pool, r.a.WorkspaceID, integrationID)
	if err != nil {
		return false, err
	}
	imp, ok := o.Connector.(connectors.CRMImporter)
	if !ok {
		return false, errInvalid(o.Name + " cannot import a CRM")
	}
	r.o, r.imp = o, imp
	if r.progress.Plan == nil {
		if err := r.plan(ctx); err != nil {
			return false, err
		}
		if err := r.save(ctx, false); err != nil {
			return false, err
		}
	}
	deadline := time.Now().Add(crmImportStep)
	for r.progress.Object < len(r.progress.Plan.Objects) {
		op := &r.progress.Plan.Objects[r.progress.Object]
		page, err := r.imp.CRMRecords(ctx, o.Client, o.Creds, op.Slug, r.progress.Offset, crmImportPage)
		if err != nil {
			return false, err
		}
		res := r.result(op)
		for start := 0; start < len(page); start += crmImportBatch {
			if err := r.batch(ctx, op, res, page[start:min(start+crmImportBatch, len(page))]); err != nil {
				return false, err
			}
		}
		if len(page) < crmImportPage {
			r.progress.Object, r.progress.Offset = r.progress.Object+1, 0
		} else {
			r.progress.Offset += len(page)
		}
		if r.progress.Object == len(r.progress.Plan.Objects) {
			return true, nil
		}
		if err := r.save(ctx, false); err != nil {
			return false, err
		}
		if time.Now().After(deadline) {
			return false, nil
		}
	}
	return true, nil
}

func (r *crmRun) result(op *crmObjectPlan) *crmObjectResult {
	for i := range r.sum.Objects {
		if r.sum.Objects[i].Object == op.Slug {
			return &r.sum.Objects[i]
		}
	}
	r.sum.Objects = append(r.sum.Objects, crmObjectResult{Object: op.Slug, Name: op.Name, RecordType: op.Type})
	return &r.sum.Objects[len(r.sum.Objects)-1]
}

// ---- The plan ----

// plan reads the CRM's objects and members and makes the catalog ready:
// the record types, option lists and fields the records need, reusing what
// the workspace already has.
func (r *crmRun) plan(ctx context.Context) error {
	preview, err := r.imp.CRMPreview(ctx, r.o.Client, r.o.Creds, 0)
	if err != nil {
		return err
	}
	members, err := r.imp.CRMMembers(ctx, r.o.Client, r.o.Creds)
	if err != nil {
		return err
	}
	plan := &crmPlan{Members: map[string]string{}}
	want := map[string]bool{}
	for _, slug := range r.objects {
		want[slug] = true
	}
	// Companies before people, so a person's companies are already here.
	objects := slices.Clone(preview.Objects)
	order := map[string]int{"companies": 0, "people": 1}
	slices.SortStableFunc(objects, func(x, y connectors.CRMObject) int {
		rank := func(o connectors.CRMObject) int {
			if n, ok := order[o.Slug]; ok {
				return n
			}
			return 2
		}
		return rank(x) - rank(y)
	})
	for _, slug := range r.objects {
		if !slices.ContainsFunc(objects, func(o connectors.CRMObject) bool { return o.Slug == slug }) {
			return errInvalid(fmt.Sprintf("%s has no object %s", r.o.Name, slug))
		}
	}

	return pgx.BeginFunc(ctx, r.s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT u.id::text, lower(u.email) FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.workspace_id = $1`, r.a.WorkspaceID)
		if err != nil {
			return err
		}
		byEmail := map[string]string{}
		for rows.Next() {
			var id, email string
			if err := rows.Scan(&id, &email); err != nil {
				return err
			}
			byEmail[email] = id
		}
		if err := rows.Err(); err != nil {
			return err
		}
		r.sum.Members.Total = len(members)
		for _, m := range members {
			if id, ok := byEmail[strings.ToLower(m.Email)]; ok {
				plan.Members[m.ID] = id
				r.sum.Members.Matched++
			}
		}

		c, err := loadCatalog(ctx, tx, r.a.WorkspaceID, false)
		if err != nil {
			return err
		}
		// Each object's record type first, so references can point at them.
		types := map[string]string{}
		for _, o := range objects {
			if len(want) > 0 && !want[o.Slug] {
				continue
			}
			key, err := r.recordType(ctx, tx, c, o)
			if err != nil {
				return err
			}
			types[o.Slug] = key
			plan.Objects = append(plan.Objects, crmObjectPlan{Slug: o.Slug, Name: o.Plural, Type: key, Fields: map[string]crmFieldPlan{}})
		}
		if c, err = loadCatalog(ctx, tx, r.a.WorkspaceID, false); err != nil {
			return err
		}
		for i, o := range objects {
			if _, ok := types[o.Slug]; !ok {
				continue
			}
			op := &plan.Objects[slices.IndexFunc(plan.Objects, func(p crmObjectPlan) bool { return p.Slug == objects[i].Slug })]
			for _, f := range o.Fields {
				fp, why, err := r.field(ctx, tx, c, op, f, types)
				if err != nil {
					return err
				}
				if why != "" {
					r.sum.Catalog.FieldsSkipped = append(r.sum.Catalog.FieldsSkipped, o.Slug+"."+f.Slug+": "+why)
					continue
				}
				op.Fields[f.Slug] = fp
				// A field just added is in the catalog for the next one.
				if c, err = loadCatalog(ctx, tx, r.a.WorkspaceID, false); err != nil {
					return err
				}
			}
		}
		r.progress.Plan = plan
		return nil
	})
}

// recordType finds or adds the record type an object's records become.
func (r *crmRun) recordType(ctx context.Context, tx pgx.Tx, c *catalog, o connectors.CRMObject) (string, error) {
	switch o.Slug {
	case "people":
		return "person", nil
	case "companies":
		return "account", nil
	}
	key := crmKey(o.Name, 40)
	if key == "" {
		key = crmKey(o.Slug, 40)
	}
	if key == "" || key == "person" || key == "account" {
		key = crmKey("attio_"+o.Slug, 40)
	}
	if t := c.recordType(key); t != nil {
		if t.Archived {
			return "", errInvalid(fmt.Sprintf("record type %s is archived; restore it in the catalog, or import without %s", key, o.Slug))
		}
		return key, nil
	}
	name, plural := o.Name, o.Plural
	if name == "" {
		name = o.Slug
	}
	if plural == "" {
		plural = name + "s"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO record_types (workspace_id, key, name, plural, description, created_by) VALUES ($1, $2, $3, $4, $5, $6)`,
		r.a.WorkspaceID, key, name, plural, "Imported from "+r.o.Connector.Definition().Name+".", nullIfEmpty(r.a.ActorID)); err != nil {
		return "", err
	}
	r.sum.Catalog.TypesCreated = append(r.sum.Catalog.TypesCreated, key)
	return key, record(ctx, tx, &r.a.principal, r.a.WorkspaceID, "record_type.created", "record_type", key, map[string]any{"via": "crm_import"})
}

// field decides where a CRM field's values go, adding the field and its
// option list when the type has no field that fits. It returns why the
// field is left out, or "".
func (r *crmRun) field(ctx context.Context, tx pgx.Tx, c *catalog, op *crmObjectPlan, f connectors.CRMField, types map[string]string) (crmFieldPlan, string, error) {
	fp := crmFieldPlan{Kind: f.Kind, To: crmToField}
	if why, ok := crmSkippedFields["*"][f.Slug]; ok {
		return fp, why, nil
	}
	if why, ok := crmSkippedFields[op.Slug][f.Slug]; ok {
		return fp, why, nil
	}
	vt, ok := crmValueTypes[f.Kind]
	if !ok {
		return fp, "worked out by the CRM", nil
	}
	// The record's name, identities and the built-in fields.
	switch {
	case f.Slug == "name" && (f.Kind == connectors.CRMText || f.Kind == connectors.CRMPersonName):
		fp.To = crmToName
		return fp, "", nil
	case f.Kind == connectors.CRMEmail && op.Type == "person":
		fp.To = crmToEmail
		return fp, "", nil
	case f.Kind == connectors.CRMPhone && op.Type == "person":
		fp.To = crmToPhone
		return fp, "", nil
	case f.Kind == connectors.CRMDomain && op.Type == "account":
		fp.To = crmToDomain
		return fp, "", nil
	case f.Slug == "linkedin" && f.Kind == connectors.CRMText && (op.Type == "person" || op.Type == "account"):
		fp.To = crmToLinkedIn
		return fp, "", nil
	case f.Slug == "company" && op.Slug == "people" && f.Kind == connectors.CRMReference:
		fp.Key, fp.ValueType, fp.Multiple, fp.Target = "companies", "record_reference", true, "account"
		r.sum.Catalog.FieldsReused = append(r.sum.Catalog.FieldsReused, op.Type+".companies")
		return fp, "", nil
	case f.Slug == "job_title" && op.Type == "person" && f.Kind == connectors.CRMText:
		fp.Key, fp.ValueType = "job_title", "text"
		r.sum.Catalog.FieldsReused = append(r.sum.Catalog.FieldsReused, op.Type+".job_title")
		return fp, "", nil
	}

	fp.ValueType = vt
	fp.Multiple = f.Multiple && slices.Contains(multiTypes, vt)
	fp.Place = vt == "location"
	if vt == "record_reference" {
		if len(f.Target) != 1 {
			return fp, "points at several kinds of record", nil
		}
		target, ok := types[f.Target[0]]
		if !ok {
			return fp, "points at " + f.Target[0] + ", which this import leaves out", nil
		}
		fp.Target = target
	}
	t := c.recordType(op.Type)
	key := crmKey(f.Slug, 63)
	if key == "" {
		key = crmKey("attio_"+f.Slug, 63)
	}
	if slices.Contains(reservedFieldKeys, key) {
		key = crmKey("attio_"+key, 63)
	}
	fits := func(e *fieldView) bool {
		return !e.Archived && e.ValueType == vt && e.WritePolicy != "computed" && (vt != "record_reference" || deref(e.TargetType) == fp.Target)
	}
	if e := t.field(key); e != nil && !fits(e) {
		key = crmKey("attio_"+key, 63)
	}
	fp.Key = key
	if e := t.field(key); e != nil {
		if !fits(e) {
			return fp, fmt.Sprintf("%s already has a field %s for another kind of value", t.Name, key), nil
		}
		fp.Multiple = e.Multiple
		r.sum.Catalog.FieldsReused = append(r.sum.Catalog.FieldsReused, op.Type+"."+key)
		if vt == "select" || vt == "status" {
			opts, err := r.options(ctx, tx, c, deref(e.OptionSet), "", f.Options)
			if err != nil {
				return fp, "", err
			}
			fp.Options = opts
		}
		return fp, "", nil
	}

	var set *string
	if vt == "select" || vt == "status" {
		k := crmKey(op.Type+"_"+key, 40)
		opts, err := r.options(ctx, tx, c, k, f.Title, f.Options)
		if err != nil {
			return fp, "", err
		}
		fp.Options, set = opts, &k
	}
	var target *string
	if fp.Target != "" {
		target = &fp.Target
	}
	label := strings.TrimSpace(f.Title)
	if label == "" {
		label = f.Slug
	}
	if _, err := tx.Exec(ctx, `INSERT INTO field_definitions (workspace_id, record_type, key, label, description, value_type, option_set, multiple, target_type, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, r.a.WorkspaceID, op.Type, key, label, "Imported from "+r.o.Connector.Definition().Name+".",
		vt, set, fp.Multiple, target, nullIfEmpty(r.a.ActorID)); err != nil {
		return fp, "", err
	}
	r.sum.Catalog.FieldsCreated = append(r.sum.Catalog.FieldsCreated, op.Type+"."+key)
	return fp, "", record(ctx, tx, &r.a.principal, r.a.WorkspaceID, "field.created", "field", op.Type+"."+key,
		map[string]any{"record_type": op.Type, "value_type": vt, "multiple": fp.Multiple, "via": "crm_import"})
}

// options maps the CRM's choices to the option list key's options, adding
// the ones it lacks (and the list itself, named name, when it is new). A
// built-in list is never changed: choices it lacks are left out.
func (r *crmRun) options(ctx context.Context, tx pgx.Tx, c *catalog, key, name string, titles []string) (map[string]string, error) {
	out := map[string]string{}
	set := c.optionSet(key)
	var current []catalogOption
	if set != nil {
		current = slices.Clone(set.Options)
	}
	used := map[string]bool{}
	for _, o := range current {
		used[o.Key] = true
	}
	added := false
	for i, title := range titles {
		match := slices.IndexFunc(current, func(o catalogOption) bool {
			return strings.EqualFold(o.Label, strings.TrimSpace(title)) || strings.EqualFold(o.Key, title)
		})
		if match >= 0 {
			out[title] = current[match].Key
			continue
		}
		if set != nil && set.Builtin {
			continue
		}
		k := crmKey(title, 60)
		if k == "" || !optionKey.MatchString(k) {
			k = fmt.Sprintf("option_%d", i+1)
		}
		for n := 2; used[k]; n++ {
			k = fmt.Sprintf("%s_%d", strings.TrimSuffix(k[:min(len(k), 56)], "_"), n)
		}
		used[k] = true
		label := strings.TrimSpace(title)
		if len(label) > 80 {
			label = label[:80]
		}
		if label == "" {
			label = k
		}
		current = append(current, catalogOption{Key: k, Label: label})
		out[title] = k
		added = true
	}
	switch {
	case set == nil:
		if name = strings.TrimSpace(name); name == "" || len(name) > 60 {
			name = key
		}
		if current == nil {
			current = []catalogOption{}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO option_sets (workspace_id, key, name, options, created_by) VALUES ($1, $2, $3, $4, $5)`,
			r.a.WorkspaceID, key, name, current, nullIfEmpty(r.a.ActorID)); err != nil {
			return nil, err
		}
		r.sum.Catalog.OptionSetsCreated = append(r.sum.Catalog.OptionSetsCreated, key)
		return out, record(ctx, tx, &r.a.principal, r.a.WorkspaceID, "option_set.created", "option_set", key,
			map[string]any{"options": len(current), "via": "crm_import"})
	case added:
		if len(current) > 500 {
			return nil, errInvalid("option list " + key + " would have more than 500 options")
		}
		if _, err := tx.Exec(ctx, `UPDATE option_sets SET options = $3, updated_at = now() WHERE workspace_id = $1 AND key = $2`,
			r.a.WorkspaceID, key, current); err != nil {
			return nil, err
		}
		return out, record(ctx, tx, &r.a.principal, r.a.WorkspaceID, "option_set.updated", "option_set", key,
			map[string]any{"options": len(current), "via": "crm_import"})
	}
	return out, nil
}

// ---- Records ----

// identityNamespace is where a record keeps its id in the CRM.
func (r *crmRun) identityNamespace() string { return r.o.Connector.Definition().ID + ".record" }

// batch writes up to crmImportBatch records in one transaction.
func (r *crmRun) batch(ctx context.Context, op *crmObjectPlan, res *crmObjectResult, recs []connectors.CRMRecord) error {
	// Kept aside until the transaction commits, so a retried batch does not
	// count twice.
	sum, counts := r.sum, *res
	sum.Warnings = slices.Clone(r.sum.Warnings)
	err := pgx.BeginFunc(ctx, r.s.pool, func(tx pgx.Tx) error {
		c, err := loadCatalog(ctx, tx, r.a.WorkspaceID, false)
		if err != nil {
			return err
		}
		t := c.recordType(op.Type)
		if t == nil {
			return errInvalid("record type " + op.Type + " is gone; start the import again")
		}
		for _, rec := range recs {
			if err := r.one(ctx, tx, c, t, op, &counts, &sum, rec); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		r.sum, *res = sum, counts
	}
	return err
}

// one writes a single CRM record: found by its CRM id, or matched by a
// verified email or domain, or added.
func (r *crmRun) one(ctx context.Context, tx pgx.Tx, c *catalog, t *recordTypeView, op *crmObjectPlan, res *crmObjectResult, sum *crmImportSummary, rec connectors.CRMRecord) error {
	res.Seen++
	label := op.Slug + " " + rec.ID
	id, err := r.find(ctx, tx, r.identityNamespace(), rec.ID, "")
	if err != nil {
		return err
	}
	how := "updated"
	if id == "" {
		if id, err = r.match(ctx, tx, op, rec); err != nil {
			return err
		}
		how = "matched"
	}
	created := false
	if id == "" {
		if id, err = r.add(ctx, tx, op.Type, rec.ID); err != nil {
			return err
		}
		how, created = "created", true
	} else if err := r.identity(ctx, tx, sum, label, id, r.identityNamespace(), rec.ID, true); err != nil {
		return err
	}
	cur, err := lockRecord(ctx, tx, r.a, id)
	if err != nil {
		return err
	}
	if cur.Attributes == nil {
		cur.Attributes = map[string]any{}
	}
	if cur.AttributeSources == nil {
		cur.AttributeSources = map[string]string{}
	}
	changed, name := false, cur.Name

	for slug, fp := range op.Fields {
		values := rec.Values[slug]
		if len(values) == 0 {
			continue
		}
		switch fp.To {
		case crmToName:
			switch v := values[0].(type) {
			case string:
				name = strings.TrimSpace(v)
			case connectors.CRMName:
				name = strings.TrimSpace(v.Full)
				if name == "" {
					name = strings.TrimSpace(v.First + " " + v.Last)
				}
				if op.Type == "person" {
					attrs := map[string]any{}
					if v.First != "" {
						attrs["first_name"] = v.First
					}
					if v.Last != "" {
						attrs["last_name"] = v.Last
					}
					ok, err := r.apply(ctx, tx, c, t, sum, label, &cur, attrs)
					if err != nil {
						return err
					}
					changed = changed || ok
				}
			}
		case crmToEmail, crmToPhone, crmToDomain:
			for _, v := range values {
				s, _ := v.(string)
				if err := r.identity(ctx, tx, sum, label, id, fp.To, s, fp.To == crmToDomain); err != nil {
					return err
				}
			}
			if fp.To == crmToDomain {
				if _, has := cur.Attributes["website"]; !has {
					if d, _ := values[0].(string); d != "" {
						ok, err := r.apply(ctx, tx, c, t, sum, label, &cur, map[string]any{"website": "https://" + strings.TrimPrefix(d, "www.")})
						if err != nil {
							return err
						}
						changed = changed || ok
					}
				}
			}
		case crmToLinkedIn:
			s, _ := values[0].(string)
			if s = strings.TrimSpace(s); strings.Contains(strings.ToLower(s), "linkedin.com/") {
				if !strings.HasPrefix(s, "http") {
					s = "https://" + s
				}
				ok, err := r.apply(ctx, tx, c, t, sum, label, &cur, map[string]any{"linkedin_url": s})
				if err != nil {
					return err
				}
				changed = changed || ok
			}
		case crmToField:
			attrs, err := r.values(ctx, tx, sum, label, fp, values)
			if err != nil {
				return err
			}
			// A location fills city and country where the type has them.
			for _, k := range []string{"city", "country"} {
				if _, ok := attrs[k]; ok && k != fp.Key && t.field(k) == nil {
					delete(attrs, k)
				}
			}
			ok, err := r.apply(ctx, tx, c, t, sum, label, &cur, attrs)
			if err != nil {
				return err
			}
			changed = changed || ok
		}
	}
	if name != cur.Name && name != "" {
		if !created {
			if err := attributeChanged(ctx, tx, r.a, id, "name", cur.Name, name, true, "crm_sync"); err != nil {
				return err
			}
		}
		cur.Name, changed = name, true
	}
	switch {
	case created:
		res.Created++
	case how == "matched":
		res.Matched++
	case changed:
		res.Updated++
	default:
		res.Unchanged++
	}
	if !changed {
		return nil
	}
	bump := 1
	if created {
		bump = 0
	}
	_, err = tx.Exec(ctx, `UPDATE records SET name = $2, attributes = $3, attribute_sources = $4, version = version + $5, updated_at = now() WHERE id = $1`,
		id, cur.Name, cur.Attributes, cur.AttributeSources, bump)
	return err
}

// apply writes attributes one by one, so a value the catalog refuses is a
// warning and the rest still land.
func (r *crmRun) apply(ctx context.Context, tx pgx.Tx, c *catalog, t *recordTypeView, sum *crmImportSummary, label string, cur *recordView, attrs map[string]any) (bool, error) {
	changed := false
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		raw, _ := json.Marshal(attrs[k])
		ok, err := applyAttributes(ctx, tx, c, t, r.a, cur, map[string]json.RawMessage{k: raw}, "crm_sync")
		var inv errInvalid
		if errors.As(err, &inv) {
			sum.warn("%s: %s left out: %s", label, k, string(inv))
			continue
		}
		if err != nil {
			return false, err
		}
		changed = changed || ok
	}
	cur.Skipped = nil
	return changed, nil
}

// values turns a CRM field's values into what its Outflare field takes,
// keyed by the field (and the built-in city and country for a location).
func (r *crmRun) values(ctx context.Context, tx pgx.Tx, sum *crmImportSummary, label string, fp crmFieldPlan, values []any) (map[string]any, error) {
	out := []any{}
	attrs := map[string]any{}
	for _, v := range values {
		switch x := v.(type) {
		case string:
			switch fp.ValueType {
			case "select", "status":
				k, ok := fp.Options[x]
				if !ok {
					sum.warn("%s: %s left out: %q is not one of its options", label, fp.Key, x)
					continue
				}
				out = append(out, k)
			case "url":
				out = append(out, "https://"+x)
			case "actor_reference":
				if u, ok := r.progress.Plan.Members[x]; ok {
					out = append(out, u)
				}
			case "text":
				if len(x) > 10000 {
					x = x[:10000]
				}
				out = append(out, x)
			default:
				out = append(out, x)
			}
		case float64, bool:
			out = append(out, x)
		case connectors.CRMMoney:
			code := strings.ToUpper(x.Currency)
			out = append(out, map[string]any{"amount_minor": math.Round(x.Amount * math.Pow10(minorDigits(code))), "currency": code})
		case connectors.CRMName:
			s := strings.TrimSpace(x.Full)
			if s == "" {
				s = strings.TrimSpace(x.First + " " + x.Last)
			}
			if s != "" {
				out = append(out, s)
			}
		case connectors.CRMLocation:
			loc := map[string]any{}
			for k, s := range map[string]string{"line1": x.Line1, "line2": x.Line2, "city": x.City, "region": x.Region, "postal_code": x.PostalCode} {
				if s = strings.TrimSpace(s); s != "" {
					loc[k] = s[:min(len(s), 200)]
				}
			}
			country := strings.ToUpper(strings.TrimSpace(x.Country))
			if slices.ContainsFunc(countries, func(p [2]string) bool { return p[0] == country }) {
				loc["country"] = country
			}
			if len(loc) > 0 {
				out = append(out, loc)
			}
			if fp.Place && len(out) == 1 {
				if city, ok := loc["city"]; ok {
					attrs["city"] = city
				}
				if c, ok := loc["country"]; ok {
					attrs["country"] = c
				}
			}
		case connectors.CRMRef:
			id, err := r.reference(ctx, tx, sum, fp.Target, x.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return attrs, nil
	}
	if fp.Multiple {
		if len(out) > 100 {
			out = out[:100]
		}
		attrs[fp.Key] = out
	} else {
		attrs[fp.Key] = out[0]
	}
	return attrs, nil
}

// minorDigits is how many decimal places a currency's amounts have.
func minorDigits(code string) int {
	switch code {
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF", "UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	}
	return 2
}

// find returns the record an identity points at (following merges), or "".
// recordType limits it to one type when set; only verified identities count
// then.
func (r *crmRun) find(ctx context.Context, tx pgx.Tx, namespace, value, recordType string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT coalesce(r.merged_into, r.id)::text FROM record_identities i JOIN records r ON r.id = i.record_id
		WHERE i.workspace_id = $1 AND i.namespace = $2 AND i.value = $3 AND i.valid_to IS NULL
		  AND ($4 = '' OR (r.record_type = $4 AND i.verified AND r.merged_into IS NULL))
		ORDER BY i.created_at LIMIT 1`, r.a.WorkspaceID, namespace, value, recordType).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// match finds the Outflare person holding one of the record's emails
// verified, or the company holding one of its domains verified.
func (r *crmRun) match(ctx context.Context, tx pgx.Tx, op *crmObjectPlan, rec connectors.CRMRecord) (string, error) {
	for slug, fp := range op.Fields {
		var normalize func(string) (string, error)
		switch {
		case fp.To == crmToEmail && op.Type == "person":
			normalize = normalizeEmail
		case fp.To == crmToDomain && op.Type == "account":
			normalize = normalizeDomain
		default:
			continue
		}
		for _, v := range rec.Values[slug] {
			s, _ := v.(string)
			n, err := normalize(s)
			if err != nil {
				continue
			}
			id, err := r.find(ctx, tx, fp.To, n, op.Type)
			if id != "" || err != nil {
				return id, err
			}
		}
	}
	return "", nil
}

// add creates an empty record for a CRM record, with its CRM id.
func (r *crmRun) add(ctx context.Context, tx pgx.Tx, recordType, crmID string) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, origin, acquired_via, created_by) VALUES ($1, $2, 'import', $4, $3) RETURNING id::text`,
		r.a.WorkspaceID, recordType, nullIfEmpty(r.a.ActorID), r.o.Integration.Connector).Scan(&id); err != nil {
		return "", err
	}
	_, err := tx.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, verified_at, source, created_by)
		VALUES ($1, $2, $3, $4, true, now(), $5, $6)`, r.a.WorkspaceID, id, r.identityNamespace(), crmID, r.o.Connector.Definition().ID, nullIfEmpty(r.a.ActorID))
	return id, err
}

// reference returns the Outflare record for a CRM record another record
// points at. One not imported yet gets an empty placeholder, which its own
// import fills in later.
func (r *crmRun) reference(ctx context.Context, tx pgx.Tx, sum *crmImportSummary, recordType, crmID string) (string, error) {
	id, err := r.find(ctx, tx, r.identityNamespace(), crmID, "")
	if id != "" || err != nil {
		return id, err
	}
	sum.Placeholders++
	return r.add(ctx, tx, recordType, crmID)
}

// identity adds an identity to a record unless it has it. An email or phone
// from a CRM is unverified (nobody proved they own it). A domain or CRM id
// is trusted, unless another record already holds it verified, when it is
// added unverified.
func (r *crmRun) identity(ctx context.Context, tx pgx.Tx, sum *crmImportSummary, label, recordID, namespace, value string, verified bool) error {
	var err error
	switch namespace {
	case crmToEmail:
		value, err = normalizeEmail(value)
	case crmToPhone:
		value, err = normalizePhone(value)
	case crmToDomain:
		value, err = normalizeDomain(value)
	}
	if err != nil || value == "" || len(value) > 500 {
		sum.warn("%s: a %s that is not valid was left out", label, namespace)
		return nil
	}
	if verified {
		var conflict recordConflict
		if err := claimFree(ctx, tx, r.a, recordID, namespace, value); errors.As(err, &conflict) {
			verified = false
		} else if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, verified_at, source, created_by)
		VALUES ($1, $2, $3, $4, $5, CASE WHEN $5 THEN now() END, $6, $7)
		ON CONFLICT (record_id, namespace, value) WHERE valid_to IS NULL DO NOTHING`,
		r.a.WorkspaceID, recordID, namespace, value, verified, r.o.Connector.Definition().ID, nullIfEmpty(r.a.ActorID))
	return err
}
