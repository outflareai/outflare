package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Service evaluates rules in jobs and runs absence timers.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

// Register adds the job handlers to q.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(customers.JobRuleEvent, s.eventJob)
	q.Handle(customers.JobRuleChange, s.changeJob)
	q.Handle(customers.JobRuleAudience, s.audienceJob)
}

// Columns is what Load selects, in the order scanRule reads.
const Columns = `id::text, workspace_id::text, brand_id::text, status, record_type, trigger, conditions, match, emit_event, fire,
	cooldown_seconds, manual_edits, version, activated_at`

func scanRule(row pgx.Row) (Rule, error) {
	var r Rule
	var trigger, conds []byte
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.BrandID, &r.Status, &r.RecordType, &trigger, &conds, &r.Match, &r.Emit, &r.Fire,
		&r.Cooldown, &r.ManualEdits, &r.Version, &r.ActivatedAt)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(trigger, &r.Trigger); err != nil {
		return r, fmt.Errorf("rule %s trigger: %w", r.ID, err)
	}
	r.Conditions = []Condition{}
	if err := json.Unmarshal(conds, &r.Conditions); err != nil {
		return r, fmt.Errorf("rule %s conditions: %w", r.ID, err)
	}
	return r, nil
}

// Querier is a pool, a connection or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Load reads a workspace's rules that match where (SQL over the rules
// table, with args from $2 on).
func Load(ctx context.Context, q Querier, ws, where string, args ...any) ([]Rule, error) {
	rows, err := q.Query(ctx, `SELECT `+Columns+` FROM rules WHERE workspace_id = $1 AND `+where+` ORDER BY created_at`, append([]any{ws}, args...)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Rule, error) { return scanRule(row) })
}

// subject is the record a rule is evaluated for.
type subject struct {
	id, recordType string
	brandID        *string
	attributes     map[string]any
}

