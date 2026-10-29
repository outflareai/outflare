package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/playbooks"
	"github.com/outflareai/outflare/engine/internal/smartlists"
)

// Flow kits: a kit's smart lists, rules and journeys (playbooks/flows.go).
// Each is added as a draft that does nothing until a person turns it on,
// and remembers its kit and key, so applying the kit again leaves it as
// it is.

// kitRef names the flow kit that added a smart list, rule or journey.
type kitRef struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

func newKitRef(id, key *string) *kitRef {
	if id == nil || key == nil {
		return nil
	}
	return &kitRef{ID: *id, Key: *key}
}

// planFlows adds the kit's smart lists, rules and journeys to plan: each is
// reused when the kit added it before, a conflict when something else has
// its name, and added otherwise.
func planFlows(ctx context.Context, tx pgx.Tx, ws string, k playbooks.Kit, plan *kitPlan) error {
	type part struct {
		kind, key, name, table, live, what, adds string
	}
	var parts []part
	for _, l := range k.SmartLists {
		adds := "a smart list; it fills from its rules and does nothing on its own"
		if l.Kind == smartlists.KindStatic {
			adds = "a static list, empty until a journey or a person adds people"
		}
		parts = append(parts, part{"smart_list", l.Key, l.Name, "smart_lists", "true", "smart list", adds})
	}
	for _, r := range k.Rules {
		parts = append(parts, part{"rule", r.Key, r.Name, "rules", "status <> 'archived'", "rule", "a draft rule; turn it on in Rules"})
	}
	for _, j := range k.Journeys {
		parts = append(parts, part{"journey", j.Key, j.Name, "journeys", "status <> 'archived'", "journey",
			fmt.Sprintf("a draft journey of %s; check it in the builder, then activate it", countOf(len(j.Steps), "step", "steps"))})
	}
	for _, p := range parts {
		it := kitItem{Kind: p.kind, Key: p.key, Label: p.name, Action: kitAdd, Detail: p.adds}
		err := tx.QueryRow(ctx, `SELECT id::text FROM `+p.table+` WHERE workspace_id = $1 AND kit_id = $2 AND kit_key = $3 AND `+p.live,
			ws, k.ID, p.key).Scan(&it.existing)
		switch {
		case err == nil:
			it.Action, it.Detail = kitReuse, "the kit added it before; it stays as it is"
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		default:
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+p.table+` WHERE workspace_id = $1 AND lower(name) = lower($2) AND `+p.live+`)`,
				ws, p.name).Scan(&taken); err != nil {
				return err
			}
			if taken {
				it.Action, it.Detail = kitConflict, "a "+p.what+" named "+p.name+" is already there; rename it first"
			}
		}
		plan.add(it)
	}
	return nil
}

// flowScopes says what applying the plan's flows needs beyond
// catalog.manage, or "".
func flowScopes(plan *kitPlan, a *access) error {
	var lists, journeysToo bool
	for _, it := range plan.Items {
		if it.Action != kitAdd {
			continue
		}
		lists = lists || it.Kind == "smart_list" || it.Kind == "rule"
		journeysToo = journeysToo || it.Kind == "journey"
	}
	switch {
	case (lists || journeysToo) && !a.writes(nil):
		return errDenied("a kit's smart lists, rules and journeys are for every brand, which needs access to every brand")
	case lists && !a.has(auth.ScopeCustomersManage):
		return errDenied("this kit adds smart lists and rules, which needs the customers.manage scope")
	case journeysToo && !a.has(auth.ScopeEmailsManage):
		return errDenied("this kit adds journeys, which needs the emails.manage scope")
	}
	return nil
}

