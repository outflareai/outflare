package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

// Lateness is how late an event may arrive and still move someone, like
// the rules engine's: older news only counts as usage.
const Lateness = 24 * time.Hour

// Service moves records between stages in jobs, and runs the inactivity
// sweep.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

// Register adds the job handler to q.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(customers.JobLifecycleEvent, s.eventJob)
}

// Product is a product's settings, as the engine needs them.
type Product struct {
	ID, Key, Name, ChurnMode string
	AtRiskDays, ChurnDays    int
}

func (p Product) days(after string) int {
	if after == "churn_days" {
		return p.ChurnDays
	}
	return p.AtRiskDays
}

// config is a brand's stages and moves.
type config struct {
	stages map[string]map[string]Stage // level → key → stage
	moves  map[string][]Move           // level → moves in order
}

func loadConfig(ctx context.Context, tx pgx.Tx, brand string) (*config, error) {
	c := &config{stages: map[string]map[string]Stage{LevelCompany: {}, LevelPerson: {}}, moves: map[string][]Move{}}
	rows, err := tx.Query(ctx, `SELECT level, key, name, category, rank FROM lifecycle_stages WHERE brand_id = $1`, brand)
	if err != nil {
		return nil, err
	}
	stages, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Stage, error) {
		var s Stage
		return s, row.Scan(&s.Level, &s.Key, &s.Name, &s.Category, &s.Rank)
	})
	if err != nil {
		return nil, err
	}
	for _, s := range stages {
		c.stages[s.Level][s.Key] = s
	}
	moves, err := LoadMoves(ctx, tx, brand)
	if err != nil {
		return nil, err
	}
	for _, m := range moves {
		c.moves[m.Level] = append(c.moves[m.Level], m)
	}
	return c, nil
}

// LoadMoves reads a brand's moves in the order they are tried.
func LoadMoves(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, brand string) ([]Move, error) {
	rows, err := q.Query(ctx, `SELECT level, key, name, kind, COALESCE(event, ''), COALESCE(after, ''), conditions, from_stages, any_stage,
			to_stage, COALESCE(churn_mode, ''), emit_event, position
		FROM lifecycle_moves WHERE brand_id = $1 ORDER BY level, position, key`, brand)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Move, error) {
		var m Move
		var conds []byte
		if err := row.Scan(&m.Level, &m.Key, &m.Name, &m.Kind, &m.Event, &m.After, &conds, &m.FromStages, &m.AnyStage,
			&m.ToStage, &m.ChurnMode, &m.Emit, &m.Position); err != nil {
			return m, err
		}
		m.Conditions = []journeys.Condition{}
		_ = json.Unmarshal(conds, &m.Conditions)
		return m, nil
	})
}

func loadProduct(ctx context.Context, tx pgx.Tx, id string) (*Product, error) {
	var p Product
	err := tx.QueryRow(ctx, `SELECT id::text, key, name, churn_mode, at_risk_days, churn_days FROM products WHERE id = $1 AND archived_at IS NULL`, id).
		Scan(&p.ID, &p.Key, &p.Name, &p.ChurnMode, &p.AtRiskDays, &p.ChurnDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &p, err
}

// state is where a record stands with a product, at a level.
type state struct {
	stage     string
	enteredAt time.Time
	lastUsed  *time.Time
	idleSince time.Time
}

// lockState reads and locks a record's state for a product and level; it
// returns nil when the record has no stage there yet.
func lockState(ctx context.Context, tx pgx.Tx, rec, product, level string) (*state, error) {
	var st state
	// One move at a time per record, product and level, even before its
	// state row exists.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "lifecycle:"+rec+":"+product+":"+level); err != nil {
		return nil, err
	}
	err := tx.QueryRow(ctx, `SELECT stage, entered_at, last_used_at, idle_since FROM lifecycle_states WHERE record_id = $1 AND product_id = $2 AND level = $3 FOR UPDATE`,
		rec, product, level).Scan(&st.stage, &st.enteredAt, &st.lastUsed, &st.idleSince)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &st, err
}

