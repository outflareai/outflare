package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// The catalog: the record types, fields, option lists and events a
// workspace's customer records use. It is the one definition of each fact
// for every feature (the customer data platform, the CRM, imports, list
// filters, enrichment, merge tags) and what agents read to learn what data
// exists. Built-in types, fields and option lists live here in code; a
// workspace's own are rows on top.

func (s *Server) routesCatalog(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCatalogManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/catalog", s.inBrand(read, s.getCatalog))
	mux.HandleFunc("GET /v1/workspaces/{ws}/catalog/types", s.inBrand(read, s.listRecordTypes))
	mux.HandleFunc("POST /v1/workspaces/{ws}/catalog/types", s.inWorkspace(manage, s.createRecordType))
	mux.HandleFunc("GET /v1/workspaces/{ws}/catalog/types/{type}", s.inBrand(read, s.getRecordType))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/catalog/types/{type}", s.inWorkspace(manage, s.updateRecordType))
	mux.HandleFunc("POST /v1/workspaces/{ws}/catalog/types/{type}/fields", s.inWorkspace(manage, s.createField))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/catalog/types/{type}/fields/{field}", s.inWorkspace(manage, s.updateField))
	mux.HandleFunc("GET /v1/workspaces/{ws}/catalog/option-sets", s.inBrand(read, s.listOptionSets))
	mux.HandleFunc("POST /v1/workspaces/{ws}/catalog/option-sets", s.inWorkspace(manage, s.createOptionSet))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/catalog/option-sets/{set}", s.inWorkspace(manage, s.updateOptionSet))
	mux.HandleFunc("GET /v1/workspaces/{ws}/catalog/events", s.inBrand(read, s.listEventDefinitions))
	mux.HandleFunc("POST /v1/workspaces/{ws}/catalog/events", s.inWorkspace(manage, s.createEventDefinition))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/catalog/events/{event}", s.inWorkspace(manage, s.updateEventDefinition))
}

var (
	catalogKey    = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}$`)
	fieldKeyShape = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)
	optionKey     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_]{0,62}$`)
	eventName     = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)

	valueTypes    = []string{"text", "number", "currency", "date", "timestamp", "checkbox", "select", "status", "rating", "email", "phone", "url", "location", "record_reference", "actor_reference"}
	multiTypes    = []string{"text", "select", "email", "phone", "url", "record_reference", "actor_reference"}
	writePolicies = []string{"source_owned", "platform_owned", "manual_override_allowed", "computed"}
	sensitivities = []string{"public", "internal", "confidential", "restricted"}
	sourceKinds   = []string{"manual", "first_party", "crm_sync", "enrichment", "import"}
	eventStatuses = []string{"quarantined", "approved", "deprecated"}
	// Keys a field may not take: they name the record itself.
	reservedFieldKeys = []string{"id", "name", "type", "record_type", "brand_id", "origin", "version", "identities", "attributes", "created_at", "updated_at", "archived", "archived_at"}
)

type catalogOption struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Archived bool   `json:"archived"`
}

type optionSetView struct {
	Key     string          `json:"key"`
	Name    string          `json:"name"`
	Builtin bool            `json:"builtin"`
	Options []catalogOption `json:"options"`
}

type fieldView struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	ValueType   string   `json:"value_type"`
	OptionSet   *string  `json:"option_set"`
	Multiple    bool     `json:"multiple"`
	TargetType  *string  `json:"target_type"`
	WritePolicy string   `json:"write_policy"`
	Precedence  []string `json:"precedence"`
	Sensitivity string   `json:"sensitivity"`
	Filterable  bool     `json:"filterable"`
	Builtin     bool     `json:"builtin"`
	Archived    bool     `json:"archived"`
}

type recordTypeView struct {
	Key         string      `json:"key"`
	Name        string      `json:"name"`
	Plural      string      `json:"plural"`
	Description string      `json:"description"`
	Builtin     bool        `json:"builtin"`
	Archived    bool        `json:"archived"`
	Fields      []fieldView `json:"fields"`
}

