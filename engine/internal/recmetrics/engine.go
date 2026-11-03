package recmetrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// JobBackfill rebuilds one metric from stored events, quietly.
const JobBackfill = "metric.backfill"

// Service keeps metrics live in jobs and runs the sweep that moves windows
// on as days pass.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

// Register adds the job handlers to q.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(customers.JobMetricEvent, s.eventJob)
	q.Handle(JobBackfill, s.backfillJob)
}

// EnqueueBackfill asks for a quiet rebuild of a metric at its version, in
// q's transaction.
func EnqueueBackfill(ctx context.Context, q jobs.Querier, ws, metricID string, version int) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobBackfill, WorkspaceID: &ws,
		Payload:        map[string]any{"metric_id": metricID, "version": version},
		IdempotencyKey: "metric-backfill:" + metricID + ":" + strconv.Itoa(version), MaxAttempts: 5})
	return err
}

// target is a record a metric is kept on, at a level.
type target struct{ rec, level string }

// targets are the records an event counts for: the person, and their
// company (the event's account, or the company they are on); a company's
// own event counts for the company.
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
	if company != "" {
		out = append(out, target{company, LevelCompany})
	}
	return out, nil
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// number reads a property as a number, or false.
func number(props map[string]any, key string) (float64, bool) {
	switch v := props[key].(type) {
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	}
	return 0, false
}

// eventJob adds an event to every live metric of its brand that counts it.
func (s *Service) eventJob(ctx context.Context, job jobs.Job) error {
	var in struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(job.Payload, &in); err != nil || job.WorkspaceID == nil || in.EventID == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var rec, recordType, brand, name string
		var account, product *string
		var raw []byte
		var usage bool
		var occurred, received time.Time
		err := tx.QueryRow(ctx, `SELECT e.record_id::text, r.record_type, e.account_id::text, e.product_id::text, e.brand_id::text, e.name, e.properties,
				e.usage, e.occurred_at, e.received_at
			FROM customer_events e JOIN records r ON r.id = e.record_id AND r.merged_into IS NULL AND r.archived_at IS NULL AND r.deleted_at IS NULL
			WHERE e.workspace_id = $1 AND e.id = $2`, ws, in.EventID).
			Scan(&rec, &recordType, &account, &product, &brand, &name, &raw, &usage, &occurred, &received)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		// The definitions that count it. A backfill under way holds them:
		// this waits for it, then skips what it already counted.
		rows, err := tx.Query(ctx, `SELECT `+Columns+`, backfilled_at FROM metric_definitions
			WHERE brand_id = $1 AND status = 'active' AND ($2 = ANY (events) OR (usage AND $3)) ORDER BY key FOR SHARE`, brand, name, usage)
		if err != nil {
			return err
		}
		type def struct {
			Definition
			backfilled *time.Time
		}
		defs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (def, error) {
			var d def
			err := row.Scan(&d.ID, &d.Key, &d.Name, &d.Description, &d.Levels, &d.Aggregate, &d.Events, &d.Usage, &d.Property, &d.WindowDays,
				&d.OffsetDays, &d.PerProduct, &d.Version, &d.backfilled)
			return d, err
		})
		if err != nil || len(defs) == 0 {
			return err
		}
		props := map[string]any{}
		_ = json.Unmarshal(raw, &props)
		list, err := targets(ctx, tx, ws, brand, rec, recordType, account)
		if err != nil {
			return err
		}
		day := occurred.UTC().Truncate(24 * time.Hour)
		touched := map[string]bool{}
		for _, d := range defs {
			if d.backfilled == nil || !received.After(*d.backfilled) {
				metrics.RecordMetricUpdated("backfilled") // counted by the backfill
				continue
			}
			if day.Before(time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(d.span() - 1))) {
				metrics.RecordMetricUpdated("too_old")
				continue
			}
			val, isNum := number(props, d.Property)
			if (d.Aggregate == Sum || d.Aggregate == Min || d.Aggregate == Max) && !isNum {
				metrics.RecordMetricUpdated("no_value")
				continue
			}
			products := []string{NoProduct}
			if d.PerProduct && product != nil {
				products = append(products, *product)
			}
			for _, t := range list {
				if !has(d.Levels, t.level) {
					continue
				}
				for _, p := range products {
					if _, err := tx.Exec(ctx, `INSERT INTO metric_buckets (workspace_id, brand_id, metric_id, record_id, product_id, day, n, total, lo, hi, last_at)
						VALUES ($1, $2, $3, $4, $5, $6, 1, $7, $8, $8, $9)
						ON CONFLICT (metric_id, record_id, product_id, day) DO UPDATE SET n = metric_buckets.n + 1,
							total = metric_buckets.total + EXCLUDED.total, lo = LEAST(metric_buckets.lo, EXCLUDED.lo),
							hi = GREATEST(metric_buckets.hi, EXCLUDED.hi), last_at = GREATEST(metric_buckets.last_at, EXCLUDED.last_at)`,
						ws, brand, d.ID, t.rec, p, day, val, nullableNum(val, isNum), occurred); err != nil {
						return err
					}
					if err := refreshValue(ctx, tx, ws, brand, d.Definition, t.rec, p); err != nil {
						return err
					}
				}
				touched[t.rec] = true
			}
			metrics.RecordMetricUpdated("updated")
		}
		for _, r := range sorted(touched) {
			if err := publish(ctx, tx, ws, r, true); err != nil {
				return err
			}
		}
		return nil
	})
}