// event is an event about a product, as the engine reads it.
type event struct {
	id, name string
	props    map[string]any
	at       time.Time
	usage    bool
}

// pick is the first move that an event makes from stage, or nil.
func pick(moves []Move, stage string, p *Product, ev *event) *Move {
	for i := range moves {
		m := &moves[i]
		if !m.forProduct(p.ChurnMode) || !m.from(stage) || m.ToStage == stage {
			continue
		}
		switch m.Kind {
		case KindEvent:
			if m.Event == ev.name && journeys.Holds(nil, ev.props, m.Conditions, "all") {
				return m
			}
		case KindUsage:
			if ev.usage {
				return m
			}
		}
	}
	return nil
}

// dueAt is when the next inactivity move from stage falls due, counting
// from base (the inactivity clock, idle_since), or nil.
func dueAt(moves []Move, stage string, p *Product, base time.Time) *time.Time {
	var due *time.Time
	for _, m := range moves {
		if m.Kind != KindInactivity || !m.forProduct(p.ChurnMode) || !m.from(stage) || m.ToStage == stage {
			continue
		}
		t := base.AddDate(0, 0, p.days(m.After))
		if due == nil || t.Before(*due) {
			due = &t
		}
	}
	return due
}

// move is one transition about to be made.
type move struct {
	ws, brand, rec, level string
	product               *Product
	m                     *Move
	from                  string
	cause                 *string // the event that caused it
	causeKey              string
	at                    time.Time
	lastUsed              *time.Time
	idleSince             time.Time // the inactivity clock after the move
}

// apply makes a move once per cause: it records the transition, sets the
// stage, emits the move's event and writes the audit event. It reports
// false when the cause already moved this record.
func apply(ctx context.Context, tx pgx.Tx, c *config, mv move) (bool, error) {
	from := &mv.from
	if mv.from == None {
		from = nil
	}
	var tid string
	err := tx.QueryRow(ctx, `INSERT INTO lifecycle_transitions (workspace_id, brand_id, record_id, product_id, level, move, from_stage, to_stage,
			cause_event_id, cause_key, at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (record_id, level, move, cause_key) DO NOTHING RETURNING id::text`,
		mv.ws, mv.brand, mv.rec, mv.product.ID, mv.level, mv.m.Key, from, mv.m.ToStage, mv.cause, mv.causeKey, mv.at).Scan(&tid)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	due := dueAt(c.moves[mv.level], mv.m.ToStage, mv.product, mv.idleSince)
	if _, err := tx.Exec(ctx, `INSERT INTO lifecycle_states (workspace_id, brand_id, record_id, product_id, level, stage, entered_at, last_used_at, idle_since, due_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (record_id, product_id, level) DO UPDATE SET stage = EXCLUDED.stage, entered_at = EXCLUDED.entered_at,
			last_used_at = EXCLUDED.last_used_at, idle_since = EXCLUDED.idle_since, due_at = EXCLUDED.due_at, updated_at = now()`,
		mv.ws, mv.brand, mv.rec, mv.product.ID, mv.level, mv.m.ToStage, mv.at, mv.lastUsed, mv.idleSince, due); err != nil {
		return false, err
	}
	props := map[string]any{"product_id": mv.product.ID, "product_key": mv.product.Key, "level": mv.level, "move": mv.m.Key,
		"from_stage": mv.from, "to_stage": mv.m.ToStage}
	if mv.cause != nil {
		props["cause_event_id"] = *mv.cause
	}
	eventID, err := customers.RecordEvent(ctx, tx, mv.ws, customers.Event{RecordID: mv.rec, Name: mv.m.Emit, Properties: props, Source: "lifecycle",
		MessageID: "lifecycle:" + tid, OccurredAt: mv.at, ProductID: mv.product.ID})
	if err != nil {
		return false, err
	}
	if eventID != "" {
		if _, err := tx.Exec(ctx, `UPDATE lifecycle_transitions SET event_id = $2 WHERE id = $1`, tid, eventID); err != nil {
			return false, err
		}
	}
	return true, audit.Record(ctx, tx, audit.Event{WorkspaceID: mv.ws, BrandID: mv.brand, ActorType: "system", Action: "lifecycle.moved",
		TargetType: "record", TargetID: mv.rec,
		Data: map[string]any{"product_id": mv.product.ID, "level": mv.level, "move": mv.m.Key, "from_stage": mv.from, "to_stage": mv.m.ToStage,
			"kind": mv.m.Kind, "event": mv.m.Emit, "event_id": eventID},
		Props: map[string]any{"level": mv.level, "kind": mv.m.Kind}})
}

