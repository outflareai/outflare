package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Reply rules can move deals in Outflare's own CRM: a deal is a record of
// the type the rule names, its stage a status field, linked to the
// reply's person or company by its reference fields.

// replyDealSource is the source deal moves write with. A person's own
// choice (manual) outranks it, so a rule never overrides a person.
const replyDealSource = "first_party"

// dealShape is a deal rule checked against the workspace's catalog.
type dealShape struct {
	t      *recordTypeView
	stages []string // option keys in order, archived left out
	person string   // reference field to a person, if any
	acct   string   // reference field to a company, if any
}

func checkDealRule(c *catalog, d inbox.DealRule) (dealShape, error) {
	var out dealShape
	t := c.recordType(d.Type)
	if t == nil || t.Archived || t.Key == "person" || t.Key == "account" {
		return out, fmt.Errorf("deal.type must be an active record type for deals, such as deal (GET .../catalog/types)")
	}
	f := t.field(d.Field)
	if f == nil || f.Archived || f.ValueType != "status" || f.OptionSet == nil {
		return out, fmt.Errorf("deal.field must be a status field of %s that holds its stages", t.Key)
	}
	if f.WritePolicy == "computed" || f.WritePolicy == "source_owned" {
		return out, fmt.Errorf("deal.field %s can't be changed by Outflare", f.Key)
	}
	set := c.optionSet(*f.OptionSet)
	if set == nil {
		return out, fmt.Errorf("deal.field %s has no stages", f.Key)
	}
	for _, o := range set.Options {
		if !o.Archived {
			out.stages = append(out.stages, o.Key)
		}
	}
	for _, s := range append([]string{d.Stage}, d.Closed...) {
		if !slices.Contains(out.stages, s) {
			return out, fmt.Errorf("deal stage %q is not one of %s's stages (%v)", s, f.Key, out.stages)
		}
	}
	for _, rf := range t.Fields {
		if rf.Archived || rf.ValueType != "record_reference" || rf.TargetType == nil || rf.WritePolicy == "computed" {
			continue
		}
		if *rf.TargetType == "person" && out.person == "" {
			out.person = rf.Key
		}
		if *rf.TargetType == "account" && out.acct == "" {
			out.acct = rf.Key
		}
	}
	if out.person == "" && out.acct == "" {
		return out, fmt.Errorf("%s needs a field that points at a person or a company, so a reply can find its deal", t.Key)
	}
	out.t = t
	return out, nil
}

// checkDealRules checks every deal move in a rule set against the catalog.
func checkDealRules(ctx context.Context, tx pgx.Tx, ws string, rs inbox.RuleSet) error {
	var c *catalog
	for label, r := range rs {
		if r.Deal == nil {
			continue
		}
		if c == nil {
			var err error
			if c, err = loadCatalog(ctx, tx, ws, false); err != nil {
				return err
			}
		}
		if _, err := checkDealRule(c, *r.Deal); err != nil {
			return errInvalid(label + ": " + err.Error())
		}
	}
	return nil
}

// replyDealJob moves the deal for one labelled reply, if the reply still
// has the label, no review is pending and its rule still moves a deal.
func (s *Server) replyDealJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		InboundID string `json:"inbound_id"`
		Category  string `json:"category"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return fmt.Errorf("%w: bad deal job", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	// Moves are inbox.rule_applied (action deal), recorded with the change;
	// a rule that can't run or fails is the same event as a failure.
	ev := track.Event{Name: "inbox.rule_applied", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"category": p.Category, "action": "deal"}}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var cat, recordID, sequenceID *string
		var review bool
		err := tx.QueryRow(ctx, `SELECT category, needs_review, record_id::text, sequence_id::text
			FROM inbound_messages WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, p.InboundID, ws).
			Scan(&cat, &review, &recordID, &sequenceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if cat == nil || *cat != p.Category || review || recordID == nil {
			return nil
		}
		rs, err := inbox.EffectiveRules(ctx, tx, ws, sequenceID)
		if err != nil {
			return err
		}
		d := rs[p.Category].Deal
		if d == nil {
			return nil
		}
		c, err := loadCatalog(ctx, tx, ws, false)
		if err != nil {
			return err
		}
		shape, err := checkDealRule(c, *d)
		if err != nil {
			// The catalog changed under the rule: nothing to move to.
			ev.Outcome, ev.ErrorCode = track.Failure, "invalid_request"
			track.Emit(ctx, ev)
			s.log.Warn("reply deal rule no longer fits the catalog", "workspace_id", ws, "category", p.Category, "error", err)
			return nil
		}
		a := &access{principal: principal{ActorType: "system"}, WorkspaceID: ws}
		res, err := moveReplyDeal(ctx, tx, c, shape, a, *d, *recordID)
		if err != nil || res == nil {
			return err
		}
		data := map[string]any{"inbound_id": p.InboundID, "category": p.Category, "action": "deal",
			"deal_id": res.dealID, "type": d.Type, "field": d.Field, "stage": d.Stage, "from_stage": res.from, "created": res.created}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "inbox.rule_applied",
			TargetType: "inbound_message", TargetID: p.InboundID, Data: data,
			Props: map[string]any{"category": p.Category, "action": "deal", "created": res.created}})
	})
	if err != nil {
		track.Fail(ctx, s.log, ev, "internal", err)
	}
	return err
}

