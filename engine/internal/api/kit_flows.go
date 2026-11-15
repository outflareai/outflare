package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/playbooks"
	"github.com/outflareai/outflare/engine/internal/recmetrics"
	"github.com/outflareai/outflare/engine/internal/scores"
	"github.com/outflareai/outflare/engine/internal/signals"
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
func planFlows(ctx context.Context, tx pgx.Tx, ws, brand string, k playbooks.Kit, plan *kitPlan) error {
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
		err := tx.QueryRow(ctx, `SELECT id::text FROM `+p.table+` WHERE workspace_id = $1 AND kit_id = $2 AND kit_key = $3 AND `+p.live+`
			AND brand_id IS NOT DISTINCT FROM `+kitBrandSQL("$1", "$4"), ws, k.ID, p.key, brand).Scan(&it.existing)
		switch {
		case err == nil:
			it.Action, it.Detail = kitReuse, "the kit added it before; it stays as it is"
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		default:
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+p.table+` WHERE workspace_id = $1 AND lower(name) = lower($2) AND `+p.live+`
				AND brand_id IS NOT DISTINCT FROM `+kitBrandSQL("$1", "$3")+`)`, ws, p.name, brand).Scan(&taken); err != nil {
				return err
			}
			if taken {
				it.Action, it.Detail = kitConflict, "a "+p.what+" named "+p.name+" is already there; rename it first"
			}
		}
		plan.add(it)
	}
	for _, t := range k.SignalTypes {
		it := kitItem{Kind: "signal_type", Key: t.Key, Label: t.Label, Action: kitAdd, Detail: "a kind of signal for Needs attention"}
		if len(t.Events) > 0 {
			it.Detail = "a kind of signal for Needs attention, raised by " + countOf(len(t.Events), "event", "events")
		}
		var archived bool
		err := tx.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM signal_types WHERE workspace_id = $1 AND key = $2
			AND brand_id = `+kitBrandSQL("$1", "$3"), ws, t.Key, brand).Scan(&archived)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case archived:
			it.Action, it.Detail = kitConflict, "the signal type "+t.Key+" was removed; add it back in Needs attention first"
		default:
			it.Action, it.Detail = kitReuse, "already there; it stays as it is"
		}
		plan.add(it)
	}
	// Metrics and scores are the brand's by key, like signal types: one
	// the brand already has stays as it is.
	for _, m := range k.Metrics {
		it := kitItem{Kind: "metric", Key: m.Key, Label: m.Name, Action: kitAdd, Detail: "a live number on records, read as metrics." + m.Key}
		var has bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM metric_definitions WHERE workspace_id = $1 AND key = $2 AND status = 'active'
			AND brand_id = `+kitBrandSQL("$1", "$3")+`)`, ws, m.Key, brand).Scan(&has); err != nil {
			return err
		}
		if has {
			it.Action, it.Detail = kitReuse, "already there; it stays as it is"
		}
		plan.add(it)
	}
	adding := 0
	for _, sc := range k.Scores {
		it := kitItem{Kind: "score", Key: sc.Key, Label: sc.Label, Action: kitAdd,
			Detail: fmt.Sprintf("a score from 0 to 100 with %s; every record is scored once it is added", countOf(len(sc.Factors), "factor", "factors"))}
		if len(sc.Parts) > 0 {
			it.Detail = fmt.Sprintf("a headline score: the average of %d other scores, which stay visible under it", len(sc.Parts))
		}
		var has bool
		var n int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(bool_or(key = $2), false), count(*)::int FROM score_definitions WHERE workspace_id = $1 AND status = 'active'
			AND brand_id = `+kitBrandSQL("$1", "$3"), ws, sc.Key, brand).Scan(&has, &n); err != nil {
			return err
		}
		switch {
		case has:
			it.Action, it.Detail = kitReuse, "already there; it stays as it is"
		case n+adding >= scores.MaxPerBrand:
			it.Action, it.Detail = kitConflict, fmt.Sprintf("the brand already keeps %d scores, the most it can; archive one first", scores.MaxPerBrand)
		default:
			adding++
		}
		plan.add(it)
	}
	return nil
}

// kitBrandSQL is the brand a kit sets up: the one named by brand (a
// parameter, "" for none), else the workspace's first.
func kitBrandSQL(ws, brand string) string {
	return "COALESCE(NULLIF(" + brand + ", '')::uuid, outflare_first_brand(" + ws + "))"
}

// flowScopes says what applying the plan's flows needs beyond
// catalog.manage, or "".
func flowScopes(plan *kitPlan, a *access, brand string) error {
	var lists, journeysToo bool
	for _, it := range plan.Items {
		if it.Action != kitAdd {
			continue
		}
		if (it.Kind == "metric" || it.Kind == "score") && ((brand == "" && !a.writes(nil)) || (brand != "" && !a.writes(&brand))) {
			return errDenied("a kit's metrics and scores are the brand's, which needs access to that brand")
		}
		lists = lists || it.Kind == "smart_list" || it.Kind == "rule" || it.Kind == "signal_type"
		journeysToo = journeysToo || it.Kind == "journey"
	}
	switch {
	case (lists || journeysToo) && brand == "" && !a.writes(nil):
		return errDenied("a kit's smart lists, rules, journeys and signal types set up the workspace's first brand from this route, which needs access to every brand; apply it on the brand's own route")
	case (lists || journeysToo) && brand != "" && !a.writes(&brand):
		return errDenied("a kit's smart lists, rules, journeys and signal types are the brand's, which needs access to that brand")
	case lists && !a.has(auth.ScopeCustomersManage):
		return errDenied("this kit adds smart lists, rules or signal types, which needs the customers.manage scope")
	case journeysToo && !a.has(auth.ScopeEmailsManage):
		return errDenied("this kit adds journeys, which needs the emails.manage scope")
	}
	return nil
}

// applyFlows adds the plan's new smart lists, rules and journeys, in tx,
// with values for the kit's inputs.
func applyFlows(ctx context.Context, tx pgx.Tx, a *access, brand string, k playbooks.Kit, plan *kitPlan, values map[string]string, holdout int) error {
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
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sequences WHERE workspace_id = $1 AND id = $2 AND status <> 'archived'
			AND brand_id IS NOT DISTINCT FROM `+kitBrandSQL("$1", "$3")+`)`, ws, values[in.Name], brand).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid(in.Name + " is not a sequence of this brand")
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
	for _, t := range flows.SignalTypes {
		if !slices.ContainsFunc(plan.Items, func(it kitItem) bool { return it.Kind == "signal_type" && it.Key == t.Key && it.Action == kitAdd }) {
			continue
		}
		var brandID string
		if err := tx.QueryRow(ctx, `SELECT `+kitBrandSQL("$1", "$2")+`::text`, ws, brand).Scan(&brandID); err != nil {
			return err
		}
		if err := signals.EnsureTypes(ctx, tx, ws, brandID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO signal_types (workspace_id, brand_id, key, label, description, events, due_days)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, ws, brandID, t.Key, t.Label, t.Description, t.Events, t.DueDays); err != nil {
			return err
		}
		if err := record(ctx, tx, p, ws, "signal_type.created", "signal_type", "", map[string]any{"key": t.Key, "brand_id": brandID, "kit_id": k.ID}); err != nil {
			return err
		}
	}
	addsPart := func(kind, key string) bool {
		return slices.ContainsFunc(plan.Items, func(it kitItem) bool { return it.Kind == kind && it.Key == key && it.Action == kitAdd })
	}
	if len(flows.Metrics)+len(flows.Scores) > 0 {
		var brandID string
		if err := tx.QueryRow(ctx, `SELECT `+kitBrandSQL("$1", "$2")+`::text`, ws, brand).Scan(&brandID); err != nil {
			return err
		}
		var metricsOn bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM metric_settings WHERE brand_id = $1 AND workspace_id = $2 AND enabled_at IS NOT NULL)`,
			brandID, ws).Scan(&metricsOn); err != nil {
			return err
		}
		for _, d := range flows.Metrics {
			if !addsPart("metric", d.Key) {
				continue
			}
			id, err := recmetrics.Insert(ctx, tx, ws, brandID, d)
			if err != nil {
				return err
			}
			// Counted from stored events when the brand has metrics on;
			// otherwise it starts counting when they are turned on.
			if metricsOn {
				if _, err := tx.Exec(ctx, `UPDATE metric_definitions SET backfilling = true WHERE id = $1`, id); err != nil {
					return err
				}
				if err := recmetrics.EnqueueBackfill(ctx, tx, ws, id, 1); err != nil {
					return err
				}
			}
			if err := record(ctx, tx, p, ws, "metric.created", "brand", brandID,
				map[string]any{"metric_id": id, "aggregate": d.Aggregate, "window_days": d.WindowDays, "kit_id": k.ID}); err != nil {
				return err
			}
		}
		for _, d := range flows.Scores {
			if !addsPart("score", d.Key) {
				continue
			}
			factors, err := json.Marshal(d.Factors)
			if err != nil {
				return err
			}
			parts, err := json.Marshal(d.Parts)
			if err != nil {
				return err
			}
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO score_definitions (workspace_id, brand_id, key, label, description, record_type, base, factors, high_from, low_below, watch_fields,
					nature, parts, higher_is_better, needs)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) RETURNING id::text`,
				ws, brandID, d.Key, d.Label, d.Description, d.RecordType, d.Base, factors, d.HighFrom, d.LowBelow, scores.WatchFields(d),
				d.Nature, parts, d.HigherIsBetter, d.Needs).Scan(&id); err != nil {
				return err
			}
			if err := scores.EnqueuePass(ctx, tx, ws, brandID, d.RecordType, id, 1); err != nil {
				return err
			}
			if err := record(ctx, tx, p, ws, "score.created", "brand", brandID,
				map[string]any{"score_id": id, "record_type": d.RecordType, "factors": len(d.Factors), "version": 1, "kit_id": k.ID}); err != nil {
				return err
			}
		}
	}
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
		if _, err := tx.Exec(ctx, `INSERT INTO smart_lists (id, workspace_id, brand_id, name, description, kind, rules, match, refreshed_at, created_by, kit_id, kit_key)
			VALUES ($1, $2, `+kitBrandSQL("$2", "$12")+`, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, id, ws, l.Name, l.Description, l.Kind, raw, l.Match, refreshed, createdBy, k.ID, l.Key, brand); err != nil {
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
		// A kit sets up its brand, like its lists.
		if _, err := tx.Exec(ctx, `INSERT INTO rules (id, workspace_id, brand_id, name, description, record_type, trigger_kind, trigger, conditions, match,
			emit_event, fire, cooldown_seconds, manual_edits, watch_events, watch_fields, list_id, created_by, kit_id, kit_key)
			VALUES ($1, $2, `+kitBrandSQL("$2", "$20")+`, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
			append(append([]any{id, ws, v.Name, v.Description}, ruleColumns(v)...), createdBy, k.ID, r.Key, brand)...); err != nil {
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
		if err := kitJourneyTemplates(ctx, tx, ws, brand, j.Steps); err != nil {
			return err
		}
		filter, _ := json.Marshal(j.TriggerFilter)
		// Hold some back, when asked, so the journey can be measured.
		j.Steps = journeys.WithHoldout(j.Steps, holdout)
		if msg := journeys.CheckFor(j.RecordType, j.Steps); msg != "" {
			return errInvalid("the journey " + j.Key + " can't hold some back: " + msg)
		}
		steps, _ := json.Marshal(j.Steps)
		exitConds, _ := json.Marshal(j.ExitConditions)
		id := ids["journey."+j.Key]
		if _, err := tx.Exec(ctx, `INSERT INTO journeys (id, workspace_id, brand_id, name, description, trigger_kind, trigger_event, trigger_filter,
			entry, goal_event, steps, created_by, exit_events, exit_conditions, kit_id, kit_key, record_type)
			VALUES ($1, $2, `+kitBrandSQL("$2", "$16")+`, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $17)`,
			id, ws, j.Name, j.Description, j.TriggerKind, j.TriggerEvent, filter, j.Entry, j.GoalEvent, steps, createdBy, j.ExitEvents, exitConds,
			k.ID, j.Key, brand, j.RecordType); err != nil {
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
func kitJourneyTemplates(ctx context.Context, tx pgx.Tx, ws, brand string, steps []journeys.Step) error {
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
		err := tx.QueryRow(ctx, `SELECT purpose, status FROM email_templates WHERE workspace_id = $1 AND handle = $2
			AND brand_id = `+kitBrandSQL("$1", "$3"), ws, ref, brand).Scan(&purpose, &status)
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
