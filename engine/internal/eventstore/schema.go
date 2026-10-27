package eventstore

import (
	"context"
	"fmt"
	"strconv"
)

// migrations build the ClickHouse schema, in order. Like Postgres
// migrations they are append-only: never change one that shipped.
var migrations = []string{
	// 1: product events, a copy of customer_events. A row is replaced by a
	// newer copy (version updated_at) and dropped by a tombstone
	// (deleted = 1), so merges and deletions in Postgres reach it.
	// Properties, traits and context are JSON text, read with JSONExtract.
	`CREATE TABLE IF NOT EXISTS events (
		id           UUID,
		workspace_id UUID,
		record_id    Nullable(UUID),
		account_id   Nullable(UUID),
		brand_id     Nullable(UUID),
		kind         LowCardinality(String),
		name         LowCardinality(String),
		raw_name     String,
		user_id      String,
		anonymous_id String,
		properties   String,
		traits       String,
		context      String,
		source       LowCardinality(String),
		occurred_at  DateTime64(3, 'UTC'),
		received_at  DateTime64(3, 'UTC'),
		updated_at   DateTime64(6, 'UTC'),
		deleted      UInt8 DEFAULT 0
	) ENGINE = ReplacingMergeTree(updated_at, deleted)
	PARTITION BY toYYYYMM(occurred_at)
	ORDER BY (workspace_id, name, toDate(occurred_at), id)`,
}

// Migrate brings the schema up to date. It is safe to run on every start
// and from several servers at once: each step is idempotent.
func (c *ClickHouse) Migrate(ctx context.Context) (applied int, err error) {
	applied, _, err = c.migrate(ctx)
	return applied, err
}

// migrate also says whether the database was new (had no tables of ours).
func (c *ClickHouse) migrate(ctx context.Context) (applied int, fresh bool, err error) {
	if err := c.Exec(ctx, `CREATE TABLE IF NOT EXISTS outflare_schema (version UInt32, applied_at DateTime DEFAULT now())
		ENGINE = ReplacingMergeTree ORDER BY version`, nil); err != nil {
		return 0, false, err
	}
	rows, err := c.Query(ctx, `SELECT max(version) AS v FROM outflare_schema`, nil)
	if err != nil {
		return 0, false, err
	}
	have := 0
	if len(rows) == 1 {
		have, _ = strconv.Atoi(fmt.Sprint(rows[0]["v"]))
	}
	for i := have; i < len(migrations); i++ {
		if err := c.Exec(ctx, migrations[i], nil); err != nil {
			return applied, false, fmt.Errorf("clickhouse migration %d: %w", i+1, err)
		}
		if err := c.Insert(ctx, "outflare_schema", []map[string]any{{"version": i + 1}}); err != nil {
			return applied, false, err
		}
		applied++
	}
	return applied, have == 0, nil
}