// sorted is a set's members in order: records are always locked in the
// same order, so jobs that touch the same ones never deadlock.
func sorted[V any](set map[string]V) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func nullableNum(v float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return &v
}

// refreshValue works out one value again from its buckets, stores it, and
// sets when the window next moves it.
func refreshValue(ctx context.Context, tx pgx.Tx, ws, brand string, d Definition, rec, product string) error {
	// A window with buckets in it moves at the next UTC midnight; the last
	// value of an event never ages out on its own within its window, but
	// leaves it after window_days.
	_, err := tx.Exec(ctx, `INSERT INTO metric_values (workspace_id, brand_id, metric_id, record_id, product_id, value, due_at)
		SELECT $1, $2, $3, $4, $5, `+d.valueSQL()+`,
			CASE WHEN count(*) > 0 THEN date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '1 day' END
		FROM metric_buckets b WHERE b.metric_id = $3 AND b.record_id = $4 AND b.product_id = $5
		ON CONFLICT (metric_id, record_id, product_id) DO UPDATE SET value = EXCLUDED.value, due_at = EXCLUDED.due_at, updated_at = now()`,
		ws, brand, d.ID, rec, product)
	return err
}

// publish writes a record's metrics to its attributes. live tells rules
// and waiting journeys about a change; a backfill passes false, so the
// values it writes are where rules start from and nothing fires for the
// past.
func publish(ctx context.Context, tx pgx.Tx, ws, rec string, live bool) error {
	var oldRaw, newRaw []byte
	err := tx.QueryRow(ctx, `SELECT attributes->'metrics', outflare_record_metrics($1) FROM records WHERE id = $1 AND workspace_id = $2 FOR NO KEY UPDATE`, rec, ws).
		Scan(&oldRaw, &newRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var oldV, newV map[string]any
	_ = json.Unmarshal(oldRaw, &oldV)
	_ = json.Unmarshal(newRaw, &newV)
	if len(newV) == 0 {
		newV = nil
	}
	if reflect.DeepEqual(oldV, newV) {
		return nil
	}
	if newV == nil {
		_, err = tx.Exec(ctx, `UPDATE records SET attributes = attributes - 'metrics', updated_at = now() WHERE id = $1`, rec)
	} else {
		_, err = tx.Exec(ctx, `UPDATE records SET attributes = jsonb_set(COALESCE(attributes, '{}'), '{metrics}', $2), updated_at = now() WHERE id = $1`, rec, newRaw)
	}
	if err != nil || !live {
		return err
	}
	change := customers.FieldChange{RecordID: rec, Field: "metrics", HadOld: oldV != nil, Source: "metric"}
	if oldV != nil {
		change.Old = oldV
	}
	return customers.FieldChanged(ctx, tx, ws, change)
}

// backfillJob rebuilds a metric from stored events, quietly: it writes the
// values rules start from and fires nothing.
func (s *Service) backfillJob(ctx context.Context, job jobs.Job) error {
	var in struct {
		MetricID string `json:"metric_id"`
		Version  int    `json:"version"`
	}
	if err := json.Unmarshal(job.Payload, &in); err != nil || job.WorkspaceID == nil || in.MetricID == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	start := time.Now()
	n := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var brand string
		d, err := Scan(tx.QueryRow(ctx, `SELECT `+Columns+` FROM metric_definitions WHERE workspace_id = $1 AND id = $2 AND status = 'active' FOR UPDATE`,
			ws, in.MetricID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Version != in.Version {
			return nil // a newer version has its own backfill
		}
		if err := tx.QueryRow(ctx, `SELECT brand_id::text FROM metric_definitions WHERE id = $1`, d.ID).Scan(&brand); err != nil {
			return err
		}
		// One backfill at a time per brand: they rewrite the same records.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('metric-backfill:' || $1, 0))`, brand); err != nil {
			return err
		}
		// Events received from now on are counted live; the backfill
		// counts those before.
		if _, err := tx.Exec(ctx, `UPDATE metric_definitions SET backfilling = true, backfilled_at = now() WHERE id = $1`, d.ID); err != nil {
			return err
		}
		// Records that had it, to take it off those that no longer do.
		rows, err := tx.Query(ctx, `SELECT DISTINCT record_id::text FROM metric_values WHERE metric_id = $1`, d.ID)
		if err != nil {
			return err
		}
		before, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM metric_buckets WHERE metric_id = $1`, d.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM metric_values WHERE metric_id = $1`, d.ID); err != nil {
			return err
		}
		// Each matching event, for the person and for their company.
		value := `CASE WHEN e.properties->>$5 ~ '^\s*-?[0-9]+(\.[0-9]+)?\s*$' THEN (e.properties->>$5)::numeric END`
		match := `e.workspace_id = $1 AND e.brand_id = $2 AND ($3::text[] && ARRAY[e.name] OR ($4 AND e.usage))
			AND e.occurred_at >= (now() AT TIME ZONE 'UTC')::date - $6::int + 1 AND e.received_at < (SELECT backfilled_at FROM metric_definitions WHERE id = $7)`
		needValue := d.Aggregate == Sum || d.Aggregate == Min || d.Aggregate == Max
		if needValue {
			match += ` AND ` + value + ` IS NOT NULL`
		}
		levels := ""
		if has(d.Levels, LevelPerson) {
			levels += `SELECT r.id AS rec, e.product_id, e.occurred_at, ` + value + ` AS v FROM customer_events e
				JOIN records r ON r.id = e.record_id AND r.record_type = 'person' AND r.merged_into IS NULL AND r.deleted_at IS NULL WHERE ` + match
		}
		if has(d.Levels, LevelCompany) {
			if levels != "" {
				levels += ` UNION ALL `
			}
			levels += `SELECT c.id AS rec, e.product_id, e.occurred_at, ` + value + ` AS v FROM customer_events e
				JOIN records r ON r.id = e.record_id AND r.merged_into IS NULL AND r.deleted_at IS NULL
				JOIN LATERAL (SELECT CASE WHEN r.record_type <> 'person' THEN r.id ELSE COALESCE(e.account_id, (SELECT l.to_id FROM record_relationships l
						JOIN records x ON x.id = l.to_id AND x.brand_id = e.brand_id AND x.merged_into IS NULL AND x.deleted_at IS NULL
						WHERE l.from_id = r.id AND l.kind = 'companies' AND l.valid_to IS NULL ORDER BY l.valid_from, l.id LIMIT 1)) END AS id) c ON c.id IS NOT NULL
				WHERE ` + match
		}
		products := `SELECT rec, '` + NoProduct + `'::uuid AS product_id, occurred_at, v FROM hits`
		if d.PerProduct {
			products += ` UNION ALL SELECT rec, product_id, occurred_at, v FROM hits WHERE product_id IS NOT NULL`
		}
		tag, err := tx.Exec(ctx, `WITH hits AS (`+levels+`), split AS (`+products+`)
			INSERT INTO metric_buckets (workspace_id, brand_id, metric_id, record_id, product_id, day, n, total, lo, hi, last_at)
			SELECT $1, $2, $7, rec, product_id, (occurred_at AT TIME ZONE 'UTC')::date, count(*), COALESCE(sum(v), 0), min(v), max(v), max(occurred_at)
			FROM split GROUP BY rec, product_id, (occurred_at AT TIME ZONE 'UTC')::date`,
			ws, brand, d.Events, d.Usage, d.Property, d.span(), d.ID)
		if err != nil {
			return err
		}
		n = int(tag.RowsAffected())
		if _, err := tx.Exec(ctx, `INSERT INTO metric_values (workspace_id, brand_id, metric_id, record_id, product_id, value, due_at)
			SELECT $1, $2, $3, b.record_id, b.product_id, `+d.valueSQL()+`, date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '1 day'
			FROM metric_buckets b WHERE b.metric_id = $3 GROUP BY b.record_id, b.product_id`, ws, brand, d.ID); err != nil {
			return err
		}
		// Write the attributes quietly: nothing fires for the past.
		rows, err = tx.Query(ctx, `SELECT DISTINCT record_id::text FROM metric_values WHERE metric_id = $1`, d.ID)
		if err != nil {
			return err
		}
		after, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		all := map[string]bool{}
		for _, r := range append(before, after...) {
			all[r] = true
		}
		for _, r := range sorted(all) {
			if err := publish(ctx, tx, ws, r, false); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE metric_definitions SET backfilling = false WHERE id = $1`, d.ID)
		return err
	})
	metrics.RecordMetricBackfill(time.Since(start), err == nil)
	if err != nil {
		return err
	}
	track.Emit(ctx, track.Event{Name: "metric.backfilled", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"metric_id": in.MetricID, "version": in.Version, "buckets": n, "duration_ms": time.Since(start).Milliseconds()}})
	return nil
}

// Unpublish takes an archived metric off every record that had it,
// quietly. Call it in the transaction that archived it.
func Unpublish(ctx context.Context, tx pgx.Tx, ws, metricID string) error {
	rows, err := tx.Query(ctx, `DELETE FROM metric_values WHERE metric_id = $1 RETURNING record_id::text`, metricID)
	if err != nil {
		return err
	}
	recs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM metric_buckets WHERE metric_id = $1`, metricID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, r := range recs {
		seen[r] = true
	}
	for _, r := range sorted(seen) {
		if err := publish(ctx, tx, ws, r, false); err != nil {
			return err
		}
	}
	return nil
}

// sweepBatch is the most values one sweep transaction moves on.
const sweepBatch = 500

// Run moves windows on as days pass, every tick until ctx ends.
func (s *Service) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		n, err := s.Sweep(ctx)
		if err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "metric.sweep_failed", ActorType: "system", Surface: "system"}, "internal", err)
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

