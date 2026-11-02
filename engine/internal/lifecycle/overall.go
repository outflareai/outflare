package lifecycle

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
)

// Summary is a company's overall status across its products: the best
// stage by rank wins, plus flags.
type Summary struct {
	Status         string   `json:"status"`
	Flags          []string `json:"flags"`
	ActiveProducts int      `json:"active_products"`
	// Products counted, and how many are churned or in trial.
	products, churned, trial int
}

// summarise works out a company's overall status from its billing stages.
func summarise(ctx context.Context, tx pgx.Tx, rec string) (Summary, error) {
	sum := Summary{Flags: []string{}}
	rows, err := tx.Query(ctx, `SELECT s.stage, COALESCE(st.category, ''), COALESCE(st.rank, -1)
		FROM lifecycle_states s JOIN products p ON p.id = s.product_id AND p.archived_at IS NULL
		LEFT JOIN lifecycle_stages st ON st.brand_id = s.brand_id AND st.level = 'company' AND st.key = s.stage
		WHERE s.record_id = $1 AND s.level = 'company'`, rec)
	if err != nil {
		return sum, err
	}
	best := -2
	defer rows.Close()
	for rows.Next() {
		var stage, cat string
		var rank int
		if err := rows.Scan(&stage, &cat, &rank); err != nil {
			return sum, err
		}
		sum.products++
		switch cat {
		case CatActive, CatAtRisk:
			sum.ActiveProducts++
		case CatTrial:
			sum.trial++
		case CatChurned:
			sum.churned++
		}
		if rank > best || (rank == best && stage < sum.Status) {
			best, sum.Status = rank, stage
		}
	}
	if err := rows.Err(); err != nil {
		return sum, err
	}
	if sum.churned > 0 && sum.churned < sum.products {
		sum.Flags = append(sum.Flags, FlagPartlyChurned)
	}
	if sum.ActiveProducts > 0 && sum.trial > 0 {
		sum.Flags = append(sum.Flags, FlagTriallingAnother)
	}
	return sum, nil
}

// Overall works out a company's overall status again after one of its
// billing stages changed, stores it, and emits the overall events for what
// changed: became customer (a first product went active), partly churned,
// fully churned and expanded (another product went active). cause makes
// each event once.
func Overall(ctx context.Context, tx pgx.Tx, ws, brand, rec, cause string, at time.Time) error {
	sum, err := summarise(ctx, tx, rec)
	if err != nil || sum.products == 0 {
		return err
	}
	var prevStatus string
	var prevFlags []string
	var prevActive int
	found := true
	err = tx.QueryRow(ctx, `SELECT status, flags, active_products FROM lifecycle_overall WHERE record_id = $1 FOR UPDATE`, rec).
		Scan(&prevStatus, &prevFlags, &prevActive)
	if errors.Is(err, pgx.ErrNoRows) {
		found, err = false, nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO lifecycle_overall (record_id, workspace_id, brand_id, status, flags, active_products)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (record_id) DO UPDATE SET status = EXCLUDED.status, flags = EXCLUDED.flags, active_products = EXCLUDED.active_products, updated_at = now()`,
		rec, ws, brand, sum.Status, sum.Flags, sum.ActiveProducts); err != nil {
		return err
	}
	// Whether the company was ever a paying customer before: then a first
	// active product again is a reactivation, not a new customer.
	var wasCustomer bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lifecycle_transitions WHERE record_id = $1 AND level = 'overall' AND move = $2)`,
		rec, EvBecameCustomer).Scan(&wasCustomer); err != nil {
		return err
	}
	var events []string
	if prevActive == 0 && sum.ActiveProducts > 0 && !wasCustomer {
		events = append(events, EvBecameCustomer)
	}
	if found && prevActive > 0 && sum.ActiveProducts > prevActive {
		events = append(events, EvExpanded)
	}
	if slices.Contains(sum.Flags, FlagPartlyChurned) && !slices.Contains(prevFlags, FlagPartlyChurned) {
		events = append(events, EvPartlyChurned)
	}
	// Fully churned: every product churned now, and not before (the best
	// stage then was not a churned one).
	var prevCat string
	if found {
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT category FROM lifecycle_stages WHERE brand_id = $1 AND level = 'company' AND key = $2), '')`,
			brand, prevStatus).Scan(&prevCat); err != nil {
			return err
		}
	}
	if sum.churned == sum.products && prevCat != CatChurned {
		events = append(events, EvFullyChurned)
	}
	for _, name := range events {
		var tid string
		err := tx.QueryRow(ctx, `INSERT INTO lifecycle_transitions (workspace_id, brand_id, record_id, level, move, from_stage, to_stage, cause_key, at)
			VALUES ($1, $2, $3, 'overall', $4, NULLIF($5, ''), $6, $7, $8) ON CONFLICT (record_id, level, move, cause_key) DO NOTHING RETURNING id::text`,
			ws, brand, rec, name, prevStatus, sum.Status, cause, at).Scan(&tid)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		eventID, err := customers.RecordEvent(ctx, tx, ws, customers.Event{RecordID: rec, Name: name, Source: "lifecycle", MessageID: "lifecycle:" + tid,
			OccurredAt: at, Properties: map[string]any{"status": sum.Status, "previous_status": prevStatus, "flags": sum.Flags,
				"active_products": sum.ActiveProducts, "previous_active_products": prevActive}})
		if err != nil {
			return err
		}
		if eventID != "" {
			if _, err := tx.Exec(ctx, `UPDATE lifecycle_transitions SET event_id = $2 WHERE id = $1`, tid, eventID); err != nil {
				return err
			}
		}
	}
	return nil
}
