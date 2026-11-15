package rules

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// A change rule fires when a change to a record makes its conditions
// true. Conditions that name a date relative to today (renews_on lt +60d,
// last_contacted lt -30d) also come true when nothing changes and a day
// passes, so those rules are tested against every record of their type
// once a day (UTC), and straight after they go live.
//
// The first check after a rule goes live only notes where every record
// stands: a record that already matches doesn't fire, as with everything
// else that was true before a rule went live. From then on a record fires
// when it comes to match, once per time it does.

// recheckPage is how many records one transaction of a daily check tests.
const recheckPage = 500

// Daily queues the daily check of every active rule that is due one, and
// reports how many it queued.
func (s *Service) Daily(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// A relative date in the conditions, as jsonb prints it.
		rows, err := tx.Query(ctx, `SELECT id::text, workspace_id::text, (checked_at IS NULL OR checked_at < activated_at) FROM rules
			WHERE status = 'active' AND trigger_kind = 'change' AND conditions::text ~ '"value": "(today|[+-][0-9]{1,4}d)"'
			  AND (checked_at IS NULL OR checked_at < activated_at OR checked_at < date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
			ORDER BY id LIMIT 200 FOR UPDATE SKIP LOCKED`)
		if err != nil {
			return err
		}
		type due struct {
			id, ws string
			first  bool
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
			var d due
			return d, row.Scan(&d.id, &d.ws, &d.first)
		})
		if err != nil {
			return err
		}
		for _, d := range list {
			if _, err := tx.Exec(ctx, `UPDATE rules SET checked_at = now() WHERE id = $1`, d.id); err != nil {
				return err
			}
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: customers.JobRuleRecheck, WorkspaceID: &d.ws,
				Payload: map[string]any{"rule_id": d.id, "first": d.first}}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// recheckJob tests one rule against every record of its type and brand.
func (s *Service) recheckJob(ctx context.Context, job jobs.Job) error {
	var in struct {
		RuleID string `json:"rule_id"`
		First  bool   `json:"first"`
	}
	if err := json.Unmarshal(job.Payload, &in); err != nil || job.WorkspaceID == nil || in.RuleID == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	_, err := s.Recheck(ctx, *job.WorkspaceID, in.RuleID, in.First)
	return err
}

// Recheck tests a change rule against every record of its type and brand
// and fires for those that came to match since the last check. first notes
// where each record stands without firing. It reports how many fired.
func (s *Service) Recheck(ctx context.Context, ws, ruleID string, first bool) (int, error) {
	fired, after := 0, "00000000-0000-0000-0000-000000000000"
	for {
		n := 0
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			got, err := Load(ctx, tx, ws, `id = $2 AND status = 'active'`, ruleID)
			if err != nil || len(got) == 0 || !got[0].Daily() {
				return err
			}
			r := got[0]
			// Only records that match now, or matched at the last check,
			// can change anything: the rest are read and left alone.
			rows, err := tx.Query(ctx, `SELECT r.id::text, r.attributes, COALESCE(st.holds, false), st.rule_id IS NOT NULL
				FROM records r LEFT JOIN rule_states st ON st.rule_id = $2 AND st.record_id = r.id
				WHERE r.workspace_id = $1 AND r.record_type = $3 AND r.brand_id IS NOT DISTINCT FROM $4::uuid AND r.id > $5
				  AND r.merged_into IS NULL AND r.archived_at IS NULL
				ORDER BY r.id LIMIT $6`, ws, r.ID, r.RecordType, r.BrandID, after, recheckPage)
			if err != nil {
				return err
			}
			type rec struct {
				id          string
				attrs       []byte
				held, known bool
			}
			list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (rec, error) {
				var x rec
				return x, row.Scan(&x.id, &x.attrs, &x.held, &x.known)
			})
			if err != nil {
				return err
			}
			n = len(list)
			for _, x := range list {
				after = x.id
				attrs := map[string]any{}
				_ = json.Unmarshal(x.attrs, &attrs)
				now := journeys.Holds(attrs, nil, r.Conditions, r.Match)
				if now == x.held && (x.known || !now) {
					continue
				}
				st, err := lockState(ctx, tx, r.WorkspaceID, r.ID, x.id)
				if err != nil {
					return err
				}
				episode := st.episode
				comes := now && !st.holds && !first
				if comes {
					episode++
				}
				if _, err := tx.Exec(ctx, `UPDATE rule_states SET holds = $3, episode = $4, updated_at = now() WHERE rule_id = $1 AND record_id = $2`,
					r.ID, x.id, now, episode); err != nil {
					return err
				}
				outcome := "no_match"
				if comes {
					if outcome, err = fire(ctx, tx, r, x.id, fmt.Sprintf("change:%d", episode), nil, map[string]any{"checked": "daily"}, 1); err != nil {
						return err
					}
					if outcome == "fired" {
						fired++
					}
				}
				metrics.RuleEvaluated(KindChange, outcome)
			}
			return nil
		})
		if err != nil || n < recheckPage {
			return fired, err
		}
	}
}
