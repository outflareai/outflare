package scores

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// JobPass scores every record of a brand and record type again.
const JobPass = "score.pass"

// passPage is how many records one transaction of a pass scores.
const passPage = 500

// Service scores records in jobs and starts the daily pass.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

// Register adds the job handlers to q.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(customers.JobScoreRecord, s.recordJob)
	q.Handle(JobPass, s.passJob)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Load reads a brand's active scores for a record type.
func Load(ctx context.Context, q querier, ws, brand, recordType string) ([]Definition, error) {
	rows, err := q.Query(ctx, `SELECT `+Columns+` FROM score_definitions
		WHERE workspace_id = $1 AND brand_id = $2 AND record_type = $3 AND status = 'active' ORDER BY key`, ws, brand, recordType)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, Scan)
}

// Scan reads a definition's Columns.
func Scan(row pgx.CollectableRow) (Definition, error) {
	var d Definition
	var raw, parts []byte
	if err := row.Scan(d.Dest(&raw, &parts)...); err != nil {
		return d, err
	}
	return d, d.Fill(raw, parts)
}

// Dest is where a row's Columns scan to; Fill then reads the factors and
// parts.
func (d *Definition) Dest(factors, parts *[]byte) []any {
	return []any{&d.ID, &d.Key, &d.Label, &d.Description, &d.RecordType, &d.Base, factors, &d.HighFrom, &d.LowBelow, &d.Version,
		&d.Nature, parts, &d.HigherIsBetter, &d.Needs}
}

// Fill reads a row's factors and parts into d.
func (d *Definition) Fill(factors, parts []byte) error {
	if err := json.Unmarshal(factors, &d.Factors); err != nil {
		return err
	}
	if err := json.Unmarshal(parts, &d.Parts); err != nil {
		return err
	}
	if d.Factors == nil {
		d.Factors = []Factor{}
	}
	if d.Parts == nil {
		d.Parts = []Part{}
	}
	return nil
}

type rowQuerier interface {
	querier
	jobs.Querier
}

// View is what a record's scores read: its own fields and, for a deal,
// the deal's own facts under deal (days_open, days_in_stage,
// days_to_close, days_quiet, contacts) and its company's fields under
// account, so a deal's score can read how the customer is doing.
func View(ctx context.Context, q rowQuerier, ws, rec, recordType string, attrs map[string]any) (map[string]any, error) {
	if recordType != "deal" {
		return attrs, nil
	}
	view := map[string]any{}
	for k, v := range attrs {
		view[k] = v
	}
	var opened time.Time
	var with *string
	var staged, quiet *time.Time
	err := q.QueryRow(ctx, `SELECT d.opened_at, d.record_id::text,
			(SELECT max(c.changed_at) FROM attribute_changes c WHERE c.workspace_id = d.workspace_id AND c.record_id = d.id AND c.field_key = 'stage'),
			(SELECT max(a.created_at) FROM crm_activities a WHERE a.workspace_id = d.workspace_id AND a.record_id IN (d.id, d.record_id))
		FROM deals d WHERE d.id = $1 AND d.workspace_id = $2`, rec, ws).Scan(&opened, &with, &staged, &quiet)
	if errors.Is(err, pgx.ErrNoRows) {
		return view, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now()
	days := func(t time.Time) float64 { return float64(int(now.Sub(t).Hours() / 24)) }
	deal := map[string]any{"days_open": days(opened), "days_in_stage": days(opened), "days_quiet": days(opened)}
	if staged != nil && staged.After(opened) {
		deal["days_in_stage"] = days(*staged)
	}
	if quiet != nil && quiet.After(opened) {
		deal["days_quiet"] = days(*quiet)
	}
	if people, ok := attrs["associated_people"].([]any); ok {
		deal["contacts"] = float64(len(people))
	} else {
		deal["contacts"] = float64(0)
	}
	if day, _ := attrs["close_date"].(string); len(day) >= 10 {
		if t, err := time.Parse(time.DateOnly, day[:10]); err == nil {
			deal["days_to_close"] = float64(int(t.Sub(now.UTC().Truncate(24*time.Hour)).Hours() / 24))
		}
	}
	view["deal"] = deal
	if with == nil {
		return view, nil
	}
	// The company the deal is with, or the person when they have none.
	account := *with
	if company, err := customers.CompanyOf(ctx, q, ws, *with, nil); err != nil {
		return nil, err
	} else if company != "" {
		account = company
	}
	var raw []byte
	err = q.QueryRow(ctx, `SELECT attributes FROM records WHERE id = $1 AND workspace_id = $2 AND merged_into IS NULL AND deleted_at IS NULL`, account, ws).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	fields := map[string]any{}
	_ = json.Unmarshal(raw, &fields)
	view["account"] = fields
	return view, nil
}

// EnqueuePass asks for every record of a score's brand and type to be
// scored again, quietly, in q's transaction, and marks the score as being
// worked out.
func EnqueuePass(ctx context.Context, q jobs.Querier, ws, brand, recordType, scoreID string, version int) error {
	if _, err := q.Exec(ctx, `UPDATE score_definitions SET scoring_since = COALESCE(scoring_since, now()) WHERE id = $1 AND workspace_id = $2`, scoreID, ws); err != nil {
		return err
	}
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobPass, WorkspaceID: &ws,
		Payload:        pass{BrandID: brand, RecordType: recordType},
		IdempotencyKey: "score-pass:" + scoreID + ":" + strconv.Itoa(version), MaxAttempts: 5})
	return err
}