type eventDefinitionView struct {
	Name        string          `json:"name"`
	Label       string          `json:"label"`
	Description string          `json:"description"`
	Properties  []eventProperty `json:"properties"`
	Status      string          `json:"status"`
	Sensitivity string          `json:"sensitivity"`
	FirstSeenAt *time.Time      `json:"first_seen_at"`
	LastSeenAt  *time.Time      `json:"last_seen_at"`
	SeenCount   int64           `json:"seen_count"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type eventProperty struct {
	Key         string `json:"key"`
	ValueType   string `json:"value_type"`
	Description string `json:"description"`
}

// ---- Built in ----

var defaultPrecedence = []string{"manual", "first_party", "crm_sync", "enrichment"}

func builtinField(key, label, valueType, set, target string, multiple, filterable bool, description string) fieldView {
	f := fieldView{Key: key, Label: label, Description: description, ValueType: valueType, Multiple: multiple, WritePolicy: "platform_owned",
		Precedence: defaultPrecedence, Sensitivity: "internal", Filterable: filterable, Builtin: true}
	if set != "" {
		f.OptionSet = &set
	}
	if target != "" {
		f.TargetType = &target
	}
	return f
}

// computedField is a built-in field Outflare works out itself, such as
// revenue from the workspace's payment connections. Nobody writes it
// through the API.
func computedField(key, label, valueType, set string, filterable bool, description string) fieldView {
	f := builtinField(key, label, valueType, set, "", false, filterable, description)
	f.WritePolicy = "computed"
	return f
}

// billingFields come from payment connections (Stripe), on the person or
// company that pays.
var billingFields = []fieldView{
	computedField("billing_plan", "Plan", "text", "", true, "The plan they pay for: the product of their largest subscription."),
	computedField("mrr", "Monthly revenue", "currency", "", true, "What their active subscriptions bring in a month, before discounts and tax."),
	computedField("lifetime_revenue", "Lifetime revenue", "currency", "", true, "Everything they have paid, less refunds."),
	computedField("customer_since", "Customer since", "timestamp", "", true, "Their first payment."),
	computedField("billing_status", "Billing status", "select", "billing_status", true, "Trialing, active, past due or canceled, from their subscriptions."),
}

// lateBuiltins are built-in field keys added after workspaces could make
// their own fields, so a workspace may already have a field of that key.
// Where it does, its own field wins and the built-in is left out.
var lateBuiltins = func() []string {
	keys := make([]string, 0, len(billingFields)+len(touchFields))
	for _, f := range slices.Concat(billingFields, touchFields) {
		keys = append(keys, f.Key)
	}
	return keys
}()

// lateOptionSets are built-in option sets added the same way.
var lateOptionSets = []string{"billing_status"}

// builtinTypes are the record types every workspace has. A person's emails,
// phones and ids are identities, not fields, so one email is never stored
// twice.
var builtinTypes = []recordTypeView{
	{Key: "person", Name: "Person", Plural: "People", Builtin: true,
		Description: "Someone: a user of your product, a customer, a contact or a prospect. Emails, phones and ids are the person's identities.",
		Fields: slices.Concat([]fieldView{
			builtinField("first_name", "First name", "text", "", "", false, false, ""),
			builtinField("last_name", "Last name", "text", "", "", false, false, ""),
			builtinField("job_title", "Job title", "text", "", "", false, false, ""),
			builtinField("seniority", "Seniority", "select", "seniority", "", false, true, ""),
			builtinField("companies", "Companies", "record_reference", "", "account", true, true, "The companies the person works at or belongs to."),
			builtinField("country", "Country", "select", "country", "", false, true, ""),
			builtinField("city", "City", "text", "", "", false, false, ""),
			builtinField("timezone", "Time zone", "text", "", "", false, false, "An IANA time zone such as Australia/Sydney, for send times."),
			builtinField("linkedin_url", "LinkedIn", "url", "", "", false, false, ""),
			builtinField("lifecycle_stage", "Lifecycle stage", "status", "lifecycle_stage", "", false, true, ""),
			billingFields[0], billingFields[1], billingFields[2], billingFields[3], billingFields[4],
		}, touchFields)},
	{Key: "account", Name: "Company", Plural: "Companies", Builtin: true,
		Description: "A company or other organization: a customer account, a prospect company, a partner. Its domains are identities.",
		Fields: []fieldView{
			builtinField("website", "Website", "url", "", "", false, false, ""),
			builtinField("industry", "Industry", "select", "industry", "", false, true, ""),
			builtinField("company_size", "Company size", "select", "company_size", "", false, true, "Employees."),
			builtinField("country", "Country", "select", "country", "", false, true, "Where the company is based."),
			builtinField("linkedin_url", "LinkedIn", "url", "", "", false, false, ""),
			builtinField("lifecycle_stage", "Lifecycle stage", "status", "lifecycle_stage", "", false, true, ""),
			billingFields[0], billingFields[1], billingFields[2], billingFields[3], billingFields[4],
		}},
}

func opts(pairs ...string) []catalogOption {
	out := make([]catalogOption, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, catalogOption{Key: pairs[i], Label: pairs[i+1]})
	}
	return out
}

// builtinOptionSets are the shared choices every workspace has, so the
// industry or country of a lead, a CRM company, an import and a project's
// ideal customer all use the same values.
var builtinOptionSets = func() []optionSetView {
	c := make([]catalogOption, 0, len(countries))
	for _, p := range countries {
		c = append(c, catalogOption{Key: p[0], Label: p[1]})
	}
	return []optionSetView{
		{Key: "country", Name: "Country", Builtin: true, Options: c},
		{Key: "industry", Name: "Industry", Builtin: true, Options: opts(
			"software", "Software and SaaS", "internet", "Internet and online services", "ecommerce", "Ecommerce and retail",
			"marketing", "Marketing and advertising", "media", "Media and publishing", "gaming", "Games", "fintech", "Financial services and fintech",
			"crypto", "Crypto and web3", "telecom", "Telecommunications", "infrastructure", "Cloud and infrastructure", "security", "Security",
			"ai", "AI and data", "education", "Education", "healthcare", "Healthcare", "real_estate", "Real estate", "travel", "Travel and hospitality",
			"manufacturing", "Manufacturing", "logistics", "Logistics and transport", "energy", "Energy", "professional_services", "Professional services",
			"agency", "Agencies", "consumer_goods", "Consumer goods", "nonprofit", "Nonprofit", "government", "Government", "other", "Other")},
		{Key: "company_size", Name: "Company size", Builtin: true, Options: opts(
			"1", "Just one", "2_10", "2 to 10", "11_50", "11 to 50", "51_200", "51 to 200", "201_500", "201 to 500", "501_1000", "501 to 1,000",
			"1001_5000", "1,001 to 5,000", "5001_10000", "5,001 to 10,000", "10001_plus", "More than 10,000")},
		{Key: "seniority", Name: "Seniority", Builtin: true, Options: opts(
			"owner", "Owner or founder", "c_suite", "C-suite", "partner", "Partner", "vp", "Vice president", "head", "Head of",
			"director", "Director", "manager", "Manager", "senior", "Senior", "entry", "Entry level", "intern", "Intern")},
		{Key: "lifecycle_stage", Name: "Lifecycle stage", Builtin: true, Options: opts(
			"prospect", "Prospect", "lead", "Lead", "signed_up", "Signed up", "trial", "Trial", "customer", "Customer",
			"churned", "Churned", "other", "Other")},
		{Key: "billing_status", Name: "Billing status", Builtin: true, Options: opts(
			"trialing", "Trialing", "active", "Active", "past_due", "Past due", "canceled", "Canceled")},
	}
}()

// ---- Loading ----

// catalog is a workspace's whole catalog: the built-ins plus its own.
type catalog struct {
	Types      []recordTypeView      `json:"types"`
	OptionSets []optionSetView       `json:"option_sets"`
	Events     []eventDefinitionView `json:"events"`
}

func (c *catalog) recordType(key string) *recordTypeView {
	for i := range c.Types {
		if c.Types[i].Key == key {
			return &c.Types[i]
		}
	}
	return nil
}

func (c *catalog) optionSet(key string) *optionSetView {
	for i := range c.OptionSets {
		if c.OptionSets[i].Key == key {
			return &c.OptionSets[i]
		}
	}
	return nil
}

func (t *recordTypeView) field(key string) *fieldView {
	for i := range t.Fields {
		if t.Fields[i].Key == key {
			return &t.Fields[i]
		}
	}
	return nil
}

// loadCatalog reads the workspace's catalog. Events are left out unless
// withEvents, since record writes do not need them.
func loadCatalog(ctx context.Context, q pgx.Tx, ws string, withEvents bool) (*catalog, error) {
	c := &catalog{}
	for _, t := range builtinTypes {
		t.Fields = slices.Clone(t.Fields)
		c.Types = append(c.Types, t)
	}
	rows, err := q.Query(ctx, `SELECT key, name, plural, description, archived_at IS NOT NULL FROM record_types WHERE workspace_id = $1 ORDER BY created_at, key`, ws)
	if err != nil {
		return nil, err
	}
	custom, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (recordTypeView, error) {
		var t recordTypeView
		err := row.Scan(&t.Key, &t.Name, &t.Plural, &t.Description, &t.Archived)
		t.Fields = []fieldView{}
		return t, err
	})
	if err != nil {
		return nil, err
	}
	c.Types = append(c.Types, custom...)
	rows, err = q.Query(ctx, `
		SELECT record_type, key, label, description, value_type, option_set, multiple, target_type, write_policy, precedence,
		       sensitivity, filterable, archived_at IS NOT NULL
		FROM field_definitions WHERE workspace_id = $1 ORDER BY created_at, key`, ws)
	if err != nil {
		return nil, err
	}
	type typedField struct {
		recordType string
		fieldView
	}
	fields, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (typedField, error) {
		var f typedField
		err := row.Scan(&f.recordType, &f.Key, &f.Label, &f.Description, &f.ValueType, &f.OptionSet, &f.Multiple, &f.TargetType,
			&f.WritePolicy, &f.Precedence, &f.Sensitivity, &f.Filterable, &f.Archived)
		return f, err
	})
	if err != nil {
		return nil, err
	}
	for _, f := range fields {
		if t := c.recordType(f.recordType); t != nil {
			// A workspace field made before a built-in of the same key
			// existed keeps its place: the built-in steps aside for it.
			if slices.Contains(lateBuiltins, f.Key) {
				t.Fields = slices.DeleteFunc(t.Fields, func(b fieldView) bool { return b.Builtin && b.Key == f.Key })
			}
			t.Fields = append(t.Fields, f.fieldView)
		}
	}
	c.OptionSets = slices.Clone(builtinOptionSets)
	rows, err = q.Query(ctx, `SELECT key, name, options FROM option_sets WHERE workspace_id = $1 ORDER BY created_at, key`, ws)
	if err != nil {
		return nil, err
	}
	sets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (optionSetView, error) {
		var o optionSetView
		err := row.Scan(&o.Key, &o.Name, &o.Options)
		return o, err
	})
	if err != nil {
		return nil, err
	}
	for _, o := range sets {
		// Likewise a workspace's own set: the built-in set steps aside, and
		// so do the built-in fields that use it, since their values would
		// not fit the workspace's options.
		if slices.Contains(lateOptionSets, o.Key) {
			c.OptionSets = slices.DeleteFunc(c.OptionSets, func(b optionSetView) bool { return b.Builtin && b.Key == o.Key })
			for i := range c.Types {
				c.Types[i].Fields = slices.DeleteFunc(c.Types[i].Fields, func(f fieldView) bool {
					return f.Builtin && f.OptionSet != nil && *f.OptionSet == o.Key
				})
			}
		}
	}
	c.OptionSets = append(c.OptionSets, sets...)
	c.Events = []eventDefinitionView{}
	if withEvents {
		if c.Events, err = loadEventDefinitions(ctx, q, ws, nil, ""); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func loadEventDefinitions(ctx context.Context, q pgx.Tx, ws string, name *string, status string) ([]eventDefinitionView, error) {
	rows, err := q.Query(ctx, `
		SELECT name, label, description, properties, status, sensitivity, first_seen_at, last_seen_at, seen_count, created_at, updated_at
		FROM event_definitions WHERE workspace_id = $1 AND ($2::text IS NULL OR name = $2) AND ($3 = '' OR status = $3)
		ORDER BY name`, ws, name, status)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (eventDefinitionView, error) {
		var e eventDefinitionView
		err := row.Scan(&e.Name, &e.Label, &e.Description, &e.Properties, &e.Status, &e.Sensitivity, &e.FirstSeenAt, &e.LastSeenAt,
			&e.SeenCount, &e.CreatedAt, &e.UpdatedAt)
		return e, err
	})
}

// readCatalog loads the catalog in its own read-only transaction.
func (s *Server) readCatalog(ctx context.Context, ws string, withEvents bool) (*catalog, error) {
	var c *catalog
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		c, err = loadCatalog(ctx, tx, ws, withEvents)
		return err
	})
	return c, err
}

// ---- Handlers ----

func (s *Server) getCatalog(w http.ResponseWriter, r *http.Request) {
	c, err := s.readCatalog(r.Context(), accessFrom(r.Context()).WorkspaceID, true)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if r.URL.Query().Get("archived") != "true" {
		c.hideArchived()
	}
	writeJSON(w, http.StatusOK, c)
}

// hideArchived drops archived types, fields and options, for readers that
// only need what is in use.
func (c *catalog) hideArchived() {
	c.Types = slices.DeleteFunc(c.Types, func(t recordTypeView) bool { return t.Archived })
	for i := range c.Types {
		c.Types[i].Fields = slices.DeleteFunc(c.Types[i].Fields, func(f fieldView) bool { return f.Archived })
	}
	for i := range c.OptionSets {
		c.OptionSets[i].Options = slices.DeleteFunc(slices.Clone(c.OptionSets[i].Options), func(o catalogOption) bool { return o.Archived })
	}
}

func (s *Server) listRecordTypes(w http.ResponseWriter, r *http.Request) {
	c, err := s.readCatalog(r.Context(), accessFrom(r.Context()).WorkspaceID, false)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if r.URL.Query().Get("archived") != "true" {
		c.hideArchived()
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": c.Types})
}

// typeFor loads the {type} path value's record type, or writes a 404.
func (s *Server) typeFor(w http.ResponseWriter, r *http.Request) (*catalog, *recordTypeView, bool) {
	c, err := s.readCatalog(r.Context(), accessFrom(r.Context()).WorkspaceID, false)
	if err != nil {
		s.internal(w, r, err)
		return nil, nil, false
	}
	t := c.recordType(r.PathValue("type"))
	if t == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "record type not found: types are person, account or one GET .../catalog/types lists")
		return nil, nil, false
	}
	return c, t, true
}

func (s *Server) getRecordType(w http.ResponseWriter, r *http.Request) {
	if _, t, ok := s.typeFor(w, r); ok {
		writeJSON(w, http.StatusOK, t)
	}
}

type recordTypeInput struct {
	Key         *string `json:"key"`
	Name        *string `json:"name"`
	Plural      *string `json:"plural"`
	Description *string `json:"description"`
	Archived    *bool   `json:"archived"`
}

func (in *recordTypeInput) validate() error {
	for _, f := range []struct {
		v    *string
		name string
		max  int
	}{{in.Name, "name", 60}, {in.Plural, "plural", 60}, {in.Description, "description", 1000}} {
		if f.v == nil {
			continue
		}
		*f.v = strings.TrimSpace(*f.v)
		if (*f.v == "" && f.name != "description") || len(*f.v) > f.max {
			return errInvalid(fmt.Sprintf("%s must be 1 to %d characters", f.name, f.max))
		}
	}
	return nil
}

func (s *Server) createRecordType(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req recordTypeInput
	if !decode(w, r, &req) {
		return
	}
	if req.Archived != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "archived is only for updates")
		return
	}
	if req.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required, such as Deal")
		return
	}
	if err := req.validate(); s.allocError(w, r, err) {
		return
	}
	key := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(*req.Name), "_"), "_")
	if req.Key != nil {
		key = strings.TrimSpace(*req.Key)
	}
	if !catalogKey.MatchString(key) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "key must be 2 to 40 lowercase letters, numbers and underscores, starting with a letter, such as deal")
		return
	}
	plural := *req.Name + "s"
	if req.Plural != nil {
		plural = *req.Plural
	}
	description := ""
	if req.Description != nil {
		description = *req.Description
	}
	ctx := r.Context()
	var view *recordTypeView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, t := range builtinTypes {
			if t.Key == key {
				return errInvalid(key + " is a built-in record type")
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO record_types (workspace_id, key, name, plural, description, created_by) VALUES ($1, $2, $3, $4, $5, $6)`,
			a.WorkspaceID, key, *req.Name, plural, description, a.ActorID); err != nil {
			if isUnique(err) {
				return errInvalid("a record type with key " + key + " already exists")
			}
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "record_type.created", "record_type", key, map[string]any{}); err != nil {
			return err
		}
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, false)
		if err == nil {
			view = c.recordType(key)
		}
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) updateRecordType(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req recordTypeInput
	if !decode(w, r, &req) {
		return
	}
	if req.Key != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a record type's key never changes; change its name instead")
		return
	}
	if err := req.validate(); s.allocError(w, r, err) {
		return
	}
	_, t, ok := s.typeFor(w, r)
	if !ok {
		return
	}
	if t.Builtin {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "built-in record types cannot be changed; add fields to them instead")
		return
	}
	ctx := r.Context()
	var view *recordTypeView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var archivedAt any = "keep"
		if req.Archived != nil {
			archivedAt = *req.Archived
		}
		if _, err := tx.Exec(ctx, `
			UPDATE record_types SET name = COALESCE($3, name), plural = COALESCE($4, plural), description = COALESCE($5, description),
				archived_at = CASE WHEN $6::text = 'true' THEN COALESCE(archived_at, now()) WHEN $6::text = 'false' THEN NULL ELSE archived_at END,
				updated_at = now()
			WHERE workspace_id = $1 AND key = $2`, a.WorkspaceID, t.Key, req.Name, req.Plural, req.Description, fmt.Sprint(archivedAt)); err != nil {
			return err
		}
		action := "record_type.updated"
		if req.Archived != nil && *req.Archived != t.Archived {
			action = map[bool]string{true: "record_type.archived", false: "record_type.restored"}[*req.Archived]
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "record_type", t.Key, map[string]any{}); err != nil {
			return err
		}
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, false)
		if err == nil {
			view = c.recordType(t.Key)
		}
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, view)
}