// loadSubject reads a record that rules may act on: not merged away or
// archived. It returns nil for anything else.
func loadSubject(ctx context.Context, q jobs.Querier, ws, id string) (*subject, error) {
	sub := subject{id: id}
	var attrs []byte
	err := q.QueryRow(ctx, `SELECT record_type, brand_id::text, attributes FROM records
		WHERE workspace_id = $1 AND id = $2 AND merged_into IS NULL AND archived_at IS NULL`, ws, id).Scan(&sub.recordType, &sub.brandID, &attrs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sub.attributes = map[string]any{}
	_ = json.Unmarshal(attrs, &sub.attributes)
	return &sub, nil
}

// inBrand reports whether a rule covers a record: a rule for every brand
// covers everyone, and a brand's rule its records and the shared ones.
func (r Rule) inBrand(sub *subject) bool {
	return r.BrandID == nil || sub.brandID == nil || *r.BrandID == *sub.brandID
}

// attributeConds and propertyConds split a rule's conditions.
func (r Rule) attributeConds() []Condition {
	var out []Condition
	for _, c := range r.Conditions {
		if c.Attribute != "" {
			out = append(out, c)
		}
	}
	return out
}

func (r Rule) propertyConds() []Condition {
	var out []Condition
	for _, c := range r.Conditions {
		if c.Property != "" {
			out = append(out, c)
		}
	}
	return out
}

// state is where one record stands with one rule.
type state struct {
	fresh       bool // made by this check: the rule never saw the record before
	holds       bool
	episode     int
	dueAt       *time.Time
	anchorAt    *time.Time
	lastFiredAt *time.Time
}

// lockState reads (making it if needed) and locks a record's state for a
// rule until tx ends, so two jobs about one record take turns.
func lockState(ctx context.Context, tx pgx.Tx, ws, ruleID, recordID string) (state, error) {
	var st state
	tag, err := tx.Exec(ctx, `INSERT INTO rule_states (workspace_id, rule_id, record_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, ws, ruleID, recordID)
	if err != nil {
		return st, err
	}
	st.fresh = tag.RowsAffected() == 1
	err = tx.QueryRow(ctx, `SELECT holds, episode, due_at, anchor_at, last_fired_at FROM rule_states WHERE rule_id = $1 AND record_id = $2 FOR UPDATE`,
		ruleID, recordID).Scan(&st.holds, &st.episode, &st.dueAt, &st.anchorAt, &st.lastFiredAt)
	return st, err
}

// fire emits the rule's event for a record once for the episode key, and
// reports how it went: fired, duplicate (that episode already fired) or
// cooldown (it fired too recently).
func fire(ctx context.Context, tx pgx.Tx, r Rule, recordID, key string, cause *string, props map[string]any, depth int) (string, error) {
	st, err := lockState(ctx, tx, r.WorkspaceID, r.ID, recordID)
	if err != nil {
		return "", err
	}
	if r.Cooldown > 0 && st.lastFiredAt != nil && time.Since(*st.lastFiredAt) < time.Duration(r.Cooldown)*time.Second {
		return "cooldown", nil
	}
	if r.Fire == "once" {
		key = "once"
	}
	var firingID string
	err = tx.QueryRow(ctx, `INSERT INTO rule_firings (workspace_id, rule_id, rule_version, record_id, episode_key, cause_event_id, depth)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (rule_id, record_id, episode_key) DO NOTHING RETURNING id::text`,
		r.WorkspaceID, r.ID, r.Version, recordID, key, cause, depth).Scan(&firingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "duplicate", nil
	}
	if err != nil {
		return "", err
	}
	out := map[string]any{}
	maps.Copy(out, props)
	out["rule_id"], out["rule_version"] = r.ID, r.Version
	eventID, err := customers.RecordEvent(ctx, tx, r.WorkspaceID, customers.Event{RecordID: recordID, Name: r.Emit, Properties: out,
		Source: "rule", MessageID: "rule:" + firingID})
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE rule_firings SET event_id = $2 WHERE id = $1`, firingID, nullIfEmpty(eventID)); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE rule_states SET last_fired_at = now(), updated_at = now() WHERE rule_id = $1 AND record_id = $2`, r.ID, recordID); err != nil {
		return "", err
	}
	return "fired", audit.Record(ctx, tx, audit.Event{WorkspaceID: r.WorkspaceID, ActorType: "system", Action: "rule.fired", TargetType: "rule", TargetID: r.ID,
		Data:  map[string]any{"record_id": recordID, "event": r.Emit, "event_id": eventID, "trigger": r.Trigger.Kind, "depth": depth, "rule_version": r.Version},
		Props: map[string]any{"trigger": r.Trigger.Kind, "depth": depth, "rule_version": r.Version}})
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// eventJob hands a new event to the event and absence rules listening to
// its name.
func (s *Service) eventJob(ctx context.Context, job jobs.Job) error {
	var in struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(job.Payload, &in); err != nil || job.WorkspaceID == nil || in.EventID == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var rec *string
		var name, source string
		var raw []byte
		var occurred, received time.Time
		err := tx.QueryRow(ctx, `SELECT record_id::text, name, properties, source, occurred_at, received_at FROM customer_events WHERE workspace_id = $1 AND id = $2`,
			ws, in.EventID).Scan(&rec, &name, &raw, &source, &occurred, &received)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && rec == nil) {
			return nil
		}
		if err != nil {
			return err
		}
		// Anyone with a site key can send a browser event, so one never
		// sets a rule off; and only approved events drive automations.
		if source == "browser" {
			return nil
		}
		var approved bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM event_definitions WHERE workspace_id = $1 AND name = $2 AND status = 'approved')`,
			ws, name).Scan(&approved); err != nil || !approved {
			return err
		}
		depth := 1
		if source == "rule" {
			var d int
			err := tx.QueryRow(ctx, `SELECT depth FROM rule_firings WHERE workspace_id = $1 AND event_id = $2`, ws, in.EventID).Scan(&d)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			depth = d + 1
		}
		sub, err := loadSubject(ctx, tx, ws, *rec)
		if err != nil || sub == nil {
			return err
		}
		list, err := Load(ctx, tx, ws, `status = 'active' AND $2 = ANY (watch_events) AND record_type = $3`, name, sub.recordType)
		if err != nil {
			return err
		}
		props := map[string]any{}
		_ = json.Unmarshal(raw, &props)
		late := received.Sub(occurred) > Lateness
		for _, r := range list {
			if !r.inBrand(sub) {
				continue
			}
			live := r.ActivatedAt == nil || !occurred.Before(*r.ActivatedAt)
			outcome := ""
			switch {
			case r.Trigger.Kind == KindAbsence:
				outcome, err = absenceEvent(ctx, tx, r, sub, in.EventID, name, props, occurred, live && !late)
			case depth > MaxDepth:
				outcome = "too_deep"
			case !live:
				outcome = "before_live"
			case late:
				outcome = "late"
			case !journeys.Holds(sub.attributes, props, r.Conditions, r.Match):
				outcome = "no_match"
			default:
				outcome, err = fire(ctx, tx, r, sub.id, "event:"+in.EventID, &in.EventID, props, depth)
			}
			if err != nil {
				return err
			}
			metrics.RuleEvaluated(r.Trigger.Kind, outcome)
		}
		return nil
	})
}