type pass struct {
	BrandID    string `json:"brand_id"`
	RecordType string `json:"record_type"`
	// Live passes tell rules and waiting journeys about each change; the
	// pass after a score is added or changed is quiet.
	Live bool `json:"live,omitempty"`
}

// recordJob scores one record again after a field its scores read changed.
func (s *Service) recordJob(ctx context.Context, job jobs.Job) error {
	var in struct {
		RecordID string `json:"record_id"`
	}
	if err := json.Unmarshal(job.Payload, &in); err != nil || job.WorkspaceID == nil || in.RecordID == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		outcome, err := Apply(ctx, tx, *job.WorkspaceID, in.RecordID, nil, true)
		if err == nil {
			metrics.ScoreComputed("change", outcome)
		}
		return err
	})
}

// Apply works out a record's scores from its fields and writes them when
// they differ, in tx. defs are its brand's active scores for its type, or
// nil to read them. Live tells rules and waiting journeys about the
// change. The outcome is changed, unchanged or no_record.
func Apply(ctx context.Context, tx pgx.Tx, ws, rec string, defs []Definition, live bool) (string, error) {
	var recordType string
	var brand *string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT record_type, brand_id::text, attributes FROM records
		WHERE id = $1 AND workspace_id = $2 AND merged_into IS NULL AND archived_at IS NULL AND deleted_at IS NULL FOR NO KEY UPDATE`, rec, ws).
		Scan(&recordType, &brand, &raw)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && brand == nil) {
		return "no_record", nil
	}
	if err != nil {
		return "", err
	}
	if defs == nil {
		if defs, err = Load(ctx, tx, ws, *brand, recordType); err != nil {
			return "", err
		}
	}
	attrs := map[string]any{}
	_ = json.Unmarshal(raw, &attrs)
	old, hadOld := attrs["scores"].(map[string]any)
	// As JSON reads numbers, so the two compare.
	view, err := View(ctx, tx, ws, rec, recordType, attrs)
	if err != nil {
		return "", err
	}
	results := All(defs, view)
	var now map[string]any
	for _, d := range defs {
		r, ok := results[d.Key]
		if !ok {
			continue
		}
		if now == nil {
			now = map[string]any{}
		}
		now[d.Key] = float64(r.Value)
		now[d.Key+"_band"] = r.Band
	}
	if (!hadOld && now == nil) || (hadOld && reflect.DeepEqual(old, now)) {
		return "unchanged", nil
	}
	if now == nil {
		_, err = tx.Exec(ctx, `UPDATE records SET attributes = attributes - 'scores', updated_at = now() WHERE id = $1`, rec)
	} else {
		var out []byte
		if out, err = json.Marshal(now); err != nil {
			return "", err
		}
		_, err = tx.Exec(ctx, `UPDATE records SET attributes = jsonb_set(COALESCE(attributes, '{}'), '{scores}', $2), updated_at = now() WHERE id = $1`, rec, out)
	}
	if err != nil {
		return "", err
	}
	// The value of each day it changed, for the trend.
	for _, d := range defs {
		r, ok := results[d.Key]
		if was, had := old[d.Key].(float64); !ok || (had && int(was) == r.Value) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO score_history (workspace_id, brand_id, record_id, score_id, day, value, band)
			VALUES ($1, $2, $3, $4, (now() AT TIME ZONE 'UTC')::date, $5, $6)
			ON CONFLICT (record_id, score_id, day) DO UPDATE SET value = EXCLUDED.value, band = EXCLUDED.band`, ws, *brand, rec, d.ID, r.Value, r.Band); err != nil {
			return "", err
		}
	}
	// A move from one band to another is part of the record's history;
	// the number itself moves too often to keep every step.
	for _, d := range defs {
		was, _ := old[d.Key+"_band"].(string)
		is, _ := now[d.Key+"_band"].(string)
		if was == is || !live {
			continue
		}
		var before any
		if was != "" {
			before = was
		}
		oldJSON, _ := json.Marshal(before)
		newJSON, _ := json.Marshal(is)
		if _, err := tx.Exec(ctx, `INSERT INTO attribute_changes (workspace_id, record_id, field_key, old_value, new_value, source)
			VALUES ($1, $2, $3, $4, $5, 'score')`, ws, rec, "scores."+d.Key+"_band", oldJSON, newJSON); err != nil {
			return "", err
		}
	}
	if !live {
		return "changed", nil
	}
	change := customers.FieldChange{RecordID: rec, Field: "scores", HadOld: hadOld, Source: "score"}
	if hadOld {
		change.Old = old
	}
	return "changed", customers.FieldChanged(ctx, tx, ws, change)
}