type fieldInput struct {
	Key         *string   `json:"key"`
	Label       *string   `json:"label"`
	Description *string   `json:"description"`
	ValueType   *string   `json:"value_type"`
	OptionSet   *string   `json:"option_set"`
	Multiple    *bool     `json:"multiple"`
	TargetType  *string   `json:"target_type"`
	WritePolicy *string   `json:"write_policy"`
	Precedence  *[]string `json:"precedence"`
	Sensitivity *string   `json:"sensitivity"`
	Filterable  *bool     `json:"filterable"`
	Archived    *bool     `json:"archived"`
}

// validateCommon checks the fields that both creating and updating a field
// may set.
func (in *fieldInput) validateCommon() error {
	if in.Label != nil {
		*in.Label = strings.TrimSpace(*in.Label)
		if *in.Label == "" || len(*in.Label) > 80 {
			return errInvalid("label must be 1 to 80 characters")
		}
	}
	if in.Description != nil {
		*in.Description = strings.TrimSpace(*in.Description)
		if len(*in.Description) > 1000 {
			return errInvalid("description is at most 1000 characters")
		}
	}
	if in.WritePolicy != nil && !slices.Contains(writePolicies, *in.WritePolicy) {
		return errInvalid("write_policy is one of " + strings.Join(writePolicies, ", "))
	}
	if in.Sensitivity != nil && !slices.Contains(sensitivities, *in.Sensitivity) {
		return errInvalid("sensitivity is one of " + strings.Join(sensitivities, ", "))
	}
	if in.Precedence != nil {
		seen := []string{}
		for _, p := range *in.Precedence {
			if !slices.Contains(sourceKinds, p) || slices.Contains(seen, p) {
				return errInvalid("precedence lists sources once each, first wins: " + strings.Join(sourceKinds, ", "))
			}
			seen = append(seen, p)
		}
		if len(seen) == 0 {
			return errInvalid("precedence needs at least one source")
		}
	}
	return nil
}

