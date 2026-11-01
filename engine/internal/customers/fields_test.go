package customers_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
)

func TestSetFields(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ws := workspace(t, pool)
	var brand string
	if err := pool.QueryRow(ctx, `INSERT INTO brands (workspace_id, name, handle) VALUES ($1, 'Acme', 'acme') RETURNING id::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	var company string
	inTx(t, pool, func(tx pgx.Tx) error {
		ids, err := customers.EnsureCompanies(ctx, tx, ws, &brand, "test", []customers.Company{{Domain: "acme.example", Name: "Acme"}})
		company = ids["acme.example"]
		return err
	})
	// The brand already has a "summary" field, as a number: it is left alone.
	if _, err := pool.Exec(ctx, `INSERT INTO field_definitions (workspace_id, brand_id, record_type, key, label, value_type)
		VALUES ($1, $2, 'account', 'summary', 'Summary', 'number')`, ws, brand); err != nil {
		t.Fatal(err)
	}
	fields := []customers.Field{
		{Key: "fit_score", Label: "Fit score", Description: "How well it fits.", Type: "number"},
		{Key: "fit_reason", Label: "Fit reason", Type: "text"},
		{Key: "summary", Label: "Summary", Type: "text"},
	}
	set := func(source string, values map[string]any) bool {
		t.Helper()
		var changed bool
		inTx(t, pool, func(tx pgx.Tx) error {
			var err error
			changed, err = customers.SetFields(ctx, tx, ws, company, source, fields, values)
			return err
		})
		return changed
	}
	if !set("enrichment", map[string]any{"fit_score": 82, "fit_reason": "Sells payroll software", "summary": "Payroll"}) {
		t.Fatal("nothing changed")
	}
	// A person corrects the reason; enrichment then keeps their edit but
	// still updates the score.
	if _, err := pool.Exec(ctx, `UPDATE records SET attributes = attributes || '{"fit_reason": "Mine"}',
		attribute_sources = attribute_sources || '{"fit_reason": "manual"}' WHERE id = $1`, company); err != nil {
		t.Fatal(err)
	}
	set("enrichment", map[string]any{"fit_score": 64, "fit_reason": "Something else"})
	// The same values again change nothing.
	if set("enrichment", map[string]any{"fit_score": 64}) {
		t.Fatal("unchanged values reported a change")
	}

	var score float64
	var reason string
	var hasSummary bool
	if err := pool.QueryRow(ctx, `SELECT (attributes->>'fit_score')::float8, attributes->>'fit_reason', attributes ? 'summary'
		FROM records WHERE id = $1`, company).Scan(&score, &reason, &hasSummary); err != nil {
		t.Fatal(err)
	}
	if score != 64 || reason != "Mine" || hasSummary {
		t.Fatalf("record: score %v, reason %q, summary %v", score, reason, hasSummary)
	}
	var defs, history, audits int
	if err := pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM field_definitions WHERE brand_id = $2 AND record_type = 'account' AND key IN ('fit_score', 'fit_reason')),
			(SELECT count(*) FROM attribute_changes WHERE record_id = $1 AND source = 'enrichment'),
			(SELECT count(*) FROM audit_events WHERE target_id = $1::text AND action = 'record.updated')`, company, brand).Scan(&defs, &history, &audits); err != nil {
		t.Fatal(err)
	}
	if defs != 2 || history != 3 || audits != 2 {
		t.Fatalf("fields %d, history %d, audit events %d", defs, history, audits)
	}

	inTx(t, pool, func(tx pgx.Tx) error {
		if _, err := customers.SetFields(ctx, tx, ws, company, "enrichment", []customers.Field{{Key: "Bad Key", Label: "x", Type: "text"}},
			map[string]any{"Bad Key": "x"}); err == nil {
			t.Fatal("a bad key was accepted")
		}
		return nil
	})
}