// target is a record to move at a level.
type target struct{ rec, level string }

// targets are where an event about a product moves: a person's own usage
// stages and their company's billing stages (or the person's own, with no
// company); a company's billing stages.
func targets(ctx context.Context, tx pgx.Tx, ws, brand, rec, recordType string, account *string) ([]target, error) {
	if recordType != "person" {
		return []target{{rec, LevelCompany}}, nil
	}
	out := []target{{rec, LevelPerson}}
	company := ""
	if account != nil {
		company = *account
	} else {
		err := tx.QueryRow(ctx, `SELECT l.to_id::text FROM record_relationships l JOIN records c ON c.id = l.to_id
			WHERE l.workspace_id = $1 AND l.from_id = $2 AND l.kind = 'companies' AND l.valid_to IS NULL AND c.brand_id = $3
			  AND c.merged_into IS NULL AND c.archived_at IS NULL AND c.deleted_at IS NULL
			ORDER BY l.valid_from, l.id LIMIT 1`, ws, rec, brand).Scan(&company)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	if company == "" {
		company = rec
	}
	return append(out, target{company, LevelCompany}), nil
}

// eventJob moves the records an event about a product is about.
func (s *Service) eventJob(ctx context.Context, job jobs.Job) error {
	var in struct {
		EventID string `json:"event_id"`
		Usage   bool   `json:"usage"`
	}
	if err := json.Unmarshal(job.Payload, &in); err != nil || job.WorkspaceID == nil || in.EventID == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var rec, recordType, brand, name, source string
		var account, productID *string
		var raw []byte
		var occurred, received time.Time
		var enabled *time.Time
		var approved bool
		err := tx.QueryRow(ctx, `SELECT e.record_id::text, r.record_type, e.account_id::text, e.product_id::text, e.brand_id::text, e.name, e.properties,
				e.source, e.occurred_at, e.received_at, l.enabled_at,
				EXISTS (SELECT 1 FROM event_definitions d WHERE d.workspace_id = e.workspace_id AND d.brand_id IS NOT DISTINCT FROM e.brand_id AND d.name = e.name AND d.status = 'approved')
			FROM customer_events e
			JOIN records r ON r.id = e.record_id AND r.merged_into IS NULL AND r.archived_at IS NULL AND r.deleted_at IS NULL
			JOIN lifecycle_settings l ON l.brand_id = e.brand_id
			WHERE e.workspace_id = $1 AND e.id = $2`, ws, in.EventID).
			Scan(&rec, &recordType, &account, &productID, &brand, &name, &raw, &source, &occurred, &received, &enabled, &approved)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		outcome := func(o string) error { metrics.LifecycleEvaluated("any", o); return nil }
		switch {
		case productID == nil || source == "browser" || source == "lifecycle":
			return nil
		case enabled == nil || occurred.Before(*enabled):
			return outcome("before_live") // greenfield: history never moves anyone
		case !approved:
			return outcome("not_approved") // a held event name drives nothing
		}
		p, err := loadProduct(ctx, tx, *productID)
		if err != nil || p == nil {
			return err
		}
		c, err := loadConfig(ctx, tx, brand)
		if err != nil {
			return err
		}
		ev := &event{id: in.EventID, name: name, props: map[string]any{}, at: occurred, usage: in.Usage}
		_ = json.Unmarshal(raw, &ev.props)
		late := received.Sub(occurred) > Lateness
		list, err := targets(ctx, tx, ws, brand, rec, recordType, account)
		if err != nil {
			return err
		}
		for _, t := range list {
			o, err := s.onEvent(ctx, tx, c, ws, brand, t, p, ev, late)
			if err != nil {
				return err
			}
			metrics.LifecycleEvaluated(t.level, o)
			if o == "moved" && t.level == LevelCompany {
				if err := Overall(ctx, tx, ws, brand, t.rec, "event:"+in.EventID, occurred); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// onEvent moves one record at one level for an event, and reports how it
// went: moved, no_move, duplicate, late or stale (older than the stage).
func (s *Service) onEvent(ctx context.Context, tx pgx.Tx, c *config, ws, brand string, t target, p *Product, ev *event, late bool) (string, error) {
	st, err := lockState(ctx, tx, t.rec, p.ID, t.level)
	if err != nil {
		return "", err
	}
	stage := None
	var lastUsed *time.Time
	idle := ev.at
	if st != nil {
		stage, lastUsed, idle = st.stage, st.lastUsed, st.idleSince
	}
	if ev.usage && (lastUsed == nil || ev.at.After(*lastUsed)) {
		at := ev.at
		lastUsed = &at
	}
	// Usage or a move by an event restarts the inactivity clock.
	clock := idle
	if ev.at.After(clock) {
		clock = ev.at
	}
	var m *Move
	outcome := "no_move"
	switch {
	case late:
		outcome = "late"
	case st != nil && ev.at.Before(st.enteredAt):
		outcome = "stale"
	default:
		m = pick(c.moves[t.level], stage, p, ev)
	}
	if m != nil {
		moved, err := apply(ctx, tx, c, move{ws: ws, brand: brand, rec: t.rec, level: t.level, product: p, m: m, from: stage,
			cause: &ev.id, causeKey: "event:" + ev.id, at: ev.at, lastUsed: lastUsed, idleSince: clock})
		if err != nil || moved {
			if moved {
				outcome = "moved"
			}
			return outcome, err
		}
		outcome = "duplicate"
	}
	// No move: keep the usage, and the inactivity clock it restarts.
	if st != nil && ev.usage {
		if _, err := tx.Exec(ctx, `UPDATE lifecycle_states SET last_used_at = $4, idle_since = $5, due_at = $6, updated_at = now()
			WHERE record_id = $1 AND product_id = $2 AND level = $3`, t.rec, p.ID, t.level, lastUsed, clock, dueAt(c.moves[t.level], st.stage, p, clock)); err != nil {
			return "", err
		}
	}
	return outcome, nil
}

// sweepBatch is the most states one sweep transaction handles.
const sweepBatch = 200

// Run makes the inactivity moves that fell due, every tick until ctx ends.
func (s *Service) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		n, err := s.Sweep(ctx)
		if err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "lifecycle.sweep_failed", ActorType: "system", Surface: "system"}, "internal", err)
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

// Sweep makes the inactivity moves that fell due, up to sweepBatch states,
// and reports how many it looked at. Workers on other servers skip the
// states this one holds.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	start := time.Now()
	n := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT s.workspace_id::text, s.brand_id::text, s.record_id::text, s.product_id::text, s.level, s.stage,
				s.idle_since, s.last_used_at, l.enabled_at IS NOT NULL
			FROM lifecycle_states s JOIN lifecycle_settings l ON l.brand_id = s.brand_id
			WHERE s.due_at <= now() ORDER BY s.due_at LIMIT $1 FOR UPDATE OF s SKIP LOCKED`, sweepBatch)
		if err != nil {
			return err
		}
		type due struct {
			ws, brand, rec, product, level, stage string
			idle                                  time.Time
			lastUsed                              *time.Time
			on                                    bool
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
			var d due
			return d, row.Scan(&d.ws, &d.brand, &d.rec, &d.product, &d.level, &d.stage, &d.idle, &d.lastUsed, &d.on)
		})
		if err != nil {
			return err
		}
		n = len(list)
		configs := map[string]*config{}
		for _, d := range list {
			if _, err := tx.Exec(ctx, `UPDATE lifecycle_states SET due_at = NULL, updated_at = now() WHERE record_id = $1 AND product_id = $2 AND level = $3`,
				d.rec, d.product, d.level); err != nil {
				return err
			}
			p, err := loadProduct(ctx, tx, d.product)
			if err != nil {
				return err
			}
			// A merged, archived or deleted record moves no more; restoring
			// it fires nothing (docs/customers.md, recovery window).
			var live bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM records WHERE id = $1 AND merged_into IS NULL AND archived_at IS NULL AND deleted_at IS NULL)`,
				d.rec).Scan(&live); err != nil {
				return err
			}
			if !d.on || p == nil || !live {
				metrics.LifecycleEvaluated(d.level, "off")
				continue
			}
			c, ok := configs[d.brand]
			if !ok {
				if c, err = loadConfig(ctx, tx, d.brand); err != nil {
					return err
				}
				configs[d.brand] = c
			}
			base := d.idle
			// The longest wait that has run out wins: a sweep that runs late
			// moves straight to churned rather than through at risk.
			var m *Move
			best := -1
			for i := range c.moves[d.level] {
				x := &c.moves[d.level][i]
				if x.Kind != KindInactivity || !x.forProduct(p.ChurnMode) || !x.from(d.stage) || x.ToStage == d.stage {
					continue
				}
				days := p.days(x.After)
				if base.AddDate(0, 0, days).After(time.Now()) || days <= best {
					continue
				}
				m, best = x, days
			}
			if m == nil {
				// Not due after all (the product's days changed): set the clock again.
				if _, err := tx.Exec(ctx, `UPDATE lifecycle_states SET due_at = $4 WHERE record_id = $1 AND product_id = $2 AND level = $3`,
					d.rec, d.product, d.level, dueAt(c.moves[d.level], d.stage, p, base)); err != nil {
					return err
				}
				metrics.LifecycleEvaluated(d.level, "no_move")
				continue
			}
			now := time.Now()
			moved, err := apply(ctx, tx, c, move{ws: d.ws, brand: d.brand, rec: d.rec, level: d.level, product: p, m: m, from: d.stage,
				causeKey: "inactive:" + base.UTC().Format(time.RFC3339Nano), at: now, lastUsed: d.lastUsed, idleSince: base})
			if err != nil {
				return err
			}
			if !moved {
				metrics.LifecycleEvaluated(d.level, "duplicate")
				continue
			}
			metrics.LifecycleEvaluated(d.level, "moved")
			if d.level == LevelCompany {
				if err := Overall(ctx, tx, d.ws, d.brand, d.rec, "inactive:"+d.product+":"+base.UTC().Format(time.RFC3339Nano), now); err != nil {
					return err
				}
			}
		}
		return nil
	})
	metrics.LifecycleSweepDuration(time.Since(start))
	return n, err
}

// Recheck has the sweep look at a brand's states again (all of them, or a
// product's), after its moves or a product's days changed. Call it in the
// transaction that changed them.
func Recheck(ctx context.Context, tx pgx.Tx, brand, product string) error {
	_, err := tx.Exec(ctx, `UPDATE lifecycle_states SET due_at = now() WHERE brand_id = $1 AND ($2 = '' OR product_id::text = $2)`, brand, product)
	return err
}