func (s *Server) createField(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req fieldInput
	if !decode(w, r, &req) {
		return
	}
	if req.Archived != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "archived is only for updates")
		return
	}
	if req.Label == nil || req.ValueType == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "label and value_type are required")
		return
	}
	if err := req.validateCommon(); s.allocError(w, r, err) {
		return
	}
	c, t, ok := s.typeFor(w, r)
	if !ok {
		return
	}
	key := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(*req.Label), "_"), "_")
	if req.Key != nil {
		key = strings.TrimSpace(*req.Key)
	}
	vt := *req.ValueType
	multiple := req.Multiple != nil && *req.Multiple
	err := func() error {
		switch {
		case !fieldKeyShape.MatchString(key):
			return errInvalid("key must be 2 to 63 lowercase letters, numbers and underscores, starting with a letter, such as plan_tier")
		case slices.Contains(reservedFieldKeys, key):
			return errInvalid(key + " is reserved for the record itself")
		case t.field(key) != nil:
			return errInvalid(t.Name + " already has a field " + key)
		case !slices.Contains(valueTypes, vt):
			return errInvalid("value_type is one of " + strings.Join(valueTypes, ", "))
		case multiple && !slices.Contains(multiTypes, vt):
			return errInvalid("multiple is for " + strings.Join(multiTypes, ", ") + " fields")
		}
		needsSet := vt == "select" || vt == "status"
		if needsSet != (req.OptionSet != nil) {
			if needsSet {
				return errInvalid("a " + vt + " field needs option_set: a key from GET .../catalog/option-sets, such as industry")
			}
			return errInvalid("option_set is only for select and status fields")
		}
		if needsSet && c.optionSet(*req.OptionSet) == nil {
			return errInvalid("option_set " + *req.OptionSet + " does not exist; create it with POST .../catalog/option-sets")
		}
		if (vt == "record_reference") != (req.TargetType != nil) {
			if vt == "record_reference" {
				return errInvalid("a record_reference field needs target_type, such as account")
			}
			return errInvalid("target_type is only for record_reference fields")
		}
		if req.TargetType != nil {
			if tt := c.recordType(*req.TargetType); tt == nil || tt.Archived {
				return errInvalid("target_type " + *req.TargetType + " is not a record type")
			}
		}
		return nil
	}()
	if s.allocError(w, r, err) {
		return
	}
	policy, sensitivity, precedence := "platform_owned", "internal", defaultPrecedence
	if req.WritePolicy != nil {
		policy = *req.WritePolicy
	}
	if req.Sensitivity != nil {
		sensitivity = *req.Sensitivity
	}
	if req.Precedence != nil {
		precedence = *req.Precedence
	}
	description := ""
	if req.Description != nil {
		description = *req.Description
	}
	ctx := r.Context()
	var view *fieldView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO field_definitions (workspace_id, record_type, key, label, description, value_type, option_set, multiple, target_type,
				write_policy, precedence, sensitivity, filterable, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			a.WorkspaceID, t.Key, key, *req.Label, description, vt, req.OptionSet, multiple, req.TargetType, policy, precedence, sensitivity,
			req.Filterable != nil && *req.Filterable, a.ActorID); err != nil {
			if isUnique(err) {
				return errInvalid(t.Name + " already has a field " + key)
			}
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "field.created", "field", t.Key+"."+key, map[string]any{
			"record_type": t.Key, "value_type": vt, "multiple": multiple,
		}); err != nil {
			return err
		}
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, false)
		if err == nil {
			view = c.recordType(t.Key).field(key)
		}
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) updateField(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req fieldInput
	if !decode(w, r, &req) {
		return
	}
	if req.Key != nil || req.ValueType != nil || req.OptionSet != nil || req.Multiple != nil || req.TargetType != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a field's key, value_type, option_set, multiple and target_type never change; add a new field instead")
		return
	}
	if err := req.validateCommon(); s.allocError(w, r, err) {
		return
	}
	_, t, ok := s.typeFor(w, r)
	if !ok {
		return
	}
	f := t.field(r.PathValue("field"))
	if f == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "field not found")
		return
	}
	if f.Builtin {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "built-in fields cannot be changed")
		return
	}
	ctx := r.Context()
	var view *fieldView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		archived := "keep"
		if req.Archived != nil {
			archived = fmt.Sprint(*req.Archived)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE field_definitions SET label = COALESCE($4, label), description = COALESCE($5, description),
				write_policy = COALESCE($6, write_policy), precedence = COALESCE($7, precedence), sensitivity = COALESCE($8, sensitivity),
				filterable = COALESCE($9, filterable),
				archived_at = CASE WHEN $10 = 'true' THEN COALESCE(archived_at, now()) WHEN $10 = 'false' THEN NULL ELSE archived_at END,
				updated_at = now()
			WHERE workspace_id = $1 AND record_type = $2 AND key = $3`,
			a.WorkspaceID, t.Key, f.Key, req.Label, req.Description, req.WritePolicy, req.Precedence, req.Sensitivity, req.Filterable, archived); err != nil {
			return err
		}
		action := "field.updated"
		if req.Archived != nil && *req.Archived != f.Archived {
			action = map[bool]string{true: "field.archived", false: "field.restored"}[*req.Archived]
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "field", t.Key+"."+f.Key, map[string]any{"record_type": t.Key}); err != nil {
			return err
		}
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, false)
		if err == nil {
			view = c.recordType(t.Key).field(f.Key)
		}
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// ---- Option lists ----

func (s *Server) listOptionSets(w http.ResponseWriter, r *http.Request) {
	c, err := s.readCatalog(r.Context(), accessFrom(r.Context()).WorkspaceID, false)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": c.OptionSets})
}

type optionSetInput struct {
	Key     *string          `json:"key"`
	Name    *string          `json:"name"`
	Options *[]catalogOption `json:"options"`
}

// mergeOptions applies a new list of options to the current one. Keys never
// go away: an option left out is archived, so records that use it stay valid.
func mergeOptions(current, next []catalogOption) ([]catalogOption, error) {
	if len(next) > 500 {
		return nil, errInvalid("an option list has at most 500 options")
	}
	out := []catalogOption{}
	seen := map[string]bool{}
	for _, o := range next {
		o.Key, o.Label = strings.TrimSpace(o.Key), strings.TrimSpace(o.Label)
		if o.Key == "" && o.Label != "" {
			o.Key = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(o.Label), "_"), "_")
		}
		if !optionKey.MatchString(o.Key) {
			return nil, errInvalid("option keys are letters, numbers and underscores, such as closed_won: " + o.Key + " is not")
		}
		if o.Label == "" {
			o.Label = o.Key
		}
		if len(o.Label) > 80 {
			return nil, errInvalid("option labels are at most 80 characters")
		}
		if seen[o.Key] {
			return nil, errInvalid("option " + o.Key + " is listed twice")
		}
		seen[o.Key] = true
		out = append(out, o)
	}
	for _, o := range current {
		if !seen[o.Key] {
			o.Archived = true
			out = append(out, o)
		}
	}
	return out, nil
}

func (s *Server) createOptionSet(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req optionSetInput
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil || strings.TrimSpace(*req.Name) == "" || len(*req.Name) > 60 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required, 1 to 60 characters, such as Deal stage")
		return
	}
	name := strings.TrimSpace(*req.Name)
	key := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "_"), "_")
	if req.Key != nil {
		key = strings.TrimSpace(*req.Key)
	}
	if !catalogKey.MatchString(key) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "key must be 2 to 40 lowercase letters, numbers and underscores, starting with a letter")
		return
	}
	for _, b := range builtinOptionSets {
		if b.Key == key {
			writeError(w, r, http.StatusBadRequest, "invalid_request", key+" is a built-in option list; use it as it is")
			return
		}
	}
	var options []catalogOption
	if req.Options != nil {
		var err error
		if options, err = mergeOptions(nil, *req.Options); s.allocError(w, r, err) {
			return
		}
	}
	if options == nil {
		options = []catalogOption{}
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO option_sets (workspace_id, key, name, options, created_by) VALUES ($1, $2, $3, $4, $5)`,
			a.WorkspaceID, key, name, options, a.ActorID); err != nil {
			if isUnique(err) {
				return errInvalid("an option list with key " + key + " already exists")
			}
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "option_set.created", "option_set", key, map[string]any{"options": len(options)})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, optionSetView{Key: key, Name: name, Options: options})
}

