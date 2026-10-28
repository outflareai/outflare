package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/playbooks"
)

// Setup kits: what a playbook needs set up before its first run (a CRM
// pipeline, fields on people and companies, email drafts), from the
// repository's playbooks/kits folder. The list is public. In a workspace,
// the preview says what applying it would add, reuse or extend, and what
// conflicts with what is there; applying it only adds, in one
// transaction, with the same rows and audit events as doing each step by
// hand.

func (s *Server) routesKits(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/kits", s.listKitCatalog)
	mux.HandleFunc("GET /v1/kits/{id}", s.getKitCatalog)
	mux.HandleFunc("GET /v1/workspaces/{ws}/kits/{id}/preview", s.inWorkspace(auth.ScopeCustomersRead, s.previewKit))
	mux.HandleFunc("POST /v1/workspaces/{ws}/kits/{id}/apply", s.inWorkspace(auth.ScopeCatalogManage, s.applyKitHandler))
}

// kitSource is where a kit's file lives on GitHub.
const kitSource = "https://github.com/outflareai/outflare/blob/main/playbooks/kits/%s.yaml"

type kitView struct {
	playbooks.Kit
	SourceURL string `json:"source_url"`
	// Playbooks that use the kit.
	Playbooks []string `json:"playbooks"`
}

func viewKit(k playbooks.Kit) kitView {
	v := kitView{Kit: k, SourceURL: fmt.Sprintf(kitSource, k.ID), Playbooks: []string{}}
	list, _ := playbooks.All()
	for _, p := range list {
		if p.Kit == k.ID {
			v.Playbooks = append(v.Playbooks, p.ID)
		}
	}
	return v
}

func (s *Server) listKitCatalog(w http.ResponseWriter, r *http.Request) {
	list, err := playbooks.Kits()
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]kitView, 0, len(list))
	for _, k := range list {
		out = append(out, viewKit(k))
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) getKitCatalog(w http.ResponseWriter, r *http.Request) {
	k, ok := playbooks.GetKit(r.PathValue("id"))
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "no kit "+r.PathValue("id")+"; GET /v1/kits lists them")
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, viewKit(k))
}

// What applying a kit does with each of its parts.
const (
	kitAdd      = "add"      // new
	kitExtend   = "extend"   // an option list of the workspace's own gains options
	kitReuse    = "reuse"    // already there and matching, left as it is
	kitConflict = "conflict" // there but different; the kit stops until it is sorted out
)

type kitItem struct {
	Kind   string `json:"kind"` // option_set, record_type, field or email_template
	Key    string `json:"key"`  // a field is type.field
	Label  string `json:"label"`
	Action string `json:"action"`
	Detail string `json:"detail"`
	// For apply: what to do.
	options  []catalogOption
	field    *playbooks.KitField
	onType   string
	ktype    *playbooks.KitType
	template *playbooks.KitTemplate
}

type kitPlan struct {
	KitID     string    `json:"kit_id"`
	Version   int       `json:"version"`
	Title     string    `json:"title"`
	Items     []kitItem `json:"items"`
	Changes   int       `json:"changes"`
	Conflicts int       `json:"conflicts"`
	Applied   bool      `json:"applied"`
}

func (p *kitPlan) add(it kitItem) {
	switch it.Action {
	case kitAdd, kitExtend:
		p.Changes++
	case kitConflict:
		p.Conflicts++
	}
	p.Items = append(p.Items, it)
}

// addsTemplates says whether applying the plan adds email drafts, which
// also needs emails.manage.
func (p *kitPlan) addsTemplates() bool {
	return slices.ContainsFunc(p.Items, func(it kitItem) bool { return it.Kind == "email_template" && it.Action == kitAdd })
}