// absenceEvent moves an absence rule's clock for an event: the event that
// must happen stops it, and the start event starts it (again). Attribute
// conditions are tested when the time runs out; property conditions test
// the start event.
func absenceEvent(ctx context.Context, tx pgx.Tx, r Rule, sub *subject, eventID, name string, props map[string]any, occurred time.Time, live bool) (string, error) {
	st, err := lockState(ctx, tx, r.WorkspaceID, r.ID, sub.id)
	if err != nil {
		return "", err
	}
	outcome := "no_match"
	if name == r.Trigger.Event && st.dueAt != nil && st.anchorAt != nil && !occurred.Before(*st.anchorAt) && !occurred.After(*st.dueAt) {
		if _, err := tx.Exec(ctx, `UPDATE rule_states SET due_at = NULL, updated_at = now() WHERE rule_id = $1 AND record_id = $2`, r.ID, sub.id); err != nil {
			return "", err
		}
		outcome = "stopped"
	}
	if name != r.Trigger.Start() {
		return outcome, nil
	}
	if !live {
		return "late", nil
	}
	if !journeys.Holds(nil, props, r.propertyConds(), r.Match) {
		return outcome, nil
	}
	due := occurred.Add(time.Duration(r.Trigger.WithinSeconds) * time.Second)
	if !due.After(time.Now()) {
		return "late", nil
	}
	raw, _ := json.Marshal(props)
	_, err = tx.Exec(ctx, `UPDATE rule_states SET due_at = $3, anchor_at = $4, anchor_event_id = $5, anchor_properties = $6, updated_at = now()
		WHERE rule_id = $1 AND record_id = $2`, r.ID, sub.id, due, occurred, eventID, raw)
	return "started", err
}

// changeJob tests the change rules that watch a field that changed on a
// record, and fires those whose conditions became true.
func (s *Service) changeJob(ctx context.Context, job jobs.Job) error {
	var c customers.FieldChange
	if err := json.Unmarshal(job.Payload, &c); err != nil || job.WorkspaceID == nil || c.RecordID == "" || c.Field == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		sub, err := loadSubject(ctx, tx, ws, c.RecordID)
		if err != nil || sub == nil {
			return err
		}
		list, err := Load(ctx, tx, ws, `status = 'active' AND trigger_kind = 'change' AND $2 = ANY (watch_fields) AND record_type = $3`, c.Field, sub.recordType)
		if err != nil {
			return err
		}
		for _, r := range list {
			if !r.inBrand(sub) {
				continue
			}
			outcome, err := s.change(ctx, tx, r, sub, c)
			if err != nil {
				return err
			}
			metrics.RuleEvaluated(KindChange, outcome)
		}
		return nil
	})
}

func (s *Service) change(ctx context.Context, tx pgx.Tx, r Rule, sub *subject, c customers.FieldChange) (string, error) {
	st, err := lockState(ctx, tx, r.WorkspaceID, r.ID, sub.id)
	if err != nil {
		return "", err
	}
	now := journeys.Holds(sub.attributes, nil, r.Conditions, r.Match)
	before := st.holds
	if st.fresh {
		// The first time the rule sees this record: was it true before
		// this change? Put the field back as it was and test again.
		prev := maps.Clone(sub.attributes)
		if c.HadOld {
			prev[c.Field] = c.Old
		} else {
			delete(prev, c.Field)
		}
		before = journeys.Holds(prev, nil, r.Conditions, r.Match)
	}
	episode := st.episode
	if now && !before {
		episode++
	}
	if _, err := tx.Exec(ctx, `UPDATE rule_states SET holds = $3, episode = $4, updated_at = now() WHERE rule_id = $1 AND record_id = $2`,
		r.ID, sub.id, now, episode); err != nil {
		return "", err
	}
	switch {
	case !now || before:
		return "no_match", nil
	case c.Source == "manual" && !r.ManualEdits:
		return "manual", nil
	}
	return fire(ctx, tx, r, sub.id, fmt.Sprintf("change:%d", episode), nil, map[string]any{"field": c.Field}, 1)
}