// Sweep works out again the values whose window moved (a new UTC day),
// drops buckets older than any window, and tells rules about the values
// that changed. It reports how many values it looked at.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	start := time.Now()
	n := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT v.workspace_id::text, v.brand_id::text, v.record_id::text, v.product_id::text, `+prefixed("d.")+`
			FROM metric_values v JOIN metric_definitions d ON d.id = v.metric_id AND d.status = 'active' AND NOT d.backfilling
			WHERE v.due_at <= now() ORDER BY v.due_at LIMIT $1 FOR UPDATE OF v SKIP LOCKED`, sweepBatch)
		if err != nil {
			return err
		}
		type due struct {
			ws, brand, rec, product string
			d                       Definition
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
			var x due
			err := row.Scan(&x.ws, &x.brand, &x.rec, &x.product, &x.d.ID, &x.d.Key, &x.d.Name, &x.d.Description, &x.d.Levels, &x.d.Aggregate, &x.d.Events,
				&x.d.Usage, &x.d.Property, &x.d.WindowDays, &x.d.OffsetDays, &x.d.PerProduct, &x.d.Version)
			return x, err
		})
		if err != nil {
			return err
		}
		n = len(list)
		touched := map[string]string{}
		for _, x := range list {
			if _, err := tx.Exec(ctx, `DELETE FROM metric_buckets WHERE metric_id = $1 AND record_id = $2 AND product_id = $3
				AND day < (now() AT TIME ZONE 'UTC')::date - $4::int + 1`, x.d.ID, x.rec, x.product, x.d.span()); err != nil {
				return err
			}
			if err := refreshValue(ctx, tx, x.ws, x.brand, x.d, x.rec, x.product); err != nil {
				return err
			}
			touched[x.rec] = x.ws
		}
		for _, rec := range sorted(touched) {
			if err := publish(ctx, tx, touched[rec], rec, true); err != nil {
				return err
			}
		}
		return nil
	})
	metrics.RecordMetricSweep(time.Since(start))
	return n, err
}

// prefixed is Columns with each column qualified by p.
func prefixed(p string) string {
	return p + `id::text, ` + p + `key, ` + p + `name, ` + p + `description, ` + p + `levels, ` + p + `aggregate, ` + p + `events, ` + p + `usage, COALESCE(` +
		p + `property, ''), ` + p + `window_days, ` + p + `offset_days, ` + p + `per_product, ` + p + `version`
}