// planKit compares kit k with the workspace's catalog and email templates.
func planKit(ctx context.Context, tx pgx.Tx, ws string, k playbooks.Kit) (*kitPlan, error) {
	c, err := loadCatalog(ctx, tx, ws, false)
	if err != nil {
		return nil, err
	}
	plan := &kitPlan{KitID: k.ID, Version: k.Version, Title: k.Title, Items: []kitItem{}}
	for _, ks := range k.OptionSets {
		it := kitItem{Kind: "option_set", Key: ks.Key, Label: ks.Name, Action: kitAdd}
		cur := c.optionSet(ks.Key)
		switch {
		case cur == nil:
			for _, o := range ks.Options {
				it.options = append(it.options, catalogOption{Key: o.Key, Label: o.Label})
			}
			it.Detail = countOf(len(ks.Options), "option", "options")
		case cur.Builtin:
			it.Action, it.Detail = kitConflict, ks.Key+" is a built-in option list"
		default:
			var missing []string
			it.options = slices.Clone(cur.Options)
			for _, o := range ks.Options {
				if !slices.ContainsFunc(cur.Options, func(have catalogOption) bool { return have.Key == o.Key }) {
					missing = append(missing, o.Label)
					it.options = append(it.options, catalogOption{Key: o.Key, Label: o.Label})
				}
			}
			if len(missing) == 0 {
				it.Action, it.Detail = kitReuse, "already there"
			} else {
				it.Action, it.Detail = kitExtend, "adds "+strings.Join(missing, ", ")
			}
		}
		plan.add(it)
	}
	for _, key := range k.OptionSetsUsed() {
		if c.optionSet(key) == nil {
			plan.add(kitItem{Kind: "option_set", Key: key, Label: key, Action: kitConflict, Detail: "the workspace has no option list " + key})
		}
	}
	fields := func(typeKey, typeName string, cur *recordTypeView, list []playbooks.KitField) {
		for i := range list {
			f := &list[i]
			it := kitItem{Kind: "field", Key: typeKey + "." + f.Key, Label: typeName + ": " + f.Label, Action: kitAdd, field: f, onType: typeKey}
			var have *fieldView
			if cur != nil {
				have = cur.field(f.Key)
			}
			switch {
			case have == nil:
				it.Detail = f.ValueType
			case have.Archived:
				it.Action, it.Detail = kitConflict, typeName+" has an archived field "+f.Key+"; restore or rename it first"
			case have.ValueType != f.ValueType || have.Multiple != f.Multiple || deref(have.OptionSet) != f.OptionSet || deref(have.TargetType) != f.TargetType:
				it.Action, it.Detail = kitConflict, fmt.Sprintf("%s already has a field %s that holds %s", typeName, f.Key, describeField(have))
			default:
				it.Action, it.Detail = kitReuse, "already there"
			}
			plan.add(it)
		}
	}
	for i := range k.Types {
		kt := &k.Types[i]
		it := kitItem{Kind: "record_type", Key: kt.Key, Label: kt.Plural, Action: kitAdd, ktype: kt}
		cur := c.recordType(kt.Key)
		switch {
		case cur == nil:
			it.Detail = countOf(len(kt.Fields), "field", "fields")
		case cur.Archived:
			it.Action, it.Detail = kitConflict, kt.Plural+" is archived; restore it first"
		default:
			it.Action, it.Detail = kitReuse, "already there"
		}
		plan.add(it)
		fields(kt.Key, kt.Name, cur, kt.Fields)
	}
	for _, on := range sortedFieldTypes(k.Fields) {
		t := c.recordType(on)
		name := on
		if t != nil {
			name = t.Name
		}
		fields(on, name, t, k.Fields[on])
	}
	for i := range k.Templates {
		kt := &k.Templates[i]
		it := kitItem{Kind: "email_template", Key: kt.Handle, Label: kt.Name, Action: kitAdd, template: kt, Detail: "a draft to read and publish"}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM email_templates WHERE workspace_id = $1 AND handle = $2)`, ws, kt.Handle).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			it.Action, it.Detail = kitReuse, "a template with this handle is already there; it stays as it is"
		}
		plan.add(it)
	}
	return plan, nil
}

func describeField(f *fieldView) string {
	s := f.ValueType
	if f.Multiple {
		s = "several " + s
	}
	if f.OptionSet != nil {
		s += " from " + *f.OptionSet
	}
	if f.TargetType != nil {
		s += " to " + *f.TargetType
	}
	return s
}

func sortedFieldTypes(m map[string][]playbooks.KitField) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// errKitConflict is a kit that conflicts with what the workspace has.
type errKitConflict string

func (e errKitConflict) Error() string { return string(e) }

// applyKit adds what kit k needs to workspace a.WorkspaceID, in tx. It
// stops with errKitConflict when anything conflicts, and errDenied when
// the kit adds email drafts the caller may not manage.
func applyKit(ctx context.Context, tx pgx.Tx, a *access, k playbooks.Kit) (*kitPlan, error) {
	// One kit at a time per workspace, so two people applying the same
	// kit don't both add it.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kit:' || $1::text))`, a.WorkspaceID); err != nil {
		return nil, err
	}
	plan, err := planKit(ctx, tx, a.WorkspaceID, k)
	if err != nil {
		return nil, err
	}
	if plan.Conflicts > 0 {
		var msgs []string
		for _, it := range plan.Items {
			if it.Action == kitConflict {
				msgs = append(msgs, it.Detail)
			}
		}
		return plan, errKitConflict("the kit " + k.ID + " conflicts with this workspace: " + strings.Join(msgs, "; "))
	}
	if plan.addsTemplates() && !a.has(auth.ScopeEmailsManage) {
		return plan, errDenied("this kit adds email drafts, which needs the emails.manage scope")
	}
	p := &a.principal
	ws := a.WorkspaceID
	// Option lists first, then types, then fields, which use both.
	for _, it := range plan.Items {
		if it.Kind != "option_set" || (it.Action != kitAdd && it.Action != kitExtend) {
			continue
		}
		if it.Action == kitAdd {
			if _, err := tx.Exec(ctx, `INSERT INTO option_sets (workspace_id, key, name, options, created_by) VALUES ($1, $2, $3, $4, $5)`,
				ws, it.Key, it.Label, it.options, a.ActorID); err != nil {
				return nil, err
			}
			if err := record(ctx, tx, p, ws, "option_set.created", "option_set", it.Key, map[string]any{"options": len(it.options), "kit_id": k.ID}); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE option_sets SET options = $3, updated_at = now() WHERE workspace_id = $1 AND key = $2`, ws, it.Key, it.options); err != nil {
			return nil, err
		}
		if err := record(ctx, tx, p, ws, "option_set.updated", "option_set", it.Key, map[string]any{"options": len(it.options), "kit_id": k.ID}); err != nil {
			return nil, err
		}
	}
	for _, it := range plan.Items {
		if it.Kind != "record_type" || it.Action != kitAdd {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO record_types (workspace_id, key, name, plural, description, created_by) VALUES ($1, $2, $3, $4, $5, $6)`,
			ws, it.ktype.Key, it.ktype.Name, it.ktype.Plural, it.ktype.Description, a.ActorID); err != nil {
			return nil, err
		}
		if err := record(ctx, tx, p, ws, "record_type.created", "record_type", it.ktype.Key, map[string]any{"kit_id": k.ID}); err != nil {
			return nil, err
		}
	}
	for _, it := range plan.Items {
		if it.Kind != "field" || it.Action != kitAdd {
			continue
		}
		f := it.field
		var set, target *string
		if f.OptionSet != "" {
			set = &f.OptionSet
		}
		if f.TargetType != "" {
			target = &f.TargetType
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO field_definitions (workspace_id, record_type, key, label, description, value_type, option_set, multiple, target_type,
				write_policy, precedence, sensitivity, filterable, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'platform_owned', $10, 'internal', $11, $12)`,
			ws, it.onType, f.Key, f.Label, f.Description, f.ValueType, set, f.Multiple, target, defaultPrecedence, f.Filterable, a.ActorID); err != nil {
			return nil, err
		}
		if err := record(ctx, tx, p, ws, "field.created", "field", it.Key, map[string]any{
			"record_type": it.onType, "value_type": f.ValueType, "multiple": f.Multiple, "kit_id": k.ID,
		}); err != nil {
			return nil, err
		}
	}
	for i, it := range plan.Items {
		if it.Kind != "email_template" || it.Action != kitAdd {
			continue
		}
		t := it.template
		vars, _ := json.Marshal(declare([]emails.Variable{}, t.Subject, "", t.Text))
		var id string
		err := tx.QueryRow(ctx, `
			INSERT INTO email_templates (workspace_id, handle, name, purpose, subject, html, text_body, variables)
			VALUES ($1, $2, $3, $4, $5, '', $6, $7)
			ON CONFLICT (workspace_id, handle) DO NOTHING RETURNING id`,
			ws, t.Handle, t.Name, t.Purpose, t.Subject, t.Text, vars).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			plan.Items[i].Action, plan.Items[i].Detail = kitReuse, "a template with this handle is already there; it stays as it is"
			plan.Changes--
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := record(ctx, tx, p, ws, "email_template.created", "email_template", id,
			map[string]any{"purpose": t.Purpose, "track_opens": false, "track_clicks": false, "topic_id": nil, "kit_id": k.ID}); err != nil {
			return nil, err
		}
	}
	count := map[string]int{}
	for _, it := range plan.Items {
		count[it.Action]++
	}
	data := map[string]any{"kit_id": k.ID, "kit_version": k.Version, "added": count[kitAdd], "extended": count[kitExtend], "reused": count[kitReuse]}
	if err := recordWithProps(ctx, tx, p, ws, "kit.applied", "kit", k.ID, data, data); err != nil {
		return nil, err
	}
	plan.Applied = true
	return plan, nil
}

func (s *Server) kitFor(w http.ResponseWriter, r *http.Request) (playbooks.Kit, bool) {
	k, ok := playbooks.GetKit(r.PathValue("id"))
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "no kit "+r.PathValue("id")+"; GET /v1/kits lists them")
	}
	return k, ok
}

func (s *Server) previewKit(w http.ResponseWriter, r *http.Request) {
	k, ok := s.kitFor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var plan *kitPlan
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		plan, err = planKit(ctx, tx, accessFrom(ctx).WorkspaceID, k)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) applyKitHandler(w http.ResponseWriter, r *http.Request) {
	k, ok := s.kitFor(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	ctx := r.Context()
	var plan *kitPlan
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		plan, err = applyKit(ctx, tx, a, k)
		return err
	})
	if s.kitError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// kitError writes err as a response and reports whether there was one.
func (s *Server) kitError(w http.ResponseWriter, r *http.Request, err error) bool {
	var conflict errKitConflict
	switch {
	case err == nil:
		return false
	case errors.As(err, &conflict):
		writeError(w, r, http.StatusConflict, "conflict", string(conflict))
	case errors.As(err, new(errDenied)):
		writeError(w, r, http.StatusForbidden, "forbidden", err.Error())
	default:
		s.internal(w, r, err)
	}
	return true
}
