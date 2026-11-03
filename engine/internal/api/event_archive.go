package api

import (
	"context"
	"time"

	"github.com/outflareai/outflare/engine/internal/eventstore"
	"github.com/outflareai/outflare/engine/internal/rules"
)

// ClickHouseArchive lets rule dry runs read the ClickHouse event store
// (Options.EventArchive), which keeps every event, beyond Postgres's
// retention.
type ClickHouseArchive struct{ CH *eventstore.ClickHouse }

// RuleEvents implements rules.EventArchive.
func (c ClickHouseArchive) RuleEvents(ctx context.Context, ws, name string, since time.Time, limit int) ([]rules.ArchivedEvent, error) {
	events, err := c.CH.RuleEvents(ctx, ws, name, since, limit)
	if err != nil {
		return nil, err
	}
	out := make([]rules.ArchivedEvent, len(events))
	for i, e := range events {
		out[i] = rules.ArchivedEvent{RecordID: e.RecordID, Properties: e.Properties, OccurredAt: e.OccurredAt}
	}
	return out, nil
}