// applyFlows adds the plan's new smart lists, rules and journeys, in tx,
// with values for the kit's inputs.
func applyFlows(ctx context.Context, tx pgx.Tx, a *access, k playbooks.Kit, plan *kitPlan, values map[string]string) error {
	ws, p := a.WorkspaceID, &a.principal
	ids := map[string]string{}
	adding := map[string]bool{}
	for _, it := range plan.Items {
		if it.Kind != "smart_list" && it.Kind != "journey" && it.Kind != "rule" {
			continue
		}
		ref := it.Kind + "." + it.Key
		if it.Action == kitReuse {
			ids[ref] = it.existing
			continue
		}
		adding[ref] = true
		var id string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
			return err
		}
		ids[ref] = id
	}
	for _, in := range k.Inputs {
		if in.Kind != playbooks.InputSequence || values[in.Name] == "" {
			continue
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sequences WHERE workspace_id = $1 AND id = $2 AND status <> 'archived')`,
			ws, values[in.Name]).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid(in.Name + " is not a sequence in this workspace")
		}
	}
	flows, err := k.Resolve(values, ids)
	if err != nil {
		return errInvalid("with these inputs, " + err.Error())
	}
	createdBy := func() *string {
		if a.ActorID == "" {
			return nil
		}
		return &a.ActorID
	}()
	for _, l := range flows.Lists {
		if !adding["smart_list."+l.Key] {
			continue
		}
		raw, _ := json.Marshal(l.Rules)
		var refreshed *time.Time
		if l.Kind == smartlists.KindStatic {
			raw, _ = json.Marshal(smartlists.NobodyRules)
			refreshed = &smartlists.Never
		}
		id := ids["smart_list."+l.Key]
		if _, err := tx.Exec(ctx, `INSERT INTO smart_lists (id, workspace_id, name, description, kind, rules, match, refreshed_at, created_by, kit_id, kit_key)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, id, ws, l.Name, l.Description, l.Kind, raw, l.Match, refreshed, createdBy, k.ID, l.Key); err != nil {
			return err
		}
		if l.Kind == smartlists.KindSmart {
			if _, err := smartlists.Enqueue(ctx, tx, ws, id, true); err != nil {
				return err
			}
		}
		if err := record(ctx, tx, p, ws, "smart_list.created", "smart_list", id,
			map[string]any{"kind": l.Kind, "rules": len(l.Rules), "match": l.Match, "branded": false, "kit_id": k.ID}); err != nil {
			return err
		}
	}
	for _, r := range flows.Rules {
		if !adding["rule."+r.Key] {
			continue
		}
		v := ruleView{Name: r.Name, Description: r.Description, RecordType: r.Rule.RecordType, Trigger: r.Rule.Trigger, Conditions: r.Rule.Conditions,
			Match: r.Rule.Match, EmitEvent: r.Rule.Emit, Fire: r.Rule.Fire, CooldownSeconds: r.Rule.Cooldown}
		id := ids["rule."+r.Key]
		if _, err := tx.Exec(ctx, `INSERT INTO rules (id, workspace_id, name, description, record_type, trigger_kind, trigger, conditions, match,
			emit_event, fire, cooldown_seconds, manual_edits, watch_events, watch_fields, list_id, created_by, kit_id, kit_key)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
			append(append([]any{id, ws, v.Name, v.Description}, ruleColumns(v)...), createdBy, k.ID, r.Key)...); err != nil {
			return err
		}
		data := ruleData(v)
		data["kit_id"] = k.ID
		if err := record(ctx, tx, p, ws, "rule.created", "rule", id, data); err != nil {
			return err
		}
	}
	for _, j := range flows.Journeys {
		if !adding["journey."+j.Key] {
			continue
		}
		if err := kitJourneyTemplates(ctx, tx, ws, j.Steps); err != nil {
			return err
		}
		filter, _ := json.Marshal(j.TriggerFilter)
		steps, _ := json.Marshal(j.Steps)
		exitConds, _ := json.Marshal(j.ExitConditions)
		id := ids["journey."+j.Key]
		if _, err := tx.Exec(ctx, `INSERT INTO journeys (id, workspace_id, name, description, trigger_kind, trigger_event, trigger_filter,
			entry, goal_event, steps, created_by, exit_events, exit_conditions, kit_id, kit_key)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			id, ws, j.Name, j.Description, j.TriggerKind, j.TriggerEvent, filter, j.Entry, j.GoalEvent, steps, createdBy, j.ExitEvents, exitConds,
			k.ID, j.Key); err != nil {
			return err
		}
		if err := record(ctx, tx, p, ws, "journey.created", "journey", id,
			map[string]any{"trigger": j.TriggerKind, "steps": len(j.Steps), "kit_id": k.ID}); err != nil {
			return err
		}
	}
	return nil
}

// kitJourneyTemplates checks, in tx, that a kit journey's emails are
// templates of the purpose each step sends (lifecycle, or transactional
// for transactional_email) that aren't archived: the kit's own drafts, or
// a template with the same handle the workspace already had.
func kitJourneyTemplates(ctx context.Context, tx pgx.Tx, ws string, steps []journeys.Step) error {
	for _, st := range steps {
		want := "lifecycle"
		switch st.Kind {
		case journeys.KindEmail:
		case "transactional_email":
			want = "transactional"
		default:
			continue
		}
		ref := st.Template
		var purpose, status string
		err := tx.QueryRow(ctx, `SELECT purpose, status FROM email_templates WHERE workspace_id = $1 AND handle = $2`, ws, ref).Scan(&purpose, &status)
		switch {
		case err != nil:
			return err
		case purpose != want:
			return errKitConflict("the workspace's template " + ref + " is " + purpose + ", but the kit's journey sends it as " + want + " email")
		case status != "active":
			return errKitConflict("the workspace's template " + ref + " is archived; restore it, then apply the kit")
		}
	}
	return nil
}