// audienceJob fires the audience rules for people who joined or left a
// smart list.
func (s *Service) audienceJob(ctx context.Context, job jobs.Job) error {
	var c customers.ListChange
	if err := json.Unmarshal(job.Payload, &c); err != nil || job.WorkspaceID == nil || c.ListID == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		list, err := Load(ctx, tx, ws, `status = 'active' AND trigger_kind = 'audience' AND list_id = $2 AND trigger->>'on' = $3`, c.ListID, c.On)
		if err != nil {
			return err
		}
		for _, r := range list {
			if r.ActivatedAt != nil && c.At.Before(*r.ActivatedAt) {
				metrics.RuleEvaluated(KindAudience, "before_live")
				continue
			}
			for _, id := range c.RecordIDs {
				sub, err := loadSubject(ctx, tx, ws, id)
				if err != nil {
					return err
				}
				outcome := ""
				switch {
				case sub == nil || !r.inBrand(sub):
					continue
				case !journeys.Holds(sub.attributes, nil, r.Conditions, r.Match):
					outcome = "no_match"
				default:
					outcome, err = fire(ctx, tx, r, id, fmt.Sprintf("%s:%d", c.On, c.At.UnixNano()), nil, map[string]any{"list_id": c.ListID, "on": c.On}, 1)
					if err != nil {
						return err
					}
				}
				metrics.RuleEvaluated(KindAudience, outcome)
			}
		}
		return nil
	})
}

// sweepBatch is the most timers one sweep transaction handles.
const sweepBatch = 200

// Run handles absence timers that ran out, every tick until ctx ends.
func (s *Service) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		n, err := s.Sweep(ctx)
		if err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "rule.timers_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		if err == nil && n == sweepBatch {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sweep fires the absence rules whose time ran out, up to sweepBatch of
// them, and reports how many timers it handled. Workers on other servers
// skip the timers this one holds.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT s.workspace_id::text, s.rule_id::text, s.record_id::text, s.anchor_event_id::text, s.anchor_properties
			FROM rule_states s JOIN rules r ON r.id = s.rule_id
			WHERE s.due_at <= now() AND r.status = 'active' ORDER BY s.due_at LIMIT $1 FOR UPDATE OF s SKIP LOCKED`, sweepBatch)
		if err != nil {
			return err
		}
		type due struct {
			ws, rule, record string
			anchor           *string
			props            []byte
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
			var d due
			return d, row.Scan(&d.ws, &d.rule, &d.record, &d.anchor, &d.props)
		})
		if err != nil {
			return err
		}
		n = len(list)
		loaded := map[string]Rule{}
		for _, d := range list {
			if _, err := tx.Exec(ctx, `UPDATE rule_states SET due_at = NULL, updated_at = now() WHERE rule_id = $1 AND record_id = $2`, d.rule, d.record); err != nil {
				return err
			}
			r, ok := loaded[d.rule]
			if !ok {
				got, err := Load(ctx, tx, d.ws, `id = $2`, d.rule)
				if err != nil {
					return err
				}
				if len(got) == 0 {
					continue
				}
				r, loaded[d.rule] = got[0], got[0]
			}
			sub, err := loadSubject(ctx, tx, d.ws, d.record)
			if err != nil {
				return err
			}
			outcome := ""
			switch {
			case sub == nil || !r.inBrand(sub):
				continue
			case !journeys.Holds(sub.attributes, nil, r.attributeConds(), r.Match):
				outcome = "no_match"
			default:
				props := map[string]any{}
				_ = json.Unmarshal(d.props, &props)
				props["start_event"], props["within_seconds"] = r.Trigger.Start(), r.Trigger.WithinSeconds
				key := "absence:"
				if d.anchor != nil {
					key += *d.anchor
				}
				outcome, err = fire(ctx, tx, r, d.record, key, d.anchor, props, 1)
				if err != nil {
					return err
				}
			}
			metrics.RuleEvaluated(KindAbsence, outcome)
		}
		return nil
	})
	return n, err
}

// Paused clears the timers that ran out while a rule was paused, so
// activating it again doesn't fire for time that passed while it was off.
// Call it in the transaction that activates the rule.
func Paused(ctx context.Context, tx pgx.Tx, ws, ruleID string) error {
	_, err := tx.Exec(ctx, `UPDATE rule_states SET due_at = NULL, updated_at = now() WHERE workspace_id = $1 AND rule_id = $2 AND due_at <= now()`, ws, ruleID)
	return err
}

// Archived stops an archived rule's timers.
func Archived(ctx context.Context, tx pgx.Tx, ws, ruleID string) error {
	_, err := tx.Exec(ctx, `UPDATE rule_states SET due_at = NULL, updated_at = now() WHERE workspace_id = $1 AND rule_id = $2 AND due_at IS NOT NULL`, ws, ruleID)
	return err
}