func (s *Server) updateOptionSet(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req optionSetInput
	if !decode(w, r, &req) {
		return
	}
	if req.Key != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "an option list's key never changes")
		return
	}
	if req.Name != nil && (strings.TrimSpace(*req.Name) == "" || len(*req.Name) > 60) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name must be 1 to 60 characters")
		return
	}
	key := r.PathValue("set")
	for _, b := range builtinOptionSets {
		if b.Key == key {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "built-in option lists cannot be changed")
			return
		}
	}
	ctx := r.Context()
	var view optionSetView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT key, name, options FROM option_sets WHERE workspace_id = $1 AND key = $2 FOR UPDATE`, a.WorkspaceID, key).
			Scan(&view.Key, &view.Name, &view.Options)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if req.Name != nil {
			view.Name = strings.TrimSpace(*req.Name)
		}
		if req.Options != nil {
			if view.Options, err = mergeOptions(view.Options, *req.Options); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE option_sets SET name = $3, options = $4, updated_at = now() WHERE workspace_id = $1 AND key = $2`,
			a.WorkspaceID, key, view.Name, view.Options); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "option_set.updated", "option_set", key, map[string]any{"options": len(view.Options)})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// ---- Event definitions ----

func (s *Server) listEventDefinitions(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && !slices.Contains(eventStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is quarantined, approved or deprecated")
		return
	}
	var list []eventDefinitionView
	err := pgx.BeginTxFunc(r.Context(), s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		list, err = loadEventDefinitions(r.Context(), tx, accessFrom(r.Context()).WorkspaceID, nil, status)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []eventDefinitionView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type eventDefinitionInput struct {
	Name        *string          `json:"name"`
	Label       *string          `json:"label"`
	Description *string          `json:"description"`
	Properties  *[]eventProperty `json:"properties"`
	Status      *string          `json:"status"`
	Sensitivity *string          `json:"sensitivity"`
}

func (in *eventDefinitionInput) validate() error {
	if in.Label != nil && len(strings.TrimSpace(*in.Label)) > 80 {
		return errInvalid("label is at most 80 characters")
	}
	if in.Description != nil && len(*in.Description) > 1000 {
		return errInvalid("description is at most 1000 characters")
	}
	if in.Status != nil && !slices.Contains(eventStatuses, *in.Status) {
		return errInvalid("status is quarantined, approved or deprecated")
	}
	if in.Sensitivity != nil && !slices.Contains(sensitivities, *in.Sensitivity) {
		return errInvalid("sensitivity is one of " + strings.Join(sensitivities, ", "))
	}
	if in.Properties != nil {
		if len(*in.Properties) > 200 {
			return errInvalid("an event has at most 200 properties")
		}
		seen := map[string]bool{}
		for i, p := range *in.Properties {
			p.Key = strings.TrimSpace(p.Key)
			if !fieldKeyShape.MatchString(p.Key) || seen[p.Key] {
				return errInvalid("property keys are unique lowercase letters, numbers and underscores, such as product_id")
			}
			if !slices.Contains(valueTypes, p.ValueType) {
				return errInvalid("a property's value_type is one of " + strings.Join(valueTypes, ", "))
			}
			seen[p.Key] = true
			(*in.Properties)[i] = p
		}
	}
	return nil
}

func (s *Server) createEventDefinition(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req eventDefinitionInput
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil || !eventName.MatchString(*req.Name) || len(*req.Name) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required: lowercase words joined by dots, such as product.activated")
		return
	}
	if err := req.validate(); s.allocError(w, r, err) {
		return
	}
	label, description, status, sensitivity, props := "", "", "approved", "internal", []eventProperty{}
	if req.Label != nil {
		label = strings.TrimSpace(*req.Label)
	}
	if req.Description != nil {
		description = strings.TrimSpace(*req.Description)
	}
	if req.Status != nil {
		status = *req.Status
	}
	if req.Sensitivity != nil {
		sensitivity = *req.Sensitivity
	}
	if req.Properties != nil {
		props = *req.Properties
	}
	ctx := r.Context()
	var view eventDefinitionView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO event_definitions (workspace_id, name, label, description, properties, status, sensitivity, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, a.WorkspaceID, *req.Name, label, description, props, status, sensitivity, a.ActorID); err != nil {
			if isUnique(err) {
				return errInvalid("event " + *req.Name + " is already defined; change it with PATCH")
			}
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "event_definition.created", "event_definition", *req.Name, map[string]any{
			"properties": len(props), "status": status,
		}); err != nil {
			return err
		}
		list, err := loadEventDefinitions(ctx, tx, a.WorkspaceID, req.Name, "")
		if err == nil && len(list) == 1 {
			view = list[0]
		}
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) updateEventDefinition(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req eventDefinitionInput
	if !decode(w, r, &req) {
		return
	}
	if req.Name != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "an event's name never changes; define a new event and deprecate this one")
		return
	}
	if err := req.validate(); s.allocError(w, r, err) {
		return
	}
	name := r.PathValue("event")
	ctx := r.Context()
	var view eventDefinitionView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var before string
		err := tx.QueryRow(ctx, `SELECT status FROM event_definitions WHERE workspace_id = $1 AND name = $2 FOR UPDATE`, a.WorkspaceID, name).Scan(&before)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE event_definitions SET label = COALESCE($3, label), description = COALESCE($4, description),
				properties = COALESCE($5, properties), status = COALESCE($6, status), sensitivity = COALESCE($7, sensitivity), updated_at = now()
			WHERE workspace_id = $1 AND name = $2`, a.WorkspaceID, name, req.Label, req.Description, req.Properties, req.Status, req.Sensitivity); err != nil {
			return err
		}
		action := "event_definition.updated"
		if req.Status != nil && *req.Status != before {
			action = "event_definition." + map[string]string{"approved": "approved", "quarantined": "quarantined", "deprecated": "deprecated"}[*req.Status]
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "event_definition", name, map[string]any{}); err != nil {
			return err
		}
		list, err := loadEventDefinitions(ctx, tx, a.WorkspaceID, &name, "")
		if err == nil && len(list) == 1 {
			view = list[0]
		}
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, view)
}