type dealMove struct {
	dealID  string
	from    *string
	created bool
}

// moveReplyDeal finds the reply's deal (the newest one not in a closed
// stage that points at the person or one of their companies) and moves it
// forward to the rule's stage, or opens a new deal there. It returns nil
// when nothing changed.
func moveReplyDeal(ctx context.Context, tx pgx.Tx, c *catalog, shape dealShape, a *access, d inbox.DealRule, personID string) (*dealMove, error) {
	person, err := lockRecord(ctx, tx, a, personID)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	var conflict recordConflict
	if errors.As(err, &conflict) && person.MergedInto != nil {
		person, err = lockRecord(ctx, tx, a, *person.MergedInto)
	}
	if err != nil {
		if errors.Is(err, errNotFound) || errors.As(err, &conflict) {
			return nil, nil
		}
		return nil, err
	}
	targets := []string{person.ID}
	var companies []string
	if list, ok := person.Attributes["companies"].([]any); ok {
		for _, x := range list {
			if id, ok := x.(string); ok {
				companies = append(companies, id)
			}
		}
	}
	targets = append(targets, companies...)
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (r.created_at, r.id) r.id::text, r.attributes ->> $3
		FROM records r JOIN record_relationships rr ON rr.from_id = r.id AND rr.valid_to IS NULL AND rr.to_id = ANY($4::uuid[])
		WHERE r.workspace_id = $1 AND r.record_type = $2 AND r.merged_into IS NULL AND r.archived_at IS NULL
		ORDER BY r.created_at DESC, r.id DESC`, a.WorkspaceID, d.Type, d.Field, targets)
	if err != nil {
		return nil, err
	}
	type found struct {
		id    string
		stage *string
	}
	deals, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (found, error) {
		var f found
		return f, row.Scan(&f.id, &f.stage)
	})
	if err != nil {
		return nil, err
	}
	target := slices.Index(shape.stages, d.Stage)
	raw, _ := json.Marshal(d.Stage)
	for _, f := range deals {
		if f.stage != nil && slices.Contains(d.Closed, *f.stage) {
			continue
		}
		// The newest open deal is the reply's: move it only forward.
		if f.stage != nil && slices.Index(shape.stages, *f.stage) >= target {
			return nil, nil
		}
		rec, err := lockRecord(ctx, tx, a, f.id)
		if err != nil {
			if errors.Is(err, errNotFound) || errors.As(err, &conflict) {
				return nil, nil
			}
			return nil, err
		}
		if rec.Attributes == nil {
			rec.Attributes = map[string]any{}
		}
		if rec.AttributeSources == nil {
			rec.AttributeSources = map[string]string{}
		}
		changed, err := applyAttributes(ctx, tx, c, shape.t, a, &rec, map[string]json.RawMessage{d.Field: raw}, replyDealSource)
		if err != nil || !changed {
			return nil, err // a person set the stage, or it's already there
		}
		if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now() WHERE id = $1`,
			rec.ID, rec.Attributes, rec.AttributeSources); err != nil {
			return nil, err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "record.updated", "record", rec.ID,
			map[string]any{"type": rec.Type, "via": "reply_rule"}); err != nil {
			return nil, err
		}
		return &dealMove{dealID: rec.ID, from: f.stage}, nil
	}
	// No open deal: open one, named after the company when there is one.
	name := person.Name
	if len(companies) > 0 {
		var acct string
		if err := tx.QueryRow(ctx, `SELECT name FROM records WHERE id = $1 AND workspace_id = $2`, companies[0], a.WorkspaceID).Scan(&acct); err == nil && acct != "" {
			name = acct
		}
	}
	attrs := map[string]json.RawMessage{d.Field: raw}
	ref := func(field, id string) {
		f := shape.t.field(field)
		if f.Multiple {
			attrs[field], _ = json.Marshal([]string{id})
		} else {
			attrs[field], _ = json.Marshal(id)
		}
	}
	if shape.person != "" {
		ref(shape.person, person.ID)
	}
	if shape.acct != "" && len(companies) > 0 {
		ref(shape.acct, companies[0])
	}
	if shape.person == "" && len(companies) == 0 {
		return nil, nil // the deal could only point at a company, and the person has none
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, brand_id, name, origin, acquired_via)
		VALUES ($1, $2, $3, $4, 'first_party', 'reply_rule') RETURNING id::text`, a.WorkspaceID, shape.t.Key, person.BrandID, name).Scan(&id); err != nil {
		return nil, err
	}
	rec := recordView{ID: id, Attributes: map[string]any{}, AttributeSources: map[string]string{}}
	if _, err := applyAttributes(ctx, tx, c, shape.t, a, &rec, attrs, replyDealSource); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3 WHERE id = $1`,
		id, rec.Attributes, rec.AttributeSources); err != nil {
		return nil, err
	}
	if err := record(ctx, tx, &a.principal, a.WorkspaceID, "record.created", "record", id,
		map[string]any{"type": shape.t.Key, "origin": "first_party", "via": "reply_rule", "identities": 0}); err != nil {
		return nil, err
	}
	return &dealMove{dealID: id, created: true}, nil
}