// passJob scores every record of a brand and type.
func (s *Service) passJob(ctx context.Context, job jobs.Job) error {
	var in pass
	if err := json.Unmarshal(job.Payload, &in); err != nil || job.WorkspaceID == nil || in.BrandID == "" || in.RecordType == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	_, err := s.Pass(ctx, *job.WorkspaceID, in.BrandID, in.RecordType, in.Live)
	return err
}

// Pass scores every record of a brand and type again and reports how many
// changed. Scores changed while it runs get a pass of their own.
func (s *Service) Pass(ctx context.Context, ws, brand, recordType string, live bool) (int, error) {
	started := time.Now()
	trigger := "pass"
	if live {
		trigger = "daily"
	}
	changed, after := 0, "00000000-0000-0000-0000-000000000000"
	for {
		n := 0
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			defs, err := Load(ctx, tx, ws, brand, recordType)
			if err != nil {
				return err
			}
			if defs == nil {
				// Nothing to score with: Apply takes the scores off.
				defs = []Definition{}
			}
			rows, err := tx.Query(ctx, `SELECT id::text FROM records
				WHERE workspace_id = $1 AND brand_id = $2 AND record_type = $3 AND id > $4
				  AND merged_into IS NULL AND archived_at IS NULL AND deleted_at IS NULL
				ORDER BY id LIMIT $5`, ws, brand, recordType, after, passPage)
			if err != nil {
				return err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			n = len(ids)
			for _, id := range ids {
				after = id
				outcome, err := Apply(ctx, tx, ws, id, defs, live)
				if err != nil {
					return err
				}
				if outcome == "changed" {
					changed++
				}
				metrics.ScoreComputed(trigger, outcome)
			}
			return nil
		})
		if err != nil {
			return changed, err
		}
		if n < passPage {
			break
		}
	}
	if live {
		// Once a day, history older than it is kept for goes.
		if _, err := s.Pool.Exec(ctx, `DELETE FROM score_history WHERE workspace_id = $1 AND brand_id = $2 AND day < current_date - $3::int`, ws, brand, HistoryDays); err != nil {
			return changed, err
		}
	}
	_, err := s.Pool.Exec(ctx, `UPDATE score_definitions SET scoring_since = NULL, scored_at = now()
		WHERE workspace_id = $1 AND brand_id = $2 AND record_type = $3 AND (scoring_since IS NULL OR scoring_since <= $4)`, ws, brand, recordType, started)
	return changed, err
}

// Daily queues today's pass for every brand and record type with an
// active score that hasn't had one today (UTC), and reports how many it
// queued. A factor can compare a date with today, and a record added
// since the last pass has no score yet, so every score is worked out
// again once a day.
func (s *Service) Daily(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT workspace_id::text, brand_id::text, record_type FROM score_definitions
			WHERE status = 'active' AND scoring_since IS NULL
			  AND (scored_at IS NULL OR scored_at < date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
			LIMIT 200`)
		if err != nil {
			return err
		}
		type due struct{ ws, brand, recordType string }
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
			var d due
			return d, row.Scan(&d.ws, &d.brand, &d.recordType)
		})
		if err != nil {
			return err
		}
		day := time.Now().UTC().Format("2006-01-02")
		for _, d := range list {
			if _, err := tx.Exec(ctx, `UPDATE score_definitions SET scoring_since = now()
				WHERE workspace_id = $1 AND brand_id = $2 AND record_type = $3 AND status = 'active' AND scoring_since IS NULL`, d.ws, d.brand, d.recordType); err != nil {
				return err
			}
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobPass, WorkspaceID: &d.ws,
				Payload:        pass{BrandID: d.brand, RecordType: d.recordType, Live: true},
				IdempotencyKey: "score-daily:" + d.brand + ":" + d.recordType + ":" + day, MaxAttempts: 5}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// Run starts the daily passes and reports how many scores wait for one,
// until ctx ends.
func (s *Service) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if _, err := s.Daily(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "score.daily_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		var waiting int
		var oldest float64
		if err := s.Pool.QueryRow(ctx, `SELECT count(*), COALESCE(EXTRACT(EPOCH FROM now() - min(scoring_since)), 0) FROM score_definitions
			WHERE status = 'active' AND scoring_since IS NOT NULL`).Scan(&waiting, &oldest); err == nil {
			metrics.SetScoresScoring(waiting, oldest)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
